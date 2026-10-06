package tui

import (
	"strings"
	"testing"

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
		name        string
		totalEvents int
		tailLevels  map[int]int
		liveCount   int
		compacting  bool
		wantContains []string
	}{
		{
			name:        "all live head",
			totalEvents: 3,
			tailLevels:  map[int]int{},
			liveCount:   3,
			compacting:  false,
			wantContains: []string{"3 msgs", "Live:3", "Idle"},
		},
		{
			name:        "with fractal tail and compaction active",
			totalEvents: 42,
			tailLevels:  map[int]int{0: 20, 1: 10, 2: 4},
			liveCount:   8,
			compacting:  true,
			wantContains: []string{"42 msgs", "L0:20", "L1:10", "L2:4", "Live:8", "Compacting"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rendered := RenderMemoryMinimap(tc.totalEvents, tc.tailLevels, tc.liveCount, tc.compacting)
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

func TestModel_CtrlCAndCtrlD(t *testing.T) {
	tmpDir := t.TempDir()
	store, _ := fractal.OpenStore(tmpDir)
	defer store.Close()
	view := fractal.NewView(store, fractal.DefaultViewConfig())

	t.Run("Ctrl+D exits unconditionally", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.textarea.SetValue("some draft text")
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmd == nil {
			t.Fatalf("expected tea.Quit cmd on Ctrl+D")
		}
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg, got %T", msg)
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

	t.Run("Ctrl+D and Ctrl+C in tree modal exits", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.showTreeModal = true

		_, cmdD := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmdD == nil {
			t.Fatalf("expected quit cmd on Ctrl+D in modal")
		}
		if _, ok := cmdD().(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg on Ctrl+D in modal")
		}

		_, cmdC := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmdC == nil {
			t.Fatalf("expected quit cmd on Ctrl+C in modal")
		}
		if _, ok := cmdC().(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg on Ctrl+C in modal")
		}
	})

	t.Run("Ctrl+D and Ctrl+C in shortcuts modal exits", func(t *testing.T) {
		m := NewModel(nil, store, view)
		m.showShortcutsModal = true

		_, cmdD := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
		if cmdD == nil {
			t.Fatalf("expected quit cmd on Ctrl+D in shortcuts modal")
		}
		if _, ok := cmdD().(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg on Ctrl+D in shortcuts modal")
		}

		_, cmdC := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
		if cmdC == nil {
			t.Fatalf("expected quit cmd on Ctrl+C in shortcuts modal")
		}
		if _, ok := cmdC().(tea.QuitMsg); !ok {
			t.Errorf("expected tea.QuitMsg on Ctrl+C in shortcuts modal")
		}
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




