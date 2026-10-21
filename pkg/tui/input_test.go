package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tick sends a key the prompt ignores: any pass through Update reconciles the
// draft box height, which is what these tests exercise.
func tick(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyF1})
	return next.(Model)
}

// TestModel_InputStartsAtOneLine covers the resting shape of the prompt: a
// single row, so the conversation keeps every row the pane can spare.
func TestModel_InputStartsAtOneLine(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	if got := m.textarea.Height(); got != inputMinRows {
		t.Fatalf("empty prompt is %d rows, want %d", got, inputMinRows)
	}
	// input row + rounded border (2) + status line (1)
	if got, want := lipgloss.Height(m.renderFooter()), inputMinRows+3; got != want {
		t.Fatalf("footer is %d rows, want %d", got, want)
	}
}

// TestModel_InputGrowsWithContent covers "growing as required": a multi-line
// draft expands the box and the conversation gives up the rows, so the frame
// still fits the pane exactly.
func TestModel_InputGrowsWithContent(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)
	bodyBefore := m.viewport.Height

	m.textarea.InsertString("first line\nsecond line\nthird line")
	m = tick(t, m)

	if got := m.textarea.Height(); got != 3 {
		t.Fatalf("three-line draft is %d rows, want 3", got)
	}
	if got, want := m.viewport.Height, bodyBefore-2; got != want {
		t.Fatalf("conversation body is %d rows, want %d (must give up the two extra input rows)", got, want)
	}
	if got := lipgloss.Height(m.View()); got != 30 {
		t.Fatalf("frame is %d rows, must stay 30", got)
	}

	// Sending the draft empties the box, and it collapses back to one line.
	m.textarea.Reset()
	m = tick(t, m)
	if got := m.textarea.Height(); got != inputMinRows {
		t.Fatalf("cleared prompt is %d rows, want %d", got, inputMinRows)
	}
	if got := m.viewport.Height; got != bodyBefore {
		t.Fatalf("conversation body is %d rows after clearing, want %d back", got, bodyBefore)
	}
}

// TestModel_InputGrowsWhenLineWraps covers the other way content grows: one
// logical line longer than the pane still needs more than one row.
func TestModel_InputGrowsWhenLineWraps(t *testing.T) {
	m := newSlashTestModel(t, 60, 30)
	width := m.textarea.Width()

	m.textarea.InsertString(strings.Repeat("word ", width/3))
	m = tick(t, m)

	want := wrappedRows(m.textarea.Value(), width)
	if want < 2 {
		t.Fatalf("test setup should wrap into several rows, got %d for width %d", want, width)
	}
	if got := m.textarea.Height(); got != want {
		t.Fatalf("wrapped draft is %d rows, want %d", got, want)
	}
}

// TestModel_InputGrowthIsCapped covers the ceiling: past the budget the
// textarea scrolls its own draft instead of squeezing the conversation away.
func TestModel_InputGrowthIsCapped(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)
	if got, want := m.inputRowBudget(), 10; got != want {
		t.Fatalf("row budget is %d, want %d (a third of 30)", got, want)
	}

	var sb strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	m.textarea.InsertString(sb.String())
	m = tick(t, m)

	if got := m.textarea.Height(); got != inputMaxRows {
		t.Fatalf("40-line draft is %d rows, want the %d-row cap", got, inputMaxRows)
	}
	if got := lipgloss.Height(m.View()); got != 30 {
		t.Fatalf("frame is %d rows with a capped draft, must stay 30", got)
	}
	if m.viewport.Height < 1 {
		t.Fatalf("conversation was squeezed out: body is %d rows", m.viewport.Height)
	}

	// A short pane caps lower than a tall one but never below one row.
	short := newSlashTestModel(t, 100, 9)
	if got, want := short.inputRowBudget(), 3; got != want {
		t.Fatalf("row budget at 9 rows is %d, want %d", got, want)
	}
	tiny := short
	tiny.height = 3
	if got := tiny.inputRowBudget(); got != inputMinRows {
		t.Fatalf("row budget on a 3-row pane is %d, want at least %d", got, inputMinRows)
	}
}

// TestModel_AltEnterBreaksTheLine covers the way to type a multi-line draft at
// all: Enter sends, so Alt+Enter (and Ctrl+J) add a line instead, and the box
// grows to show it.
func TestModel_AltEnterBreaksTheLine(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	m.textarea.InsertString("first")
	m = tick(t, m)

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	m = next.(Model)

	if got, want := m.textarea.Value(), "first\n"; got != want {
		t.Fatalf("Alt+Enter left the draft as %q, want %q", got, want)
	}
	if got := m.textarea.Height(); got != 2 {
		t.Fatalf("draft after Alt+Enter is %d rows, want 2", got)
	}
	if m.isLoading {
		t.Fatal("Alt+Enter must not send the turn")
	}

	// Ctrl+J is the readline spelling of the same thing.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = next.(Model)
	if got, want := m.textarea.Value(), "first\n\n"; got != want {
		t.Fatalf("Ctrl+J left the draft as %q, want %q", got, want)
	}
	if got := m.textarea.Height(); got != 3 {
		t.Fatalf("draft after Ctrl+J is %d rows, want 3", got)
	}
}

// TestWrappedRows pins the row counting the growth depends on.
func TestWrappedRows(t *testing.T) {
	cases := []struct {
		line  string
		width int
		want  int
	}{
		{"", 20, 1},
		{"short", 20, 1},
		{"exactly ten", 11, 1},
		{"exactly eleven", 11, 2},
		{strings.Repeat("x", 45), 20, 3},
		{"aaaa bbbb cccc", 20, 1},
	}
	for _, tc := range cases {
		if got := wrappedRows(tc.line, tc.width); got != tc.want {
			t.Errorf("wrappedRows(%q, %d) = %d, want %d", tc.line, tc.width, got, tc.want)
		}
	}
}
