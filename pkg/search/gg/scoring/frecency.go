package scoring

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// Half-life decay constant: ln(2) / 10 days
	decayConstant    = 0.0693
	secondsPerDay    = 86400.0
	maxHistoryDays   = 30.0
	maxEntriesPerFile = 64
)

// FrecencyTracker maintains access frequency and recency stats for files.
type FrecencyTracker struct {
	mu         sync.RWMutex
	records    map[string][]int64 // path -> slice of unix epoch seconds
	dbFilePath string
}

// NewFrecencyTracker creates a tracker, optionally loaded from dbPath.
func NewFrecencyTracker(dbPath string) *FrecencyTracker {
	ft := &FrecencyTracker{
		records:    make(map[string][]int64),
		dbFilePath: dbPath,
	}

	if dbPath != "" {
		_ = ft.Load()
	}

	return ft
}

// RecordAccess registers a file access event at the specified timestamp.
func (ft *FrecencyTracker) RecordAccess(relPath string, t time.Time) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	norm := filepath.ToSlash(filepath.Clean(relPath))
	sec := t.Unix()

	ts := ft.records[norm]
	ts = append(ts, sec)
	if len(ts) > maxEntriesPerFile {
		ts = ts[len(ts)-maxEntriesPerFile:]
	}
	ft.records[norm] = ts
}

// Score computes the frecency score for a file as of now.
func (ft *FrecencyTracker) Score(relPath string, now time.Time) float64 {
	ft.mu.RLock()
	defer ft.mu.RUnlock()

	norm := filepath.ToSlash(filepath.Clean(relPath))
	ts, ok := ft.records[norm]
	if !ok || len(ts) == 0 {
		return 0.0
	}

	nowSec := float64(now.Unix())
	var total float64

	for _, s := range ts {
		diffSec := nowSec - float64(s)
		if diffSec < 0 {
			diffSec = 0
		}
		days := diffSec / secondsPerDay
		if days > maxHistoryDays {
			continue
		}
		// Exponential half-life decay
		total += math.Exp(-decayConstant * days)
	}

	return total
}

// RecencyBoost returns a bonus score based on file modification age.
func RecencyBoost(modTime, now time.Time) int {
	age := now.Sub(modTime)
	switch {
	case age <= 2*time.Minute:
		return 25
	case age <= 15*time.Minute:
		return 16
	case age <= 1*time.Hour:
		return 10
	case age <= 24*time.Hour:
		return 5
	default:
		return 0
	}
}

// Save writes frecency stats to disk.
func (ft *FrecencyTracker) Save() error {
	ft.mu.RLock()
	defer ft.mu.RUnlock()

	if ft.dbFilePath == "" {
		return nil
	}

	data, err := json.Marshal(ft.records)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(ft.dbFilePath), 0755); err != nil {
		return err
	}

	return os.WriteFile(ft.dbFilePath, data, 0644)
}

// Load reads frecency stats from disk.
func (ft *FrecencyTracker) Load() error {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if ft.dbFilePath == "" {
		return nil
	}

	data, err := os.ReadFile(ft.dbFilePath)
	if err != nil {
		return err
	}

	return json.Unmarshal(data, &ft.records)
}
