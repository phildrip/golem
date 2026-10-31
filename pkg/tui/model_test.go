package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/user/golem/pkg/memory/fractal"
)

func TestModel_WindowResize(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := fractal.OpenStore(tmpDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)

	if m.ready {
		t.Errorf("expected model not ready before WindowSizeMsg")
	}

	// Send WindowSizeMsg
	newModel, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	updated := newModel.(Model)

	if !updated.ready {
		t.Errorf("expected model ready after WindowSizeMsg")
	}
	if updated.width != 100 || updated.height != 40 {
		t.Errorf("expected width 100, height 40, got width %d, height %d", updated.width, updated.height)
	}
	if updated.viewport.Width != 100 {
		t.Errorf("expected viewport width 100, got %d", updated.viewport.Width)
	}
}

func TestModel_SlashCommands(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := fractal.OpenStore(tmpDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// Seed some events
	_, _ = store.AppendEvent(fractal.KindUser, "hello")
	_, _ = store.AppendEvent(fractal.KindTalk, "greetings")

	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)

	// Initialize size
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newM.(Model)

	tests := []struct {
		name         string
		command      string
		expectModal  bool
		expectEntry  bool
		contentMatch string
	}{
		{
			name:         "help command",
			command:      "/help",
			expectModal:  false,
			expectEntry:  true,
			contentMatch: "Available Slash Commands",
		},
		{
			name:         "tree command opens modal",
			command:      "/tree",
			expectModal:  true,
			expectEntry:  false,
			contentMatch: "FRACTAL MEMORY TREE",
		},
		{
			name:         "zoom invalid arguments",
			command:      "/zoom",
			expectModal:  false,
			expectEntry:  true,
			contentMatch: "Usage: /zoom <id> <n>",
		},
		{
			name:         "zoom verbatim valid event",
			command:      "/zoom 0 1",
			expectModal:  false,
			expectEntry:  true,
			contentMatch: "Verbatim 0: user: hello",
		},
		{
			name:         "zoom non-existent node",
			command:      "/zoom 99 4",
			expectModal:  false,
			expectEntry:  true,
			contentMatch: "No node at 99+4",
		},
		{
			name:         "clear command clears entries",
			command:      "/clear",
			expectModal:  false,
			expectEntry:  false,
			contentMatch: "",
		},
		{
			name:         "shortcuts command opens modal",
			command:      "/shortcuts",
			expectModal:  false,
			expectEntry:  false,
			contentMatch: "",
		},
		{
			name:         "unknown command shows error hint",
			command:      "/foobar",
			expectModal:  false,
			expectEntry:  true,
			contentMatch: "Unknown command: /foobar",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resModel, _ := m.handleSlashCommand(tc.command)
			res := resModel.(Model)

			if tc.expectModal && !res.showTreeModal {
				t.Errorf("expected tree modal to be visible")
			}
			if tc.expectModal && tc.contentMatch != "" && !strings.Contains(res.treeContent, tc.contentMatch) {
				t.Errorf("tree content does not contain %q: %s", tc.contentMatch, res.treeContent)
			}
			if tc.expectEntry {
				if len(res.entries) == 0 {
					t.Fatalf("expected entries to contain response")
				}
				last := res.entries[len(res.entries)-1]
				if !strings.Contains(last.Content, tc.contentMatch) {
					t.Errorf("expected entry content to contain %q, got: %s", tc.contentMatch, last.Content)
				}
			}
			if tc.command == "/clear" {
				if len(res.entries) != 0 {
					t.Errorf("expected entries to be cleared, got %d", len(res.entries))
				}
			}
		})
	}
}

func TestModel_TreeModalToggling(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := fractal.OpenStore(tmpDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// Open modal via Ctrl+T
	mModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = mModel.(Model)
	if !m.showTreeModal {
		t.Errorf("expected showTreeModal to be true after Ctrl+T")
	}

	// Close modal via Esc
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mModel.(Model)
	if m.showTreeModal {
		t.Errorf("expected showTreeModal to be false after Esc")
	}

	// Open again
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = mModel.(Model)
	if !m.showTreeModal {
		t.Errorf("expected showTreeModal to be true after Ctrl+T")
	}

	// Close via 'q'
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = mModel.(Model)
	if m.showTreeModal {
		t.Errorf("expected showTreeModal to be false after 'q'")
	}
}

func TestModel_ShortcutsModalToggling(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := fractal.OpenStore(tmpDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// 1. Open shortcuts modal via Ctrl+K
	mModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = mModel.(Model)
	if !m.showShortcutsModal {
		t.Errorf("expected showShortcutsModal to be true after Ctrl+K")
	}

	// Verify view rendering when modal is open
	viewOutput := m.View()
	if !strings.Contains(viewOutput, "KEYBOARD SHORTCUTS & CONTROLS") {
		t.Errorf("expected View() to show shortcuts cheatsheet, got:\n%s", viewOutput)
	}

	// 2. Close modal via Esc
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = mModel.(Model)
	if m.showShortcutsModal {
		t.Errorf("expected showShortcutsModal to be false after Esc")
	}

	// 3. Open again via /shortcuts slash command
	mModel, _ = m.handleSlashCommand("/shortcuts")
	m = mModel.(Model)
	if !m.showShortcutsModal {
		t.Errorf("expected showShortcutsModal to be true after /shortcuts")
	}

	// 4. Close via '?'
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = mModel.(Model)
	if m.showShortcutsModal {
		t.Errorf("expected showShortcutsModal to be false after '?'")
	}

	// 5. Open again and close via 'q'
	mModel, _ = m.handleSlashCommand("/keys")
	m = mModel.(Model)
	if !m.showShortcutsModal {
		t.Errorf("expected showShortcutsModal to be true after /keys")
	}
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = mModel.(Model)
	if m.showShortcutsModal {
		t.Errorf("expected showShortcutsModal to be false after 'q'")
	}
}

func TestModel_RenderMinimap(t *testing.T) {
	tests := []struct {
		name         string
		totalEvents  int
		tailLevels   map[int]int
		liveCount    int
		compacting   bool
		problems     string
		wantContains []string
	}{
		{
			name:         "all live head",
			totalEvents:  3,
			tailLevels:   map[int]int{},
			liveCount:    3,
			compacting:   false,
			wantContains: []string{"3 msgs", "Live:3", "Idle"},
		},
		{
			name:         "with fractal tail and compaction active",
			totalEvents:  42,
			tailLevels:   map[int]int{0: 20, 1: 10, 2: 4},
			liveCount:    8,
			compacting:   true,
			wantContains: []string{"42 msgs", "L0:20", "L1:10", "L2:4", "Live:8", "Compacting"},
		},
		{
			name:         "internal failures are visible",
			totalEvents:  9,
			tailLevels:   map[int]int{},
			liveCount:    1,
			compacting:   false,
			problems:     "memory 2, compactor 1",
			wantContains: []string{"9 msgs", "⚠ memory 2, compactor 1", "Live:1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rendered := RenderMemoryMinimap(tc.totalEvents, tc.tailLevels, tc.liveCount, tc.compacting, tc.problems)
			for _, want := range tc.wantContains {
				if !strings.Contains(rendered, want) {
					t.Errorf("rendered minimap %q does not contain %q", rendered, want)
				}
			}
		})
	}
}

func TestModel_RenderToolCard(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		command      string
		output       string
		exitCode     int
		isRunning    bool
		wantContains []string
	}{
		{
			name:         "running tool",
			toolName:     "shell",
			command:      "git status",
			output:       "",
			exitCode:     0,
			isRunning:    true,
			wantContains: []string{"Tool:", "shell", "git status", "RUNNING"},
		},
		{
			name:         "successful tool execution",
			toolName:     "read_file",
			command:      "pkg/tools/tools.go",
			output:       "package tools",
			exitCode:     0,
			isRunning:    false,
			wantContains: []string{"Tool:", "read_file", "pkg/tools/tools.go", "package tools", "EXIT 0"},
		},
		{
			name:         "failed tool execution",
			toolName:     "shell",
			command:      "exit 1",
			output:       "command failed",
			exitCode:     1,
			isRunning:    false,
			wantContains: []string{"Tool:", "shell", "exit 1", "command failed", "EXIT 1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			card := RenderToolCard(tc.toolName, tc.command, tc.output, tc.exitCode, tc.isRunning, 80)
			for _, want := range tc.wantContains {
				if !strings.Contains(card, want) {
					t.Errorf("card output does not contain %q", want)
				}
			}
		})
	}
}

func TestModel_SlashCommandHelp(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)

	tests := []struct {
		name         string
		input        string
		wantContains []string
	}{
		{
			name:         "all commands on slash prefix",
			input:        "/",
			wantContains: []string{"/tree", "/zoom", "/compact", "/clear", "/help", "/exit"},
		},
		{
			name:         "zoom filter",
			input:        "/z",
			wantContains: []string{"/zoom"},
		},
		{
			name:         "unknown command",
			input:        "/unknowncmd",
			wantContains: []string{"Unknown command", "/unknowncmd"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			help := m.renderSlashCommandHelp(tc.input)
			for _, want := range tc.wantContains {
				if !strings.Contains(help, want) {
					t.Errorf("slash command help for %q missing %q: %s", tc.input, want, help)
				}
			}
		})
	}
}

