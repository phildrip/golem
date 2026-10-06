package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mockMCPServerScript creates a mock stdio server implementing initialize and tools/list.
const mockMCPServerScript = `#!/usr/bin/env python3
import sys, json

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    req = json.loads(line)
    method = req.get("method")
    req_id = req.get("id")

    if method == "initialize":
        res = {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "protocolVersion": "2024-11-05",
                "serverInfo": {"name": "mock-mcp", "version": "1.0"},
                "capabilities": {}
            }
        }
        sys.stdout.write(json.dumps(res) + "\n")
        sys.stdout.flush()
    elif method == "notifications/initialized":
        pass
    elif method == "tools/list":
        res = {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "tools": [
                    {
                        "name": "echo_test",
                        "description": "Echoes back the input text",
                        "inputSchema": {
                            "type": "object",
                            "properties": {
                                "text": {"type": "string"}
                            },
                            "required": ["text"]
                        }
                    }
                ]
            }
        }
        sys.stdout.write(json.dumps(res) + "\n")
        sys.stdout.flush()
    elif method == "tools/call":
        params = req.get("params", {})
        args = params.get("arguments", {})
        val = args.get("text", "empty")
        res = {
            "jsonrpc": "2.0",
            "id": req_id,
            "result": {
                "content": [
                    {"type": "text", "text": "echoed: " + val}
                ]
            }
        }
        sys.stdout.write(json.dumps(res) + "\n")
        sys.stdout.flush()
`

func TestClient_LifecycleAndCallTool(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "mock_server.py")
	if err := os.WriteFile(scriptPath, []byte(mockMCPServerScript), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := ServerConfig{
		Command: "python3",
		Args:    []string{scriptPath},
	}

	client := NewClient("mock-test", cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Start(ctx); err != nil {
		t.Fatalf("client.Start failed: %v", err)
	}
	defer client.Stop()

	// Verify server info & tools
	if client.ServerInfo().Name != "mock-mcp" {
		t.Errorf("expected server name 'mock-mcp', got %q", client.ServerInfo().Name)
	}

	tools := client.Tools()
	if len(tools) != 1 || tools[0].Name != "echo_test" {
		t.Fatalf("expected tool 'echo_test', got %+v", tools)
	}

	// Call tool
	res, err := client.CallTool(ctx, "echo_test", map[string]any{"text": "hello world"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if len(res.Content) == 0 || res.Content[0].Text != "echoed: hello world" {
		t.Errorf("unexpected call tool result: %+v", res)
	}
}

func TestManager_LoadAndStatus(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "mock_server.py")
	if err := os.WriteFile(scriptPath, []byte(mockMCPServerScript), 0755); err != nil {
		t.Fatal(err)
	}

	mgr := NewManager()
	defer mgr.Close()

	cfg := Config{
		Servers: map[string]ServerConfig{
			"mock-1": {
				Command: "python3",
				Args:    []string{scriptPath},
			},
			"remote-1": {
				URL: "https://example.com/mcp",
			},
		},
	}

	ctx := context.Background()
	mgr.LoadConfig(ctx, cfg)

	// Allow goroutine to start
	time.Sleep(300 * time.Millisecond)

	active := mgr.ActiveClients()
	if len(active) != 1 {
		t.Errorf("expected 1 active client, got %d", len(active))
	}

	running, toolCount, isRemote, err := mgr.ServerStatus("mock-1")
	if err != nil || !running || toolCount != 1 || isRemote {
		t.Errorf("unexpected status for mock-1: running=%v, count=%d, remote=%v, err=%v", running, toolCount, isRemote, err)
	}

	running, toolCount, isRemote, err = mgr.ServerStatus("remote-1")
	if !isRemote {
		t.Errorf("expected remote-1 to be remote")
	}
}
