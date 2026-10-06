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
