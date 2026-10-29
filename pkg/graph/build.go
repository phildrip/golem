package graph

import (
	"context"
	"fmt"
	"iter"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/tools"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// ModelSource resolves a model name to an ADK model. An empty name means "the
// session's model", which keeps a graph on the user's chosen model unless a
// node asks for something else (a cheap model for critics, a strong one for
// the synthesis).
type ModelSource interface {
	GetModel(ctx context.Context, requested string) (model.LLM, string, error)
}

// BuilderConfig is the environment a spec is built against.
//
// The tool slices are split the same way the subagent runner splits them, and
// for the same reason: a delegate has nobody to ask, so write tools run with
// the policy in unattended mode and a call the policy would confirm fails.
type BuilderConfig struct {
	Models        ModelSource
	StaticModel   model.LLM
	DefaultModel  string
	ReadOnlyTools []tool.Tool
	WriteTools    []tool.Tool
	Toolsets      []tool.Toolset
	MemoryContext func() string
	OnEvent       func(Event)
}

// Builder turns a validated spec into a runnable workflow.
type Builder struct {
	cfg BuilderConfig
}

// NewBuilder returns a Builder for cfg.
func NewBuilder(cfg BuilderConfig) *Builder { return &Builder{cfg: cfg} }

// ToolNames reports the tools a tool node may name.
func (b *Builder) ToolNames() map[string]bool {
	names := make(map[string]bool, len(b.cfg.ReadOnlyTools)+len(b.cfg.WriteTools))
	for _, t := range b.cfg.ReadOnlyTools {
		if t != nil {
			names[t.Name()] = true
		}
	}
	for _, t := range b.cfg.WriteTools {
		if t != nil {
			names[t.Name()] = true
		}
	}
	return names
}

// classTools returns the tools an agent node of the given class receives.
func (b *Builder) classTools(class string) []tool.Tool {
	switch class {
	case ToolsNone:
		return nil
	case ToolsWrite:
		out := make([]tool.Tool, 0, len(b.cfg.ReadOnlyTools)+len(b.cfg.WriteTools))
		out = append(out, b.cfg.ReadOnlyTools...)
		out = append(out, b.cfg.WriteTools...)
		return out
	default:
		return b.cfg.ReadOnlyTools
	}
}

// Build validates spec, then constructs the engine objects. The spec is the
// only durable artefact: everything here is thrown away after the run.
func (b *Builder) Build(ctx context.Context, spec *Spec) (*workflow.Workflow, *runBudget, error) {
	if err := spec.Validate(ValidateOptions{KnownTool: func(name string) bool { return b.ToolNames()[name] }}); err != nil {
		return nil, nil, err
	}

	budget := newRunBudget(spec, b.cfg.OnEvent)
	nodes := make(map[string]workflow.Node, len(spec.Nodes))
	registry := b.ToolNames()

	for _, n := range spec.Nodes {
		switch n.Kind {
		case KindAgent:
			node, err := b.buildAgentNode(ctx, spec, n, budget)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "node %q", n.ID)
			}
			nodes[n.ID] = node
		case KindTool:
			if !registry[n.Tool] {
				return nil, nil, fmt.Errorf("node %q: tool %q is not registered", n.ID, n.Tool)
			}
			t := b.lookupTool(n.Tool)
			wrapped := fixedArgsTool{
				Tool:   t,
				args:   n.Args,
				graph:  spec.Name,
				node:   n.ID,
				budget: budget,
			}
			node, err := workflow.NewNamedToolNode(n.ID, wrapped, workflow.NodeConfig{})
			if err != nil {
				return nil, nil, errors.Wrapf(err, "node %q", n.ID)
			}
			nodes[n.ID] = node
		case KindJoin:
			nodes[n.ID] = workflow.NewJoinNode(n.ID)
		}
	}

	eb := workflow.NewEdgeBuilder()
	for _, e := range spec.Edges {
		from := workflow.Start
		if e[0] != Start {
			from = nodes[e[0]]
		}
		to := nodes[e[1]]
		if len(e) == 3 {
			eb.AddRoute(from, to, workflow.StringRoute(e[2]))
		} else {
			eb.Add(from, to)
		}
	}

	wf, err := workflow.New(spec.WorkflowName(), eb.Build(), workflow.WithMaxConcurrency(spec.MaxConcurrency))
	if err != nil {
		return nil, nil, errors.Wrap(err, "building workflow")
	}
	return wf, budget, nil
}

