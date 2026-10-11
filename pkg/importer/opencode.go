package importer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/memory/fractal"
	_ "modernc.org/sqlite"
)

// importedToolTextLimit caps an imported tool result, keeping head and tail.
const importedToolTextLimit = 30000

// DefaultOpenCodeDBPath returns the platform path to OpenCode's SQLite database.
func DefaultOpenCodeDBPath() string {
	if custom := os.Getenv("OPENCODE_DB_PATH"); custom != "" {
		return custom
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		switch runtime.GOOS {
		case "darwin":
			base = filepath.Join(home, "Library", "Application Support")
		case "windows":
			base = os.Getenv("APPDATA")
			if base == "" {
				base = filepath.Join(home, "AppData", "Roaming")
			}
		default:
			base = filepath.Join(home, ".local", "share")
		}
	}
	return filepath.Join(base, "opencode", "opencode.db")
}

// openOpenCodeDB opens the OpenCode SQLite database read-only so an import never
// writes to (or locks) the user's live OpenCode state.
func openOpenCodeDB(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, errors.Wrapf(err, "opening OpenCode db %q", path)
	}
	return db, nil
}

// ocSessionRow is one row of OpenCode's session table.
type ocSessionRow struct {
	ID           string
	Title        string
	Directory    string
	CreatedMs    int64
	UpdatedMs    int64
	MessageCount int
}

func discoverOpenCodeSessions(currentWorkspace string) ([]DiscoveredSession, error) {
	dbPath := DefaultOpenCodeDBPath()
	if dbPath == "" || !fileExists(dbPath) {
		return nil, nil
	}

	db, err := openOpenCodeDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT s.id, COALESCE(s.title, ''), COALESCE(s.directory, ''),
		       s.time_created, s.time_updated,
		       (SELECT COUNT(*) FROM message m WHERE m.session_id = s.id)
		FROM session s`)
	if err != nil {
		return nil, errors.Wrap(err, "querying OpenCode sessions")
	}
	defer rows.Close()

	cleanWorkspace := ""
	if currentWorkspace != "" {
		cleanWorkspace = filepath.Clean(currentWorkspace)
	}

	var sessions []DiscoveredSession
	for rows.Next() {
		var r ocSessionRow
		if err := rows.Scan(&r.ID, &r.Title, &r.Directory, &r.CreatedMs, &r.UpdatedMs, &r.MessageCount); err != nil {
			continue
		}
		matches := cleanWorkspace != "" && filepath.Clean(r.Directory) == cleanWorkspace
		sessions = append(sessions, DiscoveredSession{
			ID:               r.ID,
			Format:           FormatOpenCode,
			Title:            r.Title,
			Path:             dbPath,
			Workspace:        r.Directory,
			CreatedAt:        msToTime(r.CreatedMs),
			UpdatedAt:        msToTime(r.UpdatedMs),
			EventCount:       r.MessageCount,
			MatchesWorkspace: matches,
		})
	}
	return sessions, rows.Err()
}

func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// ParseOpenCode parses one OpenCode session (or the most recent when sessionID is
// empty) from a DB file or its parent directory into normalized events.
func ParseOpenCode(path, sessionID string) ([]ImportedEvent, error) {
	dbPath := path
	fi, err := os.Stat(path)
	if err != nil {
		return nil, errors.Wrapf(err, "stat OpenCode path %q", path)
	}
	if fi.IsDir() {
		candidate := filepath.Join(path, "opencode.db")
		if !fileExists(candidate) {
			return nil, errors.Errorf("no opencode.db found in %q", path)
		}
		dbPath = candidate
	}

	db, err := openOpenCodeDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	if sessionID == "" {
		sessionID, err = latestOpenCodeSession(db)
		if err != nil {
			return nil, err
		}
	}
	if sessionID == "" {
		return nil, errors.Errorf("no OpenCode session found in %q", dbPath)
	}

	var sessionCreatedMs int64
	if err := db.QueryRow(`SELECT time_created FROM session WHERE id = ?`, sessionID).Scan(&sessionCreatedMs); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.Errorf("OpenCode session %q not found", sessionID)
		}
		return nil, errors.Wrap(err, "reading OpenCode session")
	}

	msgRows, err := db.Query(`
		SELECT id, time_created, data FROM message
		WHERE session_id = ?
		ORDER BY time_created, id`, sessionID)
	if err != nil {
		return nil, errors.Wrap(err, "querying OpenCode messages")
	}
	defer msgRows.Close()

	type messageRow struct {
		id        string
		createdMs int64
		data      string
	}
	var messages []messageRow
	for msgRows.Next() {
		var m messageRow
		if err := msgRows.Scan(&m.id, &m.createdMs, &m.data); err != nil {
			continue
		}
		if m.createdMs <= 0 {
			m.createdMs = sessionCreatedMs
		}
		messages = append(messages, m)
	}
	if err := msgRows.Err(); err != nil {
		return nil, err
	}

	partStmt, err := db.Prepare(`
		SELECT time_created, data FROM part
		WHERE message_id = ?
		ORDER BY time_created, id`)
	if err != nil {
		return nil, errors.Wrap(err, "preparing part query")
	}
	defer partStmt.Close()

	var events []ImportedEvent
	for _, m := range messages {
		var md ocMessageData
		if err := json.Unmarshal([]byte(m.data), &md); err != nil {
			continue
		}
		msgTime := msToTime(m.createdMs)

		partRows, err := partStmt.Query(m.id)
		if err != nil {
			return nil, errors.Wrapf(err, "querying parts for %s", m.id)
		}
		var parts []ocPartData
		for partRows.Next() {
			var createdMs int64
			var data string
			if err := partRows.Scan(&createdMs, &data); err != nil {
				continue
			}
			var pd ocPartData
			if err := json.Unmarshal([]byte(data), &pd); err != nil {
				continue
			}
			pd.createdMs = createdMs
			parts = append(parts, pd)
		}
		partRows.Close()

		switch md.Role {
		case "user":
			if text := joinPartText(parts, "text"); text != "" {
				events = append(events, ImportedEvent{Kind: fractal.KindUser, Text: text, Timestamp: msgTime})
			}
		case "assistant":
			events = append(events, assistantEvents(parts, msgTime)...)
		}
	}

	if len(events) == 0 {
		return nil, errors.Errorf("no events parsed from OpenCode session %q", sessionID)
	}
	return events, nil
}

func latestOpenCodeSession(db *sql.DB) (string, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM session ORDER BY time_updated DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", errors.Wrap(err, "finding most recent OpenCode session")
	}
	return id, nil
}

// ocMessageData is the JSON stored in message.data.
type ocMessageData struct {
	Role string `json:"role"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

// ocPartData is the JSON stored in part.data.
type ocPartData struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Tool  string `json:"tool"`
	State struct {
		Status string          `json:"status"`
		Input  json.RawMessage `json:"input"`
		Output string          `json:"output"`
	} `json:"state"`
	Time struct {
		Created int64 `json:"created"`
	} `json:"time"`

	createdMs int64 // row time_created, used when the part JSON has no time
}

