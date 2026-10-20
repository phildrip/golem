package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/user/golem/pkg/memory/fractal"
)

const (
	// startupReplayTurns is how many of the most recent user turns are replayed
	// verbatim when the TUI opens.
	startupReplayTurns = 5
	// startupReplayEvents caps the replay regardless of turn count.
	startupReplayEvents = 200
	// startupEventScanLimit is how far back from the store the startup reads, so
	// opening golem never copies the whole event log. It is larger than the
	// replay cap to leave room for the turn window to find 5 user turns.
	startupEventScanLimit = 500
	// startupMaxTalkText caps a replayed user/agent message. It is generous on
	// purpose: a long answer cut in half is what made opening golem show only
	// the *start* of the last response with no way to reach the end. Startup
	// still bounds the work, but the tail you resume on is kept whole.
	startupMaxTalkText = 20000
	// startupMaxToolText caps a replayed tool echo. Raw command output is
	// background noise at startup and stays browsable via /tree.
	startupMaxToolText = 2000
)

// buildStartupEntries builds what the TUI shows when it opens: a header line
// naming the workspace and profile, plus a replay of the recent tail of the
// event log (with a boundary line when older messages are summarized). The
// header is returned separately because it is chrome that survives /clear. It
// is a pure function so it can be unit-tested without a running TUI.
func buildStartupEntries(events []fractal.Event, workspace, profile string, now time.Time) (header string, entries []ChatEntry) {
	return buildStartupEntriesN(events, workspace, profile, now, startupReplayTurns, startupReplayEvents)
}

func buildStartupEntriesN(events []fractal.Event, workspace, profile string, now time.Time, maxTurns, maxEvents int) (header string, entries []ChatEntry) {
	header = formatStartupHeader(events, workspace, profile, now)
	if len(events) == 0 {
		return header, nil
	}

	entries = make([]ChatEntry, 0, 16)
	tail, older := tailWindow(events, maxTurns, maxEvents)
	if older > 0 {
		entries = append(entries, ChatEntry{
			Type:    TypeSystem,
			Content: fmt.Sprintf("... %d earlier message(s) are summarized in fractal memory - use /tree to browse or /zoom <id> <n> for detail.", older),
		})
	}
	entries = append(entries, eventsToEntries(tail)...)
	return header, entries
}

// tailWindow returns the trailing events to replay and how many earlier events
// were dropped. It keeps at most maxTurns user turns and at most maxEvents
// events.
func tailWindow(events []fractal.Event, maxTurns, maxEvents int) ([]fractal.Event, int) {
	start := 0
	turns := 0
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == fractal.KindUser {
			turns++
			if turns > maxTurns {
				start = i + 1
				break
			}
		}
		if len(events)-i > maxEvents {
			start = i + 1
			break
		}
		start = i
	}
	if start < 0 || start > len(events) {
		start = len(events)
	}
	return events[start:], start
}

// eventsToEntries maps logged events to the ChatEntry kinds used live, pairing
// a tool call with the echo that follows it.
func eventsToEntries(events []fractal.Event) []ChatEntry {
	entries := make([]ChatEntry, 0, len(events))
	lastTool := -1

	for _, ev := range events {
		switch ev.Kind {
		case fractal.KindUser:
			entries = append(entries, ChatEntry{Type: TypeUser, Content: capText(ev.Text, ev)})
			lastTool = -1

		case fractal.KindTalk:
			entries = append(entries, ChatEntry{Type: TypeAgent, Content: capText(ev.Text, ev)})
			lastTool = -1

		case fractal.KindTool:
			name, cmd := splitToolEvent(ev.Text)
			if name == "" {
				name = "tool"
			}
			entries = append(entries, ChatEntry{Type: TypeTool, ToolName: name, Command: cmd, IsRunning: true})
			lastTool = len(entries) - 1

		case fractal.KindEcho:
			if lastTool >= 0 && entries[lastTool].IsRunning {
				entries[lastTool].Content = capText(stripToolPrefix(ev.Text, entries[lastTool].ToolName), ev)
				entries[lastTool].IsRunning = false
				entries[lastTool].ExitCode = 0
			} else {
				entries = append(entries, ChatEntry{Type: TypeTool, ToolName: "result", Content: capText(ev.Text, ev), IsRunning: false})
			}
			lastTool = -1

		default: // KindWork and any future kind
			entries = append(entries, ChatEntry{Type: TypeSystem, Content: capText(ev.Text, ev)})
			lastTool = -1
		}
	}

	// Nothing in history is still running: a tool whose echo is missing (an
	// interrupted turn, or a pair split by the window) must not show RUNNING.
	for i := range entries {
		if entries[i].Type == TypeTool {
			entries[i].IsRunning = false
		}
	}

	return entries
}

// formatStartupHeader renders the on-open header. The workspace is always
// shown so the global history is placed in its project context.
func formatStartupHeader(events []fractal.Event, workspace, profile string, now time.Time) string {
	if strings.TrimSpace(workspace) == "" {
		workspace = "unknown workspace"
	}
	if strings.TrimSpace(profile) == "" {
		profile = "default"
	}

	if len(events) == 0 {
		return fmt.Sprintf("Golem  ·  workspace: %s  ·  profile: %s  ·  new chat", workspace, profile)
	}

	last := events[len(events)-1].Timestamp
	if last.IsZero() {
		return fmt.Sprintf("Resuming golem  ·  workspace: %s  ·  profile: %s\n%d messages",
			workspace, profile, len(events))
	}
	return fmt.Sprintf("Resuming golem  ·  workspace: %s  ·  profile: %s\n%d messages  ·  last active %s",
		workspace, profile, len(events), humanizeAgo(now.Sub(last)))
}

// clampLines truncates each line of s to width runes, appending an ellipsis
// when it cuts, so a deep workspace path cannot overflow the header block.
func clampLines(s string, width int) string {
	if width <= 1 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if utf8.RuneCountInString(ln) > width {
			lines[i] = string([]rune(ln)[:width-1]) + "…"
		}
	}
	return strings.Join(lines, "\n")
}

// humanizeAgo renders a coarse relative time.
func humanizeAgo(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// capText bounds a replayed event's display text. Conversation turns keep far
// more text than tool echoes, and a cut is labelled with the /zoom command that
// restores the rest, so a truncated replay is never a dead end.
func capText(s string, ev fractal.Event) string {
	limit := startupMaxTalkText
	if ev.Kind != fractal.KindUser && ev.Kind != fractal.KindTalk {
		limit = startupMaxToolText
	}
	if len(s) <= limit {
		return s
	}
	if ev.ID > 0 {
		return fractal.TruncateUTF8(s, limit) + fmt.Sprintf("\n\n... (truncated at %d chars - /zoom %d 1 for the rest)", limit, ev.ID)
	}
	return fractal.TruncateUTF8(s, limit) + fmt.Sprintf("\n\n... (truncated at %d chars)", limit)
}

// splitToolEvent splits a logged tool event ("<name>: <args>") into name and
// argument text.
func splitToolEvent(text string) (name, rest string) {
	if i := strings.Index(text, ": "); i >= 0 {
		return strings.TrimSpace(text[:i]), strings.TrimSpace(text[i+2:])
	}
	return strings.TrimSpace(text), ""
}

// stripToolPrefix removes a leading "<toolName>: " from a tool echo.
func stripToolPrefix(text, toolName string) string {
	prefix := toolName + ": "
	if toolName != "" && strings.HasPrefix(text, prefix) {
		return strings.TrimSpace(text[len(prefix):])
	}
	return strings.TrimSpace(text)
}