func TestModel_PromptHistoryCycling(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()

	// Seed previous event in store to verify store persistence loading
	_, _ = store.AppendEvent(fractal.KindUser, "first historic prompt")

	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)

	if len(m.inputHistory) != 1 || m.inputHistory[0] != "first historic prompt" {
		t.Fatalf("expected initial history loaded from store, got %v", m.inputHistory)
	}

	// Add more inputs
	m.inputHistory = append(m.inputHistory, "second prompt", "third prompt")
	m.historyIndex = len(m.inputHistory)

	// User types a draft: "my draft"
	m.textarea.SetValue("my draft")

	// Press KeyUp: should load "third prompt" and store "my draft" as currentDraft
	mModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = mModel.(Model)
	if m.textarea.Value() != "third prompt" {
		t.Errorf("expected 'third prompt', got %q", m.textarea.Value())
	}
	if m.currentDraft != "my draft" {
		t.Errorf("expected draft 'my draft', got %q", m.currentDraft)
	}

	// Press KeyUp again: should load "second prompt"
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = mModel.(Model)
	if m.textarea.Value() != "second prompt" {
		t.Errorf("expected 'second prompt', got %q", m.textarea.Value())
	}

	// Press KeyUp again: should load "first historic prompt"
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = mModel.(Model)
	if m.textarea.Value() != "first historic prompt" {
		t.Errorf("expected 'first historic prompt', got %q", m.textarea.Value())
	}

	// Press KeyUp at top: should stay at "first historic prompt"
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = mModel.(Model)
	if m.textarea.Value() != "first historic prompt" {
		t.Errorf("expected 'first historic prompt', got %q", m.textarea.Value())
	}

	// Press KeyDown: should move to "second prompt"
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mModel.(Model)
	if m.textarea.Value() != "second prompt" {
		t.Errorf("expected 'second prompt', got %q", m.textarea.Value())
	}

	// Press KeyDown: should move to "third prompt"
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mModel.(Model)
	if m.textarea.Value() != "third prompt" {
		t.Errorf("expected 'third prompt', got %q", m.textarea.Value())
	}

	// Press KeyDown: should restore draft "my draft"
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mModel.(Model)
	if m.textarea.Value() != "my draft" {
		t.Errorf("expected restored draft 'my draft', got %q", m.textarea.Value())
	}
}

func TestModel_ViewportAndModalScrolling(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)

	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// Populate entries
	for i := 0; i < 50; i++ {
		m.entries = append(m.entries, ChatEntry{
			Type:    TypeUser,
			Content: "line of message",
		})
	}
	m.renderConversationToViewport()
	m.viewport.GotoBottom()
	bottomY := m.viewport.YOffset

	// Test PgUp in chat
	mModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = mModel.(Model)
	if m.viewport.YOffset >= bottomY && bottomY > 0 {
		t.Errorf("expected YOffset to decrease after PgUp, got %d (bottom was %d)", m.viewport.YOffset, bottomY)
	}

	// Test PgDown in chat
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = mModel.(Model)

	// Test modal scrolling
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = mModel.(Model)
	if !m.showTreeModal {
		t.Fatalf("expected modal open")
	}

	// Tree modal scrolling keys (Down, Up, PgDown, PgUp, j, k)
	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = mModel.(Model)

	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = mModel.(Model)

	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = mModel.(Model)

	mModel, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = mModel.(Model)
}

