package fractal

import (
	"testing"
	"time"
)

func TestStoreTurnSessionAndTimeIndexing(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	t0 := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)

	if _, err := s.AppendEvent(KindUser, "fix race condition in pkg/swarm/bus.go", AppendEventOpts{
		Timestamp: t0,
		SessionID: "sess-alpha",
		TurnID:    "turn-101",
	}); err != nil {
		t.Fatalf("AppendEvent 0: %v", err)
	}
	if _, err := s.AppendEvent(KindTool, `hashline_patch: {"path":"pkg/swarm/bus.go"}`, AppendEventOpts{
		Timestamp: t0.Add(2 * time.Second),
		SessionID: "sess-alpha",
		TurnID:    "turn-101",
	}); err != nil {
		t.Fatalf("AppendEvent 1: %v", err)
	}
	if _, err := s.AppendEvent(KindTalk, "fixed mutex unlock in bus.go", AppendEventOpts{
		Timestamp: t0.Add(5 * time.Second),
		SessionID: "sess-alpha",
		TurnID:    "turn-101",
	}); err != nil {
		t.Fatalf("AppendEvent 2: %v", err)
	}
	if _, err := s.AppendEvent(KindUser, "upgrade bbolt compaction watermarks", AppendEventOpts{
		Timestamp: t1,
		SessionID: "sess-beta",
		TurnID:    "turn-202",
	}); err != nil {
		t.Fatalf("AppendEvent 3: %v", err)
	}
	if _, err := s.AppendEvent(KindTalk, "saved watermarks in bucketView", AppendEventOpts{
		Timestamp: t2,
		SessionID: "sess-beta",
		TurnID:    "turn-202",
	}); err != nil {
		t.Fatalf("AppendEvent 4: %v", err)
	}

	// Verify in-memory indexes before close
	turn101 := s.GetEventsByTurn("turn-101")
	if len(turn101) != 3 || turn101[0].ID != 0 || turn101[2].ID != 2 {
		t.Fatalf("GetEventsByTurn(turn-101) = %+v, want 3 events [0..2]", turn101)
	}

	sessBeta := s.GetEventsBySession("sess-beta")
	if len(sessBeta) != 2 || sessBeta[0].ID != 3 || sessBeta[1].ID != 4 {
		t.Fatalf("GetEventsBySession(sess-beta) = %+v, want 2 events [3,4]", sessBeta)
	}

	// Close and reopen store to ensure bbolt persistence rebuilds turnIndex & sessionIndex
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore reopen: %v", err)
	}
	defer s2.Close()

	if got := s2.GetEventsByTurn("turn-101"); len(got) != 3 {
		t.Fatalf("reopened GetEventsByTurn(turn-101) = %d events, want 3", len(got))
	}
	if got := s2.GetEventsBySession("sess-beta"); len(got) != 2 {
		t.Fatalf("reopened GetEventsBySession(sess-beta) = %d events, want 2", len(got))
	}

	// Verify time range lookup
	inWindow := s2.GetEventsByTimeRange(t0.Add(30*time.Minute), t1.Add(30*time.Minute))
	if len(inWindow) != 1 || inWindow[0].ID != 3 {
		t.Fatalf("GetEventsByTimeRange = %+v, want event #3 only", inWindow)
	}
}

