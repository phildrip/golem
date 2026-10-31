package runtime

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/memory/fractal"
)

func testBaseConfig(t *testing.T) golem.Config {
	t.Helper()
	dir := t.TempDir()
	return golem.Config{
		Provider:          "opencode",
		BaseURL:           "http://127.0.0.1:1",
		ModelName:         "test-model",
		APIKey:            "test-key",
		StorageDir:        dir,
		WorkspaceDir:      dir,
		LiveHeadCount:     4,
		ViewBudgetBytes:   1024,
		CompactorInterval: 50 * time.Millisecond,
	}
}

func TestSuspendReleasesBboltLockAndResumeRestoresAgent(t *testing.T) {
	sm := NewSessionManager(testBaseConfig(t), nil)
	defer sm.Close()

	ctx := context.Background()
	sess, err := sm.CreateSession(ctx, "Lock Test")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := sess.Agent.Store().AppendEvent(fractal.KindUser, "hello before suspend"); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	if err := sess.Suspend(ctx); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if !sess.IsSuspended() {
		t.Fatal("expected session to report IsSuspended() == true")
	}

	// Cached transcript must remain readable while suspended without reopening bbolt.
	cached := sess.Events()
	if len(cached) != 1 || cached[0].Text != "hello before suspend" {
		t.Fatalf("expected cached event while suspended, got %+v", cached)
	}

	// Verify the bbolt file lock was released by opening the store from a second handle.
	secondHandle, err := fractal.OpenStore(sess.StorageDir())
	if err != nil {
		t.Fatalf("expected bbolt lock to be released on Suspend, but OpenStore failed: %v", err)
	}
	if err := secondHandle.Close(); err != nil {
		t.Fatalf("Close second handle: %v", err)
	}

	// Resume must reopen the store and restore a working agent with persisted history.
	if err := sess.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if sess.IsSuspended() || sess.Agent == nil {
		t.Fatal("expected session to be open with non-nil Agent after Resume")
	}
	events := sess.Agent.Store().GetAllEvents()
	if len(events) != 1 || events[0].Text != "hello before suspend" {
		t.Fatalf("expected persisted event after Resume, got %+v", events)
	}
}

func TestLRUMaxOpenSessionsCapAndGoroutineCleanup(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()

	sm := NewSessionManager(testBaseConfig(t), nil)
	sm.SetMaxOpenSessions(3)
	ctx := context.Background()

	var ids []string
	for i := 0; i < 10; i++ {
		sess, err := sm.CreateSession(ctx, "")
		if err != nil {
			t.Fatalf("CreateSession %d: %v", i, err)
		}
		ids = append(ids, sess.ID)
		if open := sm.OpenSessionCount(); open > 3 {
			t.Fatalf("after creating %d sessions, open count = %d, want <= 3", i+1, open)
		}
	}

	if open := sm.OpenSessionCount(); open != 3 {
		t.Fatalf("OpenSessionCount = %d, want 3", open)
	}

	// First session created should remain active (or kept), while middle ones were suspended.
	// Switching to a suspended session should transparently resume it and keep open <= 3.
	targetID := ids[2]
	targetSess, _ := sm.GetSession(targetID)
	if !targetSess.IsSuspended() {
		t.Fatalf("expected session %s to have been LRU-suspended", targetID)
	}
	if !sm.SetActiveSession(targetID) {
		t.Fatalf("SetActiveSession(%s) failed", targetID)
	}
	if targetSess.IsSuspended() || targetSess.Agent == nil {
		t.Fatalf("expected SetActiveSession to resume %s", targetID)
	}
	if open := sm.OpenSessionCount(); open > 3 {
		t.Fatalf("after activating suspended session, OpenSessionCount = %d, want <= 3", open)
	}

	// Idle suspension should suspend all remaining open sessions once past idleTimeout.
	sm.SetIdleTimeout(10 * time.Millisecond)
	n := sm.SuspendIdleSessions(ctx, time.Now().UTC().Add(time.Second))
	if n != 3 {
		t.Fatalf("SuspendIdleSessions suspended %d sessions, want 3", n)
	}
	if open := sm.OpenSessionCount(); open != 0 {
		t.Fatalf("expected 0 open sessions after idle suspension, got %d", open)
	}

	sm.Close()
	time.Sleep(30 * time.Millisecond)
	afterGoroutines := runtime.NumGoroutine()
	if afterGoroutines > baselineGoroutines+4 {
		t.Fatalf("compactor goroutines leaked: baseline=%d after=%d", baselineGoroutines, afterGoroutines)
	}
}

