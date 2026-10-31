package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

// Client wraps an active WebSocket connection and client outbound queue.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	dead bool
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			if _, err := w.Write(message); err != nil {
				// The client is going away mid-frame; dropping the frame is the
				// only option and the reader will unregister it.
				return
			}
			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// DefaultSessionRingBufferSize is the default number of recent events retained
// per session so reconnecting mobile/web clients can replay missed stream frames.
const DefaultSessionRingBufferSize = 500

type bufferedEvent struct {
	seq  uint64
	data []byte
}

type sessionEventRing struct {
	nextSeq uint64
	events  []bufferedEvent
}

type wsClientCommand struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	LastSeq   uint64 `json:"last_seq"`
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregisterClient(c)
		c.conn.Close()
	}()

	c.conn.SetReadLimit(65536)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		var cmd wsClientCommand
		if err := json.Unmarshal(raw, &cmd); err == nil && cmd.Type == "resume" && cmd.SessionID != "" {
			c.hub.handleClientResume(c, cmd.SessionID, cmd.LastSeq)
		}
	}
}

// Hub manages active WebSocket client connections and broadcasts events.
type Hub struct {
	mu           sync.RWMutex
	clients      map[*Client]bool
	broadcast    chan []byte
	closed       bool
	upgrader     websocket.Upgrader
	allowOrigins []string
	ringSize     int
	rings        map[string]*sessionEventRing
}

// NewHub constructs a new WebSocket Hub.
func NewHub() *Hub {
	h := &Hub{
		clients:   make(map[*Client]bool),
		broadcast: make(chan []byte, 1024),
		ringSize:  DefaultSessionRingBufferSize,
		rings:     make(map[string]*sessionEventRing),
	}
	h.upgrader = websocket.Upgrader{
		CheckOrigin:     h.originAllowed,
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	return h
}

// SetRingBufferSize overrides the per-session ring buffer capacity (primarily for tests).
func (h *Hub) SetRingBufferSize(n int) {
	if n < 1 {
		n = 1
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ringSize = n
}

// SetAllowOrigins adds origins permitted to open a WebSocket beyond the request
// host and loopback. Entries may be full origins ("https://x.ts.net") or bare
// hosts ("x.ts.net").
func (h *Hub) SetAllowOrigins(origins []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.allowOrigins = append([]string(nil), origins...)
}

// Run starts the central event pump for the hub.
func (h *Hub) Run() {
	for msg := range h.broadcast {
		var toUnregister []*Client

		h.mu.Lock()
		for client := range h.clients {
			if client.dead {
				continue
			}
			select {
			case client.send <- msg:
			default:
				// Mark dead synchronously under lock to prevent goroutine leaks
				client.dead = true
				toUnregister = append(toUnregister, client)
			}
		}
		for _, deadClient := range toUnregister {
			delete(h.clients, deadClient)
			close(deadClient.send)
		}
		h.mu.Unlock()
	}
}

// Broadcast serializes and transmits an event to all connected WebSocket clients.
func (h *Hub) Broadcast(evType WSEventType, sessionID string, payload any) error {
	var raw json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		raw = data
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}

	var seq uint64
	var ring *sessionEventRing
	if sessionID != "" {
		ring = h.rings[sessionID]
		if ring == nil {
			ring = &sessionEventRing{}
			h.rings[sessionID] = ring
		}
		ring.nextSeq++
		seq = ring.nextSeq
	}

	envelope := WSEvent{
		Type:      evType,
		SessionID: sessionID,
		Seq:       seq,
		Timestamp: time.Now().UTC(),
		Payload:   raw,
	}

	bytes, err := json.Marshal(envelope)
	if err != nil {
		h.mu.Unlock()
		return err
	}

	if ring != nil {
		ring.events = append(ring.events, bufferedEvent{seq: seq, data: bytes})
		if len(ring.events) > h.ringSize {
			excess := len(ring.events) - h.ringSize
			ring.events = append([]bufferedEvent(nil), ring.events[excess:]...)
		}
	}
	h.mu.Unlock()

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil
	}

	select {
	case h.broadcast <- bytes:
		return nil
	default:
		if evType == WSEventAgentChunk {
			return errors.Errorf("broadcast queue saturated (chunk dropped: %s)", evType)
		}
		select {
		case h.broadcast <- bytes:
			return nil
		case <-time.After(250 * time.Millisecond):
			return errors.Errorf("broadcast queue saturated (event dropped: %s)", evType)
		}
	}
}

func (h *Hub) handleClientResume(client *Client, sessionID string, lastSeq uint64) {
	h.mu.RLock()
	if h.closed || client.dead {
		h.mu.RUnlock()
		return
	}
	ring := h.rings[sessionID]
	if ring == nil || len(ring.events) == 0 || lastSeq >= ring.nextSeq {
		h.mu.RUnlock()
		return
	}

	oldestSeq := ring.events[0].seq
	currentSeq := ring.nextSeq
	var toSend [][]byte

	if lastSeq+1 < oldestSeq {
		// Gap is larger than the ring buffer: notify the client to refetch full session history.
		payload, _ := json.Marshal(map[string]any{
			"last_seq":    lastSeq,
			"oldest_seq":  oldestSeq,
			"current_seq": currentSeq,
			"reason":      "gap_overflow",
		})
		failBytes, err := json.Marshal(WSEvent{
			Type:      WSEventResumeFailed,
			SessionID: sessionID,
			Seq:       currentSeq,
			Timestamp: time.Now().UTC(),
			Payload:   payload,
		})
		if err == nil {
			toSend = append(toSend, failBytes)
		}
	} else {
		for _, ev := range ring.events {
			if ev.seq > lastSeq {
				toSend = append(toSend, ev.data)
			}
		}
	}
	h.mu.RUnlock()

	for _, frame := range toSend {
		h.mu.RLock()
		dead := h.closed || client.dead
		if !dead {
			select {
			case client.send <- frame:
			default:
			}
		}
		h.mu.RUnlock()
		if dead {
			return
		}
	}
}

// HandleWS handles new incoming WebSocket upgrade requests.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	client := &Client{
		hub:  h,
		conn: conn,
		send: make(chan []byte, 1024),
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return
	}
	h.clients[client] = true
	h.mu.Unlock()

	go client.writePump()
	go client.readPump()
}

func (h *Hub) unregisterClient(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		close(client.send)
	}
}

// Close terminates the hub and disconnects all clients.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true

	for client := range h.clients {
		delete(h.clients, client)
		close(client.send)
	}
	close(h.broadcast)
	h.mu.Unlock()
}