// lookupTool finds a tool by name across the read-only and write sets.
func (b *Builder) lookupTool(name string) tool.Tool {
	for _, t := range b.cfg.ReadOnlyTools {
		if t != nil && t.Name() == name {
			return t
		}
	}
	for _, t := range b.cfg.WriteTools {
		if t != nil && t.Name() == name {
			return t
		}
	}
	return nil
}

func (b *Builder) buildAgentNode(ctx context.Context, spec *Spec, n Node, budget *runBudget) (workflow.Node, error) {
	requested := n.Model
	if requested == "" {
		requested = b.cfg.DefaultModel
	}
	var m model.LLM
	resolved := ""
	if b.cfg.Models != nil {
		var err error
		m, resolved, err = b.cfg.Models.GetModel(ctx, requested)
		if err != nil {
			return nil, errors.Wrapf(err, "resolving model %q", requested)
		}
	} else {
		m = b.cfg.StaticModel
		resolved = "static"
	}
	if m == nil {
		return nil, fmt.Errorf("no model available for node %q", n.ID)
	}
	m = &budgetModel{inner: m, budget: budget, node: n.ID}

	memory := ""
	if b.cfg.MemoryContext != nil {
		memory = b.cfg.MemoryContext()
	}
	prompt := nodePrompt(n, memory)

	description := firstLine(n.Instruction)
	if description == "" {
		description = n.Role + " node"
	}

	agentCfg := llmagent.Config{
		Name:        n.ID,
		Description: description,
		Model:       m,
		Tools:       b.classTools(n.Tools),
		Toolsets:    b.cfg.Toolsets,
		InstructionProvider: func(agent.ReadonlyContext) (string, error) {
			return prompt, nil
		},
		BeforeAgentCallbacks: []agent.BeforeAgentCallback{
			func(agent.Context) (*genai.Content, error) {
				budget.startNode(spec.Name, n, resolved)
				return nil, nil
			},
		},
		AfterAgentCallbacks: []agent.AfterAgentCallback{
			func(agent.Context) (*genai.Content, error) {
				budget.finishNode(spec.Name, n, resolved)
				return nil, nil
			},
		},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{
			// Models write numbers as strings; a node losing a call to schema
			// validation would burn its whole budget on retries.
			func(_ agent.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				tools.RepairArgs(t, args)
				return nil, nil
			},
		},
	}

	adkAgent, err := llmagent.New(agentCfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating node agent")
	}
	return workflow.NewAgentNode(adkAgent, workflow.NodeConfig{})
}

// firstLine returns the first non-empty line, clipped, for a node description.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 80 {
			line = line[:80] + "..."
		}
		return line
	}
	return ""
}

// fixedArgsTool runs a tool node with the arguments the spec pins, ignoring the
// upstream value: a tool node is a deterministic step ("read this file"), not a
// function of its predecessor.
type fixedArgsTool struct {
	tool.Tool
	args   map[string]any
	graph  string
	node   string
	budget *runBudget
}

func (f fixedArgsTool) Run(ctx agent.Context, _ any) (map[string]any, error) {
	f.budget.startTool(f.graph, f.node)
	runnable, ok := f.Tool.(interface {
		Run(agent.Context, any) (map[string]any, error)
	})
	if !ok {
		err := fmt.Errorf("tool %q cannot run as a graph node", f.Tool.Name())
		f.budget.finishTool(f.graph, f.node, err)
		return nil, err
	}
	out, err := runnable.Run(ctx, f.args)
	f.budget.finishTool(f.graph, f.node, err)
	return out, err
}

// runBudget enforces the spec's ceilings and reports progress. One budget is
// shared by every node in a run, so a fan-out cannot multiply the ceiling.
type runBudget struct {
	spec  *Spec
	limit int64
	spent atomic.Int64

	onEvent func(Event)

	// emitMu serialises progress callbacks: nodes run concurrently, and a
	// transport rendering them should not have to be goroutine-safe itself.
	emitMu sync.Mutex

	mu        sync.Mutex
	started   map[string]time.Time
	finished  map[string]bool
	nodeSpent map[string]int64
	deadline  time.Time
}

