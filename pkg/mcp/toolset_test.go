package mcp

import (
	"testing"
)

func TestBuildToolsets(t *testing.T) {
	cfg := Config{
		Servers: map[string]ServerConfig{
			"mock-stdio": {
				Command: "echo",
				Args:    []string{"hello"},
				Env:     map[string]string{"FOO": "BAR"},
			},
			"mock-remote": {
				URL: "https://api.example.com/mcp",
			},
			"disabled-server": {
				Command:  "echo",
				Disabled: true,
			},
			"invalid-empty": {
				Command: "",
			},
		},
	}

	toolsets, errs := BuildToolsets(cfg)
	if len(errs) != 1 {
		t.Errorf("expected 1 error for invalid-empty, got %d errs: %v", len(errs), errs)
	}

	if len(toolsets) != 2 {
		t.Fatalf("expected 2 active toolsets, got %d", len(toolsets))
	}

	foundStdio := false
	foundRemote := false
	for _, ts := range toolsets {
		if ts.Name == "mock-stdio" {
			foundStdio = true
			if ts.Toolset == nil {
				t.Errorf("mock-stdio toolset is nil")
			}
		}
		if ts.Name == "mock-remote" {
			foundRemote = true
			if ts.Toolset == nil {
				t.Errorf("mock-remote toolset is nil")
			}
		}
	}

	if !foundStdio || !foundRemote {
		t.Errorf("expected both stdio and remote toolsets, got: %+v", toolsets)
	}
}
