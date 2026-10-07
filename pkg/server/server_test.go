package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/importer"
	"github.com/user/golem/pkg/memory/fractal"
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

	// 5. Test GET /api/sessions/{id}/memory
	memResp, err := client.Get(testServer.URL + "/api/sessions/" + sessInfo.ID + "/memory")
	if err != nil {
		t.Fatalf("GET /api/sessions/{id}/memory failed: %v", err)
	}
	if memResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for memory endpoint, got %d", memResp.StatusCode)
	}
	var memEvents []fractal.Event
	if err := json.NewDecoder(memResp.Body).Decode(&memEvents); err != nil {
		t.Fatalf("failed decoding memory events: %v", err)
	}
	memResp.Body.Close()

	// 6. DELETE /api/sessions/{id}
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

func TestImporterEndpoints(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "golem-test-server-import-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create dummy AGY brain session
	t.Setenv("AGY_BRAIN_DIR", tempDir)
	convDir := filepath.Join(tempDir, "conv-srv", ".system_generated", "logs")
	_ = os.MkdirAll(convDir, 0755)
	_ = os.WriteFile(filepath.Join(convDir, "transcript.jsonl"), []byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-10-05T15:46:40Z","content":"<USER_REQUEST>\nServer import test\n</USER_REQUEST>"}`+"\n"), 0644)

	baseCfg := golem.Config{
		WorkspaceDir: tempDir,
		StorageDir:   filepath.Join(tempDir, "sessions"),
		ModelName:    "gemini-flash-latest",
	}

	srv := NewServer(ServerConfig{
		Port:         0,
		BaseAgentCfg: baseCfg,
	})
	testServer := httptest.NewServer(srv.httpServer.Handler)
	defer testServer.Close()

	client := testServer.Client()

	// 1. GET /api/importer/discover
	resp, err := client.Get(testServer.URL + "/api/importer/discover")
	if err != nil {
		t.Fatalf("GET /api/importer/discover failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var discovered []importer.DiscoveredSession
	if err := json.NewDecoder(resp.Body).Decode(&discovered); err != nil {
		t.Fatalf("decode discover response failed: %v", err)
	}
	resp.Body.Close()

	if len(discovered) == 0 {
		t.Fatalf("expected at least 1 discovered session, got 0")
	}

	// 2. POST /api/importer/import
	importBody := fmt.Sprintf(`{"path":%q,"title":"Imported Server Test"}`, filepath.Join(tempDir, "conv-srv"))
	resp, err = client.Post(testServer.URL+"/api/importer/import", "application/json", strings.NewReader(importBody))
	if err != nil {
		t.Fatalf("POST /api/importer/import failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}

	var sess SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		t.Fatalf("decode import response failed: %v", err)
	}
	resp.Body.Close()

	if sess.Title != "Imported Server Test" {
		t.Errorf("expected session title 'Imported Server Test', got %q", sess.Title)
	}
	if sess.TurnCount != 1 {
		t.Errorf("expected TurnCount=1 from imported event, got %d", sess.TurnCount)
	}
	if !sess.IsActive {
		t.Errorf("expected IsActive=true for newly imported session")
	}

	// 3. Security test: Forbidden path outside workspace/allowed roots
	resp, err = client.Post(testServer.URL+"/api/importer/import", "application/json", strings.NewReader(`{"path":"/etc/passwd"}`))
	if err != nil {
		t.Fatalf("POST forbidden path failed: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for /etc/passwd, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 4. Security test: Nonexistent path
	resp, err = client.Post(testServer.URL+"/api/importer/import", "application/json", strings.NewReader(`{"path":"/nonexistent/file/path"}`))
	if err != nil {
		t.Fatalf("POST nonexistent path failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for nonexistent path, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
