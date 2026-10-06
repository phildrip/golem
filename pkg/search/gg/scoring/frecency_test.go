package scoring

import (
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
