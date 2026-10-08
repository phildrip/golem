package fractal

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestViewTilingAndLiveHead(t *testing.T) {
	tempDir := t.TempDir()
	store, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// Append 15 events
	for i := 0; i < 15; i++ {
		_, err := store.AppendEvent(KindUser, fmt.Sprintf("message %d", i))
		if err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
		// Save level 0 summaries for all events
		_ = store.SaveNode(Node{
			Level: 0,
			Index: i,
			Text:  fmt.Sprintf("user: msg %d", i),
			Size:  len(fmt.Sprintf("user: msg %d", i)),
		})
	}

	// Also build level 1 nodes for pairs: (0,1), (2,3)
	_ = store.SaveNode(Node{
		Level: 1,
		Index: 0,
		Text:  "user: msg 0 and 1 merged",
		Size:  24,
	})
	_ = store.SaveNode(Node{
		Level: 1,
		Index: 1,
		Text:  "user: msg 2 and 3 merged",
		Size:  24,
	})

	// Configure View with LiveHeadCount = 5, Budget = 100 bytes (to trigger coarsening)
	cfg := ViewConfig{
		ViewBudgetBytes: 100,
		LiveHeadCount:   5,
	}
	view := NewView(store, cfg)

	// Total events = 15. Live head has 5 ([10, 15)). Tail covers 10 ([0, 10)).
	liveEvents := view.LiveHeadEvents()
	if len(liveEvents) != 5 {
		t.Errorf("expected 5 live head events, got %d", len(liveEvents))
	}
	if liveEvents[0].ID != 10 {
		t.Errorf("expected first live event ID=10, got %d", liveEvents[0].ID)
	}

	rendered := view.Render()
	if !strings.HasPrefix(rendered, "<chat>\n") || !strings.HasSuffix(rendered, "</chat>") {
		t.Errorf("expected <chat> tags in render, got:\n%s", rendered)
	}

	// Verify parent level 1 node (0+2) is present in the rendered view
	if !strings.Contains(rendered, "0+2|user: msg 0 and 1 merged") {
		t.Errorf("expected coarsened node 0+2 in rendered view, got:\n%s", rendered)
	}
}

// TestViewMergeOrderMeasuresFromPairEnd reproduces the OptChat 3.2 counterexample
// at T=10: with the view 0+4, 4+4, 8+1, 9+1, rollback-push merges the NEWEST pair
// (8-9), while the buggy "first message" age formula merges the oldest pair (0-7).
func TestViewMergeOrderMeasuresFromPairEnd(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	for i := 0; i < 10; i++ {
		if _, err := store.AppendEvent(KindUser, "x"); err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
	}

	mustNode := func(n Node) {
		if err := store.SaveNode(n); err != nil {
			t.Fatalf("SaveNode failed: %v", err)
		}
	}
	// Level 2 halves covering 0-3 and 4-7, and their parent 0+8.
	mustNode(Node{Level: 2, Index: 0, Text: "cccccccccc"})
	mustNode(Node{Level: 2, Index: 1, Text: "dddddddddd"})
	mustNode(Node{Level: 3, Index: 0, Text: "AAAA"})
	// Level 0 halves covering 8 and 9, and their parent 8+2.
	mustNode(Node{Level: 0, Index: 8, Text: "aaaaaaaaaa"})
	mustNode(Node{Level: 0, Index: 9, Text: "bbbbbbbbbb"})
	mustNode(Node{Level: 1, Index: 4, Text: "BBBB"})

	v := &View{cfg: DefaultViewConfig(), store: store}
	v.parts = []ViewPart{{Level: 2, Index: 0}, {Level: 2, Index: 1}, {Level: 0, Index: 8}, {Level: 0, Index: 9}}
	v.tailTo = 10

	oldDue := v.pairDue(ViewPart{Level: 2, Index: 0}) // pair 0-7
	newDue := v.pairDue(ViewPart{Level: 0, Index: 8}) // pair 8-9
	if !(newDue > oldDue) {
		t.Errorf("newest pair due %.4f must exceed oldest pair due %.4f", newDue, oldDue)
	}

	// bestMergeIndex must select the newest pair, at parts index 2.
	if got := v.bestMergeIndex(); got != 2 {
		t.Errorf("bestMergeIndex = %d, want 2 (newest pair 8-9)", got)
	}
}

