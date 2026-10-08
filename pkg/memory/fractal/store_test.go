package fractal

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	// 2. Re-open store and verify bbolt recovery
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

func TestStoreLegacyJSONLMigration(t *testing.T) {
	tempDir := t.TempDir()

	// Pre-create legacy events.jsonl and tree.jsonl with a torn trailing line
	eventsPath := filepath.Join(tempDir, "events.jsonl")
	eventsData := `{"id": 0, "kind": "user", "text": "legacy user message", "timestamp": "2026-10-06T12:00:00Z", "size": 25}
{"id": 1, "kind": "talk", "text": "legacy talk message", "timestamp": "2026-10-06T12:00:01Z", "size": 25}
{"id": 2, "kind": "corrup` // torn line
	if err := os.WriteFile(eventsPath, []byte(eventsData), 0644); err != nil {
		t.Fatalf("failed to write legacy events.jsonl: %v", err)
	}

	treePath := filepath.Join(tempDir, "tree.jsonl")
	treeData := `{"level": 0, "index": 0, "text": "legacy summary 0+1", "size": 20}
{"level": 0, "index": 1, "text": "legacy summary 1+1", "size": 20}
{"level": 1, "index": 0, "text": "corrup` // torn line
	if err := os.WriteFile(treePath, []byte(treeData), 0644); err != nil {
		t.Fatalf("failed to write legacy tree.jsonl: %v", err)
	}

	// OpenStore should seamlessly migrate valid entries into bbolt
	store, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore with legacy files failed: %v", err)
	}
	defer store.Close()

	if store.CountEvents() != 2 {
		t.Fatalf("expected 2 legacy events migrated, got %d", store.CountEvents())
	}
	ev0, ok := store.GetEvent(0)
	if !ok || ev0.Text != "legacy user message" {
		t.Errorf("expected legacy event 0 migrated, got %v", ev0)
	}

	n0, ok := store.GetNode(0, 0)
	if !ok || n0.Text != "legacy summary 0+1" {
		t.Errorf("expected legacy node (0, 0) migrated, got %v", n0)
	}
	n1, ok := store.GetNode(0, 1)
	if !ok || n1.Text != "legacy summary 1+1" {
		t.Errorf("expected legacy node (0, 1) migrated, got %v", n1)
	}
}

func TestStoreReactivePubSub(t *testing.T) {
	tempDir := t.TempDir()
	store, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	var receivedCount int32
	var receivedTexts []string
	var mu sync.Mutex

	store.OnEvent(func(ev Event) {
		atomic.AddInt32(&receivedCount, 1)
		mu.Lock()
		receivedTexts = append(receivedTexts, ev.Text)
		mu.Unlock()
	})

	for i := 0; i < 5; i++ {
		_, err := store.AppendEvent(KindUser, "msg")
		if err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
	}

	// Closing store drains eventChan and waits for runDispatcher to finish completely
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Verify all 5 events were strictly dispatched
	if count := atomic.LoadInt32(&receivedCount); count != 5 {
		t.Errorf("expected 5 events received by listener, got %d", count)
	}
	mu.Lock()
	if len(receivedTexts) != 5 {
		t.Errorf("expected 5 received texts, got %d", len(receivedTexts))
	}
	mu.Unlock()
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

func TestAppendEventOptsTimestamp(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "golem-test-opts-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	store, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// 1. Append with default options -> should be roughly now
	before := time.Now().UTC().Add(-time.Second)
	evDefault, err := store.AppendEvent(KindUser, "default time")
	if err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)
	if evDefault.Timestamp.Before(before) || evDefault.Timestamp.After(after) {
		t.Errorf("expected default timestamp around now, got %v", evDefault.Timestamp)
	}

	// 2. Append with historical timestamp in AppendEventOpts
	historicalTime := time.Date(2025, 4, 15, 10, 30, 0, 0, time.UTC)
	evHistorical, err := store.AppendEvent(KindTalk, "historical answer", AppendEventOpts{
		Timestamp: historicalTime,
	})
	if err != nil {
		t.Fatalf("AppendEvent with opts failed: %v", err)
	}
	if !evHistorical.Timestamp.Equal(historicalTime) {
		t.Errorf("expected historical timestamp %v, got %v", historicalTime, evHistorical.Timestamp)
	}

	// 3. Verify retrieval from memory and bbolt
	retrieved, ok := store.GetEvent(evHistorical.ID)
	if !ok {
		t.Fatalf("GetEvent(%d) failed", evHistorical.ID)
	}
	if !retrieved.Timestamp.Equal(historicalTime) {
		t.Errorf("retrieved event timestamp mismatch: expected %v, got %v", historicalTime, retrieved.Timestamp)
	}

	// 4. Verify persistence across store re-open
	store.Close()
	store2, err := OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore re-open failed: %v", err)
	}
	defer store2.Close()

	retrieved2, ok := store2.GetEvent(evHistorical.ID)
	if !ok {
		t.Fatalf("GetEvent after reopen failed")
	}
	if !retrieved2.Timestamp.Equal(historicalTime) {
		t.Errorf("reopened event timestamp mismatch: expected %v, got %v", historicalTime, retrieved2.Timestamp)
	}
}

func TestStoreEventsTail(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	for i := 0; i < 5; i++ {
		if _, err := store.AppendEvent(KindUser, fmt.Sprintf("e%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	tail := store.EventsTail(3)
	if len(tail) != 3 || tail[0].Text != "e2" || tail[2].Text != "e4" {
		t.Errorf("EventsTail(3) = %v, want e2..e4", tail)
	}
	if all := store.EventsTail(100); len(all) != 5 || all[0].Text != "e0" {
		t.Errorf("EventsTail(100) = %v, want all 5 in order", all)
	}
	if none := store.EventsTail(0); len(none) != 0 {
		t.Errorf("EventsTail(0) = %v, want empty", none)
	}
	if neg := store.EventsTail(-1); len(neg) != 0 {
		t.Errorf("EventsTail(-1) = %v, want empty", neg)
	}
}