func newRunBudget(spec *Spec, onEvent func(Event)) *runBudget {
	return &runBudget{
		spec:      spec,
		limit:     int64(spec.Budgets.MaxTokens),
		onEvent:   onEvent,
		started:   map[string]time.Time{},
		finished:  map[string]bool{},
		nodeSpent: map[string]int64{},
	}
}

func (b *runBudget) Tokens() int64 { return b.spent.Load() }

func (b *runBudget) add(node string, n int) {
	b.spent.Add(int64(n))
	if node == "" || n == 0 {
		return
	}
	b.mu.Lock()
	b.nodeSpent[node] += int64(n)
	b.mu.Unlock()
}

// nodeTokens reports what one node spent, for the per-node digest.
func (b *runBudget) nodeTokens(node string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nodeSpent[node]
}

// setDeadline arms the wall-clock ceiling. The engine has no per-run deadline
// of its own, so the outermost check lives on the model call: a hung node is
// almost always a hung provider request.
func (b *runBudget) setDeadline(t time.Time) { b.deadline = t }

func (b *runBudget) expired() bool {
	if b.deadline.IsZero() {
		return false
	}
	return time.Now().After(b.deadline)
}

func (b *runBudget) exhausted() bool { return b.limit > 0 && b.spent.Load() >= b.limit }

func (b *runBudget) emit(ev Event) {
	if b.onEvent == nil {
		return
	}
	b.emitMu.Lock()
	defer b.emitMu.Unlock()
	b.onEvent(ev)
}

func (b *runBudget) startNode(graph string, n Node, model string) {
	b.mu.Lock()
	b.started[n.ID] = time.Now()
	b.mu.Unlock()
	b.emit(Event{Graph: graph, Node: n.ID, Role: n.Role, Model: model, Kind: EventStart, Prompt: firstLine(n.Instruction)})
}

func (b *runBudget) finishNode(graph string, n Node, model string) {
	b.mu.Lock()
	b.finished[n.ID] = true
	b.mu.Unlock()
	b.emit(Event{Graph: graph, Node: n.ID, Role: n.Role, Model: model, Kind: EventDone, Duration: b.elapsed(n.ID)})
}

func (b *runBudget) startTool(graph, node string) {
	b.mu.Lock()
	b.started[node] = time.Now()
	b.mu.Unlock()
	b.emit(Event{Graph: graph, Node: node, Kind: EventStart})
}

func (b *runBudget) finishTool(graph, node string, err error) {
	b.mu.Lock()
	b.finished[node] = true
	b.mu.Unlock()
	kind := EventDone
	errText := ""
	if err != nil {
		kind = EventError
		errText = err.Error()
	}
	b.emit(Event{Graph: graph, Node: node, Kind: kind, Err: errText, Duration: b.elapsed(node)})
}

// inFlight reports the nodes that started and never finished. On a wall-clock
// kill this is the answer to "what was it doing", which the orchestrator needs
// in order to narrow the graph instead of guessing.
func (b *runBudget) inFlight() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for id := range b.started {
		if !b.finished[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (b *runBudget) elapsed(node string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.started[node]
	if !ok {
		return 0
	}
	return time.Since(t)
}

// budgetModel stops a run at the token ceiling instead of at the provider's
// quota error. The check happens before the call as well: once the budget is
// gone, further work cannot help.
type budgetModel struct {
	inner  model.LLM
	budget *runBudget
	node   string
}

func (m *budgetModel) Name() string { return m.inner.Name() }

func (m *budgetModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.budget.expired() {
			yield(nil, context.DeadlineExceeded)
			return
		}
		if m.budget.exhausted() {
			yield(nil, fmt.Errorf("graph token budget exhausted (%d of %d tokens spent) at node %q",
				m.budget.Tokens(), m.budget.limit, m.node))
			return
		}
		for resp, err := range m.inner.GenerateContent(ctx, req, stream) {
			if resp != nil && resp.UsageMetadata != nil {
				m.budget.add(m.node, int(resp.UsageMetadata.TotalTokenCount))
			}
			if !yield(resp, err) {
				return
			}
			if err != nil {
				return
			}
		}
	}
}
