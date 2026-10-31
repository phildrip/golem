package runtime

import (
	"context"
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
