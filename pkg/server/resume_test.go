package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/user/golem/pkg/golem"
)

func TestHubResumeReplayAndGapOverflow(t *testing.T) {
	hub := NewHub()
	hub.SetRingBufferSize(5)
	go hub.Run()
	defer hub.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWS(w, r)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Broadcast 5 events on session-A (seq 1..5) before client connects
	for i := 1; i <= 5; i++ {
		if err := hub.Broadcast(WSEventAgentChunk, "session-A", map[string]int{"chunk": i}); err != nil {
			t.Fatalf("Broadcast %d: %v", i, err)
		}
	}

	// Client connects having seen up to seq 2 -> should receive seq 3, 4, 5 in order
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"type":       "resume",
		"session_id": "session-A",
		"last_seq":   2,
	}); err != nil {
		t.Fatalf("WriteJSON resume: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for wantSeq := uint64(3); wantSeq <= 5; wantSeq++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage wantSeq=%d: %v", wantSeq, err)
		}
		var ev WSEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if ev.SessionID != "session-A" || ev.Seq != wantSeq {
			t.Fatalf("got session=%s seq=%d, want session-A seq=%d", ev.SessionID, ev.Seq, wantSeq)
		}
	}

	// Now broadcast 5 more events (seq 6..10), aging seq 1..5 out of the 5-element ring buffer
	for i := 6; i <= 10; i++ {
		if err := hub.Broadcast(WSEventAgentChunk, "session-A", map[string]int{"chunk": i}); err != nil {
			t.Fatalf("Broadcast %d: %v", i, err)
		}
	}

	// A second client reconnects claiming last_seq=2 (gap overflow: oldest in ring is 6)
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial conn2: %v", err)
	}
	defer conn2.Close()

	if err := conn2.WriteJSON(map[string]any{
		"type":       "resume",
		"session_id": "session-A",
		"last_seq":   2,
	}); err != nil {
		t.Fatalf("WriteJSON conn2 resume: %v", err)
	}

	_ = conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn2.ReadMessage()
	if err != nil {
		t.Fatalf("conn2 ReadMessage: %v", err)
	}
	var failEv WSEvent
	if err := json.Unmarshal(data, &failEv); err != nil {
		t.Fatalf("Unmarshal failEv: %v", err)
	}
	if failEv.Type != WSEventResumeFailed {
		t.Fatalf("expected WSEventResumeFailed on gap overflow, got %q", failEv.Type)
	}
}

func TestGraphsEndpoint(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(ServerConfig{
		Host: "127.0.0.1",
		BaseAgentCfg: golem.Config{
			Provider:          "opencode",
			BaseURL:           "http://127.0.0.1:1",
			ModelName:         "test-model",
			APIKey:            "test-key",
			StorageDir:        dir,
			WorkspaceDir:      dir,
			LiveHeadCount:     4,
			ViewBudgetBytes:   1024,
			CompactorInterval: 50 * time.Millisecond,
		},
	})
	defer func() { _ = srv.Shutdown(t.Context()) }()

	req := httptest.NewRequest(http.MethodGet, "/api/graphs", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graphs status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPWAManifestServedAndValid(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(ServerConfig{
		Host:     "127.0.0.1",
		Token:    "require-auth-token",
		StaticFS: GetEmbeddedUIFS(),
		BaseAgentCfg: golem.Config{
			Provider:          "opencode",
			BaseURL:           "http://127.0.0.1:1",
			ModelName:         "test-model",
			APIKey:            "test-key",
			StorageDir:        dir,
			WorkspaceDir:      dir,
			LiveHeadCount:     4,
			ViewBudgetBytes:   1024,
			CompactorInterval: 50 * time.Millisecond,
		},
	})
	defer func() { _ = srv.Shutdown(t.Context()) }()

	// Request without auth token — manifest must still be served so browsers can install PWA
	req := httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /manifest.webmanifest status=%d body=%s", rec.Code, rec.Body.String())
	}
	var manifest struct {
		Name        string `json:"name"`
		ShortName   string `json:"short_name"`
		Display     string `json:"display"`
		ThemeColor  string `json:"theme_color"`
		BgColor     string `json:"background_color"`
		Icons       []any  `json:"icons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest.webmanifest invalid JSON: %v", err)
	}
	if manifest.ShortName != "Golem" || manifest.Display != "standalone" || manifest.ThemeColor != "#0b0f19" {
		t.Fatalf("unexpected manifest fields: %+v", manifest)
	}
	if len(manifest.Icons) < 2 {
		t.Fatalf("expected >= 2 icons in manifest, got %d", len(manifest.Icons))
	}
}