// TestModel_StreamingDoesNotYankScroll verifies that incoming agent chunks keep
// the view pinned to the bottom when the user is already there, but do NOT pull
// the user back down after they have scrolled up to read history.
func TestModel_StreamingDoesNotYankScroll(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)

	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = newM.(Model)

	for i := 0; i < 60; i++ {
		m.entries = append(m.entries, ChatEntry{Type: TypeUser, Content: "line of message"})
	}
	m.renderConversationToViewport()
	m.viewport.GotoBottom()
	if !m.viewport.AtBottom() {
		t.Fatalf("expected viewport at bottom after initial render")
	}

	// Streaming while pinned to the bottom should follow the tail.
	newM, _ = m.Update(agentChunkMsg("partial reply"))
	m = newM.(Model)
	if !m.viewport.AtBottom() {
		t.Errorf("expected viewport to follow the tail while pinned, offset=%d", m.viewport.YOffset)
	}

	// Scroll up to read history.
	m.viewport.LineUp(20)
	if m.viewport.AtBottom() {
		t.Fatalf("precondition failed: expected to be scrolled up")
	}
	scrolledOffset := m.viewport.YOffset

	// More streaming must not yank the user back to the bottom.
	newM, _ = m.Update(agentChunkMsg("more reply text"))
	m = newM.(Model)
	if m.viewport.YOffset != scrolledOffset {
		t.Errorf("streaming yanked scroll from %d to %d", scrolledOffset, m.viewport.YOffset)
	}
}

func TestModel_CtrlCAndCtrlD(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())

	t.Run("Ctrl+D on empty line when idle exits", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.textarea.SetValue("")
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmd == nil {
			t.Fatalf("expected tea.Quit cmd on Ctrl+D when line is empty")
		}
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg, got %T", msg)
		}
	})

	t.Run("Ctrl+D on non-empty line performs forward delete and does not exit", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.textarea.SetValue("draft")
		m.textarea.CursorStart()
		newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		res := newM.(Model)
		if cmd != nil {
			msg := cmd()
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatalf("expected no quit cmd when Ctrl+D has text in buffer")
			}
		}
		// Expect 'd' to be forward deleted
		if res.textarea.Value() != "raft" {
			t.Errorf("expected forward delete 'raft', got %q", res.textarea.Value())
		}
	})

	t.Run("Ctrl+D while loading turn is ignored and does not quit", func(t *testing.T) {
		m := NewModel(nil, store, view)
		cancelled := false
		m.isLoading = true
		m.cancelTurn = func() {
			cancelled = true
		}
		newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		res := newM.(Model)
		if cmd != nil {
			msg := cmd()
			if _, isQuit := msg.(tea.QuitMsg); isQuit {
				t.Fatalf("expected no quit cmd when Ctrl+D pressed while loading")
			}
		}
		if cancelled {
			t.Errorf("expected cancelTurn not to be invoked on Ctrl+D")
		}
		if !res.isLoading {
			t.Errorf("expected isLoading to remain true")
		}
	})

	t.Run("Ctrl+C while loading interrupts the turn", func(t *testing.T) {
		m := NewModel(nil, store, view)
		cancelled := false
		m.isLoading = true
		m.cancelTurn = func() {
			cancelled = true
		}

		newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		res := newM.(Model)

		if cmd != nil {
			t.Errorf("expected no quit cmd when interrupting turn")
		}
		if !cancelled {
			t.Errorf("expected cancelTurn to be invoked")
		}
		if res.isLoading {
			t.Errorf("expected isLoading to be false after Ctrl+C")
		}
		if len(res.entries) == 0 || !strings.Contains(res.entries[len(res.entries)-1].Content, "interrupted") {
			t.Errorf("expected turn interrupted system entry in conversation")
		}
	})

	t.Run("Ctrl+C keeps the text the turn had already produced", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.isLoading = true
		m.cancelTurn = func() {}
		m.streamingReply = "I read the diff and found three things\n"
		m.entries = append(m.entries, ChatEntry{Type: TypeUser, Content: "look at agy's changes"})

		newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		res := newM.(Model)

		var reply *ChatEntry
		for i := range res.entries {
			if res.entries[i].Type == TypeAgent {
				reply = &res.entries[i]
			}
		}
		if reply == nil {
			t.Fatalf("interrupt threw away the streamed reply: %+v", res.entries)
		}
		if !strings.Contains(reply.Content, "found three things") {
			t.Errorf("partial reply = %q, want the text streamed before ^C", reply.Content)
		}
		if res.streamingReply != "" {
			t.Errorf("streamingReply should be folded into the transcript, got %q", res.streamingReply)
		}
		last := res.entries[len(res.entries)-1]
		if last.Type != TypeSystem || !strings.Contains(last.Content, "interrupted") {
			t.Errorf("expected the interrupt marker last, got %+v", last)
		}
	})

	t.Run("Ctrl+C stops cards the turn was still inside", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.isLoading = true
		m.cancelTurn = func() {}
		m.entries = append(m.entries,
			ChatEntry{Type: TypeTool, ToolName: "shell", Command: "sleep 999", IsRunning: true},
			ChatEntry{Type: TypeSubagent, SubagentRole: "researcher", IsRunning: true},
		)

		newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		res := newM.(Model)

		tool, sub := res.entries[0], res.entries[1]
		if tool.IsRunning {
			t.Errorf("tool card still spinning after interrupt: %+v", tool)
		}
		if tool.ExitCode != ExitCodeUnknown {
			t.Errorf("tool exit code = %d, want ExitCodeUnknown (never observed)", tool.ExitCode)
		}
		if !strings.Contains(RenderToolCard(tool.ToolName, tool.Command, tool.Content, tool.ExitCode, tool.IsRunning, 80), "EXIT ?") {
			t.Errorf("interrupted tool card should not claim a status")
		}
		if sub.IsRunning || !sub.Interrupted {
			t.Errorf("subagent card = %+v, want stopped and marked interrupted", sub)
		}
		if !strings.Contains(RenderSubagentCard(sub.SubagentRole, "m", "p", "", false, sub.Interrupted, 80), "INTERRUPTED") {
			t.Errorf("interrupted subagent card should say INTERRUPTED")
		}
	})

	t.Run("A superseded turn's done message cannot finish the next turn", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.isLoading = true
		m.turnSeq = 4
		m.cancelTurn = func() {}
		newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		res := newM.(Model)

		// The next turn starts while the interrupted goroutine is still unwinding.
		res.isLoading = true
		res.turnSeq = 5
		res.streamChan = make(<-chan tea.Msg)

		newM2, _ := res.Update(agentDoneMsg{reply: "the turn you cut off", seq: 4})
		after := newM2.(Model)
		if !after.isLoading {
			t.Errorf("a stale done message cleared the running turn")
		}
		for _, e := range after.entries {
			if e.Type == TypeAgent && strings.Contains(e.Content, "cut off") {
				t.Errorf("stale reply leaked into the transcript: %+v", e)
			}
		}

		// The current turn's own message still lands.
		newM3, _ := after.Update(agentDoneMsg{reply: "the new answer", seq: 5})
		done := newM3.(Model)
		if done.isLoading {
			t.Errorf("current turn's done message was ignored")
		}
		if last := done.entries[len(done.entries)-1]; last.Type != TypeAgent || last.Content != "the new answer" {
			t.Errorf("current reply not appended: %+v", last)
		}
	})

	t.Run("Ctrl+C clears non-empty line without quitting", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.textarea.SetValue("partially typed command")
		newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		res := newM.(Model)

		if cmd != nil {
			t.Errorf("expected no quit cmd when clearing line")
		}
		if res.textarea.Value() != "" {
			t.Errorf("expected textarea to be cleared, got %q", res.textarea.Value())
		}
	})

	t.Run("Ctrl+C on empty line exits", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.textarea.SetValue("")
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmd == nil {
			t.Fatalf("expected tea.Quit cmd on Ctrl+C with empty line")
		}
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg, got %T", msg)
		}
	})

	t.Run("Ctrl+D and Ctrl+U in tree modal scrolls half page and does not exit", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.showTreeModal = true

		newM, cmdD := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmdD != nil {
			if _, isQuit := cmdD().(tea.QuitMsg); isQuit {
				t.Errorf("expected no quit on Ctrl+D in tree modal")
			}
		}
		res := newM.(Model)
		if !res.showTreeModal {
			t.Errorf("expected tree modal to remain open on Ctrl+D")
		}

		newM2, cmdU := res.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
		if cmdU != nil {
			if _, isQuit := cmdU().(tea.QuitMsg); isQuit {
				t.Errorf("expected no quit on Ctrl+U in tree modal")
			}
		}
		res2 := newM2.(Model)
		if !res2.showTreeModal {
			t.Errorf("expected tree modal to remain open on Ctrl+U")
		}

		_, cmdC := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmdC == nil {
			t.Fatalf("expected quit cmd on Ctrl+C in modal")
		}
		if _, ok := cmdC().(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg on Ctrl+C in modal")
		}
	})

	t.Run("Ctrl+D and Ctrl+U in shortcuts modal scrolls half page and does not exit", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.showShortcutsModal = true

		newM, cmdD := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmdD != nil {
			if _, isQuit := cmdD().(tea.QuitMsg); isQuit {
				t.Errorf("expected no quit on Ctrl+D in shortcuts modal")
			}
		}
		res := newM.(Model)
		if !res.showShortcutsModal {
			t.Errorf("expected shortcuts modal to remain open on Ctrl+D")
		}

		_, cmdC := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmdC == nil {
			t.Fatalf("expected quit cmd on Ctrl+C in shortcuts modal")
		}
		if _, ok := cmdC().(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg on Ctrl+C in shortcuts modal")
		}
	})

	t.Run("Ctrl+D in profile and MCP modals scrolls and does not exit", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.showProfileModal = true
		newM, cmdD := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmdD != nil {
			if _, isQuit := cmdD().(tea.QuitMsg); isQuit {
				t.Errorf("expected no quit on Ctrl+D in profile modal")
			}
		}
		if !newM.(Model).showProfileModal {
			t.Errorf("expected profile modal to remain open")
		}

		m2 := NewModel(nil, store, view)
		m2.showMCPModal = true
		newM2, cmdD2 := m2.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmdD2 != nil {
			if _, isQuit := cmdD2().(tea.QuitMsg); isQuit {
				t.Errorf("expected no quit on Ctrl+D in MCP modal")
			}
		}
		if !newM2.(Model).showMCPModal {
			t.Errorf("expected MCP modal to remain open")
		}
	})

	t.Run("Ctrl+D and Ctrl+U when blurred scrolls conversation viewport", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.isBlurred = true
		newM, cmdD := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmdD != nil {
			if _, isQuit := cmdD().(tea.QuitMsg); isQuit {
				t.Errorf("expected no quit on Ctrl+D when blurred")
			}
		}
		newM2, cmdU := newM.(Model).Update(tea.KeyMsg{Type: tea.KeyCtrlU})
		if cmdU != nil {
			if _, isQuit := cmdU().(tea.QuitMsg); isQuit {
				t.Errorf("expected no quit on Ctrl+U when blurred")
			}
		}
		_ = newM2
	})
}

