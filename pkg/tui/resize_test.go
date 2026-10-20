package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/golem/pkg/memory/fractal"
)

// newResizeTestModel builds a model with enough long lines that the wrapped
// line count changes noticeably with the pane width, then performs the first
// layout pass at 120x30.
func newResizeTestModel(t *testing.T, entries int) Model {
	t.Helper()

	store, err := fractal.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	m := NewModel(nil, store, fractal.NewView(store, fractal.DefaultViewConfig()))
	for i := 0; i < entries; i++ {
		m.entries = append(m.entries, ChatEntry{
			Type:    TypeAgent,
			Content: fmt.Sprintf("History line %d: %s", i, strings.Repeat("wrapped prose ", 12)),
		})
	}

	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return newM.(Model)
}

// TestModel_ResizeReflowsConversation covers "relayout if the pane size
// changes": narrowing the pane must re-wrap the markdown at the new width (not
// leave every line rendered for the old one) and a reader watching the newest
// output must stay pinned to it.
func TestModel_ResizeReflowsConversation(t *testing.T) {
	m := newResizeTestModel(t, 40)

	wideLines := m.viewport.TotalLineCount()
	if !m.viewport.AtBottom() {
		t.Fatalf("setup should start pinned to the newest output, YOffset=%d", m.viewport.YOffset)
	}

	newM, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	m = newM.(Model)

	narrowLines := m.viewport.TotalLineCount()
	if narrowLines <= wideLines {
		t.Fatalf("narrowing the pane should re-wrap into more lines: wide=%d narrow=%d", wideLines, narrowLines)
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("a reader pinned to the newest output must stay pinned across a resize: YOffset=%d of %d",
			m.viewport.YOffset, narrowLines)
	}
	if view := m.viewport.View(); !strings.Contains(view, "History line 39") {
		t.Fatal("the newest entry should still be on screen after the resize")
	}

	// Widening back re-wraps again and stays on the newest output.
	newM, _ = m.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	m = newM.(Model)
	if got := m.viewport.TotalLineCount(); got >= narrowLines {
		t.Fatalf("widening the pane should re-wrap into fewer lines: narrow=%d wide=%d", narrowLines, got)
	}
	if !m.viewport.AtBottom() {
		t.Fatal("widening the pane should keep the newest output in view")
	}
}

// TestModel_ResizeKeepsReadingPosition is the other half of a relayout: someone
// reading the middle of a long answer must not be teleported to the top or the
// bottom when the pane resizes.
func TestModel_ResizeKeepsReadingPosition(t *testing.T) {
	m := newResizeTestModel(t, 60)

	m.viewport.GotoTop()
	m.viewport.SetYOffset(80)
	prevOffset := m.viewport.YOffset
	prevMax := max(0, m.viewport.TotalLineCount()-m.viewport.Height)
	if prevOffset <= 0 || prevOffset >= prevMax {
		t.Fatalf("setup should sit mid-transcript, offset=%d max=%d", prevOffset, prevMax)
	}

	newM, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 30})
	m = newM.(Model)

	if m.viewport.AtBottom() {
		t.Fatal("resizing must not jump a mid-transcript reader to the newest output")
	}
	if m.viewport.YOffset == 0 {
		t.Fatal("resizing must not jump a mid-transcript reader to the top")
	}

	newMax := max(0, m.viewport.TotalLineCount()-m.viewport.Height)
	want := prevOffset * newMax / prevMax
	if drift := m.viewport.YOffset - want; drift < -newMax/4 || drift > newMax/4 {
		t.Fatalf("reading position drifted too far: got offset %d, proportional target %d (old %d/%d, new max %d)",
			m.viewport.YOffset, want, prevOffset, prevMax, newMax)
	}
}

