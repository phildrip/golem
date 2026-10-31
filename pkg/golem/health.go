package golem

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/user/golem/pkg/memory/fractal"
)

// Internal failures the turn loop cannot hand back to a caller - a memory
// append while a tool runs, a background compaction pass - used to be discarded
// with an ignored error. Discarding them makes a broken store look like a
// working one: the tool's work never reaches memory and the view sits at
// "not summarized yet" with no signal anywhere. They are recorded here instead,
// so they are logged once and stay visible in the TUI header (golem-068).

// maxHealthProblems bounds the ring; a failing store can fail on every tool
// call, and a thousand identical entries say nothing a dozen do not.
const maxHealthProblems = 50

// Problem is one recorded internal failure.
type Problem struct {
	Time      time.Time `json:"time"`
	Component string    `json:"component"`
	Operation string    `json:"operation"`
	Error     string    `json:"error"`
}

// Health is a bounded record of internal failures, safe for concurrent use and
// safe on a nil receiver (the tool callbacks may run before the agent exists).
type Health struct {
	mu     sync.Mutex
	recent []Problem
	counts map[string]int
	last   map[string]Problem
}

func newHealth() *Health {
	return &Health{
		counts: make(map[string]int),
		last:   make(map[string]Problem),
	}
}

// isContextCanceled reports whether err represents a context cancellation or deadline expiration.
func isContextCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "context deadline exceeded")
}

// Record notes a failure that could not be returned to its caller. Context
// cancellations and deadline expirations (e.g. during agent shutdown or turn
// interruption) are suppressed to avoid false alarm health flags and log spam.
func (h *Health) Record(component, operation string, err error) {
	if err == nil || isContextCanceled(err) {
		return
	}
	p := Problem{
		Time:      time.Now().UTC(),
		Component: component,
		Operation: operation,
		Error:     err.Error(),
	}
	if h == nil {
		log.Printf("golem: %s: %s: %v", component, operation, err)
		return
	}

	h.mu.Lock()
	h.recent = append(h.recent, p)
	if len(h.recent) > maxHealthProblems {
		h.recent = h.recent[len(h.recent)-maxHealthProblems:]
	}
	h.counts[component]++
	h.last[component] = p
	h.mu.Unlock()

	log.Printf("golem: %s: %s: %v", component, operation, err)
}

// Problems returns a copy of the retained failures, oldest first.
func (h *Health) Problems() []Problem {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.recent) == 0 {
		return nil
	}
	out := make([]Problem, len(h.recent))
	copy(out, h.recent)
	return out
}

// Count reports how many failures a component has recorded since start.
func (h *Health) Count(component string) int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.counts[component]
}

// Total reports the number of recorded failures across all components.
func (h *Health) Total() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, n := range h.counts {
		total += n
	}
	return total
}

// Last returns the most recent failure for a component, if any.
func (h *Health) Last(component string) (Problem, bool) {
	if h == nil {
		return Problem{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.last[component]
	return p, ok
}

// Summary renders one line, e.g. "memory 2, compactor 1", or "" when clean.
func (h *Health) Summary() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.counts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(h.counts))
	for component, n := range h.counts {
		parts = append(parts, component+" "+strconv.Itoa(n))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// eventAppender is the slice of *fractal.Store the tool callbacks need; a
// narrow interface lets a test inject a store whose writes fail.
type eventAppender interface {
	AppendEvent(kind fractal.EventKind, text string, opts ...fractal.AppendEventOpts) (fractal.Event, error)
}

// appendMemoryEvent writes one memory event and reports a failure instead of
// dropping it: a lost tool event means the work never reaches the view.
func appendMemoryEvent(h *Health, store eventAppender, kind fractal.EventKind, text string, opts ...fractal.AppendEventOpts) {
	if store == nil {
		return
	}
	if _, err := store.AppendEvent(kind, text, opts...); err != nil {
		h.Record("memory", "append "+string(kind), err)
	}
}
