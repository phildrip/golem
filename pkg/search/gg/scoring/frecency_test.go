package scoring

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestFrecencyTracker(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "frecency.json")

	ft := NewFrecencyTracker(dbPath)
	now := time.Now()

	ft.RecordAccess("pkg/search/gg/engine.go", now.Add(-1*time.Hour))
	ft.RecordAccess("pkg/search/gg/engine.go", now)

	score := ft.Score("pkg/search/gg/engine.go", now)
	if score <= 1.0 {
		t.Errorf("expected score > 1.0 after 2 accesses, got: %f", score)
	}

	zeroScore := ft.Score("unknown.go", now)
	if zeroScore != 0.0 {
		t.Errorf("expected zero score for unknown file, got: %f", zeroScore)
	}

	if err := ft.Save(); err != nil {
		t.Fatalf("failed to save frecency: %v", err)
	}

	// Load into a new tracker instance
	ft2 := NewFrecencyTracker(dbPath)
	score2 := ft2.Score("pkg/search/gg/engine.go", now)
	if score2 != score {
		t.Errorf("expected loaded score %f to match original %f", score2, score)
	}
}

// TestFrecencyHalfLifeDecay_FFFPorted ports FFF's 10-day half-life decay tests.
func TestFrecencyHalfLifeDecay_FFFPorted(t *testing.T) {
	ft := NewFrecencyTracker("")
	now := time.Now()

	// Single access 10 days ago should decay to ~0.5
	tenDaysAgo := now.Add(-10 * 24 * time.Hour)
	ft.RecordAccess("old.go", tenDaysAgo)
	score10 := ft.Score("old.go", now)
	if math.Abs(score10-0.5) > 0.05 {
		t.Errorf("expected score ~0.5 after 10 days, got %f", score10)
	}

	// Single access 20 days ago should decay to ~0.25
	twentyDaysAgo := now.Add(-20 * 24 * time.Hour)
	ft.RecordAccess("older.go", twentyDaysAgo)
	score20 := ft.Score("older.go", now)
	if math.Abs(score20-0.25) > 0.05 {
		t.Errorf("expected score ~0.25 after 20 days, got %f", score20)
	}

	// Frequent recent file should rank higher than old file
	ft.RecordAccess("recent.go", now)
	scoreRecent := ft.Score("recent.go", now)
	if scoreRecent <= score10 {
		t.Errorf("recent score %f should exceed old score %f", scoreRecent, score10)
	}

	// Cap test: recording > 64 entries does not exceed maxEntriesPerFile
	for i := 0; i < 100; i++ {
		ft.RecordAccess("frequent.go", now)
	}
	ft.mu.RLock()
	count := len(ft.records["frequent.go"])
	ft.mu.RUnlock()
	if count != maxEntriesPerFile {
		t.Errorf("expected capped entries %d, got %d", maxEntriesPerFile, count)
	}
}

func TestRecencyBoost(t *testing.T) {
	now := time.Now()
	boost1m := RecencyBoost(now.Add(-1*time.Minute), now)
	if boost1m != 25 {
		t.Errorf("expected 25 boost for 1m, got: %d", boost1m)
	}

	boost10m := RecencyBoost(now.Add(-10*time.Minute), now)
	if boost10m != 16 {
		t.Errorf("expected 16 boost for 10m, got: %d", boost10m)
	}

	boostOld := RecencyBoost(now.Add(-48*time.Hour), now)
	if boostOld != 0 {
		t.Errorf("expected 0 boost for 48h, got: %d", boostOld)
	}
}
