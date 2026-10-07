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
	if p := ResolveProvider("", FileConfig{}, "opencode-go/kimi-k3"); p != ProviderOpenCode {
		t.Errorf("expected opencode from opencode-go prefix, got %s", p)
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

func TestProfilesAndMountedMemories(t *testing.T) {
	t.Setenv("WORK_SECRET_KEY", "work-secret-env-123")

	fileCfg := FileConfig{
		ActiveProfile: "personal",
		Profiles: map[string]ProfileConfig{
			"personal": {
				Provider:  "gemini",
				Model:     "gemini-flash-latest",
				APIKey:    "personal-gemini-key",
				MemoryDir: "/custom/personal/memory",
			},
			"work": {
				Provider:  "opencode",
				Model:     "deepseek-chat",
				BaseURL:   "https://api.opencode.ai/v1",
				APIKeyEnv: "WORK_SECRET_KEY",
				MountedMemories: []MountedMemoryConfig{
					{
						Name:     "shared-docs",
						Path:     "/company/shared-docs/memory",
						ReadOnly: true,
					},
				},
			},
		},
	}

	// 1. Default active profile from config is 'personal'
	name, prof := ResolveActiveProfile("", fileCfg)
	if name != "personal" {
		t.Errorf("expected profile 'personal', got %q", name)
	}
	if prof.Provider != "gemini" || prof.APIKey != "personal-gemini-key" || prof.MemoryDir != "/custom/personal/memory" {
		t.Errorf("unexpected personal profile config: %+v", prof)
	}

	// 2. Explicit flag override to 'work'
	name, prof = ResolveActiveProfile("work", fileCfg)
	if name != "work" {
		t.Errorf("expected profile 'work', got %q", name)
	}
	if prof.Provider != "opencode" || prof.Model != "deepseek-chat" {
		t.Errorf("unexpected work profile config: %+v", prof)
	}
	if prof.APIKey != "work-secret-env-123" {
		t.Errorf("expected APIKey resolved from WORK_SECRET_KEY, got %q", prof.APIKey)
	}
	if len(prof.MountedMemories) != 1 || prof.MountedMemories[0].Name != "shared-docs" {
		t.Errorf("expected mounted memories in work profile, got %+v", prof.MountedMemories)
	}

	// 3. ApplyProfile updates FileConfig
	applied := fileCfg.ApplyProfile("work", prof)
	if applied.ActiveProfile != "work" || applied.Provider != "opencode" || applied.Model != "deepseek-chat" {
		t.Errorf("ApplyProfile did not overlay settings: %+v", applied)
	}
}

func TestMergeConfigs(t *testing.T) {
	userCfg := FileConfig{
		ActiveProfile: "personal",
		Provider:      "gemini",
		Profiles: map[string]ProfileConfig{
			"personal": {Provider: "gemini"},
		},
	}

	wsCfg := FileConfig{
		ActiveProfile: "work",
		Provider:      "opencode",
		Profiles: map[string]ProfileConfig{
			"work": {Provider: "opencode"},
		},
		MountedMemories: []MountedMemoryConfig{
			{Name: "team-knowledge", Path: "/workspace/knowledge/memory", ReadOnly: true},
		},
	}

	merged := MergeConfigs(userCfg, wsCfg)
	if merged.ActiveProfile != "work" {
		t.Errorf("expected workspace active profile 'work', got %q", merged.ActiveProfile)
	}
	if merged.Provider != "opencode" {
		t.Errorf("expected workspace provider 'opencode', got %q", merged.Provider)
	}
	if len(merged.Profiles) != 2 {
		t.Errorf("expected 2 merged profiles, got %d", len(merged.Profiles))
	}
	if len(merged.MountedMemories) != 1 {
		t.Errorf("expected 1 mounted memory, got %d", len(merged.MountedMemories))
	}
}

func TestCompactorConfig(t *testing.T) {
	userCfg := FileConfig{
		Provider: "openai",
		Model:    "gpt-4o",
		Compactor: &CompactorConfig{
			Provider:    "gemini",
			Model:       "gemini-flash-latest",
			TargetBytes: 512,
		},
	}

	wsCfg := FileConfig{
		Compactor: &CompactorConfig{
			Provider:    "opencode",
			Model:       "deepseek-coder",
			TargetBytes: 256,
		},
	}

	merged := MergeConfigs(userCfg, wsCfg)
	if merged.Compactor == nil || merged.Compactor.Model != "deepseek-coder" {
		t.Errorf("expected workspace compactor model deepseek-coder, got %+v", merged.Compactor)
	}

	// Profile inheritance
	_, prof := ResolveActiveProfile("default", userCfg)
	if prof.Compactor == nil || prof.Compactor.Model != "gemini-flash-latest" {
		t.Errorf("expected inherited compactor config in default profile, got %+v", prof.Compactor)
	}

	// Partial merge preserves non-overridden user fields
	partialWs := FileConfig{
		Compactor: &CompactorConfig{
			TargetBytes: 1024,
		},
	}
	partialMerged := MergeConfigs(userCfg, partialWs)
	if partialMerged.Compactor.Model != "gemini-flash-latest" {
		t.Errorf("expected preserved compactor model gemini-flash-latest, got %q", partialMerged.Compactor.Model)
	}
	if partialMerged.Compactor.TargetBytes != 1024 {
		t.Errorf("expected merged TargetBytes 1024, got %d", partialMerged.Compactor.TargetBytes)
	}

	// Cross-provider key isolation: generic APIKey should not poison Gemini when primary is OpenAI
	geminiKey := ResolveProviderKey(ProviderGemini, "", userCfg)
	if geminiKey != "" {
		t.Errorf("expected empty Gemini key when only OpenAI key is set, got %q", geminiKey)
	}
}