func TestModel_MessagesAndLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// 1. Tool execution lifecycle: toolStartMsg -> toolDoneMsg
	newM, _ = m.Update(toolStartMsg{
		toolName: "shell",
		command:  "ls -la",
	})
	m = newM.(Model)
	if len(m.entries) != 1 || !m.entries[0].IsRunning {
		t.Fatalf("expected running tool entry, got %v", m.entries)
	}

	newM, _ = m.Update(toolDoneMsg{
		toolName: "shell",
		output:   "file1\nfile2",
		exitCode: 0,
	})
	m = newM.(Model)
	if m.entries[0].IsRunning || m.entries[0].Content != "file1\nfile2" {
		t.Errorf("expected finished tool entry, got %v", m.entries[0])
	}

	// 2. Streaming chunk updates
	newM, _ = m.Update(agentChunkMsg("Hello, "))
	m = newM.(Model)
	newM, _ = m.Update(agentChunkMsg("world!"))
	m = newM.(Model)
	if m.streamingReply != "Hello, world!" {
		t.Errorf("expected streaming reply 'Hello, world!', got %q", m.streamingReply)
	}

	// 3. Agent done message
	m.isLoading = true
	newM, _ = m.Update(agentDoneMsg{
		reply: "Final answer.",
		err:   nil,
	})
	m = newM.(Model)
	if m.isLoading {
		t.Errorf("expected isLoading to be false")
	}
	if len(m.entries) < 2 || m.entries[len(m.entries)-1].Content != "Final answer." {
		t.Errorf("expected final answer entry in entries")
	}

	// 4. Memory stats message
	newM, _ = m.Update(memoryStatsMsg{
		totalEvents: 10,
		tailLevels:  map[int]int{0: 5},
		liveCount:   5,
	})
	m = newM.(Model)
	if m.memoryStats.totalEvents != 10 {
		t.Errorf("expected totalEvents 10, got %d", m.memoryStats.totalEvents)
	}

	// 5. Tool card with > 8 lines truncation
	longOutput := strings.Repeat("line\n", 15)
	card := RenderToolCard("shell", "long_cmd", longOutput, 0, false, 80)
	if !strings.Contains(card, "more lines") {
		t.Errorf("expected card to truncate long output with 'more lines'")
	}
}

