package golem

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/user/golem/pkg/memory/fractal"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ModelName != DefaultModelName {
		t.Errorf("model = %q, want %q", cfg.ModelName, DefaultModelName)
	}
	if cfg.LiveHeadCount != fractal.DefaultLiveHeadCount {
		t.Errorf("liveHeadCount = %d, want %d", cfg.LiveHeadCount, fractal.DefaultLiveHeadCount)
	}
	if cfg.ViewBudgetBytes != fractal.DefaultViewBudgetBytes {
		t.Errorf("viewBudget = %d, want %d", cfg.ViewBudgetBytes, fractal.DefaultViewBudgetBytes)
	}
}

func TestFractalInstructionInjection(t *testing.T) {
	tempDir := t.TempDir()
	store, err := fractal.OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// Seed some history
	for i := 0; i < 5; i++ {
		_, _ = store.AppendEvent(fractal.KindUser, "user instruction")
		_ = store.SaveNode(fractal.Node{Level: 0, Index: i, Text: "user: instruction"})
	}

	view := fractal.NewView(store, fractal.ViewConfig{
		ViewBudgetBytes: 1024,
		LiveHeadCount:   2,
	})

	rendered := view.Render()
	if !strings.Contains(rendered, "<chat>") {
		t.Errorf("expected <chat> in rendered view, got %q", rendered)
	}
	if !strings.Contains(rendered, "0+1|user: instruction") {
		t.Errorf("expected level 0 node in view, got %q", rendered)
	}

	_ = context.Background()
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "503 high demand",
			err:  errors.New("Error 503, Message: This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later., Status: UNAVAILABLE"),
			want: true,
		},
		{
			name: "429 rate limit",
			err:  errors.New("Error 429: RESOURCE_EXHAUSTED"),
			want: true,
		},
		{
			name: "timeout",
			err:  errors.New("context deadline exceeded (Client.Timeout exceeded)"),
			want: true,
		},
		{
			name: "invalid argument (not retryable)",
			err:  errors.New("Error 400: INVALID_ARGUMENT"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientError(tc.err)
			if got != tc.want {
				t.Errorf("isTransientError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestAgentLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	cfg := Config{
		ModelName:       "gemini-flash-latest",
		APIKey:          "fake-api-key-for-testing",
		StorageDir:      tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	ctx := context.Background()
	agent, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New agent failed: %v", err)
	}
	defer agent.Close()

	if agent.Store() == nil {
		t.Errorf("expected Store() to be non-nil")
	}
	if agent.View() == nil {
		t.Errorf("expected View() to be non-nil")
	}
	if agent.Compactor() == nil {
		t.Errorf("expected Compactor() to be non-nil")
	}

	// Verify appending event and syncing view
	if _, err := agent.Store().AppendEvent(fractal.KindUser, "lifecycle test"); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	agent.View().Sync()

	liveEvents := agent.View().LiveHeadEvents()
	if len(liveEvents) != 1 || !strings.Contains(liveEvents[0].Text, "lifecycle test") {
		t.Errorf("expected live head to contain lifecycle test, got %v", liveEvents)
	}

	// Test Close
	if err := agent.Close(); err != nil {
		t.Errorf("agent.Close() failed: %v", err)
	}
}

func TestAgentMultiProviderInit(t *testing.T) {
	tempDir := t.TempDir()
	cfg := Config{
		Provider:        "opencode",
		BaseURL:         "https://api.opencode.ai/v1",
		ModelName:       "deepseek-chat",
		APIKey:          "opencode-test-key",
		StorageDir:      tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	ctx := context.Background()
	agent, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New agent with OpenCode provider failed: %v", err)
	}
	defer agent.Close()

	if agent.Store() == nil {
		t.Errorf("expected Store() to be non-nil")
	}
	if agent.View() == nil {
		t.Errorf("expected View() to be non-nil")
	}
	if agent.Compactor() == nil {
		t.Errorf("expected Compactor() to be non-nil")
	}

	// Verify appending event and syncing view
	if _, err := agent.Store().AppendEvent(fractal.KindUser, "opencode test"); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	agent.View().Sync()

	liveEvents := agent.View().LiveHeadEvents()
	if len(liveEvents) != 1 || !strings.Contains(liveEvents[0].Text, "opencode test") {
		t.Errorf("expected live head to contain opencode test, got %v", liveEvents)
	}
}