func TestSessionManager_PersistsSessionsAcrossRestarts(t *testing.T) {
	tmpDir := t.TempDir()
	baseCfg := golem.Config{
		Provider:          "opencode",
		BaseURL:           "http://127.0.0.1:1",
		ModelName:         "test-model",
		APIKey:            "test-key",
		StorageDir:        tmpDir,
		WorkspaceDir:      tmpDir,
		LiveHeadCount:     4,
		ViewBudgetBytes:   1024,
		CompactorInterval: 50 * time.Millisecond,
	}

	ctx := context.Background()
	sm1 := NewSessionManager(baseCfg, nil)

	sessA, err := sm1.CreateSession(ctx, "Project Alpha")
	if err != nil {
		t.Fatalf("CreateSession Project Alpha: %v", err)
	}
	idA := sessA.ID
	if _, err := sessA.Agent.Store().AppendEvent(fractal.KindUser, "task 1 in alpha"); err != nil {
		t.Fatalf("AppendEvent A: %v", err)
	}

	sessB, err := sm1.CreateSession(ctx, "Project Beta")
	if err != nil {
		t.Fatalf("CreateSession Project Beta: %v", err)
	}
	idB := sessB.ID
	if _, err := sessB.Agent.Store().AppendEvent(fractal.KindUser, "task 2 in beta"); err != nil {
		t.Fatalf("AppendEvent B: %v", err)
	}

	// Close first manager instance
	sm1.Close()

	// Verify sessions.json exists on disk
	registryFile := filepath.Join(tmpDir, "sessions.json")
	if _, err := os.Stat(registryFile); err != nil {
		t.Fatalf("expected sessions.json to exist at %s", registryFile)
	}

	// Boot up a second SessionManager instance pointing to the same storage directory
	sm2 := NewSessionManager(baseCfg, nil)
	defer sm2.Close()

	list := sm2.ListSessions()
	if len(list) != 2 {
		t.Fatalf("expected 2 restored sessions, got %d: %+v", len(list), list)
	}

	foundA, foundB := false, false
	for _, info := range list {
		if info.ID == idA && info.Title == "Project Alpha" {
			foundA = true
			if !info.Suspended {
				t.Errorf("expected restored session Alpha to start suspended")
			}
		}
		if info.ID == idB && info.Title == "Project Beta" {
			foundB = true
			if !info.Suspended {
				t.Errorf("expected restored session Beta to start suspended")
			}
		}
	}
	if !foundA || !foundB {
		t.Fatalf("missing restored sessions: foundA=%v foundB=%v", foundA, foundB)
	}

	// Read events on suspended session without full resumption
	restoredA, ok := sm2.GetSession(idA)
	if !ok {
		t.Fatalf("GetSession(%s) failed", idA)
	}
	evsA := restoredA.Events()
	if len(evsA) != 1 || evsA[0].Text != "task 1 in alpha" {
		t.Fatalf("expected event 'task 1 in alpha' on restored suspended session, got %+v", evsA)
	}

	// Resume session A and verify working state
	if err := restoredA.Resume(ctx); err != nil {
		t.Fatalf("Resume(%s) failed: %v", idA, err)
	}
	if restoredA.IsSuspended() || restoredA.Agent == nil {
		t.Fatalf("expected session A to be resumed with active agent")
	}

	// Delete session B
	if err := sm2.DeleteSession(idB); err != nil {
		t.Fatalf("DeleteSession(%s) failed: %v", idB, err)
	}
	if len(sm2.ListSessions()) != 1 {
		t.Fatalf("expected 1 session remaining after deletion")
	}

	// Boot up a third instance: session B must remain deleted
	sm3 := NewSessionManager(baseCfg, nil)
	defer sm3.Close()
	if len(sm3.ListSessions()) != 1 || sm3.ListSessions()[0].ID != idA {
		t.Fatalf("expected only session A persisted, got %+v", sm3.ListSessions())
	}
}

func TestSessionManager_DiscoversExistingSessionDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	baseCfg := golem.Config{
		Provider:          "opencode",
		BaseURL:           "http://127.0.0.1:1",
		ModelName:         "test-model",
		APIKey:            "test-key",
		StorageDir:        tmpDir,
		WorkspaceDir:      tmpDir,
		LiveHeadCount:     4,
		ViewBudgetBytes:   1024,
		CompactorInterval: 50 * time.Millisecond,
	}

	// Pre-create a session directory on disk with a store and a prompt
	sessDir := filepath.Join(tmpDir, "session-1799999999999-1")
	store, err := fractal.OpenStore(sessDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, err := store.AppendEvent(fractal.KindUser, "Inspect memory leak in subsystem"); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}

	// Boot up SessionManager without any pre-existing sessions.json
	sm := NewSessionManager(baseCfg, nil)
	defer sm.Close()

	list := sm.ListSessions()
	if len(list) != 1 {
		t.Fatalf("expected 1 discovered session, got %d", len(list))
	}
	if list[0].ID != "session-1799999999999-1" {
		t.Errorf("expected discovered ID session-1799999999999-1, got %s", list[0].ID)
	}
	if list[0].Title != "Inspect memory leak in s..." {
		t.Errorf("expected title inferred from prompt 'Inspect memory leak in s...', got %q", list[0].Title)
	}

	// Verify sessions.json was written
	if _, err := os.Stat(filepath.Join(tmpDir, "sessions.json")); err != nil {
		t.Errorf("expected sessions.json to be created after discovery")
	}
}

