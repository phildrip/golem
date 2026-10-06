package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigLoadAndSave(t *testing.T) {
	tempDir := t.TempDir()
	confPath := filepath.Join(tempDir, "config.json")

	// 1. Loading non-existent file returns zero-value config
	cfg, err := Load(confPath)
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if cfg.APIKey != "" {
		t.Errorf("expected empty APIKey, got %q", cfg.APIKey)
	}

	// 2. Save config
	toSave := FileConfig{
		APIKey:    "test-key-12345",
		Model:     "gemini-flash-latest",
		MemoryDir: "/custom/memory/dir",
	}
	if err := Save(confPath, toSave); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify permissions are 0600
	info, err := os.Stat(confPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected file mode 0600, got %v", info.Mode().Perm())
	}

	// 3. Load saved config
	loaded, err := Load(confPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.APIKey != toSave.APIKey || loaded.Model != toSave.Model || loaded.MemoryDir != toSave.MemoryDir {
		t.Errorf("loaded %+v does not match saved %+v", loaded, toSave)
	}
}

func TestResolveAPIKey(t *testing.T) {
	tempDir := t.TempDir()
	confPath := filepath.Join(tempDir, "config.json")

	tests := []struct {
		name       string
		flag       string
		envKey     string
		fileKey    string
		stdinInput string
		wantKey    string
	}{
		{
			name:    "flag takes precedence",
			flag:    "flag-key-1",
			envKey:  "env-key-2",
			fileKey: "file-key-3",
			wantKey: "flag-key-1",
		},
		{
			name:    "env takes precedence over file",
			flag:    "",
			envKey:  "env-key-2",
			fileKey: "file-key-3",
			wantKey: "env-key-2",
		},
		{
			name:    "file used if flag and env are empty",
			flag:    "",
			envKey:  "",
			fileKey: "file-key-3",
			wantKey: "file-key-3",
		},
		{
			name:       "interactive fallback when all empty",
			flag:       "",
			envKey:     "",
			fileKey:    "",
			stdinInput: "interactive-key-4\ny\n",
			wantKey:    "interactive-key-4",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(confPath)
			if tc.fileKey != "" {
				_ = Save(confPath, FileConfig{APIKey: tc.fileKey})
			}

			if tc.envKey != "" {
				t.Setenv("GEMINI_API_KEY", tc.envKey)
			} else {
				t.Setenv("GEMINI_API_KEY", "")
				t.Setenv("GOOGLE_API_KEY", "")
			}

			var scanner *bufio.Scanner
			if tc.stdinInput != "" {
				scanner = bufio.NewScanner(strings.NewReader(tc.stdinInput))
			}

			gotKey, err := ResolveAPIKey(tc.flag, confPath, scanner)
			if err != nil {
				t.Fatalf("ResolveAPIKey failed: %v", err)
			}
			if gotKey != tc.wantKey {
				t.Errorf("got %q, want %q", gotKey, tc.wantKey)
			}
		})
	}
}

func TestUserDirAndWorkspaceDir(t *testing.T) {
	// 1. WorkspaceDir
	if got := WorkspaceDir("/my/project"); got != "/my/project/.golem" {
		t.Errorf("expected /my/project/.golem, got %s", got)
	}
	if got := WorkspaceDir(""); got != "" {
		t.Errorf("expected empty string for empty workspace, got %s", got)
	}

	// 2. GOLEM_CONFIG_DIR env override
	t.Setenv("GOLEM_CONFIG_DIR", "/custom/golem/config")
	dir, err := UserDir()
	if err != nil {
		t.Fatalf("UserDir failed: %v", err)
	}
	if dir != "/custom/golem/config" {
		t.Errorf("expected /custom/golem/config, got %s", dir)
	}
}

func TestMultiProviderResolution(t *testing.T) {
	// 1. ResolveProvider
	if p := ResolveProvider("opencode", FileConfig{}, ""); p != ProviderOpenCode {
		t.Errorf("expected opencode, got %s", p)
	}
	if p := ResolveProvider("", FileConfig{Provider: "openrouter"}, ""); p != ProviderOpenRouter {
		t.Errorf("expected openrouter, got %s", p)
	}
	if p := ResolveProvider("", FileConfig{}, "opencode/deepseek-chat"); p != ProviderOpenCode {
		t.Errorf("expected opencode from model prefix, got %s", p)
	}
	if p := ResolveProvider("", FileConfig{}, "gemini-flash-latest"); p != ProviderGemini {
		t.Errorf("expected gemini default, got %s", p)
	}

	// 2. ResolveProviderKey
	t.Setenv("OPENCODE_API_KEY", "opencode-key-test")
	if k := ResolveProviderKey(ProviderOpenCode, "", FileConfig{}); k != "opencode-key-test" {
		t.Errorf("expected opencode-key-test, got %s", k)
	}

	// 3. ResolveBaseURL
	if u := ResolveBaseURL(ProviderOpenCode, "", FileConfig{}); u != DefaultOpenCodeBaseURL {
		t.Errorf("expected %s, got %s", DefaultOpenCodeBaseURL, u)
	}
	if u := ResolveBaseURL(ProviderOpenCode, "https://custom.opencode/v1", FileConfig{}); u != "https://custom.opencode/v1" {
		t.Errorf("expected custom url, got %s", u)
	}
}