func TestMemorySearchBM25AndFilters(t *testing.T) {
	primaryDir := t.TempDir()
	mountedDir := t.TempDir()

	primary, err := OpenStore(primaryDir)
	if err != nil {
		t.Fatalf("OpenStore primary: %v", err)
	}
	defer primary.Close()

	mounted, err := OpenStore(mountedDir)
	if err != nil {
		t.Fatalf("OpenStore mounted: %v", err)
	}
	defer mounted.Close()

	now := time.Now().UTC()
	_, _ = primary.AppendEvent(KindUser, "how do we configure Landlock sandbox policies?", AppendEventOpts{
		Timestamp: now.Add(-48 * time.Hour),
		SessionID: "s1",
		TurnID:    "t1",
	})
	_, _ = primary.AppendEvent(KindTalk, "Landlock rules live in pkg/sandbox/linux.go and use Landlock ABI v3.", AppendEventOpts{
		Timestamp: now.Add(-48 * time.Hour),
		SessionID: "s1",
		TurnID:    "t1",
	})
	_, _ = primary.AppendEvent(KindUser, "fix token caching sawtooth window in formatLiveHead", AppendEventOpts{
		Timestamp: now.Add(-1 * time.Hour),
		SessionID: "s2",
		TurnID:    "t2",
	})

	// Save a higher-level summary node to verify IncludeNodes search
	_ = primary.SaveNode(Node{
		Level: 2,
		Index: 0,
		Text:  "0+4|Resolved Landlock sandbox ABI v3 enforcement in pkg/sandbox/linux.go and verified formatLiveHead sawtooth",
	})

	// Populate mounted memory
	_, _ = mounted.AppendEvent(KindWork, "Remembered [work]: Always use PostgreSQL advisory locks for migrations", AppendEventOpts{
		Timestamp: now.Add(-12 * time.Hour),
		SessionID: "s-mounted",
		TurnID:    "t-mounted",
	})

	mgr := NewMemoryManager(primary, NewView(primary, DefaultViewConfig()))
	mgr.Mount("work", mounted, NewView(mounted, DefaultViewConfig()), true)

	// 1. BM25 search for Landlock should rank the Landlock events at the top
	res, err := Search(mgr, SearchArgs{Query: "Landlock pkg/sandbox/linux.go"})
	if err != nil {
		t.Fatalf("Search BM25: %v", err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("expected 2 Landlock matches, got %d: %+v", len(res.Matches), res.Matches)
	}
	if res.Matches[0].Address != "1+1" {
		t.Fatalf("expected event 1+1 (with both 'Landlock' and 'pkg/sandbox/linux.go') to rank #1, got %s", res.Matches[0].Address)
	}

	// 2. IncludeNodes=true should also surface the 0+4 L2 node
	resNodes, err := Search(mgr, SearchArgs{Query: "Landlock", IncludeNodes: true})
	if err != nil {
		t.Fatalf("Search IncludeNodes: %v", err)
	}
	foundNode := false
	for _, m := range resNodes.Matches {
		if m.Address == "0+4" {
			foundNode = true
			break
		}
	}
	if !foundNode {
		t.Fatalf("expected 0+4 summary node in IncludeNodes results: %+v", resNodes.Matches)
	}

	// 3. TurnID exact lookup
	resTurn, err := Search(mgr, SearchArgs{TurnID: "t2"})
	if err != nil {
		t.Fatalf("Search TurnID: %v", err)
	}
	if len(resTurn.Matches) != 1 || resTurn.Matches[0].Address != "2+1" {
		t.Fatalf("expected TurnID=t2 to return 2+1, got %+v", resTurn.Matches)
	}

	// 4. Relative 'since' filter ("2h" should only match event 2+1 from 1h ago)
	resRecent, err := Search(mgr, SearchArgs{Since: "2h"})
	if err != nil {
		t.Fatalf("Search Since 2h: %v", err)
	}
	if len(resRecent.Matches) != 1 || resRecent.Matches[0].Address != "2+1" {
		t.Fatalf("expected Since=2h to return only 2+1, got %+v", resRecent.Matches)
	}

	// 5. Cross-store search with Target="*"
	resAll, err := Search(mgr, SearchArgs{Query: "PostgreSQL advisory locks", Target: "*"})
	if err != nil {
		t.Fatalf("Search Target=*: %v", err)
	}
	if len(resAll.Matches) != 1 || resAll.Matches[0].Target != "work" {
		t.Fatalf("expected cross-store search to find hit in mounted 'work' store, got %+v", resAll.Matches)
	}
}
