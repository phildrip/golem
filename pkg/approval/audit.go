package approval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/user/golem/pkg/sandbox"
)

// Record is one policy decision, as the audit log keeps it. Every tool call
// that the policy looked at produces one, allow included: "what did it do" is
// only answerable if the allowed calls are there too.
type Record struct {
	Time    time.Time `json:"time"`
	Tool    string    `json:"tool"`
	Verdict string    `json:"verdict"`
	Rule    string    `json:"rule,omitempty"`
	Action  string    `json:"action,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	// Unattended marks a decision taken with nobody to ask, so a refusal is
	// explained by the mode rather than by the command.
	Unattended bool `json:"unattended,omitempty"`
}

// Auditor keeps the recent decisions in memory and, when given a path, appends
// each one to a JSONL file so the log survives a restart.
//
// It is deliberately small and non-blocking in spirit: a decision must never
// fail because the log did, so write errors are counted and ignored.
type Auditor struct {
	mu      sync.Mutex
	ring    []Record
	max     int
	file    *os.File
	written int
	failed  int
}

// NewAuditor builds an auditor keeping the last max records. path may be empty,
// in which case nothing is persisted.
func NewAuditor(path string, max int) (*Auditor, error) {
	if max <= 0 {
		max = 500
	}
	a := &Auditor{max: max}
	if path == "" {
		return a, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	a.file = f
	return a, nil
}

// Path is empty for an in-memory-only auditor.
func (a *Auditor) Path() string {
	if a == nil || a.file == nil {
		return ""
	}
	return a.file.Name()
}

// Record adds one decision.
func (a *Auditor) Record(r Record) {
	if a == nil {
		return
	}
	if r.Time.IsZero() {
		r.Time = time.Now().UTC()
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	a.ring = append(a.ring, r)
	if len(a.ring) > a.max {
		a.ring = a.ring[len(a.ring)-a.max:]
	}
	if a.file == nil {
		return
	}
	line, err := json.Marshal(r)
	if err != nil {
		a.failed++
		return
	}
	if _, err := a.file.Write(append(line, '\n')); err != nil {
		a.failed++
		return
	}
	a.written++
}

// Recent returns the last n records, oldest first.
func (a *Auditor) Recent(n int) []Record {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if n <= 0 || n > len(a.ring) {
		n = len(a.ring)
	}
	out := make([]Record, n)
	copy(out, a.ring[len(a.ring)-n:])
	return out
}

// Close flushes and closes the log file.
func (a *Auditor) Close() error {
	if a == nil || a.file == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.file.Close()
	a.file = nil
	return err
}

// The process-wide auditor: one policy decision point means one audit log.
var (
	auditorMu sync.RWMutex
	auditor   *Auditor
)

// SetAuditor installs the process-wide audit log. Passing nil disables it.
func SetAuditor(a *Auditor) {
	auditorMu.Lock()
	auditor = a
	auditorMu.Unlock()
}

// CurrentAuditor returns the installed auditor, or nil.
func CurrentAuditor() *Auditor {
	auditorMu.RLock()
	defer auditorMu.RUnlock()
	return auditor
}

// Note records a decision taken outside Gate - MCP toolsets, which the ADK
// gates itself - so the log stays complete.
func Note(mode Mode, toolName string, d sandbox.Decision, action string) {
	note(mode, toolName, d, action)
}

// note records d if an auditor is installed.
func note(mode Mode, toolName string, d sandbox.Decision, action string) {
	a := CurrentAuditor()
	if a == nil {
		return
	}
	a.Record(Record{
		Tool:       toolName,
		Verdict:    string(d.Verdict),
		Rule:       d.Rule,
		Action:     action,
		Reason:     d.Reason,
		Unattended: mode == Unattended,
	})
}
