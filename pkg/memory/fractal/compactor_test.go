package fractal

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
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
	return TruncateUTF8(summary, DefaultNodeTargetBytes), nil
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
			res := TruncateUTF8(tc.input, tc.limit)
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

func TestSummarizeBoundedCapsNonConverging(t *testing.T) {
	ctx := context.Background()
	huge := strings.Repeat("x", 100000)
	stub := &correctingStub{
		initial:   huge,
		corrected: []string{huge, huge, huge, huge, huge},
	}

	got, err := SummarizeBounded(ctx, stub, "view", "input", false)
	if err != nil {
		t.Fatalf("SummarizeBounded failed: %v", err)
	}
	if len([]byte(got)) != maxNodeBytes {
		t.Errorf("got %d bytes, want safety cap %d", len([]byte(got)), maxNodeBytes)
	}
	if stub.calls != maxSummaryCorrections {
		t.Errorf("Correct calls = %d, want %d", stub.calls, maxSummaryCorrections)
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

func TestFreeMergeJoinsWithNewline(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	_, _ = store.AppendEvent(KindUser, "a")
	_, _ = store.AppendEvent(KindTalk, "b")
	_ = store.SaveNode(Node{Level: 0, Index: 0, Text: "user: a"})
	_ = store.SaveNode(Node{Level: 0, Index: 1, Text: "talk: b"})

	c := NewCompactor(store, NewView(store, ViewConfig{LiveHeadCount: 0}), nil, DefaultCompactorConfig())
	node, err := c.BuildNode(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("BuildNode failed: %v", err)
	}
	if !strings.Contains(node.Text, "\n") {
		t.Errorf("short lines should be joined by a newline, got %q", node.Text)
	}
	if strings.Contains(node.Text, "; ") {
		t.Errorf("merge still uses a semicolon separator: %q", node.Text)
	}
}

func TestPumpWatermarkSkipsAlreadyBuilt(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	for i := 0; i < 4; i++ {
		_, _ = store.AppendEvent(KindUser, fmt.Sprintf("e%d", i))
	}
	view := NewView(store, ViewConfig{LiveHeadCount: 0})
	c := NewCompactor(store, view, &stubSummarizer{}, DefaultCompactorConfig())
	ctx := context.Background()

	if _, err := c.Pump(ctx); err != nil {
		t.Fatalf("first Pump failed: %v", err)
	}
	// Nothing changed: the watermark makes the second pass a no-op.
	built, err := c.Pump(ctx)
	if err != nil {
		t.Fatalf("second Pump failed: %v", err)
	}
	if built != 0 {
		t.Errorf("second Pump built %d nodes, want 0 (watermark)", built)
	}

	// New events must still be processed.
	_, _ = store.AppendEvent(KindUser, "e4")
	_, _ = store.AppendEvent(KindUser, "e5")
	if built, err := c.Pump(ctx); err != nil {
		t.Fatalf("third Pump failed: %v", err)
	} else if built == 0 {
		t.Errorf("third Pump built 0 nodes, want the new events compressed")
	}
	for i := 0; i < 6; i++ {
		if _, ok := store.GetNode(0, i); !ok {
			t.Errorf("level-0 node %d missing after pumps", i)
		}
	}
}

// capturingLLM records the last request for assertions.
type capturingLLM struct{ req *model.LLMRequest }

func (c *capturingLLM) Name() string { return "capture" }
func (c *capturingLLM) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		c.req = req
		yield(&model.LLMResponse{
			Content:      &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "summary"}}},
			TurnComplete: true,
		}, nil)
	}
}

func TestLLMSummarizerSharesTools(t *testing.T) {
	fm := &capturingLLM{}
	s := NewLLMSummarizer(fm, "")
	s.SetTools([]*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
		{Name: "shell", Description: "run a command"},
	}}})

	if _, err := s.Summarize(context.Background(), "<chat></chat>", "user: hi", false); err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if fm.req == nil || fm.req.Config == nil || len(fm.req.Config.Tools) == 0 {
		t.Fatalf("compaction request did not carry the turn tools")
	}
	if got := fm.req.Config.Tools[0].FunctionDeclarations[0].Name; got != "shell" {
		t.Errorf("tool name = %q, want shell", got)
	}
}

func TestLLMSummarizerWithoutToolsKeepsTheConfigLean(t *testing.T) {
	fm := &capturingLLM{}
	s := NewLLMSummarizer(fm, "")

	if _, err := s.Summarize(context.Background(), "<chat></chat>", "user: hi", false); err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if fm.req == nil {
		t.Fatalf("no request captured")
	}
	if fm.req.Config != nil && len(fm.req.Config.Tools) > 0 {
		t.Errorf("request carried tools nobody set")
	}
}

func TestLLMSummarizerSharesSystemPrompt(t *testing.T) {
	fm := &capturingLLM{}
	s := NewLLMSummarizer(fm, "STATIC SYSTEM PROMPT")
	out, err := s.Summarize(context.Background(), "<chat></chat>", "user: hi", false)
	if err != nil {
		t.Fatalf("Summarize failed: %v", err)
	}
	if out != "summary" {
		t.Errorf("out = %q, want summary", out)
	}
	if fm.req == nil || fm.req.Config == nil || fm.req.Config.SystemInstruction == nil {
		t.Fatalf("compaction request did not carry a system instruction")
	}
	if got := fm.req.Config.SystemInstruction.Parts[0].Text; got != "STATIC SYSTEM PROMPT" {
		t.Errorf("system instruction = %q, want the shared prompt", got)
	}
}

// The first pass after a restart must not rescan the whole tree (golem-01i.11).
func TestPumpWatermarksSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	for i := 0; i < 4; i++ {
		if _, err := store.AppendEvent(KindUser, fmt.Sprintf("e%d", i)); err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
	}
	view := NewView(store, ViewConfig{LiveHeadCount: 0})
	first := NewCompactor(store, view, &stubSummarizer{}, DefaultCompactorConfig())
	if _, err := first.Pump(context.Background()); err != nil {
		t.Fatalf("Pump failed: %v", err)
	}
	marks := first.Watermarks()
	if len(marks) == 0 || marks[0] != 4 {
		t.Fatalf("watermarks after pump = %v, want level 0 at 4", marks)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen: a new compactor on the same store picks the watermarks back up.
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer reopened.Close()

	restarted := NewCompactor(reopened, NewView(reopened, ViewConfig{LiveHeadCount: 0}), &stubSummarizer{}, DefaultCompactorConfig())
	if got := restarted.Watermarks(); len(got) == 0 || got[0] != 4 {
		t.Fatalf("watermarks after restart = %v, want level 0 at 4", got)
	}
	built, err := restarted.Pump(context.Background())
	if err != nil {
		t.Fatalf("Pump after restart failed: %v", err)
	}
	if built != 0 {
		t.Errorf("restarted Pump built %d nodes, want 0 (nothing new)", built)
	}
}
