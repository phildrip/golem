package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/user/golem/pkg/memory/fractal"
)

// newSlashTestModel builds a sized model with a conversation to render over.
func newSlashTestModel(t *testing.T, width, height int) Model {
	t.Helper()
	store, err := fractal.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	view := fractal.NewView(store, fractal.DefaultViewConfig())
	m := NewModel(nil, store, view)
	newM, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return newM.(Model)
}

func TestSlashCommandsTableIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range slashCommands() {
		if !strings.HasPrefix(c.Name, "/") {
			t.Errorf("command %q must start with a slash", c.Name)
		}
		if seen[c.Name] {
			t.Errorf("duplicate command %q", c.Name)
		}
		seen[c.Name] = true
		if c.Desc == "" {
			t.Errorf("command %q has no description", c.Name)
		}
		for _, a := range c.Aliases {
			if !strings.HasPrefix(a, "/") {
				t.Errorf("alias %q of %q must start with a slash", a, c.Name)
			}
			if seen[a] {
				t.Errorf("alias %q of %q collides with another command", a, c.Name)
			}
			seen[a] = true
		}
	}
	if !seen["/model"] {
		t.Error("expected the palette to offer /model")
	}
}

func TestMatchSlashCommands(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      []string
		wantEmpty bool
	}{
		{name: "bare slash lists everything", input: "/", want: []string{"/model", "/profile", "/help", "/exit"}},
		{name: "model prefix", input: "/mo", want: []string{"/model"}},
		{name: "alias prefix finds tree", input: "/mem", want: []string{"/tree"}},
		{name: "case insensitive", input: "/HEL", want: []string{"/help"}},
		{name: "arguments end completion", input: "/model ", wantEmpty: true},
		{name: "no match", input: "/zzz", wantEmpty: true},
		{name: "plain text is not a command", input: "hello", wantEmpty: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := matchSlashCommands(tc.input)
			if tc.wantEmpty {
				if len(got) != 0 {
					t.Fatalf("expected no matches for %q, got %d", tc.input, len(got))
				}
				return
			}
			names := make(map[string]bool, len(got))
			for _, c := range got {
				names[c.Name] = true
			}
			for _, want := range tc.want {
				if !names[want] {
					t.Errorf("matchSlashCommands(%q) missing %q (got %v)", tc.input, want, names)
				}
			}
		})
	}
}

func TestSlashExactMatch(t *testing.T) {
	tests := map[string]bool{
		"/model":  true,
		"/quit":   true,
		"/models": true,
		"/mod":    false,
		"/":       false,
		"model":   false,
	}
	for input, want := range tests {
		if got := slashExactMatch(input); got != want {
			t.Errorf("slashExactMatch(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestModel_SlashMenuOpensWhileTypingCommand(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	if m.slashMenuVisible() {
		t.Fatal("expected no popup on an empty prompt")
	}

	m.textarea.SetValue("/")
	if !m.slashMenuVisible() {
		t.Fatal("expected the popup to open on a bare slash")
	}

	rows := m.renderSlashMenuRows()
	if len(rows) == 0 || len(rows) > slashMenuMaxRows {
		t.Fatalf("expected 1..%d popup rows, got %d", slashMenuMaxRows, len(rows))
	}
	if !strings.Contains(strings.Join(rows, "\n"), "/model") {
		t.Errorf("expected the first popup page to offer /model, got %v", rows)
	}

	m.textarea.SetValue("/help") // already a complete command
	if m.slashMenuVisible() {
		t.Error("expected the popup to close once the command is complete")
	}

	m.textarea.SetValue("/model ")
	if m.slashMenuVisible() {
		t.Error("expected the popup to close once arguments are being typed")
	}
}

func TestModel_SlashMenuTabCompletes(t *testing.T) {
	m := newSlashTestModel(t, 120, 30)
	m.textarea.SetValue("/mod")

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)

	if got, want := m.textarea.Value(), "/model "; got != want {
		t.Fatalf("after Tab the prompt is %q, want %q", got, want)
	}
	if m.slashMenuVisible() {
		t.Error("popup should be closed after completing a command with arguments")
	}
}

func TestModel_SlashMenuTabCompletesBareCommand(t *testing.T) {
	m := newSlashTestModel(t, 120, 30)
	m.textarea.SetValue("/hel")

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)

	// /help takes no arguments, so it lands complete and Enter submits it.
	if got, want := m.textarea.Value(), "/help"; got != want {
		t.Fatalf("after Tab the prompt is %q, want %q", got, want)
	}
}

