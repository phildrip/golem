package graph

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// Result is one run's outcome. It is what the orchestrator's tool call returns:
// the sink's text plus a per-node digest, so the model learns both the answer
// and what the answer cost.
type Result struct {
	Graph         string             `json:"graph"`
	RunID         string             `json:"run_id,omitempty"`
	Status        string             `json:"status,omitempty"`
	Output        string             `json:"output"`
	Nodes         []NodeResult       `json:"nodes"`
	Waiting       []WaitingInterrupt `json:"waiting,omitempty"`
	Tokens        int64              `json:"tokens"`
	CachedTokens  int64              `json:"cached_tokens,omitempty"`
	CacheHitRatio float64            `json:"cache_hit_ratio,omitempty"`
	Activations   int                `json:"activations,omitempty"`
	Duration      time.Duration      `json:"duration"`
	Err           string             `json:"error,omitempty"`
}

// NodeResult is one node's contribution.
type NodeResult struct {
	ID           string        `json:"id"`
	Role         string        `json:"role,omitempty"`
	Kind         string        `json:"kind"`
	Text         string        `json:"text,omitempty"`
	Tokens       int64         `json:"tokens,omitempty"`
	CachedTokens int64         `json:"cached_tokens,omitempty"`
	Activations  int           `json:"activations,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
	Err          string        `json:"error,omitempty"`
}

// nodeCapture accumulates one node's events.
type nodeCapture struct {
	id         string
	role       string
	kind       string
	text       strings.Builder
	out        any
	seen       bool
	activation int
}

// maxNodeText bounds what one node may contribute to the tool result: a node
// that dumps 50k characters would defeat the point of the graph, which is to
// keep the orchestrator's context small.
const maxNodeText = 8000

// Run executes spec with input as the seed handed to every START successor and
// returns the sink's output. The run is durable: its record and its session
// events live under .golem/graphs/runs/<run-id>/, so a run that parks on a
// human approval can be answered later — in another tool call, or after the
// process has restarted — with Resume.
func (m *Manager) Run(ctx agent.Context, spec *Spec, input string) (*Result, error) {
	if err := spec.Validate(ValidateOptions{KnownTool: func(name string) bool { return m.builder.ToolNames()[name] }}); err != nil {
		return nil, err
	}
	rec := &RunRecord{
		RunID:  newRunID(spec.Name),
		Graph:  spec.Name,
		Spec:   spec,
		Input:  input,
		Status: RunRunning,
	}
	rec.SessionID = rec.RunID
	if err := m.saveRunRecord(rec); err != nil {
		return nil, err
	}
	m.pruneRuns(maxKeptRuns)
	return m.execute(ctx, spec, rec, nil)
}

// maxKeptRuns caps how many run directories a workspace keeps. A run holds its
// session transcript, which is what makes a resume possible; the oldest runs
// are pruned once the cap is reached so .golem/graphs/runs/ does not grow
// without bound.
const maxKeptRuns = 20

// Resume answers a paused run and drives it on.
//
// The decision applies to every interrupt the run is waiting on: nodes parked
// on a tool confirmation get Approve, nodes parked for input get Answer.
// Resuming is a second turn of the same durable session, so nodes that already
// finished do not run again and their output carries forward from the record.
func (m *Manager) Resume(ctx agent.Context, runID string, d Decision) (*Result, error) {
	rec, err := m.LoadRun(runID)
	if err != nil {
		return nil, err
	}
	if rec.Status != RunWaiting {
		return nil, fmt.Errorf("run %s is %s, not waiting for an answer", rec.RunID, rec.Status)
	}
	if rec.Spec == nil {
		return nil, fmt.Errorf("run %s has no spec recorded", rec.RunID)
	}
	return m.execute(ctx, rec.Spec, rec, &d)
}

// execute drives one turn of a run: build, run, capture, persist.
//
// A turn is one ADK runner invocation. On a fresh run that is wf.Run; on a
// resume it is wf.Resume with the engine state rebuilt from the session this
// run already has on disk.
func (m *Manager) execute(ctx agent.Context, spec *Spec, rec *RunRecord, resume *Decision) (*Result, error) {
	builder := m.builder
	if resume != nil {
		// The human's answers ride on the tools themselves: an approved call
		// runs instead of asking a second time, a refused one fails fast.
		builder = m.builder.withApprovals(decisionsFor(rec.Waiting, *resume))
	}
	wf, budget, err := builder.Build(ctx, spec)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(spec.Budgets.MaxWallSeconds) * time.Second
	budget.setDeadline(time.Now().Add(timeout))
	runCtx := context.Context(ctx)
	if timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if resume != nil {
		// Tokens spent by earlier turns still count against the ceiling.
		budget.add("", int(rec.Tokens))
	}

	start := time.Now()
	res := &Result{Graph: spec.Name, RunID: rec.RunID}
	captures := priorCaptures(rec, resume != nil)
	seen := map[string]bool{}

	byNode := make(map[string]Node, len(spec.Nodes))
	for _, n := range spec.Nodes {
		byNode[n.ID] = n
	}
	capture := func(author string) *nodeCapture {
		c, ok := captures[author]
		if ok {
			return c
		}
		n := byNode[author]
		c = &nodeCapture{id: author, role: n.Role, kind: n.Kind}
		captures[author] = c
		return c
	}

	// A graph gets its own runner and session, not a bare wf.RunNode: only a
	// runner appends events to a session, and the LLM flow rebuilds each
	// request from session history. Driven directly, a node agent cannot see
	// the function responses from its own tool calls and re-calls the same
	// tool until the wall clock kills the run. The session is the run's durable
	// memory, so it outlives this turn when the graph pauses.
	seq, drv, err := m.drive(ctx, runCtx, spec, rec, wf, resume)
	if err != nil {
		res.Duration = time.Since(start)
		res.Err = fmt.Sprintf("graph failed to start: %v", err)
		rec.Status, rec.Err = RunFailed, res.Err
		_ = m.saveRunRecord(rec)
		return res, errors.New(res.Err)
	}

	var runErr error
	for ev, err := range seq {
		if err != nil {
			runErr = err
			break
		}
		if ev == nil {
			continue
		}
		// Breaking the range is how the engine learns to cancel in-flight
		// nodes, so a run that overruns its wall clock stops here rather
		// than after the graph completes.
		if budget.expired() {
			runErr = context.DeadlineExceeded
			break
		}
		author := eventNodeName(ev, spec)
		if author == "" {
			continue
		}
		c := capture(author)
		if act := budget.nodeActivationCount(author); act > c.activation {
			c.text.Reset()
			c.out = nil
			c.activation = act
		}
		c.seen = true
		seen[author] = true
		if ev.Output != nil {
			c.out = ev.Output
		}
		if ev.Content != nil {
			for _, p := range ev.Content.Parts {
				if p.Text != "" && !p.Thought {
					c.text.WriteString(p.Text)
				}
			}
		}
	}

	res.Duration = time.Since(start)
	res.Tokens = budget.Tokens()
	res.CachedTokens = budget.CachedTokens()
	res.CacheHitRatio = budget.CacheHitRatio()
	res.Activations = budget.Activations()

	events, evErr := drv.events(ctx)
	if evErr != nil && runErr == nil {
		runErr = evErr
	}
	// An unanswered long-running call is the engine's pause signal: the node
	// parks in NodeWaiting and the iterator simply ends.
	interrupts := openInterrupts(events, spec)

	// Report in spec order, so the digest reads like the graph rather than like
	// whichever node happened to finish first.
	prior := priorNodes(rec)
	sink := spec.Sink()
	for _, n := range spec.Nodes {
		c := captures[n.ID]
		nr := NodeResult{ID: n.ID, Role: n.Role, Kind: n.Kind}
		if old, ok := prior[n.ID]; ok {
			nr.Tokens, nr.Duration = old.Tokens, old.Duration
			nr.CachedTokens, nr.Activations = old.CachedTokens, old.Activations
			nr.Err = old.Err
		}
		switch {
		case c != nil && c.seen && seen[n.ID]:
			nr.Text = clipRunes(c.text.String(), maxNodeText)
			nr.Tokens = budget.nodeTokens(n.ID)
			nr.CachedTokens = budget.nodeCachedTokens(n.ID)
			nr.Activations = budget.nodeActivationCount(n.ID)
			nr.Duration = budget.elapsed(n.ID)
			nr.Err = ""
		case c != nil && c.seen:
			nr.Text = clipRunes(c.text.String(), maxNodeText)
		case n.Kind == KindJoin:
			// A join is a barrier: it merges, it does not produce text of its
			// own, so an absent event is normal.
			nr.Tokens = budget.nodeTokens(n.ID)
			nr.Err = ""
		default:
			nr.Err = "did not run"
		}
		res.Nodes = append(res.Nodes, nr)
	}

	res.Output = renderSinkOutput(spec, captures)

	rec.Nodes = res.Nodes
	rec.Tokens = res.Tokens
	rec.Duration += res.Duration
	rec.InvocationID = drv.invocationID

	switch {
	case runErr != nil:
		res.Err = describeRunError(runErr, timeout, budget)
		rec.Status, rec.Err, rec.Waiting = RunFailed, res.Err, nil
	case len(interrupts) > 0:
		res.Status = string(RunWaiting)
		res.Waiting = interrupts
		rec.Status, rec.Err, rec.Waiting = RunWaiting, "", interrupts
	case res.Output == "":
		res.Err = fmt.Sprintf("graph finished but sink %q produced no output", sink)
		rec.Status, rec.Err, rec.Waiting = RunFailed, res.Err, nil
	default:
		res.Status = string(RunCompleted)
		rec.Status, rec.Err, rec.Waiting = RunCompleted, "", nil
		rec.Output = res.Output
	}
	if saveErr := m.saveRunRecord(rec); saveErr != nil && res.Err == "" {
		res.Err = "graph finished but its run record could not be saved: " + saveErr.Error()
	}
	if runErr != nil {
		return res, errors.New(res.Err)
	}
	if len(interrupts) > 0 {
		// A pause is a question, not a failure: the orchestrator answers it
		// with graph_resume.
		return res, nil
	}
	if res.Output == "" {
		return res, errors.New(res.Err)
	}
	return res, nil
}

// priorCaptures seeds the node captures with what earlier turns of this run
// already produced, so the sink's join render and the digest stay complete
// after a resume. A node that was waiting is cleared: it re-runs from the top,
// and keeping its pre-pause text would duplicate it.
func priorCaptures(rec *RunRecord, resuming bool) map[string]*nodeCapture {
	caps := map[string]*nodeCapture{}
	if !resuming {
		return caps
	}
	for _, n := range rec.Nodes {
		if n.Err != "" || n.Text == "" {
			continue
		}
		c := &nodeCapture{id: n.ID, role: n.Role, kind: n.Kind, seen: true}
		c.text.WriteString(n.Text)
		caps[n.ID] = c
	}
	for _, w := range rec.Waiting {
		if c, ok := caps[w.Node]; ok {
			c.text.Reset()
		}
	}
	return caps
}

// priorNodes indexes a record's node results by node ID.
func priorNodes(rec *RunRecord) map[string]NodeResult {
	out := make(map[string]NodeResult, len(rec.Nodes))
	for _, n := range rec.Nodes {
		out[n.ID] = n
	}
	return out
}

// describeRunError turns the engine's error into something the orchestrator can
// act on: a wall-clock kill and a failed node need different responses.
func describeRunError(err error, timeout time.Duration, budget *runBudget) string {
	if errors.Is(err, context.DeadlineExceeded) {
		msg := fmt.Sprintf("graph hit its %s wall clock and was cancelled part way through", timeout)
		if stuck := budget.inFlight(); len(stuck) > 0 {
			msg += fmt.Sprintf("; still running: %s", strings.Join(stuck, ", "))
		}
		return msg + "; raise budgets.max_wall_seconds or narrow the graph"
	}
	if errors.Is(err, context.Canceled) {
		return "graph was cancelled part way through"
	}
	return fmt.Sprintf("graph failed: %v", err)
}

// eventNodeName maps an event back to the spec node that produced it. The
// scheduler stamps the node name on NodeInfo.Path (Author is the workflow, not
// the node), so path is the reliable half; Author is the fallback for nodes
// whose event never went through the scheduler's stamping.
func eventNodeName(ev *session.Event, spec *Spec) string {
	var path string
	if ev.NodeInfo != nil {
		path = ev.NodeInfo.Path
	}
	for _, n := range spec.Nodes {
		if path == n.ID || strings.HasSuffix(path, "/"+n.ID) {
			return n.ID
		}
	}
	for _, n := range spec.Nodes {
		if ev.Author == n.ID {
			return n.ID
		}
	}
	return ""
}

// renderSinkOutput renders the sink node's result. In a conditionally routed
// graph with multiple terminal sinks, the first sink that produced output is
// rendered. A join sink is rebuilt from its predecessors' captured output: the
// engine's join event carries the map but is not attributable, and the
// reconstruction is deterministic anyway.
func renderSinkOutput(spec *Spec, captures map[string]*nodeCapture) string {
	for _, sink := range spec.Sinks() {
		if out := renderOneSink(spec, sink, captures); out != "" {
			return out
		}
	}
	return ""
}

func renderOneSink(spec *Spec, sink string, captures map[string]*nodeCapture) string {
	c, ok := captures[sink]
	if !ok && spec.kindOf(sink) != KindJoin {
		return ""
	}

	if spec.kindOf(sink) == KindJoin {
		var sb strings.Builder
		for _, n := range spec.Nodes {
			if !precedes(spec, n.ID, sink) {
				continue
			}
			text := ""
			if pc, ok := captures[n.ID]; ok {
				text = strings.TrimSpace(pc.text.String())
				if text == "" {
					if s, ok := pc.out.(string); ok {
						text = strings.TrimSpace(s)
					}
				}
			}
			if text == "" {
				continue
			}
			fmt.Fprintf(&sb, "## %s\n%s\n\n", n.ID, clipRunes(text, maxNodeText))
		}
		return strings.TrimSpace(sb.String())
	}

	if m, ok := c.out.(map[string]any); ok && len(m) > 0 {
		return renderMapOutput(m, captures)
	}
	if text := strings.TrimSpace(c.text.String()); text != "" {
		return clipRunes(text, maxNodeText)
	}
	if s, ok := c.out.(string); ok {
		return clipRunes(strings.TrimSpace(s), maxNodeText)
	}
	return ""
}

// precedes reports whether from is wired straight into to.
func precedes(spec *Spec, from, to string) bool {
	for _, e := range spec.Edges {
		if len(e) >= 2 && e[0] == from && e[1] == to {
			return true
		}
	}
	return false
}

func renderMapOutput(m map[string]any, captures map[string]*nodeCapture) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		val := m[k]
		var text string
		if s, ok := val.(string); ok {
			text = s
		} else if c, ok := captures[k]; ok {
			text = c.text.String()
		} else {
			text = fmt.Sprint(val)
		}
		text = clipRunes(strings.TrimSpace(text), maxNodeText)
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n%s\n\n", k, text)
	}
	return strings.TrimSpace(sb.String())
}

func clipRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n... [truncated]"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// Manager owns the saved graphs and the builder that turns them into runs.
type Manager struct {
	dir      string
	builder  *Builder
	userID   string
	sessions session.Service

	mu        sync.Mutex
	catalog   string
	catalogAt time.Time
}

// NewManager returns a Manager that stores specs under dir.
func NewManager(dir string, cfg BuilderConfig) *Manager {
	return &Manager{
		dir:      dir,
		builder:  NewBuilder(cfg),
		userID:   "graph",
		sessions: session.InMemoryService(),
	}
}

// Dir is where specs live.
func (m *Manager) Dir() string { return m.dir }

// Builder exposes the builder (the tools validate against its registry).
func (m *Manager) Builder() *Builder { return m.builder }

// runDriver is one turn's handle on a run: its durable session, which the turn
// appends to, and the invocation ID the turn ran under.
type runDriver struct {
	svc          *fileSessionService
	invocationID string
}

// events re-reads the run's session history from its durable store.
func (d *runDriver) events(ctx context.Context) ([]*session.Event, error) {
	return d.svc.Events(ctx)
}

// drive runs one turn of the workflow through its own ADK runner.
//
// The runner is what makes a node agent's tool loop work: it appends every
// event to the session, and the LLM flow rebuilds each model request from that
// history. A graph driven with a bare wf.RunNode call has no such history, so a
// node re-calls the same tool forever.
//
// The session is durable — it lives at .golem/graphs/runs/<run-id>/session — and
// is what a resumed turn reads back, so a graph that paused on a human approval
// continues where it stopped instead of starting over.
func (m *Manager) drive(ctx agent.Context, runCtx context.Context, spec *Spec, rec *RunRecord, wf *workflow.Workflow, resume *Decision) (iter.Seq2[*session.Event, error], *runDriver, error) {
	svc, sess, err := openFileSessionService(ctx, m.sessionDir(rec.RunID), graphAppName, m.userID, rec.SessionID)
	if err != nil {
		return nil, nil, err
	}
	drv := &runDriver{svc: svc}

	run := wf.Run
	content := genai.NewContentFromText(rec.Input, genai.RoleUser)
	if resume != nil {
		// The answers go into the session first, so the engine can see the
		// interrupt resolved when it rebuilds the run, and the resumed turn's
		// own content is what the model reads.
		if err := appendResumeAnswers(ctx, svc, sess, rec.Waiting, *resume); err != nil {
			return nil, nil, errors.Wrap(err, "recording the answer")
		}
		run = resumeTurn(wf, rec, *resume)
		content = genai.NewContentFromText(resumeText(rec.Waiting, *resume), genai.RoleUser)
	}

	wfAgent, err := agent.New(agent.Config{
		Name:        spec.WorkflowName(),
		Description: spec.Description,
		Run: func(ictx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			drv.invocationID = ictx.InvocationID()
			return run(ictx)
		},
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "wrapping the workflow as an agent")
	}

	r, err := runner.New(runner.Config{
		AppName:           graphAppName,
		Agent:             wfAgent,
		SessionService:    svc,
		ArtifactService:   artifact.InMemoryService(),
		MemoryService:     memory.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "creating the graph runner")
	}

	seq := r.Run(runCtx, m.userID, rec.SessionID, content, agent.RunConfig{
		StreamingMode: agent.StreamingModeNone,
	})
	return seq, drv, nil
}

// resumeTurn rebuilds the paused run's engine state from the run's own session
// and hands the human's answer to the workflow, which re-runs the parked node
// with that answer as its input.
func resumeTurn(wf *workflow.Workflow, rec *RunRecord, d Decision) func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
	return func(ictx agent.InvocationContext) iter.Seq2[*session.Event, error] {
		return func(yield func(*session.Event, error) bool) {
			// An empty invocation ID means "every event in this session": the
			// session belongs to exactly one run, so nothing else can leak in,
			// and a run that paused more than once still rebuilds whole.
			state, err := wf.ReconstructRunState(ictx.Session(), "")
			if err != nil {
				yield(nil, errors.Wrapf(err, "rebuilding run %s", rec.RunID))
				return
			}
			if state == nil {
				yield(nil, fmt.Errorf("run %s has nothing left to resume", rec.RunID))
				return
			}
			responses := resumeResponses(rec.Waiting, d)
			if len(responses) == 0 {
				yield(nil, fmt.Errorf("run %s is not waiting on any interrupt", rec.RunID))
				return
			}
			// Resume takes an agent.Context (it schedules nodes, which need
			// Actions); the invocation context the runner handed us is the
			// same turn, one wrapper level down.
			for ev, err := range wf.Resume(agent.NewContext(ictx), state, responses) {
				if !yield(ev, err) {
					return
				}
			}
		}
	}
}

// uniqueStrings drops duplicates while keeping first-seen order.
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
