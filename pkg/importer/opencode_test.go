package importer

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/golem/pkg/memory/fractal"
	_ "modernc.org/sqlite"
)

// buildOpenCodeTestDB creates a minimal OpenCode-shaped SQLite database.
func buildOpenCodeTestDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL DEFAULT '', slug TEXT NOT NULL DEFAULT '',
			directory TEXT NOT NULL DEFAULT '', title TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '',
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL,
			time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
			time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	mustExec(`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
		VALUES ('ses_test', 'proj', 'slug', '/home/phil/git/golem', 'Test session', '1.0', 1000, 3000)`)

	// user message + one text part
	mustExec(`INSERT INTO message VALUES ('msg_1','ses_test',1000,1000,?)`,
		`{"role":"user","time":{"created":1000}}`)
	mustExec(`INSERT INTO part VALUES ('prt_1','msg_1','ses_test',1000,1000,?)`,
		`{"type":"text","text":"hello golem"}`)

	// assistant message: reasoning (skipped), text, and a tool call with output
	mustExec(`INSERT INTO message VALUES ('msg_2','ses_test',2000,2000,?)`,
		`{"role":"assistant","time":{"created":2000}}`)
	mustExec(`INSERT INTO part VALUES ('prt_2a','msg_2','ses_test',2000,2000,?)`,
		`{"type":"reasoning","text":"let me think"}`)
	mustExec(`INSERT INTO part VALUES ('prt_2b','msg_2','ses_test',2001,2001,?)`,
		`{"type":"text","text":"hi there"}`)
	mustExec(`INSERT INTO part VALUES ('prt_2c','msg_2','ses_test',2002,2002,?)`,
		`{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"ls"},"output":"file1\nfile2"}}`)

	// assistant message with only an empty text part
	mustExec(`INSERT INTO message VALUES ('msg_3','ses_test',3000,3000,?)`,
		`{"role":"assistant","time":{"created":3000}}`)
	mustExec(`INSERT INTO part VALUES ('prt_3','msg_3','ses_test',3000,3000,?)`,
		`{"type":"step-finish","reason":"stop"}`)

	return dbPath
}

func TestParseOpenCode(t *testing.T) {
	dbPath := buildOpenCodeTestDB(t)

	events, err := ParseOpenCode(dbPath, "ses_test")
	if err != nil {
		t.Fatalf("ParseOpenCode failed: %v", err)
	}

	type want struct {
		kind fractal.EventKind
		text string
	}
	wants := []want{
		{fractal.KindUser, "hello golem"},
		{fractal.KindTalk, "hi there"},
		{fractal.KindTool, `bash: {"command":"ls"}`},
		{fractal.KindEcho, "bash: file1\nfile2"},
	}
	if len(events) != len(wants) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(wants), events)
	}
	for i, w := range wants {
		if events[i].Kind != w.kind || events[i].Text != w.text {
			t.Errorf("event %d = (%s, %q), want (%s, %q)", i, events[i].Kind, events[i].Text, w.kind, w.text)
		}
		if events[i].Timestamp.IsZero() {
			t.Errorf("event %d has zero timestamp", i)
		}
	}
}

func TestParseOpenCodeLatestSession(t *testing.T) {
	dbPath := buildOpenCodeTestDB(t)
	events, err := ParseOpenCode(dbPath, "")
	if err != nil {
		t.Fatalf("ParseOpenCode(latest) failed: %v", err)
	}
	if len(events) == 0 || events[0].Kind != fractal.KindUser {
		t.Fatalf("unexpected latest-session events: %+v", events)
	}
}

func TestDeduceOpenCodeFormat(t *testing.T) {
	dbPath := buildOpenCodeTestDB(t)
	format, err := DeduceFormat(dbPath)
	if err != nil {
		t.Fatalf("DeduceFormat failed: %v", err)
	}
	if format != FormatOpenCode {
		t.Errorf("DeduceFormat = %q, want %q", format, FormatOpenCode)
	}

	// The containing directory is recognized too.
	if format, err := DeduceFormat(filepath.Dir(dbPath)); err != nil || format != FormatOpenCode {
		t.Errorf("DeduceFormat(dir) = %q, %v; want opencode", format, err)
	}
}

func TestDiscoverOpenCodeSessions(t *testing.T) {
	dbPath := buildOpenCodeTestDB(t)
	t.Setenv("OPENCODE_DB_PATH", dbPath)

	sessions, err := discoverOpenCodeSessions("/home/phil/git/golem")
	if err != nil {
		t.Fatalf("discoverOpenCodeSessions failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	s := sessions[0]
	if s.Format != FormatOpenCode || s.ID != "ses_test" {
		t.Errorf("unexpected session: %+v", s)
	}
	if s.Title != "Test session" || !s.MatchesWorkspace {
		t.Errorf("title/workspace wrong: %+v", s)
	}
	if s.EventCount != 3 {
		t.Errorf("EventCount = %d, want 3", s.EventCount)
	}
}

func TestClipImportedText(t *testing.T) {
	long := strings.Repeat("a", 100) + "MID" + strings.Repeat("z", 100)
	got := clipImportedText(long, 50)
	if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "zzz") {
		t.Errorf("head/tail not preserved: %q", got)
	}
	if strings.Contains(got, "MID") {
		t.Errorf("middle not elided: %q", got)
	}
}