func TestModel_FocusAndBlurHandling(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// Initially textarea is focused
	if !m.textarea.Focused() {
		t.Errorf("expected textarea to be initially focused")
	}

	// 1. Receive BlurMsg (e.g. user selects another tmux pane)
	newM, _ = m.Update(tea.BlurMsg{})
	m = newM.(Model)
	if !m.isBlurred {
		t.Errorf("expected isBlurred to be true after BlurMsg")
	}
	if m.textarea.Focused() {
		t.Errorf("expected textarea to be blurred after BlurMsg")
	}

	// 2. Receive FocusMsg (user switches back to tmux pane)
	newM, _ = m.Update(tea.FocusMsg{})
	m = newM.(Model)
	if m.isBlurred {
		t.Errorf("expected isBlurred to be false after FocusMsg")
	}
	if !m.textarea.Focused() {
		t.Errorf("expected textarea to regain focus after FocusMsg")
	}

	// 3. Opening modal blurs textarea
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	m = newM.(Model)
	if !m.showShortcutsModal {
		t.Fatalf("expected shortcuts modal to be open")
	}
	if m.textarea.Focused() {
		t.Errorf("expected textarea to be blurred when modal is open")
	}

	// 4. Closing modal restores focus
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newM.(Model)
	if m.showShortcutsModal {
		t.Fatalf("expected shortcuts modal to be closed")
	}
	if !m.textarea.Focused() {
		t.Errorf("expected textarea to regain focus after modal closes")
	}
}

func TestModel_MCPModalToggling(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// 1. Initial state: MCP modal closed
	if m.showMCPModal {
		t.Fatalf("expected MCP modal to be initially closed")
	}

	// 2. Open via slash command /mcp
	newM, _ = m.handleSlashCommand("/mcp")
	m = newM.(Model)
	if !m.showMCPModal {
		t.Fatalf("expected MCP modal to be open after /mcp")
	}
	if m.textarea.Focused() {
		t.Errorf("expected textarea to be blurred when MCP modal is open")
	}

	// View() renders the MCP modal
	rendered := m.View()
	if !strings.Contains(rendered, "MODEL CONTEXT PROTOCOL") {
		t.Errorf("expected View() to render MCP modal, got: %s", rendered)
	}

	// 3. Navigation keys (j, k, down, up, pgdn, pgup, home, end)
	keys := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyRunes, Runes: []rune{'k'}},
		{Type: tea.KeyDown},
		{Type: tea.KeyUp},
		{Type: tea.KeyPgDown},
		{Type: tea.KeyPgUp},
		{Type: tea.KeyHome},
		{Type: tea.KeyEnd},
	}
	for _, k := range keys {
		newM, _ = m.Update(k)
		m = newM.(Model)
		if !m.showMCPModal {
			t.Errorf("expected MCP modal to remain open during navigation key %v", k)
		}
	}

	// 4. Close via 'q'
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = newM.(Model)
	if m.showMCPModal {
		t.Fatalf("expected MCP modal to be closed after 'q'")
	}
	if !m.textarea.Focused() {
		t.Errorf("expected textarea to regain focus after MCP modal closes")
	}

	// 5. Re-open via /mcp and close via Esc
	newM, _ = m.handleSlashCommand("/mcp")
	m = newM.(Model)
	if !m.showMCPModal {
		t.Fatalf("expected MCP modal to be open")
	}
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newM.(Model)
	if m.showMCPModal {
		t.Fatalf("expected MCP modal to be closed after Esc")
	}
	if !m.textarea.Focused() {
		t.Errorf("expected textarea to regain focus after MCP modal closes")
	}

	// 6. Verify /mcp in help and slash suggestions
	newM, _ = m.handleSlashCommand("/help")
	m = newM.(Model)
	lastEntry := m.entries[len(m.entries)-1]
	if !strings.Contains(lastEntry.Content, "/mcp") {
		t.Errorf("expected /help output to include /mcp, got: %s", lastEntry.Content)
	}

	palette := m.renderSlashCommandHelp("/mc")
	if !strings.Contains(palette, "/mcp") {
		t.Errorf("expected /mc to suggest /mcp, got: %s", palette)
	}
}

func TestModel_SubagentCardRendering(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// 1. Dispatch subagentStartMsg
	newM, _ = m.Update(subagentStartMsg{
		role:   "researcher",
		model:  "gemini-2.5-pro",
		prompt: "investigate memory leaks",
	})
	m = newM.(Model)

	if len(m.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m.entries))
	}
	entry := m.entries[0]
	if entry.Type != TypeSubagent || !entry.IsRunning || entry.SubagentRole != "researcher" || entry.SubagentModel != "gemini-2.5-pro" {
		t.Errorf("unexpected subagent entry state: %+v", entry)
	}

	viewContent := m.viewport.View()
	if !strings.Contains(viewContent, "Subagent") || !strings.Contains(viewContent, "RESEARCHER") || !strings.Contains(viewContent, "gemini-2.5-pro") || !strings.Contains(viewContent, "RUNNING") {
		t.Errorf("viewport missing running subagent card details: %s", viewContent)
	}

	// 2. Dispatch subagentDoneMsg
	newM, _ = m.Update(subagentDoneMsg{
		role:   "researcher",
		model:  "gemini-2.5-pro",
		result: "No memory leaks detected in heap profile.",
	})
	m = newM.(Model)

	entry = m.entries[0]
	if entry.IsRunning {
		t.Errorf("expected subagent to be marked done")
	}
	if entry.Content != "No memory leaks detected in heap profile." {
		t.Errorf("unexpected content: %s", entry.Content)
	}

	viewContent = m.viewport.View()
	if !strings.Contains(viewContent, "DONE") || !strings.Contains(viewContent, "No memory leaks detected") {
		t.Errorf("viewport missing completed subagent card details: %s", viewContent)
	}
}

func TestModel_ProfileModalAndCommands(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// 1. Open profile modal with /profile
	newM, _ = m.handleSlashCommand("/profile")
	m = newM.(Model)
	if !m.showProfileModal {
		t.Fatalf("expected profile modal to be open after /profile")
	}

	rendered := m.View()
	if !strings.Contains(rendered, "GOLEM PROVIDER PROFILES") {
		t.Errorf("expected View() to render profile modal, got: %s", rendered)
	}

	// 2. Close profile modal with 'q'
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = newM.(Model)
	if m.showProfileModal {
		t.Fatalf("expected profile modal to close on 'q'")
	}

	// 3. Switch profile via /profile <name>
	newM, _ = m.handleSlashCommand("/profile work")
	m = newM.(Model)
	if m.currentProfile != "work" {
		t.Errorf("expected currentProfile to be 'work', got: %s", m.currentProfile)
	}
	lastEntry := m.entries[len(m.entries)-1]
	if !strings.Contains(lastEntry.Content, "Switched active profile to") {
		t.Errorf("expected switch notification entry, got: %s", lastEntry.Content)
	}

	// 4. Test /remember command
	newM, _ = m.handleSlashCommand("/remember Prefer concise answers")
	m = newM.(Model)
	lastEntry = m.entries[len(m.entries)-1]
	if !strings.Contains(lastEntry.Content, "Remembered: Prefer concise answers") {
		t.Errorf("expected remember confirmation, got: %s", lastEntry.Content)
	}

	// 5. Test /remember missing argument
	newM, _ = m.handleSlashCommand("/remember")
	m = newM.(Model)
	lastEntry = m.entries[len(m.entries)-1]
	if !strings.Contains(lastEntry.Content, "Usage: /remember <text>") {
		t.Errorf("expected usage info for /remember, got: %s", lastEntry.Content)
	}
}

