package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/golem"
)

type mockAgent struct {
	responses map[string]string
	err       error
}

func (m *mockAgent) Process(ctx context.Context, prompt string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if resp, ok := m.responses[prompt]; ok {
		return resp, nil
	}
	return "mock response to: " + prompt, nil
}

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantModel   string
		wantMem     string
		wantNoTUI   bool
		wantTUI     bool
		wantErr     bool
	}{
		{
			name:        "default flags",
			args:        []string{},
			wantModel:   "",
			wantMem:     "",
			wantNoTUI:   false,
			wantTUI:     false,
			wantErr:     false,
		},
		{
			name:        "custom model and memory-dir",
			args:        []string{"-model", "gemini-test", "-memory-dir", "/custom/dir"},
			wantModel:   "gemini-test",
			wantMem:     "/custom/dir",
			wantNoTUI:   false,
			wantTUI:     false,
			wantErr:     false,
		},
		{
			name:        "no-tui flag",
			args:        []string{"-no-tui"},
			wantNoTUI:   true,
			wantTUI:     false,
			wantErr:     false,
		},
		{
			name:        "force tui flag",
			args:        []string{"-tui"},
			wantNoTUI:   false,
			wantTUI:     true,
			wantErr:     false,
		},
		{
			name:        "invalid flag",
			args:        []string{"-nonexistent-flag"},
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseFlags(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseFlags() err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if opts.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", opts.Model, tc.wantModel)
			}
			if opts.MemoryDir != tc.wantMem {
				t.Errorf("MemoryDir = %q, want %q", opts.MemoryDir, tc.wantMem)
			}
			if opts.NoTUI != tc.wantNoTUI {
				t.Errorf("NoTUI = %v, want %v", opts.NoTUI, tc.wantNoTUI)
			}
			if opts.TUI != tc.wantTUI {
				t.Errorf("TUI = %v, want %v", opts.TUI, tc.wantTUI)
			}
		})
	}
}

func TestDetermineUseTUI(t *testing.T) {
	tests := []struct {
		name       string
		isTTY      bool
		forceTUI   bool
		disableTUI bool
		want       bool
	}{
		{
			name:       "standard interactive terminal defaults to TUI",
			isTTY:      true,
			forceTUI:   false,
			disableTUI: false,
			want:       true,
		},
		{
			name:       "non-tty defaults to CLI readline",
			isTTY:      false,
			forceTUI:   false,
			disableTUI: false,
			want:       false,
		},
		{
			name:       "disableTUI overrides interactive TTY",
			isTTY:      true,
			forceTUI:   false,
			disableTUI: true,
			want:       false,
		},
		{
			name:       "disableTUI overrides forceTUI",
			isTTY:      false,
			forceTUI:   true,
			disableTUI: true,
			want:       false,
		},
		{
			name:       "forceTUI activates even when non-tty",
			isTTY:      false,
			forceTUI:   true,
			disableTUI: false,
			want:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := determineUseTUI(tc.isTTY, tc.forceTUI, tc.disableTUI)
			if got != tc.want {
				t.Errorf("determineUseTUI(%v, %v, %v) = %v, want %v", tc.isTTY, tc.forceTUI, tc.disableTUI, got, tc.want)
			}
		})
	}
}

func TestBuildConfig(t *testing.T) {
	tests := []struct {
		name       string
		opts       CLIOptions
		fileCfg    config.FileConfig
		apiKey     string
		wantModel  string
		wantMemDir string
		wantKey    string
	}{
		{
			name:       "defaults with resolved key",
			opts:       CLIOptions{},
			fileCfg:    config.FileConfig{},
			apiKey:     "test-key",
			wantModel:  golem.DefaultModelName,
			wantMemDir: golem.DefaultConfig().StorageDir,
			wantKey:    "test-key",
		},
		{
			name: "file config overrides defaults",
			opts: CLIOptions{},
			fileCfg: config.FileConfig{
				Model:     "file-model",
				MemoryDir: "/file/dir",
			},
			apiKey:     "test-key",
			wantModel:  "file-model",
			wantMemDir: "/file/dir",
			wantKey:    "test-key",
		},
		{
			name: "CLI flags override file config",
			opts: CLIOptions{
				Model:     "flag-model",
				MemoryDir: "/flag/dir",
			},
			fileCfg: config.FileConfig{
				Model:     "file-model",
				MemoryDir: "/file/dir",
			},
			apiKey:     "flag-key",
			wantModel:  "flag-model",
			wantMemDir: "/flag/dir",
			wantKey:    "flag-key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := buildConfig(tc.opts, tc.fileCfg, tc.apiKey)
			if cfg.ModelName != tc.wantModel {
				t.Errorf("ModelName = %q, want %q", cfg.ModelName, tc.wantModel)
			}
			if cfg.StorageDir != tc.wantMemDir {
				t.Errorf("StorageDir = %q, want %q", cfg.StorageDir, tc.wantMemDir)
			}
			if cfg.APIKey != tc.wantKey {
				t.Errorf("APIKey = %q, want %q", cfg.APIKey, tc.wantKey)
			}
		})
	}
}

func TestRunReadlineLoop(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		agent        *mockAgent
		wantContains []string
	}{
		{
			name:  "single turn and exit",
			input: "hello Golem\nexit\n",
			agent: &mockAgent{
				responses: map[string]string{
					"hello Golem": "Hello user!",
				},
			},
			wantContains: []string{"Ready!", "You: ", "Golem: Hello user!"},
		},
		{
			name:  "quit command breaks loop",
			input: "quit\n",
			agent: &mockAgent{},
			wantContains: []string{"Ready!", "You: "},
		},
		{
			name:  "agent error is handled gracefully",
			input: "cause error\nexit\n",
			agent: &mockAgent{
				err: errors.New("upstream model failure"),
			},
			wantContains: []string{"Error: upstream model failure"},
		},
		{
			name:  "empty lines ignored",
			input: "   \n\nhi\nexit\n",
			agent: &mockAgent{
				responses: map[string]string{
					"hi": "hey",
				},
			},
			wantContains: []string{"Golem: hey"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := strings.NewReader(tc.input)
			scanner := bufio.NewScanner(in)
			var out bytes.Buffer

			ctx := context.Background()
			err := runReadlineLoop(ctx, tc.agent, scanner, &out)
			if err != nil {
				t.Fatalf("runReadlineLoop failed: %v", err)
			}

			output := out.String()
			for _, want := range tc.wantContains {
				if !strings.Contains(output, want) {
					t.Errorf("output %q missing expected text %q", output, want)
				}
			}
		})
	}
}
