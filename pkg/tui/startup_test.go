package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/user/golem/pkg/memory/fractal"
)

func mkEvent(id int, kind fractal.EventKind, text string, ts time.Time) fractal.Event {
	return fractal.Event{ID: id, Kind: kind, Text: text, Timestamp: ts}
}

func typesOf(entries []ChatEntry) []MessageType {
	out := make([]MessageType, len(entries))
	for i, e := range entries {
		out[i] = e.Type
	}
	return out
}

func TestBuildStartupEntriesEmptyStore(t *testing.T) {
	now := time.Now()
	header, entries := buildStartupEntries(nil, "/home/phil/git/golem", "default", now)
	if len(entries) != 0 {
		t.Fatalf("empty store: got %d replay entries, want 0", len(entries))
	}
	for _, want := range []string{"workspace: /home/phil/git/golem", "profile: default", "new chat"} {
		if !strings.Contains(header, want) {
			t.Errorf("header missing %q: %s", want, header)
		}
	}
}

func TestBuildStartupEntriesReplaysTail(t *testing.T) {
	now := time.Now()
	events := []fractal.Event{
		mkEvent(0, fractal.KindUser, "u1", now.Add(-10*time.Minute)),
		mkEvent(1, fractal.KindTalk, "a1", now.Add(-9*time.Minute)),
		mkEvent(2, fractal.KindTool, `shell: {"command":"ls"}`, now.Add(-8*time.Minute)),
		mkEvent(3, fractal.KindEcho, "shell: file1\nfile2", now.Add(-8*time.Minute)),
		mkEvent(4, fractal.KindUser, "u2", now.Add(-2*time.Minute)),
		mkEvent(5, fractal.KindTalk, "a2", now.Add(-time.Minute)),
	}

	header, entries := buildStartupEntries(events, "/w", "default", now)
	// The tool call and its echo pair into a single entry.
	wantTypes := []MessageType{TypeUser, TypeAgent, TypeTool, TypeUser, TypeAgent}
	if got := typesOf(entries); fmt.Sprint(got) != fmt.Sprint(wantTypes) {
		t.Fatalf("types = %v, want %v", got, wantTypes)
	}

	tool := entries[2]
	if tool.ToolName != "shell" {
		t.Errorf("tool name = %q, want shell", tool.ToolName)
	}
	if tool.Command != `{"command":"ls"}` {
		t.Errorf("tool command = %q", tool.Command)
	}
	if tool.IsRunning {
		t.Errorf("replayed tool should be finished, not running")
	}
	if !strings.Contains(tool.Content, "file1") {
		t.Errorf("tool output not paired from echo: %q", tool.Content)
	}

	if !strings.Contains(header, "6 messages") {
		t.Errorf("header should report the message count: %s", header)
	}
}

func TestTailWindowTurnAndEventCaps(t *testing.T) {
	now := time.Now()
	var events []fractal.Event
	for i := 0; i < 10; i++ {
		events = append(events, mkEvent(i*2, fractal.KindUser, fmt.Sprintf("u%d", i), now))
		events = append(events, mkEvent(i*2+1, fractal.KindTalk, fmt.Sprintf("a%d", i), now))
	}

	// Keep only the last 3 user turns.
	tail, older := tailWindow(events, 3, 1000)
	if older != 13 {
		t.Errorf("older = %d, want 13", older)
	}
	if len(tail) != 7 {
		t.Errorf("tail len = %d, want 7", len(tail))
	}
	users := 0
	for _, ev := range tail {
		if ev.Kind == fractal.KindUser {
			users++
		}
	}
	if users != 3 {
		t.Errorf("tail users = %d, want 3", users)
	}

	// Event cap dominates when smaller than the turn budget.
	tail, older = tailWindow(events, 100, 5)
	if older != 15 || len(tail) != 5 {
		t.Errorf("event cap: older=%d len=%d, want 15/5", older, len(tail))
	}
}

func TestEventsToEntriesToolEchoPairingAndWork(t *testing.T) {
	now := time.Now()
	events := []fractal.Event{
		mkEvent(0, fractal.KindTool, `read_file: {"path":"x"}`, now),
		mkEvent(1, fractal.KindEcho, "read_file: contents", now),
		mkEvent(2, fractal.KindWork, "Remembered [default]: use tabs", now),
		mkEvent(3, fractal.KindEcho, "orphan output", now),
	}
	entries := eventsToEntries(events)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Type != TypeTool || entries[0].IsRunning || entries[0].Content != "contents" {
		t.Errorf("tool/echo not paired: %+v", entries[0])
	}
	if entries[1].Type != TypeSystem {
		t.Errorf("work event mapped to %v, want TypeSystem", entries[1].Type)
	}
	if entries[2].Type != TypeTool || entries[2].ToolName != "result" {
		t.Errorf("orphan echo mapped to %+v, want result tool", entries[2])
	}
}

func TestStartupHeaderAndHumanizeAgo(t *testing.T) {
	now := time.Now()
	events := []fractal.Event{mkEvent(0, fractal.KindUser, "hi", now.Add(-2*time.Hour))}
	header := formatStartupHeader(events, "/home/phil/git/golem", "work", now)
	for _, want := range []string{"Resuming golem", "workspace: /home/phil/git/golem", "profile: work", "2h ago"} {
		if !strings.Contains(header, want) {
			t.Errorf("header missing %q: %s", want, header)
		}
	}

	cases := map[time.Duration]string{
		-5 * time.Second: "just now",
		30 * time.Second: "just now",
		5 * time.Minute:  "5m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
	}
	for d, want := range cases {
		if got := humanizeAgo(d); got != want {
			t.Errorf("humanizeAgo(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestNewModelRestoresConversation(t *testing.T) {
	dir := t.TempDir()
	store, err := fractal.OpenStore(dir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	if _, err := store.AppendEvent(fractal.KindUser, "remember the plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(fractal.KindTalk, "noted"); err != nil {
		t.Fatal(err)
	}
	view := fractal.NewView(store, fractal.DefaultViewConfig())

	m := NewModel(nil, store, view)
	if !strings.Contains(m.startupHeader, "workspace:") {
		t.Errorf("startup header missing workspace: %q", m.startupHeader)
	}
	if !strings.Contains(m.startupHeader, "Resuming golem") {
		t.Errorf("startup header should indicate a resumed chat: %q", m.startupHeader)
	}
	if len(m.entries) != 2 {
		t.Fatalf("expected the prior exchange replayed into entries, got %d", len(m.entries))
	}
	if m.entries[0].Type != TypeUser || m.entries[1].Type != TypeAgent {
		t.Errorf("unexpected replayed entries: %+v", m.entries)
	}
}

func TestCapTextBounds(t *testing.T) {
	long := strings.Repeat("x", startupMaxEventText+500)
	got := capText(long)
	if len(got) <= startupMaxEventText || !strings.Contains(got, "truncated") {
		t.Errorf("capText did not bound a %d-byte event (got %d bytes)", len(long), len(got))
	}
	if short := capText("ok"); short != "ok" {
		t.Errorf("capText changed a short string: %q", short)
	}
}