func TestModel_StreamBridgeNonBlockingAndCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	msgChan := make(chan tea.Msg, 4) // small buffer to test non-blocking/cancellation
	bridge := &tuiStreamBridge{
		ctx:     ctx,
		msgChan: msgChan,
	}

	// 1. Fill the buffer
	bridge.OnChunk("1")
	bridge.OnChunk("2")
	bridge.OnChunk("3")
	bridge.OnChunk("4")

	// 2. Cancel the context
	cancel()

	// 3. Subsequent sends should not hang or block even when channel is full
	done := make(chan struct{})
	go func() {
		bridge.OnChunk("5")
		bridge.OnToolStart("tool", "cmd")
		bridge.OnToolDone("tool", "out", 0)
		close(done)
	}()

	select {
	case <-done:
		// Success: send did not block
	case <-time.After(1 * time.Second):
		t.Fatal("bridge.send blocked on cancelled context")
	}
}

func TestModel_StreamMsgHandlingLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// Simulate turn submission
	m.isLoading = true
	testChan := make(chan tea.Msg, 10)
	m.streamChan = testChan

	// 1. Process agentChunkMsg wrapped in streamMsg
	newM, nextCmd := m.Update(streamMsg{Payload: agentChunkMsg("chunk 1")})
	m = newM.(Model)
	if m.streamingReply != "chunk 1" {
		t.Errorf("expected streaming reply 'chunk 1', got %q", m.streamingReply)
	}
	if nextCmd == nil {
		t.Errorf("expected nextListenCmd to be scheduled while isLoading is true")
	}

	// 2. Process toolStartMsg wrapped in streamMsg
	newM, nextCmd = m.Update(streamMsg{Payload: toolStartMsg{toolName: "shell", command: "pwd"}})
	m = newM.(Model)
	if len(m.entries) != 1 || !m.entries[0].IsRunning {
		t.Errorf("expected running tool entry")
	}
	if nextCmd == nil {
		t.Errorf("expected nextListenCmd to be scheduled while isLoading is true")
	}

	// 3. Process agentDoneMsg wrapped in streamMsg
	newM, nextCmd = m.Update(streamMsg{Payload: agentDoneMsg{reply: "All done."}})
	m = newM.(Model)
	if m.isLoading {
		t.Errorf("expected isLoading to be false after agentDoneMsg")
	}
	if m.streamChan != nil {
		t.Errorf("expected streamChan to be nil after agentDoneMsg")
	}
	_ = nextCmd
}

// TestModel_MouseWheelScrolls verifies that mouse wheel events forwarded to the
// chat viewport actually scroll it (the TUI enables tea.WithMouseCellMotion).
func TestModel_MouseWheelScrolls(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = newM.(Model)

	for i := 0; i < 60; i++ {
		m.entries = append(m.entries, ChatEntry{
			Type:    TypeAgent,
			Content: fmt.Sprintf("Line of history %d", i),
		})
	}
	m.renderConversationToViewport()
	m.viewport.GotoBottom()
	bottom := m.viewport.YOffset

	// Wheel up must scroll towards the top.
	newM, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = newM.(Model)
	if m.viewport.YOffset >= bottom {
		t.Fatalf("wheel up did not scroll: was %d, now %d", bottom, m.viewport.YOffset)
	}

	// Wheel down must scroll back towards the bottom.
	scrolledUp := m.viewport.YOffset
	newM, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = newM.(Model)
	if m.viewport.YOffset <= scrolledUp {
		t.Errorf("wheel down did not scroll: was %d, now %d", scrolledUp, m.viewport.YOffset)
	}
}

func TestModel_TypingDoesNotScroll(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// Populate entries so viewport has content to scroll
	for i := 0; i < 60; i++ {
		m.entries = append(m.entries, ChatEntry{
			Type:    TypeAgent,
			Content: fmt.Sprintf("Line of history %d", i),
		})
	}
	m.renderConversationToViewport()

	// Scroll viewport to line 20
	m.viewport.SetYOffset(20)
	initialOffset := m.viewport.YOffset
	if initialOffset != 20 {
		t.Fatalf("expected initial offset 20, got %d", initialOffset)
	}

	// Type characters that previously matched viewport pager keybindings:
	// space (PageDown), d (HalfPageDown), u (HalfPageUp), j (LineDown), k (LineUp), f (PageDown), b (PageUp)
	troublesomeRunes := []rune{'d', 'e', 'b', 'u', 'g', ' ', 'j', 'o', 'b', ' ', 'f', 'a', 's', 't'}
	for _, r := range troublesomeRunes {
		newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = newM.(Model)

		if m.viewport.YOffset != initialOffset {
			t.Errorf("viewport offset moved when typing rune %q: was %d, now %d", string(r), initialOffset, m.viewport.YOffset)
		}
	}

	expectedText := "debug job fast"
	if m.textarea.Value() != expectedText {
		t.Errorf("expected textarea to contain %q, got %q", expectedText, m.textarea.Value())
	}

	// Verify PgUp and PgDown DO scroll the conversation viewport
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = newM.(Model)
	if m.viewport.YOffset >= initialOffset {
		t.Errorf("expected PgUp to scroll viewport up, was %d now %d", initialOffset, m.viewport.YOffset)
	}

	offsetAfterPgUp := m.viewport.YOffset
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = newM.(Model)
	if m.viewport.YOffset <= offsetAfterPgUp {
		t.Errorf("expected PgDown to scroll viewport down, was %d now %d", offsetAfterPgUp, m.viewport.YOffset)
	}
}

