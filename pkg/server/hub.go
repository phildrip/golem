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
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
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
}

// NewHub constructs a new WebSocket Hub.
func NewHub() *Hub {
	h := &Hub{
		clients:   make(map[*Client]bool),
		broadcast: make(chan []byte, 1024),
	}
	h.upgrader = websocket.Upgrader{
		CheckOrigin:     h.originAllowed,
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	return h
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

	envelope := WSEvent{
		Type:      evType,
		SessionID: sessionID,
		Timestamp: time.Now().UTC(),
		Payload:   raw,
	}

	bytes, err := json.Marshal(envelope)
	if err != nil {
		return err
	}

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
