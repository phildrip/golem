package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/golem"
)

// TestPatchRouteObeysThePolicy covers golem-9uw.6: /api/patch is a human action,
// but it must not be a way around the profile's write rules. A path that passes
// the traversal check and still resolves outside the workspace (a symlink) is
// refused, because the sandbox says no.
func TestPatchRouteObeysThePolicy(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	srv := NewServer(ServerConfig{
		Port: 0,
		BaseAgentCfg: golem.Config{
			Provider:        "opencode",
			BaseURL:         "http://127.0.0.1:1",
			ModelName:       "test-model",
			APIKey:          "test-key",
			StorageDir:      t.TempDir(),
			WorkspaceDir:    ws,
			LiveHeadCount:   4,
			ViewBudgetBytes: 1024,
			FileConfig: config.FileConfig{
				Sandbox: &config.SandboxConfig{Mode: "policy"},
			},
		},
	})
	go srv.hub.Run()
	defer srv.Shutdown(context.Background())
	testServer := httptest.NewServer(srv.httpServer.Handler)
	defer testServer.Close()

	body := `{"file_path": "escape/target.txt", "op": "SWAP", "anchor": "1:ff", "content": "x"}`
	resp, err := testServer.Client().Post(testServer.URL+"/api/patch", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/patch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		t.Fatalf("status = %d (%v), want 403 Forbidden", resp.StatusCode, payload)
	}
	if _, err := os.Stat(filepath.Join(outside, "target.txt")); err == nil {
		t.Fatal("the patch escaped the workspace")
	}
}
