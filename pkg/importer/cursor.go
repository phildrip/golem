package importer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/memory/fractal"
	_ "modernc.org/sqlite"
)

type cursorComposerHeader struct {
	ComposerID    string `json:"composerId"`
	Name          string `json:"name,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	LastUpdatedAt int64  `json:"lastUpdatedAt"`
	Text          string `json:"text,omitempty"`
	UnifiedMode   string `json:"unifiedMode,omitempty"`
}

type cursorComposerData struct {
	AllComposers []cursorComposerHeader `json:"allComposers"`
}

type cursorBubbleHeader struct {
	BubbleID string `json:"bubbleId"`
	Type     int    `json:"type"` // 1 = user, 2 = assistant
}

type cursorComposerDetails struct {
	ComposerID                 string               `json:"composerId"`
	CreatedAt                  int64                `json:"createdAt"`
	LastUpdatedAt              int64                `json:"lastUpdatedAt"`
	FullConversationHeadersOnly []cursorBubbleHeader `json:"fullConversationHeadersOnly"`
}

type cursorBubble struct {
	BubbleID            string          `json:"bubbleId"`
	Type                int             `json:"type"`
	Text                string          `json:"text"`
	SuggestedCodeBlocks []json.RawMessage `json:"suggestedCodeBlocks,omitempty"`
	ToolResults         []json.RawMessage `json:"toolResults,omitempty"`
}

type cursorGeneration struct {
	UnixMs          int64  `json:"unixMs"`
	TextDescription string `json:"textDescription"`
	Type            string `json:"type"`
}

// DefaultCursorGlobalStoragePath returns the platform-specific path to global state.vscdb.
func DefaultCursorGlobalStoragePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb")
	default:
		return filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb")
	}
}

// DefaultCursorWorkspaceStorageDir returns the platform-specific directory where workspace state is stored.
func DefaultCursorWorkspaceStorageDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "workspaceStorage")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "workspaceStorage")
	default:
		return filepath.Join(home, ".config", "Cursor", "User", "workspaceStorage")
	}
}

// ParseCursor parses a Cursor SQLite database or workspace folder into ImportedEvents.
func ParseCursor(path string, composerID string) ([]ImportedEvent, error) {
	dbPath := path
	fi, err := os.Stat(path)
	if err != nil {
		return nil, errors.Wrapf(err, "stat Cursor path %q", path)
	}

	if fi.IsDir() {
		candidate := filepath.Join(path, "state.vscdb")
		if fileExists(candidate) {
			dbPath = candidate
		} else {
			// Check if path is a workspace root; find its corresponding workspaceStorage folder
			resolved, err := findWorkspaceDBForDir(path)
			if err != nil {
				return nil, err
			}
			dbPath = resolved
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, errors.Wrapf(err, "opening sqlite db %q", dbPath)
	}
	defer db.Close()

	// 1. Try to read composer.composerData from ItemTable
	var rawComposerData string
	err = db.QueryRow("SELECT value FROM ItemTable WHERE key = 'composer.composerData'").Scan(&rawComposerData)

	var composers []cursorComposerHeader
	if err == nil && rawComposerData != "" {
		var cd cursorComposerData
		if err := json.Unmarshal([]byte(rawComposerData), &cd); err == nil {
			composers = cd.AllComposers
		}
	}

	// 2. Locate target composer
	var targetComposer *cursorComposerHeader
	if composerID != "" {
		for i := range composers {
			if composers[i].ComposerID == composerID {
				targetComposer = &composers[i]
				break
			}
		}
		if targetComposer == nil {
			return nil, errors.Errorf("specified composer %q not found in %q", composerID, dbPath)
		}
	} else if len(composers) > 0 {
		// Sort descending by recency
		sort.Slice(composers, func(i, j int) bool {
			tI := composers[i].LastUpdatedAt
			if tI == 0 {
				tI = composers[i].CreatedAt
			}
			tJ := composers[j].LastUpdatedAt
			if tJ == 0 {
				tJ = composers[j].CreatedAt
			}
			return tI > tJ
		})
		targetComposer = &composers[0]
	}

	// 3. Extract events from target composer or globalStorage
	if targetComposer != nil {
		events, err := extractComposerEvents(dbPath, targetComposer.ComposerID, targetComposer.CreatedAt, targetComposer.LastUpdatedAt)
		if err == nil && len(events) > 0 {
			return events, nil
		}
	}

	// 4. Fallback: try reading aiService.generations from ItemTable
	var rawGenerations string
	err = db.QueryRow("SELECT value FROM ItemTable WHERE key = 'aiService.generations'").Scan(&rawGenerations)
	if err == nil && rawGenerations != "" {
		var gens []cursorGeneration
		if err := json.Unmarshal([]byte(rawGenerations), &gens); err == nil && len(gens) > 0 {
			var events []ImportedEvent
			for _, g := range gens {
				text := strings.TrimSpace(g.TextDescription)
				if text == "" {
					continue
				}
				ts := time.Now().UTC()
				if g.UnixMs > 0 {
					ts = time.UnixMilli(g.UnixMs).UTC()
				}
				events = append(events, ImportedEvent{
					Kind:      fractal.KindUser,
					Text:      text,
					Timestamp: ts,
				})
			}
			if len(events) > 0 {
				return events, nil
			}
		}
	}

	return nil, errors.Errorf("no conversation events found in Cursor database %q", dbPath)
}

func extractComposerEvents(localDBPath, cid string, createdAtMs, lastUpdatedAtMs int64) ([]ImportedEvent, error) {
	// Bubbles might be in localDB or globalStorage
	candidates := []string{localDBPath, DefaultCursorGlobalStoragePath()}
	for _, cand := range candidates {
		if !fileExists(cand) {
			continue
		}
		events, err := extractBubblesFromDB(cand, cid, createdAtMs, lastUpdatedAtMs)
		if err == nil && len(events) > 0 {
			return events, nil
		}
	}
	return nil, errors.Errorf("no bubbles found for composer %s", cid)
}

func extractBubblesFromDB(dbPath, cid string, createdAtMs, lastUpdatedAtMs int64) ([]ImportedEvent, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Check if composerData:<cid> exists in cursorDiskKV
	var rawData string
	key := "composerData:" + cid
	err = db.QueryRow("SELECT value FROM cursorDiskKV WHERE key = ?", key).Scan(&rawData)
	if err != nil || rawData == "" {
		return nil, errors.New("composerData key not found")
	}

	var details cursorComposerDetails
	if err := json.Unmarshal([]byte(rawData), &details); err != nil {
		return nil, err
	}

	if details.CreatedAt > 0 {
		createdAtMs = details.CreatedAt
	}
	if details.LastUpdatedAt > 0 {
		lastUpdatedAtMs = details.LastUpdatedAt
	}

	if createdAtMs <= 0 {
		if lastUpdatedAtMs > 0 {
			createdAtMs = lastUpdatedAtMs
		} else {
			createdAtMs = time.Now().UnixMilli()
		}
	}
	if lastUpdatedAtMs <= 0 {
		lastUpdatedAtMs = createdAtMs
	}

	headers := details.FullConversationHeadersOnly
	if len(headers) == 0 {
		return nil, errors.New("no conversation headers")
	}

	start := time.UnixMilli(createdAtMs).UTC()
	end := time.UnixMilli(lastUpdatedAtMs).UTC()
	if end.Before(start) {
		end = start
	}

	var events []ImportedEvent
	total := len(headers)

	for i, h := range headers {
		var bubbleRaw string
		bKey := fmt.Sprintf("bubbleId:%s:%s", cid, h.BubbleID)
		err := db.QueryRow("SELECT value FROM cursorDiskKV WHERE key = ?", bKey).Scan(&bubbleRaw)
		if err != nil || bubbleRaw == "" {
			continue
		}

		var b cursorBubble
		if err := json.Unmarshal([]byte(bubbleRaw), &b); err != nil {
			continue
		}

		text := strings.TrimSpace(b.Text)
		if text == "" {
			continue
		}

		// Interpolate timestamp along turn progression
		var ts time.Time
		if total <= 1 || start.Equal(end) {
			ts = start
		} else {
			fraction := float64(i) / float64(total-1)
			diff := end.Sub(start)
			ts = start.Add(time.Duration(float64(diff) * fraction))
		}

		kind := fractal.KindTalk
		if b.Type == 1 {
			kind = fractal.KindUser
		}

		events = append(events, ImportedEvent{
			Kind:      kind,
			Text:      text,
			Timestamp: ts,
		})

		// Tool suggestions
		if len(b.SuggestedCodeBlocks) > 0 {
			events = append(events, ImportedEvent{
				Kind:      fractal.KindTool,
				Text:      fmt.Sprintf("suggested_code_block: %d blocks", len(b.SuggestedCodeBlocks)),
				Timestamp: ts,
			})
		}
	}

	return events, nil
}

func findWorkspaceDBForDir(targetDir string) (string, error) {
	wsDir := DefaultCursorWorkspaceStorageDir()
	if !fileExists(wsDir) && !isDir(wsDir) {
		return "", errors.Errorf("workspace storage dir not found at %q", wsDir)
	}

	cleanTarget := filepath.Clean(targetDir)
	entries, err := os.ReadDir(wsDir)
	if err != nil {
		return "", errors.Wrap(err, "reading workspace storage dir")
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wsFile := filepath.Join(wsDir, e.Name(), "workspace.json")
		if !fileExists(wsFile) {
			continue
		}
		data, err := os.ReadFile(wsFile)
		if err != nil {
			continue
		}
		var meta struct {
			Folder string `json:"folder"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		folderPath := strings.TrimPrefix(meta.Folder, "file://")
		if filepath.Clean(folderPath) == cleanTarget {
			vscdb := filepath.Join(wsDir, e.Name(), "state.vscdb")
			if fileExists(vscdb) {
				return vscdb, nil
			}
		}
	}

	return "", errors.Errorf("no matching Cursor workspace found for %q", targetDir)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
