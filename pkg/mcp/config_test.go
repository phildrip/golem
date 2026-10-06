package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigData_AntigravityAndCursorFormat(t *testing.T) {
	data := []byte(`{
		"mcpServers": {
			"github": {
				"command": "npx",
				"args": ["-y", "@modelcontextprotocol/server-github"],
				"env": {
					"GITHUB_TOKEN": "ghp_12345"
				}
			},
			"remote-api": {
				"url": "https://api.example.com/mcp"
			}
		}
	}`)

	cfg, err := ParseConfigData(data)
	if err != nil {
		t.Fatalf("ParseConfigData failed: %v", err)
	}

	if len(cfg.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(cfg.Servers))
	}

	gh, ok := cfg.Servers["github"]
	if !ok {
		t.Fatalf("missing github server")
	}
	if gh.Command != "npx" || len(gh.Args) != 2 || gh.Env["GITHUB_TOKEN"] != "ghp_12345" {
		t.Errorf("unexpected github config: %+v", gh)
	}
	if gh.IsRemote() {
		t.Errorf("expected github to not be remote")
	}

	remote, ok := cfg.Servers["remote-api"]
	if !ok {
		t.Fatalf("missing remote-api server")
	}
	if !remote.IsRemote() || remote.EndpointURL() != "https://api.example.com/mcp" {
		t.Errorf("unexpected remote config: %+v", remote)
	}
}

func TestParseConfigData_VSCodeFormat(t *testing.T) {
	data := []byte(`{
		"servers": {
			"gopls": {
				"type": "stdio",
				"command": "gopls",
				"args": ["mcp"]
			}
		}
	}`)

	cfg, err := ParseConfigData(data)
	if err != nil {
		t.Fatalf("ParseConfigData failed: %v", err)
	}

	gopls, ok := cfg.Servers["gopls"]
	if !ok {
		t.Fatalf("missing gopls server")
	}
	if gopls.Command != "gopls" || len(gopls.Args) != 1 || gopls.Args[0] != "mcp" {
		t.Errorf("unexpected gopls config: %+v", gopls)
	}
}

func TestParseConfigData_EnvExpansion(t *testing.T) {
	t.Setenv("TEST_MCP_BIN", "my-tool")
	t.Setenv("TEST_MCP_SECRET", "super-secret-key")

	data := []byte(`{
		"mcpServers": {
			"tool": {
				"command": "${TEST_MCP_BIN}",
				"args": ["--key", "$TEST_MCP_SECRET"],
				"env": {
					"KEY": "$TEST_MCP_SECRET"
				}
			}
		}
	}`)

	cfg, err := ParseConfigData(data)
	if err != nil {
		t.Fatalf("ParseConfigData failed: %v", err)
	}

	tool := cfg.Servers["tool"]
	if tool.Command != "my-tool" {
		t.Errorf("expected command 'my-tool', got %q", tool.Command)
	}
	if tool.Args[1] != "super-secret-key" {
		t.Errorf("expected arg 'super-secret-key', got %q", tool.Args[1])
	}
	if tool.Env["KEY"] != "super-secret-key" {
		t.Errorf("expected env 'super-secret-key', got %q", tool.Env["KEY"])
	}
}

func TestDiscoverAndMerge(t *testing.T) {
	tmpDir := t.TempDir()
	wsDir := filepath.Join(tmpDir, "workspace")
	if err := os.MkdirAll(filepath.Join(wsDir, ".golem"), 0755); err != nil {
		t.Fatal(err)
	}

	wsMcp := []byte(`{
		"mcpServers": {
			"workspace-tool": {
				"command": "echo",
				"args": ["hello"]
			}
		}
	}`)
	if err := os.WriteFile(filepath.Join(wsDir, ".golem", "mcp.json"), wsMcp, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, loaded, err := DiscoverAndMerge(wsDir)
	if err != nil {
		t.Fatalf("DiscoverAndMerge failed: %v", err)
	}

	if _, ok := cfg.Servers["workspace-tool"]; !ok {
		t.Errorf("expected workspace-tool to be loaded")
	}

	if len(loaded) == 0 {
		t.Errorf("expected loadedFiles to not be empty")
	}
}

func TestDiscoveryPaths_OnlyGolemDirs(t *testing.T) {
	wsDir := "/test/workspace"
	paths := DiscoveryPaths(wsDir)

	// Must contain workspace .golem/mcp.json
	wsPath := filepath.Join(wsDir, ".golem", "mcp.json")
	foundWs := false
	for _, p := range paths {
		if p == wsPath {
			foundWs = true
		}
		// Must NOT contain other IDEs
		if filepath.Base(filepath.Dir(p)) == ".cursor" ||
			filepath.Base(filepath.Dir(p)) == ".vscode" ||
			filepath.Base(filepath.Dir(p)) == "antigravity-cli" ||
			filepath.Base(filepath.Dir(p)) == "claude" ||
			filepath.Base(filepath.Dir(p)) == "Code" {
			t.Errorf("DiscoveryPaths should NOT include external IDE path: %s", p)
		}
	}

	if !foundWs {
		t.Errorf("expected workspace .golem/mcp.json in paths, got: %v", paths)
	}

	// Test GOLEM_CONFIG_DIR override
	t.Setenv("GOLEM_CONFIG_DIR", "/custom/golem/cfg")
	overridePaths := DiscoveryPaths(wsDir)
	foundCustom := false
	for _, p := range overridePaths {
		if p == "/custom/golem/cfg/mcp.json" {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Errorf("expected /custom/golem/cfg/mcp.json when GOLEM_CONFIG_DIR is set, got: %v", overridePaths)
	}
}

func TestDiscoverAndMerge_IgnoresOtherIDEs(t *testing.T) {
	tmpDir := t.TempDir()
	wsDir := filepath.Join(tmpDir, "workspace")

	// Create .cursor/mcp.json which should be ignored
	if err := os.MkdirAll(filepath.Join(wsDir, ".cursor"), 0755); err != nil {
		t.Fatal(err)
	}
	cursorMcp := []byte(`{
		"mcpServers": {
			"cursor-tool": {
				"command": "echo",
				"args": ["cursor"]
			}
		}
	}`)
	if err := os.WriteFile(filepath.Join(wsDir, ".cursor", "mcp.json"), cursorMcp, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := DiscoverAndMerge(wsDir)
	if err != nil {
		t.Fatalf("DiscoverAndMerge failed: %v", err)
	}

	// cursor-tool should NOT be loaded
	if _, ok := cfg.Servers["cursor-tool"]; ok {
		t.Errorf("cursor-tool should NOT have been loaded from .cursor/mcp.json")
	}
}
