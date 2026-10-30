package graph

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/session"
)

// RunsDir is the subdirectory under GraphsDir where durable run records and
// their ADK session event logs live (.golem/graphs/runs/<run-id>/).
const RunsDir = "runs"

// RunStatus is the lifecycle state of a persisted graph run.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunWaiting   RunStatus = "waiting"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
)

// WaitingInterrupt describes one open human-in-the-loop pause inside a run.
type WaitingInterrupt struct {
	Node        string         `json:"node"`
	InterruptID string         `json:"interrupt_id"`
	Kind        string         `json:"kind"` // "request_input" or "tool_confirmation"
	ToolName    string         `json:"tool_name,omitempty"`
	ToolArgs    map[string]any `json:"tool_args,omitempty"`
	Hint        string         `json:"hint,omitempty"`
	Payload     any            `json:"payload,omitempty"`
}

// RunRecord is the durable metadata stored at .golem/graphs/runs/<run-id>/run.json.
type RunRecord struct {
	RunID        string             `json:"run_id"`
	Graph        string             `json:"graph"`
	Spec         *Spec              `json:"spec"`
	Input        string             `json:"input"`
	SessionID    string             `json:"session_id"`
	InvocationID string             `json:"invocation_id,omitempty"`
	Status       RunStatus          `json:"status"`
	Waiting      []WaitingInterrupt `json:"waiting,omitempty"`
	Output       string             `json:"output,omitempty"`
	Nodes        []NodeResult       `json:"nodes,omitempty"`
	Tokens       int64              `json:"tokens,omitempty"`
	Duration     time.Duration      `json:"duration,omitempty"`
	Err          string             `json:"error,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

// runDir returns .golem/graphs/runs/<runID>.
func (m *Manager) runDir(runID string) string {
	return filepath.Join(m.dir, RunsDir, runID)
}

// sessionDir is where a run's ADK session lives:
// .golem/graphs/runs/<runID>/session (events.jsonl + state.json).
func (m *Manager) sessionDir(runID string) string {
	return filepath.Join(m.runDir(runID), "session")
}

// pruneRuns keeps at most keep run directories, oldest updated first to go. A
// run directory holds its session transcript, which is only needed while a run
// can still be resumed, so pruning the tail is safe; the record that remains is
// what graph_stat reads.
func (m *Manager) pruneRuns(keep int) {
	if keep <= 0 {
		return
	}
	runs, err := m.ListRuns()
	if err != nil || len(runs) <= keep {
		return
	}
	for _, rec := range runs[keep:] {
		if rec.Status == RunRunning || rec.Status == RunWaiting {
			continue
		}
		_ = os.RemoveAll(m.runDir(rec.RunID))
	}
}

// saveRunRecord writes RunRecord atomically to .golem/graphs/runs/<runID>/run.json.
func (m *Manager) saveRunRecord(rec *RunRecord) error {
	if rec == nil || rec.RunID == "" {
		return fmt.Errorf("run record missing run_id")
	}
	dir := m.runDir(rec.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errors.Wrapf(err, "creating run directory %s", dir)
	}
	rec.UpdatedAt = time.Now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rec.UpdatedAt
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return errors.Wrap(err, "encoding run record")
	}
	data = append(data, '\n')
	tmp := filepath.Join(dir, "run.json.tmp")
	target := filepath.Join(dir, "run.json")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return errors.Wrapf(err, "writing %s", tmp)
	}
	if err := os.Rename(tmp, target); err != nil {
		return errors.Wrapf(err, "renaming %s to %s", tmp, target)
	}
	return nil
}

// LoadRun reads one persisted RunRecord by run_id.
func (m *Manager) LoadRun(runID string) (*RunRecord, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" || strings.ContainsAny(runID, `/\..`) {
		return nil, fmt.Errorf("invalid run_id %q", runID)
	}
	path := filepath.Join(m.runDir(runID), "run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no graph run with id %q", runID)
		}
		return nil, errors.Wrapf(err, "reading run %q", runID)
	}
	var rec RunRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, errors.Wrapf(err, "parsing %s", path)
	}
	if rec.Spec != nil {
		rec.Spec.Normalize()
	}
	return &rec, nil
}

// ListRuns returns persisted run records, newest first.
func (m *Manager) ListRuns() ([]*RunRecord, error) {
	base := filepath.Join(m.dir, RunsDir)
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "reading runs directory %s", base)
	}
	var out []*RunRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rec, err := m.LoadRun(e.Name())
		if err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// fileSessionService is a durable session.Service that persists session events
// as JSONL and state as JSON inside a single run directory on disk. It wraps
// an in-memory session.Service for fast in-turn reads while mirroring every
// mutation to disk so a subsequent process can rehydrate the session verbatim.
type fileSessionService struct {
	mu      sync.Mutex
	dir     string
	appName string
	userID  string
	sessID  string
	mem     session.Service
}

// openFileSessionService creates or rehydrates a session backed by dir.
func openFileSessionService(ctx context.Context, dir, appName, userID, sessID string) (*fileSessionService, session.Session, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, errors.Wrapf(err, "creating session dir %s", dir)
	}
	mem := session.InMemoryService()
	stateInit := loadStateFile(filepath.Join(dir, "state.json"))
	created, err := mem.Create(ctx, &session.CreateRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessID,
		State:     stateInit,
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "creating in-memory session")
	}
	sess := created.Session
	eventsPath := filepath.Join(dir, "events.jsonl")
	if existing, err := readEventsJSONL(eventsPath); err == nil {
		for _, ev := range existing {
			if err := mem.AppendEvent(ctx, sess, ev); err != nil {
				return nil, nil, errors.Wrap(err, "rehydrating session event")
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, errors.Wrapf(err, "reading %s", eventsPath)
	}

	// Re-fetch so caller gets the hydrated snapshot.
	got, err := mem.Get(ctx, &session.GetRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessID,
	})
	if err != nil {
		return nil, nil, err
	}
	return &fileSessionService{
		dir:     dir,
		appName: appName,
		userID:  userID,
		sessID:  sessID,
		mem:     mem,
	}, got.Session, nil
}

func (s *fileSessionService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	return s.mem.Create(ctx, req)
}

func (s *fileSessionService) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	return s.mem.Get(ctx, req)
}

func (s *fileSessionService) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	return s.mem.List(ctx, req)
}

func (s *fileSessionService) Delete(ctx context.Context, req *session.DeleteRequest) error {
	return s.mem.Delete(ctx, req)
}

func (s *fileSessionService) AppendEvent(ctx context.Context, curSession session.Session, event *session.Event) error {
	if err := s.mem.AppendEvent(ctx, curSession, event); err != nil {
		return err
	}
	if event == nil || event.Partial {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := appendEventJSONL(filepath.Join(s.dir, "events.jsonl"), event); err != nil {
		return err
	}
	if len(event.Actions.StateDelta) > 0 {
		state := loadStateFile(filepath.Join(s.dir, "state.json"))
		if state == nil {
			state = map[string]any{}
		}
		for k, v := range event.Actions.StateDelta {
			if !strings.HasPrefix(k, session.KeyPrefixTemp) {
				state[k] = v
			}
		}
		_ = writeStateFile(filepath.Join(s.dir, "state.json"), state)
	}
	return nil
}

func (s *fileSessionService) Events(ctx context.Context) ([]*session.Event, error) {
	got, err := s.mem.Get(ctx, &session.GetRequest{
		AppName:   s.appName,
		UserID:    s.userID,
		SessionID: s.sessID,
	})
	if err != nil {
		return nil, err
	}
	evList := got.Session.Events()
	out := make([]*session.Event, 0, evList.Len())
	for i := 0; i < evList.Len(); i++ {
		out = append(out, evList.At(i))
	}
	return out, nil
}

func appendEventJSONL(path string, ev *session.Event) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return errors.Wrapf(err, "opening %s", path)
	}
	defer f.Close()
	line, err := json.Marshal(ev)
	if err != nil {
		return errors.Wrap(err, "encoding session event")
	}
	line = append(line, '\n')
	_, err = f.Write(line)
	return err
}

func readEventsJSONL(path string) ([]*session.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*session.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev session.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, errors.Wrap(err, "decoding session event")
		}
		out = append(out, &ev)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func loadStateFile(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

func writeStateFile(path string, state map[string]any) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

var (
	_ = iter.Seq[int](nil)
	_ = maps.Clone[map[string]any]
	_ = slices.Clone[[]string]
)
