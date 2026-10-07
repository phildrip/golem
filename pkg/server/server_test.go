package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/user/golem/pkg/golem"
)

func TestHub_BroadcastAndClientReceive(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	defer hub.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWS(w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	// Broadcast an event
	payload := map[string]string{"msg": "hello from test"}
	err = hub.Broadcast(WSEventUserPrompt, "sess-1", payload)
	if err != nil {
		t.Fatalf("broadcast failed: %v", err)
	}

	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msgBytes, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read websocket message: %v", err)
	}

	var ev WSEvent
	if err := json.Unmarshal(msgBytes, &ev); err != nil {
		t.Fatalf("failed to unmarshal WSEvent: %v", err)
	}

	if ev.Type != WSEventUserPrompt {
		t.Errorf("expected type %s, got %s", WSEventUserPrompt, ev.Type)
	}
	if ev.SessionID != "sess-1" {
		t.Errorf("expected session_id sess-1, got %s", ev.SessionID)
	}
}

func TestServer_RESTEndpoints(t *testing.T) {
	tempDir := t.TempDir()
	cfg := ServerConfig{
		Port: 8089,
		BaseAgentCfg: golem.Config{
			StorageDir:   tempDir + "/storage",
			WorkspaceDir: tempDir,
		},
	}
	srv := NewServer(cfg)
	defer srv.Shutdown(context.Background())

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	testServer := httptest.NewServer(mux)
	defer testServer.Close()

	client := testServer.Client()

	// 1. GET /api/sessions -> empty list
	resp, err := client.Get(testServer.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("GET /api/sessions failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// 2. POST /api/sessions -> create session
	createBody := strings.NewReader(`{"title": "Test Session"}`)
	resp, err = client.Post(testServer.URL+"/api/sessions", "application/json", createBody)
	if err != nil {
		t.Fatalf("POST /api/sessions failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var sessInfo SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&sessInfo); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if sessInfo.Title != "Test Session" {
		t.Errorf("expected title 'Test Session', got %q", sessInfo.Title)
	}
	if sessInfo.ID == "" {
		t.Fatalf("expected non-empty session ID")
	}

	// 3. GET /api/sessions/{id}
	resp, err = client.Get(testServer.URL + "/api/sessions/" + sessInfo.ID)
	if err != nil {
		t.Fatalf("GET session failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// 4. POST /api/sessions/{id}/activate
	resp, err = client.Post(testServer.URL+"/api/sessions/"+sessInfo.ID+"/activate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST activate failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// 5. DELETE /api/sessions/{id}
	req, _ := http.NewRequest(http.MethodDelete, testServer.URL+"/api/sessions/"+sessInfo.ID, nil)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("DELETE session failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// 6. Test Path Traversal rejection in /api/patch
	badPatch := `{"file_path": "../../etc/passwd", "op": "SWAP", "anchor": "1:ff", "content": "bad"}`
	resp, err = client.Post(testServer.URL+"/api/patch", "application/json", strings.NewReader(badPatch))
	if err != nil {
		t.Fatalf("POST /api/patch failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for path traversal, got %d", resp.StatusCode)
	}

	// Test exact ".." traversal rejection
	badPatchDotDot := `{"file_path": "..", "op": "SWAP", "anchor": "1:ff", "content": "bad"}`
	resp, err = client.Post(testServer.URL+"/api/patch", "application/json", strings.NewReader(badPatchDotDot))
	if err != nil {
		t.Fatalf("POST /api/patch failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for '..' path traversal, got %d", resp.StatusCode)
	}
}

func TestHub_ClientQueueSaturation(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	defer hub.Close()

	// Slow/dead client that does not read
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.HandleWS(w, r)
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer ws.Close()

	// Rapidly broadcast messages beyond client queue buffer (256)
	for i := 0; i < 300; i++ {
		_ = hub.Broadcast(WSEventUserPrompt, "sess", map[string]int{"idx": i})
	}

	// Hub should gracefully handle saturation without crashing or hanging
	time.Sleep(50 * time.Millisecond)
}