// TestViewBatchingRespectsHighAndLowWater verifies the sawtooth: an append below
// the high-water mark does not merge (no old-line churn), and crossing it merges
// in one batch down to the low-water mark.
func TestViewBatchingRespectsHighAndLowWater(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	for i := 0; i < 10; i++ {
		if _, err := store.AppendEvent(KindUser, "x"); err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
	}

	mustNode := func(n Node) {
		if err := store.SaveNode(n); err != nil {
			t.Fatalf("SaveNode failed: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		mustNode(Node{Level: 0, Index: i, Text: "0123456789"}) // 10 bytes
	}
	mustNode(Node{Level: 1, Index: 0, Text: "AAAAAA"}) // 6 bytes, parent of 0-1
	mustNode(Node{Level: 1, Index: 1, Text: "BBBBBB"}) // 6 bytes, parent of 2-3

	cfg := ViewConfig{ViewBudgetBytes: 40, LowWaterBytes: 30, LiveHeadCount: 0}

	// Below high-water: no merge, parts untouched.
	v := &View{cfg: cfg, store: store, tailTo: 10}
	v.parts = []ViewPart{{0, 0}, {0, 1}, {0, 2}} // 30 bytes
	before := append([]ViewPart(nil), v.parts...)
	v.fitLocked()
	if len(v.parts) != len(before) {
		t.Errorf("fitLocked merged below high-water: %v -> %v", before, v.parts)
	}

	// Above high-water: one batch down to at most low-water.
	v = &View{cfg: cfg, store: store, tailTo: 10}
	v.parts = []ViewPart{{0, 0}, {0, 1}, {0, 2}, {0, 3}, {0, 4}} // 50 bytes
	v.fitLocked()
	if size := v.calculateSizeBytes(); size > cfg.LowWaterBytes {
		t.Errorf("after batch size = %d, want <= low-water %d (parts %v)", size, cfg.LowWaterBytes, v.parts)
	}
	if len(v.parts) >= 5 {
		t.Errorf("expected merges to reduce part count, got %v", v.parts)
	}
	merged := false
	for _, p := range v.parts {
		if p.Level >= 1 {
			merged = true
		}
	}
	if !merged {
		t.Errorf("expected at least one merged (level>=1) part, got %v", v.parts)
	}

	// Idempotent: a second pass at/below high-water is a no-op.
	settled := append([]ViewPart(nil), v.parts...)
	v.fitLocked()
	if len(v.parts) != len(settled) {
		t.Errorf("second fitLocked churned: %v -> %v", settled, v.parts)
	}
}

// TestViewPersistsAcrossRestart verifies the view tiling is loaded rather than
// rebuilt on restart, and that an inconsistent saved view falls back safely.
func TestViewPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	// 33 events, LiveHeadCount 1 -> tail covers 0..31 (a full binary tree).
	for i := 0; i < 33; i++ {
		if _, err := store.AppendEvent(KindUser, fmt.Sprintf("event %d", i)); err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
	}
	// Build every node of a complete tree over messages 0..31.
	for lvl := 0; lvl <= 5; lvl++ {
		for idx := 0; idx < 32>>lvl; idx++ {
			if err := store.SaveNode(Node{Level: lvl, Index: idx, Text: fmt.Sprintf("L%dI%d", lvl, idx)}); err != nil {
				t.Fatalf("SaveNode failed: %v", err)
			}
		}
	}

	cfg := ViewConfig{ViewBudgetBytes: 64, LiveHeadCount: 1}
	v := NewView(store, cfg)
	before := v.Render()
	beforeParts := len(v.parts)
	if beforeParts >= 32 {
		t.Fatalf("expected merges to coarsen the view, got %d parts", beforeParts)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen: the persisted tiling must be loaded, not rebuilt.
	store2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer store2.Close()

	v2 := NewView(store2, cfg)
	if after := v2.Render(); after != before {
		t.Errorf("view changed across restart:\nbefore: %s\nafter:  %s", before, after)
	}
	if len(v2.parts) != beforeParts {
		t.Errorf("part count changed across restart: %d -> %d", beforeParts, len(v2.parts))
	}

	// Append an event: the saved tiling no longer matches, so it must fall back
	// to a rebuild that covers the new tail, without error.
	if _, err := store2.AppendEvent(KindUser, "after restart"); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	v3 := NewView(store2, cfg)
	if v3.tailTo != 33 {
		t.Errorf("fallback view tailTo = %d, want 33", v3.tailTo)
	}
	if v3.Render() == "" {
		t.Errorf("fallback view rendered empty")
	}
}

// TestViewLoadsPersistedTilingNotRebuild injects a valid tiling that a rebuild
// would never produce (coarser than the sequential algorithm), and asserts
// NewView returns exactly it. This distinguishes loading from rebuilding.
func TestViewLoadsPersistedTilingNotRebuild(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	for i := 0; i < 33; i++ {
		if _, err := store.AppendEvent(KindUser, "x"); err != nil {
			t.Fatal(err)
		}
	}
	// 16 level-1 parts tile [0, 32). A sequential rebuild with a large budget
	// would instead emit 32 level-0 parts.
	parts := make([]ViewPart, 16)
	for i := range parts {
		parts[i] = ViewPart{Level: 1, Index: i}
	}
	if err := store.SaveViewState(ViewState{Parts: parts, TailTo: 32}); err != nil {
		t.Fatalf("SaveViewState failed: %v", err)
	}

	v := NewView(store, ViewConfig{ViewBudgetBytes: DefaultViewBudgetBytes, LiveHeadCount: 1})
	if len(v.parts) != 16 {
		t.Fatalf("loaded %d parts, want the 16 persisted level-1 parts (a rebuild would give 32 level-0 parts)", len(v.parts))
	}
	for _, p := range v.parts {
		if p.Level != 1 {
			t.Fatalf("loaded part %v, want every part at level 1", p)
		}
	}
	if v.tailTo != 32 {
		t.Errorf("tailTo = %d, want 32", v.tailTo)
	}
}

// TestViewCatchesUpPersistedTiling verifies that a saved tiling behind the
// current tail is caught up (events appended without a rebuild) rather than
// discarded, and that the persisted detail is retained.
func TestViewCatchesUpPersistedTiling(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	for i := 0; i < 32; i++ {
		if _, err := store.AppendEvent(KindUser, "x"); err != nil {
			t.Fatal(err)
		}
	}
	parts := make([]ViewPart, 16)
	for i := range parts {
		parts[i] = ViewPart{Level: 1, Index: i}
	}
	if err := store.SaveViewState(ViewState{Parts: parts, TailTo: 32}); err != nil {
		t.Fatalf("SaveViewState failed: %v", err)
	}
	// Three events appended after the save: total 35, tail 34.
	for i := 0; i < 3; i++ {
		if _, err := store.AppendEvent(KindUser, "later"); err != nil {
			t.Fatal(err)
		}
	}

	cfg := ViewConfig{ViewBudgetBytes: DefaultViewBudgetBytes, LiveHeadCount: 1}
	v := NewView(store, cfg)
	if v.tailTo != 34 {
		t.Errorf("tailTo = %d, want 34 (caught up)", v.tailTo)
	}
	retained := false
	for _, p := range v.parts {
		if p.Level >= 1 {
			retained = true
		}
	}
	if !retained {
		t.Errorf("persisted merged detail was not retained: %v", v.parts)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer reopened.Close()
	if v2 := NewView(reopened, cfg); v2.tailTo != 34 {
		t.Errorf("after reopen tailTo = %d, want 34", v2.tailTo)
	}
}

// TestNewViewNormalizesWaterMarks covers the ViewConfig water-mark defaults.
func TestNewViewNormalizesWaterMarks(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	cases := []struct {
		high, low, wantLow int
	}{
		{1000, 0, 500},    // low unset -> half of high
		{1000, 1000, 500}, // low >= high -> half of high
		{1000, 200, 200},  // explicit valid low is kept
		{1, 0, 1},         // tiny high -> clamped to at least 1
	}
	for _, tc := range cases {
		v := NewView(store, ViewConfig{ViewBudgetBytes: tc.high, LowWaterBytes: tc.low})
		if v.cfg.LowWaterBytes != tc.wantLow {
			t.Errorf("high=%d low=%d -> LowWaterBytes=%d, want %d", tc.high, tc.low, v.cfg.LowWaterBytes, tc.wantLow)
		}
	}
}

func TestZoomAndDateTools(t *testing.T) {
	tempDir := t.TempDir()
	store, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	ev0, _ := store.AppendEvent(KindUser, "alpha command")
	ev1, _ := store.AppendEvent(KindTalk, "beta reply")

	_ = store.SaveNode(Node{Level: 0, Index: 0, Text: "user: alpha"})
	_ = store.SaveNode(Node{Level: 0, Index: 1, Text: "talk: beta"})
	_ = store.SaveNode(Node{Level: 1, Index: 0, Text: "user: alpha; talk: beta"})

	zoomTool, err := NewZoomTool(store)
	if err != nil {
		t.Fatalf("NewZoomTool failed: %v", err)
	}

	dateTool, err := NewDateTool(store)
	if err != nil {
		t.Fatalf("NewDateTool failed: %v", err)
	}

	if zoomTool.Name() != "zoom" {
		t.Errorf("expected zoom tool name 'zoom', got %q", zoomTool.Name())
	}
	if dateTool.Name() != "date" {
		t.Errorf("expected date tool name 'date', got %q", dateTool.Name())
	}

	_ = ev0
	_ = ev1
	_ = context.Background()
}

func TestDefaultViewBudgets(t *testing.T) {
	if DefaultViewBudgetBytes != 64_000 {
		t.Errorf("DefaultViewBudgetBytes = %d, want 64000", DefaultViewBudgetBytes)
	}
	if DefaultViewLowWaterBytes != 32_000 {
		t.Errorf("DefaultViewLowWaterBytes = %d, want 32000", DefaultViewLowWaterBytes)
	}
}
