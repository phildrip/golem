package fractal

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// stubSummarizer implements Summarizer for testing.
type stubSummarizer struct {
	calls []string
}

func (s *stubSummarizer) Summarize(ctx context.Context, contextView string, content string, isMerge bool) (string, error) {
	tag := "compressed:"
	if isMerge {
		tag = "merged:"
	}
	summary := fmt.Sprintf("%s %s", tag, content)
	s.calls = append(s.calls, summary)
	return truncateUTF8(summary, DefaultNodeTargetBytes), nil
}

func TestTruncateUTF8(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		limit      int
		wantLength int
	}{
		{
			name:       "short string under limit",
			input:      "hello",
			limit:      10,
			wantLength: 5,
		},
		{
			name:       "exact limit",
			input:      "12345",
			limit:      5,
			wantLength: 5,
		},
		{
			name:       "ascii cut cleanly",
			input:      "1234567890",
			limit:      5,
			wantLength: 5,
		},
		{
			name:       "utf8 multi-byte rune boundary",
			input:      "hello 世界", // "hello " (6) + "世" (3) + "界" (3) = 12 bytes
			limit:      8,        // 8 cuts inside "世" (bytes 6, 7, 8) -> should truncate to 6 ("hello ")
			wantLength: 6,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := truncateUTF8(tc.input, tc.limit)
			if len([]byte(res)) != tc.wantLength {
				t.Errorf("len(res) = %d, want %d (content: %q)", len([]byte(res)), tc.wantLength, res)
			}
		})
	}
}

func TestCompactorFreeNodesAndSummarization(t *testing.T) {
	tempDir := t.TempDir()
	store, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	stub := &stubSummarizer{}
	viewCfg := ViewConfig{
		ViewBudgetBytes: 1024,
		LiveHeadCount:   2, // Keep last 2 events in live head
	}
	view := NewView(store, viewCfg)
	compactor := NewCompactor(store, view, stub, DefaultCompactorConfig())

	// 1. Append a short event (<= 512 bytes)
	_, _ = store.AppendEvent(KindUser, "short command")

	// 2. Append a huge event (> 512 bytes)
	largeText := strings.Repeat("huge tool payload content ", 30) // ~780 bytes
	_, _ = store.AppendEvent(KindEcho, largeText)

	// 3. Append two events that will sit in live head
	_, _ = store.AppendEvent(KindUser, "recent user msg")
	_, _ = store.AppendEvent(KindTalk, "recent talk msg")

	ctx := context.Background()
	built, err := compactor.Pump(ctx)
	if err != nil {
		t.Fatalf("Pump failed: %v", err)
	}

	// Should have built level 0 for event 0 and event 1
	if built < 2 {
		t.Errorf("expected at least 2 nodes built, got %d", built)
	}

	// Event 0 is short: should be a free node (no summarizer call)
	node0, ok0 := store.GetNode(0, 0)
	if !ok0 {
		t.Fatalf("node (0, 0) missing")
	}
	if node0.Text != "user: short command" {
		t.Errorf("expected verbatim free node, got %q", node0.Text)
	}

	// Event 1 is huge: must have been summarized
	node1, ok1 := store.GetNode(0, 1)
	if !ok1 {
		t.Fatalf("node (0, 1) missing")
	}
	if !strings.HasPrefix(node1.Text, "compressed:") {
		t.Errorf("expected compressed node, got %q", node1.Text)
	}

	// Verify that parent merge node (1, 0) was also built by Pump!
	nodeMerged, okM := store.GetNode(1, 0)
	if !okM {
		t.Errorf("expected parent merge node (1, 0) to be built")
	} else if !strings.Contains(nodeMerged.Text, "short command") {
		t.Errorf("unexpected merged text: %s", nodeMerged.Text)
	}
}