func TestModel_RenderCollapsedToolSummary(t *testing.T) {
	// 1. Single running tool
	running := RenderCollapsedToolSummary([]ChatEntry{
		{ToolName: "shell", Command: "ls -la", IsRunning: true},
	})
	if !strings.Contains(running, "Running tool") || !strings.Contains(running, "shell") || !strings.Contains(running, "RUNNING") {
		t.Errorf("expected running tool summary, got: %s", running)
	}

	// 2. Single finished tool (success)
	doneOk := RenderCollapsedToolSummary([]ChatEntry{
		{ToolName: "shell", Command: "pwd", ExitCode: 0, IsRunning: false},
	})
	if !strings.Contains(doneOk, "Tool:") || !strings.Contains(doneOk, "shell") || !strings.Contains(doneOk, "EXIT 0") || !strings.Contains(doneOk, "Ctrl+O to expand") {
		t.Errorf("expected finished ok tool summary, got: %s", doneOk)
	}

	// 3. Single finished tool (error)
	doneErr := RenderCollapsedToolSummary([]ChatEntry{
		{ToolName: "shell", Command: "exit 1", ExitCode: 1, IsRunning: false},
	})
	if !strings.Contains(doneErr, "Tool:") || !strings.Contains(doneErr, "shell") || !strings.Contains(doneErr, "EXIT 1") || !strings.Contains(doneErr, "Ctrl+O to expand") {
		t.Errorf("expected finished error tool summary, got: %s", doneErr)
	}

	// 4. Multiple tools
	multi := RenderCollapsedToolSummary([]ChatEntry{
		{ToolName: "shell", Command: "git status", ExitCode: 0},
		{ToolName: "read_file", Command: "main.go", ExitCode: 0},
	})
	if !strings.Contains(multi, "2 tool calls") || !strings.Contains(multi, "shell, read_file") || !strings.Contains(multi, "2 DONE") || !strings.Contains(multi, "Ctrl+O to expand") {
		t.Errorf("expected multiple tools summary, got: %s", multi)
	}

	// 5. Multiple tools with error
	multiErr := RenderCollapsedToolSummary([]ChatEntry{
		{ToolName: "shell", Command: "git status", ExitCode: 0},
		{ToolName: "shell", Command: "bad command", ExitCode: 127},
	})
	if !strings.Contains(multiErr, "2 tool calls") || !strings.Contains(multiErr, "1 ERRORS") || !strings.Contains(multiErr, "Ctrl+O to expand") {
		t.Errorf("expected multiple tools with error summary, got: %s", multiErr)
	}
}

func TestModel_ExpandToolsToggle(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = newM.(Model)

	// Verify default is collapsed (expandTools == false)
	if m.expandTools {
		t.Errorf("expected expandTools to be false by default")
	}

	// Add a tool entry and user entry
	m.entries = append(m.entries,
		ChatEntry{Type: TypeUser, Content: "inspect repo"},
		ChatEntry{Type: TypeTool, ToolName: "shell", Command: "git status", Content: "On branch main", ExitCode: 0},
	)
	m.renderConversationToViewport()

	// In collapsed mode: viewport has "Ctrl+O to expand" and NOT the full box border "╭"
	collapsedView := m.viewport.View()
	if !strings.Contains(collapsedView, "Ctrl+O to expand") {
		t.Errorf("expected collapsed view to contain 'Ctrl+O to expand', got:\n%s", collapsedView)
	}
	if strings.Contains(collapsedView, "╭") {
		t.Errorf("expected collapsed view NOT to have full box border '╭', got:\n%s", collapsedView)
	}

	// Press Ctrl+O to expand
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = newM.(Model)
	if !m.expandTools {
		t.Errorf("expected expandTools to be true after Ctrl+O")
	}

	// In expanded mode: viewport contains the full ToolCard with "╭"
	expandedView := m.viewport.View()
	if !strings.Contains(expandedView, "╭") {
		t.Errorf("expected expanded view to contain ToolCardBox '╭', got:\n%s", expandedView)
	}
	if !strings.Contains(expandedView, "On branch main") {
		t.Errorf("expected expanded view to contain output 'On branch main', got:\n%s", expandedView)
	}

	// Press Ctrl+O again to collapse back
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}}) // standard key without ctrl
	// with "ctrl+o" string message:
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = newM.(Model)
	if m.expandTools {
		t.Errorf("expected expandTools to be false after second Ctrl+O")
	}

	// Toggle via /tools slash command
	newM, _ = m.handleSlashCommand("/tools")
	m = newM.(Model)
	if !m.expandTools {
		t.Errorf("expected expandTools to be true after /tools command")
	}
}

