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
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/golem"
)

// TestApprovalRequestIsBroadcast is the web transport's half of the gate: the
// prompt reaches the browser with the exact command, and the client answers it
// through the REST route.
func TestApprovalRequestIsBroadcast(t *testing.T) {
	hub := NewHub()
	go hub.Run()
	defer hub.Close()

	srv := httptest.NewServer(http.HandlerFunc(hub.HandleWS))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()
	// Give the hub a moment to register the client before broadcasting.
	time.Sleep(50 * time.Millisecond)

	adapter := &sessionStreamAdapter{sessionID: "sess-1", hub: hub}
	adapter.OnApprovalRequest(approval.Request{
		CallID: "conf-1",
		Tool:   "shell",
		Hint:   "shell wants to run: git push --force origin main",
		Action: "git push --force origin main",
		Reason: "a force push rewrites shared history",
	})

	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		_, msgBytes, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("reading event: %v", err)
		}
		var ev WSEvent
		if err := json.Unmarshal(msgBytes, &ev); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if ev.Type != WSEventApprovalRequest {
			continue
		}
		var payload struct {
			CallID string `json:"call_id"`
			Tool   string `json:"tool"`
			Action string `json:"action"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.CallID != "conf-1" || payload.Tool != "shell" {
			t.Fatalf("payload = %+v", payload)
		}
		if payload.Action != "git push --force origin main" {
			t.Fatalf("the prompt lost the exact command: %q", payload.Action)
		}
		return
	}
}

// TestApprovalRouteWithoutPendingCall keeps a stray answer harmless: nothing is
// pending, so GET says so and POST is accepted (the agent reports the mismatch
// over the socket rather than the HTTP call hanging).
func TestApprovalRouteWithoutPendingCall(t *testing.T) {
	tempDir := t.TempDir()
	srv := NewServer(ServerConfig{
		Port: 0,
		BaseAgentCfg: golem.Config{
			Provider:        "opencode",
			BaseURL:         "http://127.0.0.1:1",
			ModelName:       "test-model",
			APIKey:          "test-key",
			StorageDir:      tempDir,
			WorkspaceDir:    tempDir,
			LiveHeadCount:   4,
			ViewBudgetBytes: 1024,
		},
	})
	go srv.hub.Run()
	defer srv.Shutdown(context.Background())
	testServer := httptest.NewServer(srv.httpServer.Handler)
	defer testServer.Close()

	client := testServer.Client()
	createResp, err := client.Post(testServer.URL+"/api/sessions", "application/json", strings.NewReader(`{"title":"Approval Test"}`))
	if err != nil {
		t.Fatalf("POST /api/sessions: %v", err)
	}
	var sessInfo SessionInfo
	if err := json.NewDecoder(createResp.Body).Decode(&sessInfo); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	createResp.Body.Close()

	getResp, err := client.Get(testServer.URL + "/api/sessions/" + sessInfo.ID + "/approval")
	if err != nil {
		t.Fatalf("GET approval: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET approval status = %d", getResp.StatusCode)
	}
	var state struct {
		Pending bool `json:"pending"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&state); err != nil {
		t.Fatalf("decode approval state: %v", err)
	}
	if state.Pending {
		t.Fatal("a fresh session must not have a pending approval")
	}

	postResp, err := client.Post(testServer.URL+"/api/sessions/"+sessInfo.ID+"/approval", "application/json",
		strings.NewReader(`{"call_id":"conf-1","approved":true}`))
	if err != nil {
		t.Fatalf("POST approval: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST approval status = %d, want 202", postResp.StatusCode)
	}

	// The rejected answer must not leave the session wedged: a later turn can
	// still acquire it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		sess, ok := srv.sessions.GetSession(sessInfo.ID)
		if ok {
			sess.mu.Lock()
			busy := sess.isBusy
			sess.mu.Unlock()
			if !busy {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the session stayed busy after a stray approval")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSandboxAuditEndpoint covers the read side of the audit log: the cockpit can
// show what the policy allowed and refused, and says when it is disabled.
func TestSandboxAuditEndpoint(t *testing.T) {
	tempDir := t.TempDir()
	srv := NewServer(ServerConfig{
		Port: 0,
		BaseAgentCfg: golem.Config{
			Provider:        "opencode",
			BaseURL:         "http://127.0.0.1:1",
			ModelName:       "test-model",
			APIKey:          "test-key",
			StorageDir:      tempDir,
			WorkspaceDir:    tempDir,
			LiveHeadCount:   4,
			ViewBudgetBytes: 1024,
		},
	})
	go srv.hub.Run()
	defer srv.Shutdown(context.Background())
	testServer := httptest.NewServer(srv.httpServer.Handler)
	defer testServer.Close()

	approval.SetAuditor(nil)
	client := testServer.Client()
	resp, err := client.Get(testServer.URL + "/api/sandbox/audit")
	if err != nil {
		t.Fatalf("GET audit: %v", err)
	}
	var state struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if state.Enabled {
		t.Fatal("expected the audit log to be reported disabled")
	}

	auditor, err := approval.NewAuditor("", 10)
	if err != nil {
		t.Fatalf("NewAuditor: %v", err)
	}
	approval.SetAuditor(auditor)
	t.Cleanup(func() { approval.SetAuditor(nil) })
	auditor.Record(approval.Record{Tool: "shell", Verdict: "deny", Rule: "privilege-escalation", Action: "sudo ls"})

	resp, err = client.Get(testServer.URL + "/api/sandbox/audit")
	if err != nil {
		t.Fatalf("GET audit: %v", err)
	}
	defer resp.Body.Close()
	var full struct {
		Enabled bool              `json:"enabled"`
		Records []approval.Record `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&full); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !full.Enabled || len(full.Records) != 1 || full.Records[0].Action != "sudo ls" {
		t.Fatalf("audit payload = %+v", full)
	}
}