func TestModel_SlashMenuArrowSelectionAndEnter(t *testing.T) {
	m := newSlashTestModel(t, 120, 30)
	m.textarea.SetValue("/")

	matches := matchSlashCommands("/")
	if len(matches) < 3 {
		t.Fatalf("expected several matches, got %d", len(matches))
	}

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newM.(Model)
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newM.(Model)
	if m.slashSel != 2 {
		t.Fatalf("expected selection 2 after two Downs, got %d", m.slashSel)
	}

	// Wrapping upwards from the top lands on the last row.
	m.slashSel = 0
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = newM.(Model)
	if m.slashSel != len(matches)-1 {
		t.Fatalf("expected wrap-around to row %d, got %d", len(matches)-1, m.slashSel)
	}

	// Enter on an ambiguous prefix completes the highlighted command instead of
	// submitting an unknown-command error.
	m.textarea.SetValue("/m")
	m.slashSel = 0
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)
	if got := m.textarea.Value(); got != "/model " {
		t.Fatalf("expected Enter to complete /model, got %q", got)
	}
	for _, e := range m.entries {
		if e.Type == TypeSystem && strings.Contains(e.Content, "Unknown command") {
			t.Fatalf("Enter should not submit a partial command, got entry %q", e.Content)
		}
	}
}

func TestModel_SlashMenuEnterAcceptsUnambiguousMatch(t *testing.T) {
	m := newSlashTestModel(t, 120, 30)
	m.textarea.SetValue("/mod")

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)

	if got, want := m.textarea.Value(), "/model "; got != want {
		t.Fatalf("expected Enter to accept %q, got %q", want, got)
	}
}

func TestModel_SlashMenuEscDismissesAndTypingReopens(t *testing.T) {
	m := newSlashTestModel(t, 120, 30)
	m.textarea.SetValue("/m")

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newM.(Model)
	if m.slashMenuVisible() {
		t.Fatal("Esc should dismiss the popup")
	}

	// Typing another character changes the token and re-arms the popup.
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = newM.(Model)
	if got, want := m.textarea.Value(), "/mc"; got != want {
		t.Fatalf("prompt is %q, want %q", got, want)
	}
	if !m.slashMenuVisible() {
		t.Fatal("typing after Esc should reopen the popup")
	}
}

func TestModel_SlashMenuKeepsFrameHeight(t *testing.T) {
	m := newSlashTestModel(t, 100, 24)
	for i := 0; i < 40; i++ {
		m.entries = append(m.entries, ChatEntry{Type: TypeUser, Content: "line of conversation"})
	}
	m.renderConversationToViewport()
	m.viewport.GotoBottom()

	closed := strings.Count(m.View(), "\n") + 1

	m.textarea.SetValue("/")
	if !m.slashMenuVisible() {
		t.Fatal("expected the popup to be open")
	}
	opened := strings.Count(m.View(), "\n") + 1

	if closed != opened {
		t.Fatalf("popup changed the frame height: %d lines closed vs %d open", closed, opened)
	}
	if opened > m.height {
		t.Fatalf("frame is %d lines tall, pane is %d", opened, m.height)
	}
	if !strings.Contains(m.View(), "Tab: complete") {
		t.Error("expected the popup key legend in the status line")
	}
}

func TestModel_ModelCommandWithoutAgent(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	newM, _ := m.handleSlashCommand("/model")
	m = newM.(Model)
	last := m.entries[len(m.entries)-1]
	if !strings.Contains(last.Content, "No active agent") {
		t.Fatalf("expected a clear error without an agent, got %q", last.Content)
	}

	newM, _ = m.handleSlashCommand("/model some-model")
	m = newM.(Model)
	last = m.entries[len(m.entries)-1]
	if !strings.Contains(last.Content, "No active agent") {
		t.Fatalf("expected a clear error without an agent, got %q", last.Content)
	}
}

func TestModel_HelpMentionsModelCommand(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	newM, _ := m.handleSlashCommand("/help")
	m = newM.(Model)
	last := m.entries[len(m.entries)-1]
	if !strings.Contains(last.Content, "/model [name]") {
		t.Fatalf("expected /help to document /model, got %q", last.Content)
	}
}

func TestModel_SlashMenuReopensAfterPromptCleared(t *testing.T) {
	m := newSlashTestModel(t, 120, 30)
	m.textarea.SetValue("/")

	// Esc dismisses the popup for this token.
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newM.(Model)
	if m.slashMenuVisible() {
		t.Fatal("Esc should dismiss the popup")
	}

	// Ctrl+C clears the line; the next "/" must open the popup again.
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = newM.(Model)
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("expected Ctrl+C to clear the prompt, got %q", got)
	}

	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = newM.(Model)
	if !m.slashMenuVisible() {
		t.Fatal("a fresh slash should reopen the popup after the prompt was cleared")
	}
}