// TestModel_ViewFitsPane guards the tmux symptom behind "relayout": if the frame
// is even one line taller than the pane, tmux scrolls the whole screen and the
// layout appears to jump. The chrome is measured, so the frame must always match
// the terminal height exactly — including when the status line wraps.
func TestModel_ViewFitsPane(t *testing.T) {
	m := newResizeTestModel(t, 20)

	sizes := []tea.WindowSizeMsg{
		{Width: 120, Height: 30},
		{Width: 60, Height: 18},
		{Width: 44, Height: 12},
		{Width: 100, Height: 14},
		{Width: 80, Height: 40},
	}
	for _, size := range sizes {
		newM, _ := m.Update(size)
		m = newM.(Model)

		if got := lipgloss.Height(m.View()); got != size.Height {
			t.Fatalf("%dx%d: frame is %d lines, must be exactly %d", size.Width, size.Height, got, size.Height)
		}
		// No line may be wider than the pane either: the terminal would wrap it
		// and the frame would grow a row past the pane height.
		for _, line := range strings.Split(m.View(), "\n") {
			if got := lipgloss.Width(line); got > size.Width {
				t.Fatalf("%dx%d: line is %d cells wide, must fit %d: %q",
					size.Width, size.Height, got, size.Width, line)
			}
		}
		if m.viewport.Width != size.Width {
			t.Fatalf("%dx%d: viewport width %d, want %d", size.Width, size.Height, m.viewport.Width, size.Width)
		}
		wantBody := size.Height - lipgloss.Height(m.renderHeader()) - lipgloss.Height(m.renderFooter())
		if m.viewport.Height != wantBody {
			t.Fatalf("%dx%d: body height %d, want %d", size.Width, size.Height, m.viewport.Height, wantBody)
		}
		// The textarea reserves the "> " prompt, so its editable width is the
		// requested width minus the prompt.
		if got, want := m.textarea.Width(), max(20, size.Width-4)-len("> "); got != want {
			t.Fatalf("%dx%d: textarea width %d, want %d", size.Width, size.Height, got, want)
		}
	}

	// The [MOUSE OFF] badge sits in front of the hints; at narrow widths the
	// hints have to give up room for it instead of pushing the line over.
	m.SetMouseEnabled(false)
	for _, size := range []tea.WindowSizeMsg{{Width: 44, Height: 12}, {Width: 30, Height: 10}} {
		newM, _ := m.Update(size)
		m = newM.(Model)
		for _, line := range strings.Split(m.View(), "\n") {
			if got := lipgloss.Width(line); got > size.Width {
				t.Fatalf("%dx%d: MOUSE OFF line is %d cells wide: %q", size.Width, size.Height, got, line)
			}
		}
		if got := lipgloss.Height(m.View()); got != size.Height {
			t.Fatalf("%dx%d: frame is %d lines, must be %d", size.Width, size.Height, got, size.Height)
		}
	}
	m.SetMouseEnabled(true)

	// A long status message wraps in a narrow pane; the body must give up the
	// line so the frame still fits.
	m.statusMessage = "Mouse capture OFF • tmux can now scroll/copy the pane with its own keys • Ctrl+G re-enables in-app scrolling"
	m.statusMessageAt = time.Now()
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 50, Height: 16})
	m = newM.(Model)
	if got := lipgloss.Height(m.View()); got != 16 {
		t.Fatalf("wrapped status line: frame is %d lines, must be 16", got)
	}
}

// TestModel_ModalResizeFitsPane covers resizing while a popup is open: the
// overlays are sized from the window too, so they must stay inside the pane.
func TestModel_ModalResizeFitsPane(t *testing.T) {
	m := newResizeTestModel(t, 5)

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = newM.(Model)
	if !m.showTreeModal {
		t.Fatal("Ctrl+T should open the memory-tree modal")
	}

	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 20}, {Width: 80, Height: 20}, {Width: 50, Height: 20}} {
		newM, _ := m.Update(size)
		m = newM.(Model)

		view := m.View()
		if got := lipgloss.Height(view); got > size.Height {
			t.Fatalf("%dx%d: modal is %d lines tall", size.Width, size.Height, got)
		}
		for _, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.Width {
				t.Fatalf("%dx%d: modal line is %d cells wide", size.Width, size.Height, got)
			}
		}
		if m.treeViewport.Width != size.Width-10 {
			t.Fatalf("%dx%d: tree viewport width %d, want %d", size.Width, size.Height, m.treeViewport.Width, size.Width-10)
		}
	}
}
