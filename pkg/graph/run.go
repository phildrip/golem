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
	"google.golang.org/genai"
)

// Result is one run's outcome. It is what the orchestrator's tool call returns:
// the sink's text plus a per-node digest, so the model learns both the answer
// and what the answer cost.
type Result struct {
	Graph    string        `json:"graph"`
	Output   string        `json:"output"`
	Nodes    []NodeResult  `json:"nodes"`
	Tokens   int64         `json:"tokens"`
	Duration time.Duration `json:"duration"`
	Err      string        `json:"error,omitempty"`
}

// NodeResult is one node's contribution.
type NodeResult struct {
	ID       string        `json:"id"`
	Role     string        `json:"role,omitempty"`
	Kind     string        `json:"kind"`
	Text     string        `json:"text,omitempty"`
	Tokens   int64         `json:"tokens,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
	Err      string        `json:"error,omitempty"`
}

// nodeCapture accumulates one node's events.
type nodeCapture struct {
	id   string
	role string
	kind string
	text strings.Builder
	out  any
	seen bool
}

// maxNodeText bounds what one node may contribute to the tool result: a node
// that dumps 50k characters would defeat the point of the graph, which is to
// keep the orchestrator's context small.
const maxNodeText = 8000

// Run executes spec with input as the seed handed to every START successor.
//
// Errors are returned alongside a partially filled Result: a fan-out where one
// branch died still tells the orchestrator what the other branches learned.
func (m *Manager) Run(ctx agent.Context, spec *Spec, input string) (*Result, error) {
	if err := spec.Validate(ValidateOptions{KnownTool: func(name string) bool { return m.builder.ToolNames()[name] }}); err != nil {
		return nil, err
	}

	wf, budget, err := m.builder.Build(ctx, spec)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(spec.Budgets.MaxWallSeconds) * time.Second
	budget.setDeadline(time.Now().Add(timeout))

	start := time.Now()
	res := &Result{Graph: spec.Name}
	captures := map[string]*nodeCapture{}
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
	// tool until the wall clock kills the run.
	events, cleanup, err := m.drive(ctx, spec, wf, input)
	if err != nil {
		res.Duration = time.Since(start)
		res.Err = fmt.Sprintf("graph failed to start: %v", err)
		return res, errors.New(res.Err)
	}
	defer cleanup()

	var runErr error
	var paused []string
	for ev, err := range events {
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
		// A long-running tool call is the engine's pause signal: the node is
		// parked in NodeWaiting and the iterator simply ends. Graphs run
		// unattended, so there is nobody to answer it.
		if len(ev.LongRunningToolIDs) > 0 {
			paused = append(paused, author)
		}
		c := capture(author)
		c.seen = true
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

	// Report in spec order, so the digest reads like the graph rather than
	// like whichever node happened to finish first.
	sink := spec.Sink()
	for _, n := range spec.Nodes {
		c := captures[n.ID]
		nr := NodeResult{ID: n.ID, Role: n.Role, Kind: n.Kind}
		switch {
		case c != nil && c.seen:
			nr.Text = clipRunes(c.text.String(), maxNodeText)
			nr.Tokens = budget.nodeTokens(n.ID)
			nr.Duration = budget.elapsed(n.ID)
		case n.Kind == KindJoin:
			// A join is a barrier: it merges, it does not produce text of its
			// own, so an absent event is normal.
			nr.Tokens = budget.nodeTokens(n.ID)
		default:
			nr.Err = "did not run"
		}
		res.Nodes = append(res.Nodes, nr)
	}

	res.Output = renderSinkOutput(spec, captures)
	if runErr != nil {
		res.Err = describeRunError(runErr, timeout, budget)
		return res, errors.New(res.Err)
	}
	if len(paused) > 0 {
		res.Err = fmt.Sprintf("node(s) %s paused waiting for a human to approve a tool call; "+
			"a graph runs unattended, so no one can answer it — give the node read-only work and do the gated "+
			"call in the main turn, or add the tool to sandbox.mcp_allow", strings.Join(uniqueStrings(paused), ", "))
		return res, errors.New(res.Err)
	}
	if res.Output == "" {
		res.Err = fmt.Sprintf("graph finished but sink %q produced no output", sink)
		return res, errors.New(res.Err)
	}
	return res, nil
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

// renderSinkOutput renders the sink node's result. A join sink is rebuilt from
// its predecessors' captured output: the engine's join event carries the map
// but is not attributable, and the reconstruction is deterministic anyway.
func renderSinkOutput(spec *Spec, captures map[string]*nodeCapture) string {
	sink := spec.Sink()
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

// drive runs the workflow through its own ADK runner.
//
// The runner is what makes a node agent's tool loop work: it appends every
// event to the session, and the LLM flow rebuilds each model request from that
// history. A graph driven with a bare wf.RunNode call has no such history, so a
// node re-calls the same tool forever.
//
// The session is deleted when the run finishes. Nothing needs it afterwards
// today; when pause/resume lands it becomes the durable record instead.
func (m *Manager) drive(ctx agent.Context, spec *Spec, wf interface {
	Run(agent.InvocationContext) iter.Seq2[*session.Event, error]
}, input string) (iter.Seq2[*session.Event, error], func(), error) {
	wfAgent, err := agent.New(agent.Config{
		Name:        spec.WorkflowName(),
		Description: spec.Description,
		Run:         wf.Run,
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "wrapping the workflow as an agent")
	}

	r, err := runner.New(runner.Config{
		AppName:           "golem-graph",
		Agent:             wfAgent,
		SessionService:    m.sessions,
		ArtifactService:   artifact.InMemoryService(),
		MemoryService:     memory.InMemoryService(),
		AutoCreateSession: true,
	})
	if err != nil {
		return nil, nil, errors.Wrap(err, "creating the graph runner")
	}

	sessionID := fmt.Sprintf("graph-%s-%d", spec.Name, time.Now().UnixNano())
	cleanup := func() {
		_ = m.sessions.Delete(context.Background(), &session.DeleteRequest{
			AppName:   "golem-graph",
			UserID:    m.userID,
			SessionID: sessionID,
		})
	}
	seq := r.Run(ctx, m.userID, sessionID, genai.NewContentFromText(input, genai.RoleUser), agent.RunConfig{
		StreamingMode: agent.StreamingModeNone,
	})
	return seq, cleanup, nil
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
