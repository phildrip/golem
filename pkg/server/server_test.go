package server

import (
	"bytes"
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
	"github.com/user/golem/pkg/config"
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

	// Rapidly broadcast messages beyond client queue buffer (1024)
	for i := 0; i < 1200; i++ {
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

func TestProfileEndpoints(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "golem-test-server-profiles-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("GOLEM_CONFIG_DIR", filepath.Join(tempDir, "user_golem"))

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

	// 1. GET /api/profiles
	resp, err := client.Get(testServer.URL + "/api/profiles")
	if err != nil {
		t.Fatalf("GET /api/profiles failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var getResp ProfilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode GET /api/profiles failed: %v", err)
	}
	resp.Body.Close()

	// 2. POST /api/profiles (create 'fast' profile, set active)
	saveReq := SaveProfileRequest{
		Name:      "fast",
		Scope:     "workspace",
		Provider:  "gemini",
		Model:     "gemini-2.5-flash",
		SetActive: true,
		Compactor: &config.CompactorConfig{
			Provider: "gemini",
			Model:    "gemini-2.5-flash",
		},
	}
	reqBytes, _ := json.Marshal(saveReq)
	resp, err = client.Post(testServer.URL+"/api/profiles", "application/json", bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("POST /api/profiles failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var postResp ProfilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&postResp); err != nil {
		t.Fatalf("decode POST /api/profiles failed: %v", err)
	}
	resp.Body.Close()

	if postResp.ActiveProfile != "fast" {
		t.Errorf("expected active profile 'fast', got %q", postResp.ActiveProfile)
	}
	if _, ok := postResp.Profiles["fast"]; !ok {
		t.Fatalf("expected 'fast' in profiles map")
	}

	// 3. POST /api/profiles (create 'coding' profile)
	saveReq2 := SaveProfileRequest{
		Name:      "coding",
		Scope:     "workspace",
		Provider:  "opencode",
		Model:     "deepseek-chat",
		SetActive: false,
	}
	reqBytes2, _ := json.Marshal(saveReq2)
	resp, err = client.Post(testServer.URL+"/api/profiles", "application/json", bytes.NewReader(reqBytes2))
	if err != nil {
		t.Fatalf("POST /api/profiles (2) failed: %v", err)
	}
	resp.Body.Close()

	// 4. POST /api/profiles/active (switch to 'coding')
	switchReq := SetActiveProfileRequest{
		Name:  "coding",
		Scope: "workspace",
	}
	switchBytes, _ := json.Marshal(switchReq)
	resp, err = client.Post(testServer.URL+"/api/profiles/active", "application/json", bytes.NewReader(switchBytes))
	if err != nil {
		t.Fatalf("POST /api/profiles/active failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var switchResp ProfilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&switchResp); err != nil {
		t.Fatalf("decode switch active failed: %v", err)
	}
	resp.Body.Close()

	if switchResp.ActiveProfile != "coding" {
		t.Errorf("expected active profile 'coding', got %q", switchResp.ActiveProfile)
	}

	// 5. DELETE /api/profiles/fast
	delReq, _ := http.NewRequest(http.MethodDelete, testServer.URL+"/api/profiles/fast", nil)
	resp, err = client.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE /api/profiles/fast failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}
	var delResp ProfilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&delResp); err != nil {
		t.Fatalf("decode delete response failed: %v", err)
	}
	resp.Body.Close()

	if _, ok := delResp.Profiles["fast"]; ok {
		t.Errorf("expected 'fast' to be removed from profiles")
	}

	// 6. DELETE active profile -> 400 Bad Request
	badDelActive, _ := http.NewRequest(http.MethodDelete, testServer.URL+"/api/profiles/coding", nil)
	resp, err = client.Do(badDelActive)
	if err != nil {
		t.Fatalf("DELETE active failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for deleting active profile, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 7. DELETE nonexistent profile -> 404
	badDelReq, _ := http.NewRequest(http.MethodDelete, testServer.URL+"/api/profiles/does-not-exist", nil)
	resp, err = client.Do(badDelReq)
	if err != nil {
		t.Fatalf("DELETE nonexistent failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 8. POST with invalid profile name -> 400
	invalidNameReq := SaveProfileRequest{
		Name:     "../../evil",
		Provider: "gemini",
		Model:    "gemini-flash-latest",
	}
	b, _ := json.Marshal(invalidNameReq)
	resp, err = client.Post(testServer.URL+"/api/profiles", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST invalid name failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for invalid profile name, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 9. POST with unsupported provider -> 400
	invalidProvReq := SaveProfileRequest{
		Name:     "unsupported",
		Provider: "fake-provider",
		Model:    "test-model",
	}
	b, _ = json.Marshal(invalidProvReq)
	resp, err = client.Post(testServer.URL+"/api/profiles", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST unsupported provider failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for unsupported provider, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 10. API Key Masking & Unmasking roundtrip
	secretReq := SaveProfileRequest{
		Name:     "secret-prof",
		Provider: "gemini",
		Model:    "gemini-flash-latest",
		APIKey:   "sk-real-super-secret-12345",
	}
	b, _ = json.Marshal(secretReq)
	resp, err = client.Post(testServer.URL+"/api/profiles", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST secret profile failed: %v", err)
	}
	var secretResp ProfilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&secretResp); err != nil {
		t.Fatalf("decode secret resp failed: %v", err)
	}
	resp.Body.Close()

	if secretResp.Profiles["secret-prof"].APIKey != "••••••••" {
		t.Errorf("expected masked API key '••••••••', got %q", secretResp.Profiles["secret-prof"].APIKey)
	}

	// Update secret-prof with masked placeholder preserved
	secretUpdateReq := SaveProfileRequest{
		Name:     "secret-prof",
		Provider: "gemini",
		Model:    "gemini-2.5-pro",
		APIKey:   "••••••••", // Send back placeholder
	}
	b, _ = json.Marshal(secretUpdateReq)
	resp, err = client.Post(testServer.URL+"/api/profiles", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST update secret profile failed: %v", err)
	}
	resp.Body.Close()

	// Verify disk config actually kept the real key!
	userCfg, _ := config.Load(filepath.Join(tempDir, ".golem", "config.json"))
	if userCfg.Profiles["secret-prof"].APIKey != "sk-real-super-secret-12345" {
		t.Errorf("expected real secret key on disk to be preserved, got %q", userCfg.Profiles["secret-prof"].APIKey)
	}
}

func TestGetModelsEndpoint(t *testing.T) {
	srv := NewServer(ServerConfig{
		Port: 0,
	})
	testServer := httptest.NewServer(srv.httpServer.Handler)
	defer testServer.Close()

	client := testServer.Client()

	// 1. GET /api/models?provider=opencode
	resp, err := client.Get(testServer.URL + "/api/models?provider=opencode")
	if err != nil {
		t.Fatalf("GET /api/models failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	var modResp ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modResp); err != nil {
		t.Fatalf("decode models response failed: %v", err)
	}

	if modResp.Provider != "opencode" {
		t.Errorf("expected provider opencode, got %q", modResp.Provider)
	}
	if len(modResp.Models) == 0 {
		t.Fatalf("expected non-empty models list")
	}

	// 2. GET /api/models?provider=gemini
	resp2, err := client.Get(testServer.URL + "/api/models?provider=gemini")
	if err != nil {
		t.Fatalf("GET /api/models gemini failed: %v", err)
	}
	defer resp2.Body.Close()

	var gemResp ModelsResponse
	if err := json.NewDecoder(resp2.Body).Decode(&gemResp); err != nil {
		t.Fatalf("decode gemini response failed: %v", err)
	}
	if len(gemResp.Models) == 0 || gemResp.Models[0] != "gemini-2.5-flash" {
		t.Errorf("unexpected gemini models: %v", gemResp.Models)
	}
}

func TestPromptStreamingAndEventDelivery(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"role":"assistant","content":"Testing"}}]}`)
		flusher.Flush()

		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":" response"}}]}`)
		flusher.Flush()

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mockServer.Close()

	tempDir := t.TempDir()
	baseCfg := golem.Config{
		Provider:        "opencode",
		BaseURL:         mockServer.URL,
		ModelName:       "test-model",
		APIKey:          "test-key",
		StorageDir:      tempDir,
		WorkspaceDir:    tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	srv := NewServer(ServerConfig{
		Port:         0,
		BaseAgentCfg: baseCfg,
	})
	go srv.hub.Run()
	defer srv.Shutdown(context.Background())
	testServer := httptest.NewServer(srv.httpServer.Handler)
	defer testServer.Close()

	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer ws.Close()

	client := testServer.Client()
	createResp, err := client.Post(testServer.URL+"/api/sessions", "application/json", strings.NewReader(`{"title":"Stream Test"}`))
	if err != nil {
		t.Fatalf("POST /api/sessions failed: %v", err)
	}
	var sessInfo SessionInfo
	if err := json.NewDecoder(createResp.Body).Decode(&sessInfo); err != nil {
		t.Fatalf("decode session failed: %v", err)
	}
	createResp.Body.Close()

	promptResp, err := client.Post(testServer.URL+"/api/sessions/"+sessInfo.ID+"/prompt", "application/json", strings.NewReader(`{"prompt":"hello"}`))
	if err != nil {
		t.Fatalf("POST /api/sessions/{id}/prompt failed: %v", err)
	}
	if promptResp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted, got %d", promptResp.StatusCode)
	}
	promptResp.Body.Close()

	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var receivedChunks []string
	var receivedDeltas []string
	var sawPrompt bool
	var sawDone bool
	var doneResponse string

	for {
		_, msgBytes, err := ws.ReadMessage()
		if err != nil {
			break
		}
		var ev WSEvent
		if err := json.Unmarshal(msgBytes, &ev); err != nil {
			continue
		}
		if ev.SessionID != sessInfo.ID {
			continue
		}

		switch ev.Type {
		case WSEventUserPrompt:
			sawPrompt = true
		case WSEventAgentChunk:
			var payload struct {
				Token string `json:"token"`
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			if payload.Token != "" {
				receivedChunks = append(receivedChunks, payload.Token)
			}
			if payload.Delta != "" {
				receivedDeltas = append(receivedDeltas, payload.Delta)
			}
		case WSEventAgentDone:
			sawDone = true
			var payload struct {
				Response string `json:"response"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			doneResponse = payload.Response
		}

		if sawDone {
			break
		}
	}

	if !sawPrompt {
		t.Errorf("expected user_prompt event")
	}
	if len(receivedChunks) == 0 {
		t.Errorf("expected agent_chunk events with token")
	}
	if len(receivedDeltas) == 0 {
		t.Errorf("expected agent_chunk events with delta")
	}
	if len(receivedChunks) != len(receivedDeltas) {
		t.Errorf("expected equal number of tokens and deltas, got %d vs %d", len(receivedChunks), len(receivedDeltas))
	}
	if !sawDone {
		t.Errorf("expected agent_done event")
	}
	if doneResponse != "Testing response" {
		t.Errorf("expected done response 'Testing response', got %q", doneResponse)
	}
}

func TestPromptErrorSurfacingAndEventDelivery(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer mockServer.Close()

	tempDir := t.TempDir()
	baseCfg := golem.Config{
		Provider:        "opencode",
		BaseURL:         mockServer.URL,
		ModelName:       "test-model",
		APIKey:          "test-key",
		StorageDir:      tempDir,
		WorkspaceDir:    tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	srv := NewServer(ServerConfig{
		Port:         0,
		BaseAgentCfg: baseCfg,
	})
	go srv.hub.Run()
	defer srv.Shutdown(context.Background())
	testServer := httptest.NewServer(srv.httpServer.Handler)
	defer testServer.Close()

	wsURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v", err)
	}
	defer ws.Close()

	client := testServer.Client()
	createResp, err := client.Post(testServer.URL+"/api/sessions", "application/json", strings.NewReader(`{"title":"Error Test"}`))
	if err != nil {
		t.Fatalf("POST /api/sessions failed: %v", err)
	}
	var sessInfo SessionInfo
	if err := json.NewDecoder(createResp.Body).Decode(&sessInfo); err != nil {
		t.Fatalf("decode session failed: %v", err)
	}
	createResp.Body.Close()

	promptResp, err := client.Post(testServer.URL+"/api/sessions/"+sessInfo.ID+"/prompt", "application/json", strings.NewReader(`{"prompt":"trigger error"}`))
	if err != nil {
		t.Fatalf("POST /api/sessions/{id}/prompt failed: %v", err)
	}
	if promptResp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted, got %d", promptResp.StatusCode)
	}
	promptResp.Body.Close()

	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var sawPrompt bool
	var sawDone bool
	var doneError string

	for {
		_, msgBytes, err := ws.ReadMessage()
		if err != nil {
			break
		}
		var ev WSEvent
		if err := json.Unmarshal(msgBytes, &ev); err != nil {
			continue
		}
		if ev.SessionID != sessInfo.ID {
			continue
		}

		switch ev.Type {
		case WSEventUserPrompt:
			sawPrompt = true
		case WSEventAgentDone:
			sawDone = true
			var payload struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(ev.Payload, &payload)
			doneError = payload.Error
		}

		if sawDone {
			break
		}
	}

	if !sawPrompt {
		t.Errorf("expected user_prompt event")
	}
	if !sawDone {
		t.Errorf("expected agent_done event")
	}
	if doneError == "" {
		t.Errorf("expected non-empty error in agent_done payload")
	}
}

func TestPromptConflict409(t *testing.T) {
	blockTurn := make(chan struct{})
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockTurn
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"done"}}]}`)
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer mockServer.Close()

	tempDir := t.TempDir()
	baseCfg := golem.Config{
		Provider:        "opencode",
		BaseURL:         mockServer.URL,
		ModelName:       "test-model",
		StorageDir:      tempDir,
		WorkspaceDir:    tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	srv := NewServer(ServerConfig{
		Port:         0,
		BaseAgentCfg: baseCfg,
	})
	go srv.hub.Run()
	defer srv.Shutdown(context.Background())
	testServer := httptest.NewServer(srv.httpServer.Handler)
	defer testServer.Close()

	client := testServer.Client()
	createResp, err := client.Post(testServer.URL+"/api/sessions", "application/json", strings.NewReader(`{"title":"Conflict Test"}`))
	if err != nil {
		t.Fatalf("POST /api/sessions failed: %v", err)
	}
	var sessInfo SessionInfo
	_ = json.NewDecoder(createResp.Body).Decode(&sessInfo)
	createResp.Body.Close()

	// Launch first prompt
	resp1, err := client.Post(testServer.URL+"/api/sessions/"+sessInfo.ID+"/prompt", "application/json", strings.NewReader(`{"prompt":"turn 1"}`))
	if err != nil {
		t.Fatalf("first prompt failed: %v", err)
	}
	if resp1.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted for first prompt, got %d", resp1.StatusCode)
	}
	t.Log("posted prompt 1, status:", resp1.StatusCode)
	resp1.Body.Close()

	// Immediately attempt second concurrent prompt on the same session
	resp2, err := client.Post(testServer.URL+"/api/sessions/"+sessInfo.ID+"/prompt", "application/json", strings.NewReader(`{"prompt":"turn 2"}`))
	if err != nil {
		t.Fatalf("second prompt failed: %v", err)
	}
	defer resp2.Body.Close()
	t.Log("posted prompt 2, status:", resp2.StatusCode)

	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 Conflict for overlapping turn, got %d", resp2.StatusCode)
	}

	// Unblock first prompt turn so it completes before shutdown
	t.Log("closing blockTurn")
	close(blockTurn)
	t.Log("test body finished")
}

func TestSessionManager_DeleteActiveTurn(t *testing.T) {
	blockTurn := make(chan struct{})
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockTurn
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"done"}}]}`)
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer mockServer.Close()

	tempDir := t.TempDir()
	baseCfg := golem.Config{
		Provider:        "opencode",
		BaseURL:         mockServer.URL,
		ModelName:       "test-model",
		StorageDir:      tempDir,
		WorkspaceDir:    tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	sm := NewSessionManager(baseCfg, NewHub())
	sess, err := sm.CreateSession(context.Background(), "Delete Test")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	turnAcquired := make(chan struct{})
	go func() {
		close(turnAcquired)
		_, _ = sm.SendPrompt(context.Background(), sess.ID, "in flight prompt")
	}()

	<-turnAcquired
	time.Sleep(20 * time.Millisecond)

	// Close the turn block to allow clean abort
	close(blockTurn)

	// DeleteSession while turn is executing should acquire lock and not panic or corrupt store
	if err := sm.DeleteSession(sess.ID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}
}


