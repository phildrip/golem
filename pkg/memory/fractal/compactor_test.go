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
			limit:      8,          // 8 cuts inside "世" (bytes 6, 7, 8) -> should truncate to 6 ("hello ")
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

func TestScaleLineIsExactly512Bytes(t *testing.T) {
	if got := len([]byte(ScaleLine512Bytes)); got != DefaultNodeTargetBytes {
		t.Errorf("ScaleLine512Bytes = %d bytes, want exactly %d", got, DefaultNodeTargetBytes)
	}
}

// correctingStub returns an initial (usually over-length) candidate and then a
// scripted sequence of corrections.
type correctingStub struct {
	initial    string
	corrected  []string
	calls      int
	lastPrompt string
}

func (s *correctingStub) Summarize(ctx context.Context, contextView, content string, isMerge bool) (string, error) {
	return s.initial, nil
}

func (s *correctingStub) Correct(ctx context.Context, contextView, content string, isMerge bool, prior, correction string) (string, error) {
	s.lastPrompt = correction
	idx := s.calls
	s.calls++
	if idx < len(s.corrected) {
		return s.corrected[idx], nil
	}
	return prior, nil
}

func TestSummarizeBoundedCorrectsOverLength(t *testing.T) {
	ctx := context.Background()

	// A single correction brings it under the limit.
	stub := &correctingStub{
		initial:   strings.Repeat("a", 600),
		corrected: []string{strings.Repeat("b", 500)},
	}
	got, err := SummarizeBounded(ctx, stub, "view", "input", false)
	if err != nil {
		t.Fatalf("SummarizeBounded failed: %v", err)
	}
	if len([]byte(got)) != 500 {
		t.Errorf("got %d bytes, want the 500-byte corrected candidate", len([]byte(got)))
	}
	if stub.calls != 1 {
		t.Errorf("Correct calls = %d, want 1", stub.calls)
	}
	if !strings.Contains(stub.lastPrompt, "Too long") {
		t.Errorf("correction prompt missing 'Too long': %q", stub.lastPrompt)
	}

	// If corrections never reach the limit, stop after maxSummaryCorrections and
	// keep the shortest candidate (no truncation).
	stub2 := &correctingStub{
		initial: strings.Repeat("a", 600),
		corrected: []string{
			strings.Repeat("b", 560),
			strings.Repeat("c", 570),
			strings.Repeat("d", 580),
			strings.Repeat("e", 590),
			strings.Repeat("f", 595),
			strings.Repeat("g", 599),
		},
	}
	got2, err := SummarizeBounded(ctx, stub2, "view", "input", false)
	if err != nil {
		t.Fatalf("SummarizeBounded failed: %v", err)
	}
	if len([]byte(got2)) != 560 {
		t.Errorf("got %d bytes, want shortest candidate 560", len([]byte(got2)))
	}
	if stub2.calls != maxSummaryCorrections {
		t.Errorf("Correct calls = %d, want %d", stub2.calls, maxSummaryCorrections)
	}

	// Within the limit: no correction at all.
	stub3 := &correctingStub{initial: strings.Repeat("z", 100)}
	got3, err := SummarizeBounded(ctx, stub3, "view", "input", true)
	if err != nil {
		t.Fatalf("SummarizeBounded failed: %v", err)
	}
	if got3 != stub3.initial {
		t.Errorf("in-limit candidate changed: %q", got3)
	}
	if stub3.calls != 0 {
		t.Errorf("Correct calls = %d, want 0", stub3.calls)
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
