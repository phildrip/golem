package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/runtime"
)

func TestAttachClientSharedSessionsAndServerRestart(t *testing.T) {
	dir := t.TempDir()
	baseCfg := golem.Config{
		Provider:          "opencode",
		BaseURL:           "http://127.0.0.1:1",
		ModelName:         "test-model",
		APIKey:            "test-key",
		StorageDir:        dir,
		WorkspaceDir:      dir,
		LiveHeadCount:     4,
		ViewBudgetBytes:   1024,
		CompactorInterval: 50 * time.Millisecond,
	}

	// Start server instance #1
	srv1 := NewServer(ServerConfig{
		Host:         "127.0.0.1",
		Token:        "secret-token-123",
		BaseAgentCfg: baseCfg,
	})
	go srv1.Hub().Run()
	if _, err := srv1.SessionManager().CreateSession(context.Background(), "Initial Server Session"); err != nil {
		t.Fatalf("srv1 CreateSession: %v", err)
	}
	ts1 := httptest.NewServer(srv1.httpServer.Handler)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := runtime.NewAttachClient(runtime.AttachConfig{
		BaseURL:           ts1.URL,
		Token:             "secret-token-123",
		ReconnectInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewAttachClient: %v", err)
	}
	defer client.Close()

	// 1. List initial sessions on server #1
	sessions, err := client.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions on srv1: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatalf("expected default session on srv1")
	}

	// 2. Create a shared session via AttachClient and verify server #1 sees it
	created, err := client.CreateSession(ctx, "Shared Attach Session")
	if err != nil {
		t.Fatalf("CreateSession via AttachClient: %v", err)
	}
	if created.Title != "Shared Attach Session" {
		t.Fatalf("unexpected created title: %q", created.Title)
	}

	if err := client.ConnectWS(ctx, created.ID); err != nil {
		t.Fatalf("ConnectWS on srv1: %v", err)
	}

	// 3. Simulate server restart: close server #1 (releasing bbolt locks) and launch server #2
	ts1.Close()
	_ = srv1.Shutdown(ctx)

	srv2 := NewServer(ServerConfig{
		Host:         "127.0.0.1",
		Token:        "secret-token-123",
		BaseAgentCfg: baseCfg,
	})
	go srv2.Hub().Run()
	// Re-create the session on srv2 to simulate persisted session discovery
	if _, err := srv2.SessionManager().CreateSession(ctx, "Shared Attach Session"); err != nil {
		t.Fatalf("srv2 CreateSession: %v", err)
	}
	ts2 := httptest.NewServer(srv2.httpServer.Handler)
	defer func() {
		ts2.Close()
		_ = srv2.Shutdown(ctx)
	}()

	if err := client.SetBaseURL(ts2.URL); err != nil {
		t.Fatalf("SetBaseURL: %v", err)
	}

	// 4. WaitReconnect should succeed against server #2 and return the sessions
	reconnectedSessions, err := client.WaitReconnect(ctx, created.ID)
	if err != nil {
		t.Fatalf("WaitReconnect after server restart: %v", err)
	}
	found := false
	for _, s := range reconnectedSessions {
		if s.Title == "Shared Attach Session" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'Shared Attach Session' after server restart, got %+v", reconnectedSessions)
	}
}