// newScrollTestModel returns a model with plenty of conversation history and a
// sized viewport, scrolled to the bottom.
func newScrollTestModel(t *testing.T) Model {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := fractal.OpenStore(tmpDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = newM.(Model)
	for i := 0; i < 200; i++ {
		m.entries = append(m.entries, ChatEntry{
			Type:    TypeAgent,
			Content: fmt.Sprintf("Line of history %d", i),
		})
	}
	m.renderConversationToViewport()
	m.viewport.GotoBottom()
	return m
}

// TestModel_WheelScrollIsRateLimited is the regression guard for "my Marathon
// mouse scrolls insanely fast": a free-spinning wheel delivers dozens of events
// in one flick, and without a limiter each one jumps several lines.
func TestModel_WheelScrollIsRateLimited(t *testing.T) {
	original := wheelRefillInterval
	wheelRefillInterval = time.Hour // no refill: only the burst may be spent
	t.Cleanup(func() { wheelRefillInterval = original })

	m := newScrollTestModel(t)
	start := m.viewport.YOffset
	if start == 0 {
		t.Fatal("expected a scrollable conversation")
	}

	const events = 50
	for i := 0; i < events; i++ {
		newM, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
		m = newM.(Model)
	}

	scrolled := start - m.viewport.YOffset
	if scrolled <= 0 {
		t.Fatal("rate limiter swallowed every event; a flick must still scroll")
	}
	if scrolled > wheelBurst {
		t.Errorf("%d wheel events scrolled %d lines, want at most %d (the burst budget)", events, scrolled, wheelBurst)
	}
}

// TestModel_WheelScrollRefillsOverTime makes sure the limiter spaces events out
// instead of dropping them forever: with time passing, later events scroll.
func TestModel_WheelScrollRefillsOverTime(t *testing.T) {
	original := wheelRefillInterval
	wheelRefillInterval = time.Nanosecond
	t.Cleanup(func() { wheelRefillInterval = original })

	m := newScrollTestModel(t)
	start := m.viewport.YOffset

	for i := 0; i < 10; i++ {
		newM, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
		m = newM.(Model)
		time.Sleep(2 * time.Millisecond)
	}

	if scrolled := start - m.viewport.YOffset; scrolled != 10 {
		t.Errorf("scrolled %d lines across 10 spaced events, want 10", scrolled)
	}
}

// TestModel_WheelScrollIgnoresNonWheelEvents ensures clicks and motion do not
// move the buffer (Golem has no clickable UI, and the wheel-only capture is what
// keeps selection usable).
func TestModel_WheelScrollIgnoresNonWheelEvents(t *testing.T) {
	m := newScrollTestModel(t)
	start := m.viewport.YOffset

	newM, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	m = newM.(Model)
	if m.viewport.YOffset != start {
		t.Errorf("left-click release scrolled the conversation (%d -> %d)", start, m.viewport.YOffset)
	}

	newM, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m = newM.(Model)
	if m.viewport.YOffset != start {
		t.Errorf("left-click scrolled the conversation (%d -> %d)", start, m.viewport.YOffset)
	}
}

// TestModel_CtrlGTogglesMouseCapture covers handing the mouse back to tmux: the
// key must flip the state and emit the matching bubbletea command both ways.
func TestModel_CtrlGTogglesMouseCapture(t *testing.T) {
	m := newScrollTestModel(t)
	if !m.MouseEnabled() {
		t.Fatal("mouse capture should default to enabled inside the TUI")
	}

	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = newM.(Model)
	if m.MouseEnabled() {
		t.Error("Ctrl+G did not release mouse capture")
	}
	// bubbletea keeps these message types unexported, so match on the type name:
	// DisableMouse() must produce disableMouseMsg (and never an enable*).
	if cmd == nil {
		t.Error("Ctrl+G must emit a DisableMouse command")
	} else if got := fmt.Sprintf("%T", cmd()); got != "tea.disableMouseMsg" {
		t.Errorf("Ctrl+G emitted %s, want tea.disableMouseMsg", got)
	}
	if !strings.Contains(m.statusMessage, "OFF") {
		t.Errorf("status message = %q, want it to mention mouse capture being off", m.statusMessage)
	}

	newM, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = newM.(Model)
	if !m.MouseEnabled() {
		t.Error("second Ctrl+G did not re-enable mouse capture")
	}
	if cmd == nil {
		t.Error("Ctrl+G must emit an EnableMouseCellMotion command")
	} else if got := fmt.Sprintf("%T", cmd()); got != "tea.enableMouseCellMotionMsg" {
		t.Errorf("Ctrl+G emitted %s, want tea.enableMouseCellMotionMsg", got)
	}
	if !strings.Contains(m.statusMessage, "ON") {
		t.Errorf("status message = %q, want it to mention mouse capture being on", m.statusMessage)
	}
}

// TestModel_SetMouseEnabled covers the --no-mouse wiring: the model must start
// with capture off, and Ctrl+G must then be able to turn it on.
func TestModel_SetMouseEnabled(t *testing.T) {
	m := newScrollTestModel(t)
	m.SetMouseEnabled(false)
	if m.MouseEnabled() {
		t.Fatal("SetMouseEnabled(false) did not take effect")
	}
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = newM.(Model)
	if !m.MouseEnabled() {
		t.Error("Ctrl+G did not enable mouse capture after a --no-mouse start")
	}
}

// TestModel_StartupLandsOnNewestOutput is the regression guard for "when
// starting golem I see the start of the last response": the replay is rendered
// from the top of the log, so the first layout pass must jump to the bottom.
func TestModel_StartupLandsOnNewestOutput(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := fractal.OpenStore(tmpDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	view := fractal.NewView(store, fractal.DefaultViewConfig())

	m := NewModel(nil, store, view)
	for i := 0; i < 200; i++ {
		m.entries = append(m.entries, ChatEntry{
			Type:    TypeAgent,
			Content: fmt.Sprintf("Replayed line %d", i),
		})
	}

	newM, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	m = newM.(Model)
	if !m.viewport.AtBottom() {
		t.Fatalf("startup should show the newest output, YOffset=%d of %d lines",
			m.viewport.YOffset, m.viewport.TotalLineCount())
	}

	// A later resize (e.g. terminal drag) must not yank the reader back down.
	m.viewport.GotoTop()
	newM, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(Model)
	if m.viewport.AtBottom() {
		t.Fatal("resize after startup must preserve the reader's scroll position")
	}
}

// TestModel_HomeEndJumpToConversationEnds covers the answer to "or don't know
// how to scroll to the end": Home/End go straight to either end of the
// conversation while the prompt is empty.
func TestModel_HomeEndJumpToConversationEnds(t *testing.T) {
	m := newScrollTestModel(t)
	if !m.viewport.AtBottom() {
		t.Fatal("test setup should start pinned to the newest output")
	}

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = newM.(Model)
	if m.viewport.YOffset != 0 {
		t.Fatalf("Home should jump to the top, YOffset=%d", m.viewport.YOffset)
	}

	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = newM.(Model)
	if !m.viewport.AtBottom() {
		t.Fatalf("End should jump to the newest output, YOffset=%d", m.viewport.YOffset)
	}

	// With a draft in the prompt, Home/End stay textarea keys.
	m.textarea.SetValue("draft text")
	before := m.viewport.YOffset
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = newM.(Model)
	if m.viewport.YOffset != before {
		t.Fatalf("Home with a draft must not scroll the conversation, YOffset=%d want %d", m.viewport.YOffset, before)
	}
	m.textarea.Reset()
}

// TestModel_ScrolledUpShowsWayBack asserts the status bar tells you how to
// return when newer output is off-screen.
func TestModel_ScrolledUpShowsWayBack(t *testing.T) {
	m := newScrollTestModel(t)
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = newM.(Model)

	view := m.View()
	if !strings.Contains(view, "scrolled up") {
		t.Fatal("scrolled-up view should hint how to reach the newest output")
	}
	if !strings.Contains(view, "End: newest") {
		t.Fatal("scrolled-up hint should name the End key")
	}

	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = newM.(Model)
	if strings.Contains(m.View(), "scrolled up") {
		t.Fatal("hint must disappear once the newest output is visible again")
	}
}

// TestModel_StartupReplayShowsNewestTurnEndToEnd drives the real store path:
// a long conversation is logged, the TUI opens, and the screen must already be
// showing the most recent turn (the bug was: you saw the top of the last
// response with no obvious way down).
func TestModel_StartupReplayShowsNewestTurnEndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := fractal.OpenStore(tmpDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for i := 0; i < 60; i++ {
		if _, err := store.AppendEvent(fractal.KindUser, fmt.Sprintf("question %d", i)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		if _, err := store.AppendEvent(fractal.KindTalk, fmt.Sprintf("answer %d\n%s", i, strings.Repeat("detail line\n", 6))); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	if _, err := store.AppendEvent(fractal.KindTalk, "FINAL-ANSWER-MARKER"); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = newM.(Model)

	if !m.viewport.AtBottom() {
		t.Fatalf("startup viewport should be pinned to the newest output, YOffset=%d", m.viewport.YOffset)
	}
	if !strings.Contains(m.View(), "FINAL-ANSWER-MARKER") {
		t.Fatal("startup screen should show the end of the most recent response")
	}
}