func (p ocPartData) timestamp(fallback time.Time) time.Time {
	if p.Time.Created > 0 {
		return msToTime(p.Time.Created)
	}
	if p.createdMs > 0 {
		return msToTime(p.createdMs)
	}
	return fallback
}

// assistantEvents maps an assistant message's parts to events, in order. Text
// becomes talk, tool calls become tool + echo, and reasoning/step/patch parts are
// dropped (thoughts are never logged).
func assistantEvents(parts []ocPartData, msgTime time.Time) []ImportedEvent {
	var events []ImportedEvent
	for _, p := range parts {
		ts := p.timestamp(msgTime)
		switch p.Type {
		case "text":
			if text := strings.TrimSpace(p.Text); text != "" {
				events = append(events, ImportedEvent{Kind: fractal.KindTalk, Text: text, Timestamp: ts})
			}
		case "tool":
			input := "{}"
			if len(p.State.Input) > 0 {
				input = strings.TrimSpace(string(p.State.Input))
			}
			events = append(events, ImportedEvent{
				Kind:      fractal.KindTool,
				Text:      fmt.Sprintf("%s: %s", p.Tool, input),
				Timestamp: ts,
			})
			if out := strings.TrimSpace(p.State.Output); out != "" {
				events = append(events, ImportedEvent{
					Kind:      fractal.KindEcho,
					Text:      fmt.Sprintf("%s: %s", p.Tool, clipImportedText(out, importedToolTextLimit)),
					Timestamp: ts,
				})
			}
		}
	}
	return events
}

// joinPartText concatenates the text of every part of the given type.
func joinPartText(parts []ocPartData, partType string) string {
	var texts []string
	for _, p := range parts {
		if p.Type != partType {
			continue
		}
		if t := strings.TrimSpace(p.Text); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.Join(texts, "\n\n")
}

// clipImportedText clips s to limit bytes, keeping the head and the tail.
func clipImportedText(s string, limit int) string {
	if len(s) <= limit || limit <= 0 {
		return s
	}
	head := limit * 2 / 3
	tail := limit - head
	return s[:head] + "\n... [truncated] ...\n" + s[len(s)-tail:]
}
