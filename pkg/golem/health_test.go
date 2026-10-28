package golem

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/user/golem/pkg/memory/fractal"
)

// failingStore is a store whose writes always fail, so the error path of a tool
// callback can be exercised without breaking a real database.
type failingStore struct {
	err error
}

func (f failingStore) AppendEvent(kind fractal.EventKind, text string, opts ...fractal.AppendEventOpts) (fractal.Event, error) {
	return fractal.Event{}, f.err
}

func TestAppendMemoryEventReportsFailure(t *testing.T) {
	h := newHealth()
	storeErr := fmt.Errorf("database is locked")
	appendMemoryEvent(h, failingStore{err: storeErr}, fractal.KindTool, "shell: ls")

	if got := h.Count("memory"); got != 1 {
		t.Fatalf("memory failures = %d, want 1", got)
	}
	last, ok := h.Last("memory")
	if !ok {
		t.Fatalf("no last problem recorded")
	}
	if last.Component != "memory" || last.Operation != "append tool" {
		t.Errorf("problem = %s/%s, want memory/append tool", last.Component, last.Operation)
	}
	if !strings.Contains(last.Error, "database is locked") {
		t.Errorf("problem error = %q, want the store error", last.Error)
	}
	if summary := h.Summary(); summary != "memory 1" {
		t.Errorf("summary = %q, want %q", summary, "memory 1")
	}
}

func TestAppendMemoryEventWritesSilently(t *testing.T) {
	store, err := fractal.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	h := newHealth()
	appendMemoryEvent(h, store, fractal.KindEcho, "shell: ok")

	if got := h.Count("memory"); got != 0 {
		t.Errorf("memory failures = %d, want 0", got)
	}
	if got := store.CountEvents(); got != 1 {
		t.Errorf("events = %d, want 1", got)
	}
}

func TestHealthRingKeepsTheLatestProblems(t *testing.T) {
	h := newHealth()
	for i := 0; i < maxHealthProblems*2; i++ {
		h.Record("memory", "append", fmt.Errorf("boom %d", i))
	}

	problems := h.Problems()
	if len(problems) != maxHealthProblems {
		t.Fatalf("retained %d problems, want %d", len(problems), maxHealthProblems)
	}
	if got := problems[len(problems)-1].Error; got != fmt.Sprintf("boom %d", maxHealthProblems*2-1) {
		t.Errorf("newest problem = %q, want the last failure", got)
	}
	if got := h.Total(); got != maxHealthProblems*2 {
		t.Errorf("total = %d, want %d (counts keep counting)", got, maxHealthProblems*2)
	}
}

func TestHealthSummaryListsEveryComponent(t *testing.T) {
	h := newHealth()
	h.Record("memory", "append tool", fmt.Errorf("x"))
	h.Record("memory", "append echo", fmt.Errorf("y"))
	h.Record("compactor", "pump", fmt.Errorf("z"))

	if got := h.Summary(); got != "compactor 1, memory 2" {
		t.Errorf("summary = %q, want %q", got, "compactor 1, memory 2")
	}
}

func TestNilHealthIsSafe(t *testing.T) {
	var h *Health
	h.Record("memory", "append", fmt.Errorf("ignored"))
	if h.Problems() != nil || h.Count("memory") != 0 || h.Total() != 0 || h.Summary() != "" {
		t.Errorf("nil Health reported failures: %q", h.Summary())
	}
	if _, ok := h.Last("memory"); ok {
		t.Errorf("nil Health returned a last problem")
	}
}

// failingSummarizer fails every compaction, standing in for provider trouble.
type failingSummarizer struct{ err error }

func (f failingSummarizer) Summarize(ctx context.Context, contextView, content string, isMerge bool) (string, error) {
	return "", f.err
}

func TestPumpCompactorSurfacesFailures(t *testing.T) {
	store, err := fractal.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// Oversized event: level-0 needs the model, and the model is failing.
	if _, err := store.AppendEvent(fractal.KindTalk, strings.Repeat("long reasoning ", 100)); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	view := fractal.NewView(store, fractal.ViewConfig{LiveHeadCount: 0})
	compactor := fractal.NewCompactor(store, view, failingSummarizer{err: fmt.Errorf("provider 503")}, fractal.DefaultCompactorConfig())

	a := &Agent{compactor: compactor, health: newHealth()}
	a.pumpCompactor(context.Background())

	if got := a.Health().Count("compactor"); got != 1 {
		t.Fatalf("compactor failures = %d, want 1", got)
	}
	last, ok := a.Health().Last("compactor")
	if !ok || !strings.Contains(last.Error, "provider 503") {
		t.Errorf("last problem = %+v, want the summarizer error", last)
	}
	if failures, pumpErr := compactor.PumpStatus(); failures != 1 || pumpErr == nil {
		t.Errorf("PumpStatus = (%d, %v), want (1, non-nil)", failures, pumpErr)
	}
}
