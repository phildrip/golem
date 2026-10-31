package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Architecture Decision (golem-3m4.5): Should TUI and Web Share Sessions?
//
// Decision: Hybrid model — independent in-process SessionManager by default,
// with opt-in shared runtime attachment via `golem -attach <url>`.
//
// Trade-offs evaluated:
//  1. Independent in-process runtime (`golem` default):
//     - Pros: Zero daemon/server requirement, instant startup, no local port
//       or auth token friction, full offline reliability, and direct in-memory
//       access to `fractal.Store` and `SessionManager` for `Ctrl+N`, `Ctrl+S`,
//       and `Ctrl+W`.
//     - Cons: Bbolt holds an exclusive file lock per open store, so a separate
//       `golem -serve` process cannot simultaneously write to the active
//       session's store unless that session is suspended (golem-3m4.4) or
//       hosted in a single shared process.
//  2. Shared runtime via WebSocket/REST attach (`golem -attach <url>`):
//     - Pros: Both Web UI and CLI/TUI clients observe the exact same
//       `SessionManager`, active sessions, and swarm/graph runs in real time.
//     - Cons: Requires a running `golem -serve` process and reconnect logic
//       when the server restarts.
//
// Resolution:
//   - `pkg/runtime.SessionManager` powers both the standalone TUI and
//     `pkg/server` in-process, with LRU + idle suspension releasing bbolt file
//     locks for inactive sessions.
//   - `AttachClient` (`-attach <url>`) connects to a running `golem -serve`
//     instance over REST + WebSocket, lists and creates shared sessions, and
//     transparently reconnects across server restarts.

// AttachConfig configures an AttachClient connected to a running golem server.
type AttachConfig struct {
	BaseURL            string
	Token              string
	HTTPClient         *http.Client
	ReconnectInterval  time.Duration
	MaxReconnectChecks int
}

// AttachEvent represents a real-time event received from the remote server's
// WebSocket hub.
type AttachEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Seq       uint64          `json:"seq,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// AttachClient connects a CLI or TUI session to a running `golem -serve`
// multiplexer so both surfaces share the same runtime.SessionManager.
type AttachClient struct {
	baseURL           string
	token             string
	httpClient        *http.Client
	reconnectInterval time.Duration

	mu          sync.Mutex
	conn        *websocket.Conn
	lastSeqByID map[string]uint64
	closed      bool
}

// NewAttachClient constructs a client for attaching to a running golem server.
func NewAttachClient(cfg AttachConfig) (*AttachClient, error) {
	raw := strings.TrimSpace(cfg.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("attach base URL is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid attach URL %q: %w", cfg.BaseURL, err)
	}
	u.Path = strings.TrimRight(u.Path, "/")

	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	interval := cfg.ReconnectInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}

	return &AttachClient{
		baseURL:           u.String(),
		token:             strings.TrimSpace(cfg.Token),
		httpClient:        hc,
		reconnectInterval: interval,
		lastSeqByID:       make(map[string]uint64),
	}, nil
}

// SetBaseURL updates the server endpoint (used in tests when a restarted server
// binds to a new ephemeral port).
func (c *AttachClient) SetBaseURL(raw string) error {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	u.Path = strings.TrimRight(u.Path, "/")
	c.mu.Lock()
	c.baseURL = u.String()
	c.mu.Unlock()
	return nil
}

func (c *AttachClient) getBaseURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.baseURL
}

func (c *AttachClient) addAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// ListSessions returns the sessions hosted on the remote server.
func (c *AttachClient) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.getBaseURL()+"/api/sessions", nil)
	if err != nil {
		return nil, err
	}
	c.addAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list sessions failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var list []SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list, nil
}

// CreateSession creates a new session on the remote server.
func (c *AttachClient) CreateSession(ctx context.Context, title string) (SessionInfo, error) {
	payload, _ := json.Marshal(map[string]string{"title": title})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.getBaseURL()+"/api/sessions", bytes.NewReader(payload))
	if err != nil {
		return SessionInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return SessionInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return SessionInfo{}, fmt.Errorf("create session failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var info SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return SessionInfo{}, err
	}
	return info, nil
}

// ActivateSession switches the active session on the remote server.
func (c *AttachClient) ActivateSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/sessions/%s/activate", c.getBaseURL(), url.PathEscape(sessionID)), nil)
	if err != nil {
		return err
	}
	c.addAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("activate session failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// SendPrompt submits a prompt to a session on the remote server.
func (c *AttachClient) SendPrompt(ctx context.Context, sessionID, message string) error {
	payload, _ := json.Marshal(map[string]string{
		"session_id": sessionID,
		"message":    message,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/sessions/%s/prompt", c.getBaseURL(), url.PathEscape(sessionID)), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("send prompt failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// ConnectWS opens a WebSocket stream to the remote server. If sessionID has a
// previously observed sequence number, ConnectWS automatically sends a resume
// frame so missed events from the server's ring buffer are replayed.
func (c *AttachClient) ConnectWS(ctx context.Context, sessionID string) error {
	base := c.getBaseURL()
	wsURL := "ws" + strings.TrimPrefix(base, "http") + "/ws"
	headers := http.Header{}
	if c.token != "" {
		headers.Set("Authorization", "Bearer "+c.token)
	}

	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return err
	}

	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn = conn
	lastSeq := c.lastSeqByID[sessionID]
	c.mu.Unlock()

	if sessionID != "" && lastSeq > 0 {
		resumeFrame := map[string]any{
			"type":       "resume",
			"session_id": sessionID,
			"last_seq":   lastSeq,
		}
		_ = conn.WriteJSON(resumeFrame)
	}
	return nil
}

// ReadEvent reads the next WebSocket event from the remote server, automatically
// recording per-session sequence numbers for seamless resume.
func (c *AttachClient) ReadEvent() (AttachEvent, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return AttachEvent{}, fmt.Errorf("websocket not connected")
	}

	_, data, err := conn.ReadMessage()
	if err != nil {
		return AttachEvent{}, err
	}
	var ev AttachEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return AttachEvent{}, err
	}
	if ev.SessionID != "" && ev.Seq > 0 {
		c.mu.Lock()
		if ev.Seq > c.lastSeqByID[ev.SessionID] {
			c.lastSeqByID[ev.SessionID] = ev.Seq
		}
		c.mu.Unlock()
	}
	return ev, nil
}

// WaitReconnect polls the remote server until it is reachable again after a
// restart, re-establishes the WebSocket stream, and returns the refreshed
// session list.
func (c *AttachClient) WaitReconnect(ctx context.Context, sessionID string) ([]SessionInfo, error) {
	ticker := time.NewTicker(c.reconnectInterval)
	defer ticker.Stop()

	for {
		sessions, err := c.ListSessions(ctx)
		if err == nil {
			_ = c.ConnectWS(ctx, sessionID)
			return sessions, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// LastSeq returns the highest monotonic sequence number observed for sessionID.
func (c *AttachClient) LastSeq(sessionID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSeqByID[sessionID]
}

// Close closes any open WebSocket connection.
func (c *AttachClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}
