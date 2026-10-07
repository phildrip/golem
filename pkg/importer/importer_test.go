package importer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/user/golem/pkg/memory/fractal"
	_ "modernc.org/sqlite"
)

func TestDeduceFormat(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "golem-test-deduce-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 1. AGY file
	agyFile := filepath.Join(tempDir, "transcript.jsonl")
	if err := os.WriteFile(agyFile, []byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-10-05T15:46:40Z","content":"hello"}`+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	fmtAgy, err := DeduceFormat(agyFile)
	if err != nil || fmtAgy != FormatAGY {
		t.Errorf("expected FormatAGY, got %v (err: %v)", fmtAgy, err)
	}

	// 2. AGY directory
	agyDir := filepath.Join(tempDir, "agy-brain")
	logDir := filepath.Join(agyDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(`{"step_index":0,"type":"USER_INPUT"}`+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	fmtAgyDir, err := DeduceFormat(agyDir)
	if err != nil || fmtAgyDir != FormatAGY {
		t.Errorf("expected FormatAGY for dir, got %v (err: %v)", fmtAgyDir, err)
	}

	// 3. Cursor SQLite file
	cursorFile := filepath.Join(tempDir, "test.vscdb")
	db, err := sql.Open("sqlite", cursorFile)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	_, _ = db.Exec("CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT);")
	db.Close()

	fmtCursor, err := DeduceFormat(cursorFile)
	if err != nil || fmtCursor != FormatCursor {
		t.Errorf("expected FormatCursor, got %v (err: %v)", fmtCursor, err)
	}
}

func TestParseAGY(t *testing.T) {
	rawJSONL := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-10-05T15:46:40Z","content":"<USER_REQUEST>\nImplement caching for task service\n</USER_REQUEST>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-10-05T15:46:45Z","content":"I will inspect the existing task cache implementation.","tool_calls":[{"name":"view_file","args":{"path":"service.go"}}]}
{"step_index":2,"source":"SYSTEM","type":"TOOL_RESPONSE","status":"DONE","created_at":"2026-10-05T15:46:48Z","content":"func CacheTask() {}"}`

	events, err := ParseAGY(strings.NewReader(rawJSONL))
	if err != nil {
		t.Fatalf("ParseAGY failed: %v", err)
	}

	if len(events) != 4 {
		t.Fatalf("expected 4 events (user, talk, tool, echo), got %d", len(events))
	}

	// Check User event
	if events[0].Kind != fractal.KindUser || events[0].Text != "Implement caching for task service" {
		t.Errorf("unexpected user event: %+v", events[0])
	}
	expectedT0 := time.Date(2026, 10, 5, 15, 46, 40, 0, time.UTC)
	if !events[0].Timestamp.Equal(expectedT0) {
		t.Errorf("expected timestamp %v, got %v", expectedT0, events[0].Timestamp)
	}

	// Check Talk event
	if events[1].Kind != fractal.KindTalk || events[1].Text != "I will inspect the existing task cache implementation." {
		t.Errorf("unexpected talk event: %+v", events[1])
	}

	// Check Tool event
	if events[2].Kind != fractal.KindTool || !strings.Contains(events[2].Text, "view_file") {
		t.Errorf("unexpected tool event: %+v", events[2])
	}

	// Check Echo event
	if events[3].Kind != fractal.KindEcho || events[3].Text != "func CacheTask() {}" {
		t.Errorf("unexpected echo event: %+v", events[3])
	}
}

func TestParseCursorAndImportToStore(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "golem-test-cursor-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create synthetic Cursor SQLite database
	dbPath := filepath.Join(tempDir, "state.vscdb")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}

	_, err = db.Exec("CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT);")
	if err != nil {
		t.Fatalf("create ItemTable failed: %v", err)
	}

	// Insert generations fallback
	gens := []cursorGeneration{
		{
			UnixMs:          1765000000000,
			TextDescription: "How do I optimize the SQLite indexing?",
			Type:            "composer",
		},
		{
			UnixMs:          1765000060000,
			TextDescription: "Please add an index on (updated_at DESC)",
			Type:            "composer",
		},
	}
	gensJSON, _ := json.Marshal(gens)
	_, err = db.Exec("INSERT INTO ItemTable (key, value) VALUES ('aiService.generations', ?)", string(gensJSON))
	if err != nil {
		t.Fatalf("insert generations failed: %v", err)
	}
	db.Close()

	// Create a fractal memory store
	storeDir := filepath.Join(tempDir, "store")
	store, err := fractal.OpenStore(storeDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// Run ImportFileOrDir
	importedCount, err := ImportFileOrDir(dbPath, store)
	if err != nil {
		t.Fatalf("ImportFileOrDir failed: %v", err)
	}
	if importedCount != 2 {
		t.Errorf("expected 2 imported events, got %d", importedCount)
	}

	// Verify imported events in fractal store
	allEvents := store.GetAllEvents()
	if len(allEvents) != 2 {
		t.Fatalf("expected 2 events in store, got %d", len(allEvents))
	}

	ev0 := allEvents[0]
	if ev0.Kind != fractal.KindUser || ev0.Text != "How do I optimize the SQLite indexing?" {
		t.Errorf("unexpected ev0: %+v", ev0)
	}
	expectedTs0 := time.UnixMilli(1765000000000).UTC()
	if !ev0.Timestamp.Equal(expectedTs0) {
		t.Errorf("expected timestamp %v, got %v", expectedTs0, ev0.Timestamp)
	}
}

func TestDiscoverHistories(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "golem-test-discover-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Set AGY_BRAIN_DIR to tempDir
	t.Setenv("AGY_BRAIN_DIR", tempDir)

	// Create 2 fake AGY conversation dirs
	// Conv 1: matches workspace /my/workspace
	conv1Dir := filepath.Join(tempDir, "conv-1", ".system_generated", "logs")
	if err := os.MkdirAll(conv1Dir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	c1Data := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-10-01T10:00:00Z","content":"<USER_REQUEST>\nTask in my workspace\n</USER_REQUEST>\nActive Workspaces:\n- /my/workspace"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-10-01T10:05:00Z","content":"Working on it"}`
	_ = os.WriteFile(filepath.Join(conv1Dir, "transcript.jsonl"), []byte(c1Data), 0644)

	// Conv 2: different workspace, but newer
	conv2Dir := filepath.Join(tempDir, "conv-2", ".system_generated", "logs")
	if err := os.MkdirAll(conv2Dir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	c2Data := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-10-02T10:00:00Z","content":"<USER_REQUEST>\nOther task\n</USER_REQUEST>\nActive Workspaces:\n- /other/dir"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-10-02T10:05:00Z","content":"Working on it"}`
	_ = os.WriteFile(filepath.Join(conv2Dir, "transcript.jsonl"), []byte(c2Data), 0644)

	// Discover with currentWorkspace = /my/workspace
	sessions, err := DiscoverHistories("/my/workspace")
	if err != nil {
		t.Fatalf("DiscoverHistories failed: %v", err)
	}

	if len(sessions) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(sessions))
	}

	// First session should be the matching workspace one (conv-1) despite conv-2 being newer
	if sessions[0].ID != "conv-1" {
		t.Errorf("expected sessions[0].ID == 'conv-1' (matched workspace), got %q", sessions[0].ID)
	}
	if !sessions[0].MatchesWorkspace {
		t.Errorf("expected sessions[0].MatchesWorkspace == true")
	}
	if sessions[0].Title != "Task in my workspace" {
		t.Errorf("expected title 'Task in my workspace', got %q", sessions[0].Title)
	}
}

func TestParseAGYLargeLine(t *testing.T) {
	// Create an AGY step with content > 1MB
	largeText := strings.Repeat("A", 1200000)
	rawJSONL := fmt.Sprintf(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-10-05T15:46:40Z","content":%q}`, largeText)

	events, err := ParseAGY(strings.NewReader(rawJSONL))
	if err != nil {
		t.Fatalf("ParseAGY with large line failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if len(events[0].Text) != len(largeText) {
		t.Errorf("text length mismatch: expected %d, got %d", len(largeText), len(events[0].Text))
	}
}
