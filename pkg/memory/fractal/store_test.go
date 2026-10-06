package fractal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodeAddress(t *testing.T) {
	tests := []struct {
		name       string
		level      int
		index      int
		wantID     int
		wantN      int
		wantAddr   string
		shouldFail bool
	}{
		{
			name:     "level 0 index 0",
			level:    0,
			index:    0,
			wantID:   0,
			wantN:    1,
			wantAddr: "0+1",
		},
		{
			name:     "level 3 index 5",
			level:    3,
			index:    5,
			wantID:   40,
			wantN:    8,
			wantAddr: "40+8",
		},
		{
			name:     "level 8 index 2",
			level:    8,
			index:    2,
			wantID:   512,
			wantN:    256,
			wantAddr: "512+256",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			node := Node{Level: tc.level, Index: tc.index, Text: "sample summary"}
			start, end := node.Range()
			if start != tc.wantID {
				t.Errorf("start = %d, want %d", start, tc.wantID)
			}
			if end != tc.wantID+tc.wantN {
				t.Errorf("end = %d, want %d", end, tc.wantID+tc.wantN)
			}
			if addr := node.Address(); addr != tc.wantAddr {
				t.Errorf("address = %s, want %s", addr, tc.wantAddr)
			}

			// Test roundtrip with ParseAddress
			lvl, idx, err := ParseAddress(tc.wantID, tc.wantN)
			if err != nil {
				t.Fatalf("ParseAddress failed: %v", err)
			}
			if lvl != tc.level || idx != tc.index {
				t.Errorf("ParseAddress = (%d, %d), want (%d, %d)", lvl, idx, tc.level, tc.index)
			}
		})
	}
}

func TestStorePersistenceAndDurability(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Open new store and write events and nodes
	store, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	ev1, err := store.AppendEvent(KindUser, "hello golem")
	if err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	if ev1.ID != 0 {
		t.Errorf("expected ev1.ID=0, got %d", ev1.ID)
	}

	ev2, err := store.AppendEvent(KindTalk, "golem is ready")
	if err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	if ev2.ID != 1 {
		t.Errorf("expected ev2.ID=1, got %d", ev2.ID)
	}

	node0 := Node{Level: 0, Index: 0, Text: "user: hello golem"}
	if err := store.SaveNode(node0); err != nil {
		t.Fatalf("SaveNode failed: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// 2. Simulate appending a torn partial line to test crash recovery
	eventsPath := filepath.Join(tempDir, "events.jsonl")
	f, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("failed to open events for torn line append: %v", err)
	}
	_, _ = f.WriteString(`{"id": 2, "kind": "use`) // incomplete JSON
	f.Close()

	// 3. Re-open store and verify recovery
	store2, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore on re-open failed: %v", err)
	}
	defer store2.Close()

	if store2.CountEvents() != 2 {
		t.Errorf("expected 2 valid events recovered, got %d", store2.CountEvents())
	}

	retrievedNode, ok := store2.GetNode(0, 0)
	if !ok || retrievedNode.Text != "user: hello golem" {
		t.Errorf("expected node recovered, got %v", retrievedNode)
	}

	retrievedByAddr, ok := store2.GetNodeByAddress(0, 1)
	if !ok || retrievedByAddr.Text != "user: hello golem" {
		t.Errorf("expected node by address recovered, got %v", retrievedByAddr)
	}

	// Out of bounds GetEvent
	if _, ok := store2.GetEvent(-1); ok {
		t.Errorf("expected false for negative event ID")
	}
	if _, ok := store2.GetEvent(100); ok {
		t.Errorf("expected false for out of bounds event ID")
	}

	// Test Close idempotency
	if err := store2.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := store2.Close(); err != nil {
		t.Fatalf("second Close failed (should be idempotent): %v", err)
	}

	// Test ErrStoreClosed on post-close mutations
	if _, err := store2.AppendEvent(KindUser, "post close"); err != ErrStoreClosed {
		t.Errorf("expected ErrStoreClosed from AppendEvent after close, got %v", err)
	}
	if err := store2.SaveNode(Node{Level: 1, Index: 0, Text: "post close"}); err != ErrStoreClosed {
		t.Errorf("expected ErrStoreClosed from SaveNode after close, got %v", err)
	}
}

func TestParseAddressErrors(t *testing.T) {
	tests := []struct {
		name string
		id   int
		n    int
	}{
		{"n is 0", 0, 0},
		{"n is negative", 0, -1},
		{"n is not power of 2", 0, 3},
		{"n is 5", 0, 5},
		{"id is negative", -4, 4},
		{"id not multiple of n", 5, 4},
		{"id not multiple of n (odd)", 1, 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseAddress(tc.id, tc.n)
			if err == nil {
				t.Errorf("expected error for id=%d, n=%d, got nil", tc.id, tc.n)
			}
		})
	}
}

func TestTreeTornLineRecovery(t *testing.T) {
	tempDir := t.TempDir()
	store, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	node := Node{Level: 0, Index: 0, Text: "valid node"}
	if err := store.SaveNode(node); err != nil {
		t.Fatalf("SaveNode failed: %v", err)
	}
	store.Close()

	// Append torn line to tree.jsonl
	treePath := filepath.Join(tempDir, "tree.jsonl")
	f, err := os.OpenFile(treePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("failed to open tree.jsonl: %v", err)
	}
	_, _ = f.WriteString(`{"level": 1, "index": 0, "text": "corrup`)
	f.Close()

	// Reopen and verify valid node is intact
	store2, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("re-open failed: %v", err)
	}
	defer store2.Close()

	n, ok := store2.GetNode(0, 0)
	if !ok || n.Text != "valid node" {
		t.Errorf("expected valid node recovered, got %v", n)
	}
	if _, ok := store2.GetNode(1, 0); ok {
		t.Errorf("expected torn node not recovered")
	}
}
