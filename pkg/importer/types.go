package importer

import (
	"time"

	"github.com/user/golem/pkg/memory/fractal"
)

// FormatType represents the detected agent transcript format.
type FormatType string

const (
	FormatUnknown  FormatType = "unknown"
	FormatAGY      FormatType = "agy"      // Antigravity JSONL transcripts
	FormatCursor   FormatType = "cursor"   // Cursor SQLite state.vscdb / composerData
	FormatOpenCode FormatType = "opencode" // OpenCode SQLite opencode.db
)

// DiscoveredSession represents an importable conversation found in the environment.
type DiscoveredSession struct {
	ID               string     `json:"id"`
	Format           FormatType `json:"format"`
	Title            string     `json:"title"`
	Path             string     `json:"path"`
	Workspace        string     `json:"workspace,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	EventCount       int        `json:"event_count"`
	MatchesWorkspace bool       `json:"matches_workspace"`
}

// ImportedEvent represents an individual interaction normalized for Golem fractal memory.
type ImportedEvent struct {
	Kind      fractal.EventKind `json:"kind"`
	Text      string            `json:"text"`
	Timestamp time.Time         `json:"timestamp"`
}

func safePrefix(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
