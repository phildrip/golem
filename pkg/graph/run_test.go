package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// stubModels resolves a node's model override to a canned model.
type stubModels map[string]model.LLM

func (s stubModels) GetModel(_ context.Context, requested string) (model.LLM, string, error) {
	m, ok := s[requested]
	if !ok {
		return nil, "", fmt.Errorf("no stub model for %q", requested)
	}
	return m, requested, nil
}

// echoModel replies prefix+last user text, which makes the graph's data flow
// visible in the assertion.
type echoModel struct {
	prefix string
	tokens int32
	delay  time.Duration
}

func (m *echoModel) Name() string { return "echo:" + m.prefix }

func (m *echoModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.delay > 0 {
			time.Sleep(m.delay)
		}
		var last string
		for _, c := range req.Contents {
			if c.Role != genai.RoleUser {
				continue
			}
			for _, p := range c.Parts {
				if p.Text != "" {
					last = p.Text
				}
			}
		}
		resp := &model.LLMResponse{Content: genai.NewContentFromText(m.prefix+last, genai.RoleModel)}
		if m.tokens > 0 {
			resp.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{TotalTokenCount: m.tokens}
		}
		yield(resp, nil)
	}
}

// graphCallModel calls a tool once with args, then answers.
type graphCallModel struct {
	tool string
	args map[string]any
}

func (m *graphCallModel) Name() string { return "graph-caller" }

func (m *graphCallModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					yield(&model.LLMResponse{Content: genai.NewContentFromText("done", genai.RoleModel)}, nil)
					return
				}
			}
		}
		yield(&model.LLMResponse{Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID:   "call-1",
				Name: m.tool,
				Args: m.args,
			}}},
		}}, nil)
	}
}

// invokeGraphRun drives graph_run through a real ADK runner, which is the only
// public way to obtain an agent.Context (and therefore the production path).
func invokeGraphRun(t *testing.T, m *Manager, args map[string]any) string {
	t.Helper()
	return invokeTool(t, m, "graph_run", args)
}

// invokeTool drives one graph_* tool through a real ADK runner, which is the
// only public way to obtain an agent.Context (and therefore the production
// path: schema validation included).
func invokeTool(t *testing.T, m *Manager, name string, args map[string]any) string {
	t.Helper()
	tools, err := NewTools(m)
	if err != nil {
		t.Fatalf("NewTools: %v", err)
	}
	parent, err := llmagent.New(llmagent.Config{
		Name:  "parent",
		Model: &graphCallModel{tool: name, args: args},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	r, err := runner.NewInMemory("graph-test", parent)
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	var out strings.Builder
	for ev, err := range r.Run(context.Background(), "user", "s1", genai.NewContentFromText("go", genai.RoleUser), agent.RunConfig{StreamingMode: agent.StreamingModeNone}) {
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionResponse != nil {
				if m, ok := p.FunctionResponse.Response["output"].(string); ok {
					out.WriteString(m)
				} else {
					out.WriteString(fmt.Sprint(p.FunctionResponse.Response))
				}
			}
		}
	}
	return out.String()
}

func fanOutSpec() *Spec {
	return &Spec{
		V:              1,
		Name:           "spike",
		MaxConcurrency: 2,
		Budgets:        &Budgets{MaxNodes: 8, MaxWallSeconds: 60, MaxTokens: 100_000},
		Nodes: []Node{
			{ID: "a", Kind: KindAgent, Role: RoleResearcher, Tools: ToolsNone, Instruction: "look at angle a", Model: "m_a"},
			{ID: "b", Kind: KindAgent, Role: RoleResearcher, Tools: ToolsNone, Instruction: "look at angle b", Model: "m_b"},
			{ID: "merge", Kind: KindJoin},
			{ID: "sink", Kind: KindAgent, Role: RoleGeneral, Tools: ToolsNone, Instruction: "merge", Model: "m_sink"},
		},
		Edges: [][]string{
			{"START", "a"},
			{"START", "b"},
			{"a", "merge"},
			{"b", "merge"},
			{"merge", "sink"},
		},
	}
}

func testManager(t *testing.T, tokens int32) (*Manager, *[]Event) {
	t.Helper()
	models := stubModels{
		"m_a":    &echoModel{prefix: "A:", tokens: tokens},
		"m_b":    &echoModel{prefix: "B:", tokens: tokens},
		"m_sink": &echoModel{prefix: "S:", tokens: tokens},
	}
	var events []Event
	m := NewManager(t.TempDir(), BuilderConfig{Models: models})
	m.builder.cfg.OnEvent = func(ev Event) { events = append(events, ev) }
	return m, &events
}

// Every node gets its own context, and the fan-in's map reaches the sink whole.
func TestRunFanOutThenJoin(t *testing.T) {
	m, events := testManager(t, 1200)
	out := invokeGraphRun(t, m, map[string]any{"spec": fanOutSpec(), "input": "task"})

	for _, want := range []string{"A:task", "B:task", "S:", "Node digest:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("result missing %q:\n%s", want, out)
		}
	}
	var starts, dones int
	for _, ev := range *events {
		switch ev.Kind {
		case EventStart:
			starts++
		case EventDone:
			dones++
		}
	}
	// Three agent nodes report progress; the join is a barrier, not an agent.
	if starts != 3 || dones != 3 {
		t.Fatalf("progress events: %d starts, %d dones, want 3/3", starts, dones)
	}
	t.Logf("sink received: %s", firstLine(out))
}

// The budget is shared across every node: a fan-out cannot multiply it.
func TestRunStopsAtTokenBudget(t *testing.T) {
	m, _ := testManager(t, 9_000)
	spec := fanOutSpec()
	spec.Budgets.MaxTokens = 10_000
	out := invokeGraphRun(t, m, map[string]any{"spec": spec, "input": "task"})
	if !strings.Contains(out, "token budget exhausted") {
		t.Fatalf("expected the token ceiling to stop the run:\n%s", out)
	}
}

func TestRunReportsValidationProblems(t *testing.T) {
	m, _ := testManager(t, 0)
	spec := fanOutSpec()
	spec.Nodes[0].Role = "reviewer"
	out := invokeGraphRun(t, m, map[string]any{"spec": spec, "input": "task"})
	if !strings.Contains(out, `role "reviewer" is not supported`) {
		t.Fatalf("expected the validator's message:\n%s", out)
	}
}

func TestBudgetModelHonoursWallClock(t *testing.T) {
	budget := newRunBudget(&Spec{Budgets: &Budgets{MaxTokens: 100_000}}, nil)
	budget.setDeadline(time.Now().Add(-time.Millisecond))
	m := &budgetModel{inner: &echoModel{prefix: "x"}, budget: budget, node: "n"}
	for _, err := range m.GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err == nil {
			t.Fatal("expected the expired wall clock to fail the call")
		}
		return
	}
	t.Fatal("model produced no response")
}

func TestRenderResultCarriesDigest(t *testing.T) {
	res := &Result{
		Graph: "g", Output: "the answer", Tokens: 1234, Duration: 2 * time.Second,
		Nodes: []NodeResult{{ID: "a", Role: RoleCritic, Kind: KindAgent, Text: "first line\nsecond", Tokens: 900, Duration: time.Second}},
	}
	text := renderResult(res)
	for _, want := range []string{"<graph_result", "the answer", "Node digest:", "critic", "900 tok"} {
		if !strings.Contains(text, want) {
			t.Fatalf("digest missing %q:\n%s", want, text)
		}
	}
}

var _ = tool.Tool(nil)

// A join may be the sink: its result is the labelled merge of its predecessors.
func TestJoinAsSinkRendersSections(t *testing.T) {
	m, _ := testManager(t, 100)
	spec := fanOutSpec()
	spec.Nodes = spec.Nodes[:3] // a, b, merge
	spec.Edges = spec.Edges[:4]
	spec.MaxConcurrency = 2
	out := invokeGraphRun(t, m, map[string]any{"spec": spec, "input": "task"})
	for _, want := range []string{"## a", "## b", "A:task", "B:task"} {
		if !strings.Contains(out, want) {
			t.Fatalf("join sink missing %q:\n%s", want, out)
		}
	}
}

// A tool node runs with the arguments the spec pins, ignoring its input.
func TestToolNodeUsesPinnedArgs(t *testing.T) {
	models := stubModels{"m": &echoModel{prefix: "S:"}}
	m := NewManager(t.TempDir(), BuilderConfig{
		Models:        models,
		ReadOnlyTools: []tool.Tool{newFakeProbe(t)},
	})
	spec := &Spec{
		V: 1, Name: "pinned", MaxConcurrency: 1,
		Budgets: &Budgets{MaxNodes: 4, MaxWallSeconds: 60, MaxTokens: 50_000},
		Nodes: []Node{
			{ID: "probe", Kind: KindTool, Tool: "fake_probe", Args: map[string]any{"what": "pinned"}},
			{ID: "sink", Kind: KindAgent, Role: RoleGeneral, Tools: ToolsNone, Instruction: "report", Model: "m"},
		},
		Edges: [][]string{{"START", "probe"}, {"probe", "sink"}},
	}
	out := invokeGraphRun(t, m, map[string]any{"spec": spec, "input": "ignored"})
	if !strings.Contains(out, "probe saw pinned") {
		t.Fatalf("tool node did not get its pinned args:\n%s", out)
	}
}

// newFakeProbe is a real functiontool (the ADK's llm flow requires the
// processor methods a functiontool carries) that reports its argument.
func newFakeProbe(t *testing.T) tool.Tool {
	t.Helper()
	tl, err := functiontool.New(functiontool.Config{Name: "fake_probe", Description: "reports its argument"},
		func(_ agent.Context, args map[string]any) (map[string]any, error) {
			return map[string]any{"result": "probe saw " + fmt.Sprint(args["what"])}, nil
		})
	if err != nil {
		t.Fatalf("functiontool.New: %v", err)
	}
	return tl
}

// fakeEchoTool reports the argument it was called with.
type fakeEchoTool struct{}

func (fakeEchoTool) Name() string        { return "fake_probe" }
func (fakeEchoTool) Description() string { return "reports its argument" }
func (fakeEchoTool) IsLongRunning() bool { return false }
func (fakeEchoTool) Run(_ agent.Context, args any) (map[string]any, error) {
	m, _ := args.(map[string]any)
	return map[string]any{"result": "probe saw " + fmt.Sprint(m["what"])}, nil
}

// The typed tool schema has to accept what a model actually sends: a spec
// re-encoded as plain JSON (message-shaped maps, no Go types).
func TestToolSchemaAcceptsWireArgs(t *testing.T) {
	m, _ := testManager(t, 0)
	spec := fanOutSpec()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal spec: %v", err)
	}
	out := invokeTool(t, m, "graph_validate", map[string]any{"spec": wire})
	if !strings.Contains(out, "ok: \"spike\"") {
		t.Fatalf("schema round trip rejected a valid spec:\n%s", out)
	}

	// A bad value must reach the validator, not be swallowed by schema
	// validation before our actionable message can be produced.
	wire["nodes"].([]any)[0].(map[string]any)["role"] = "reviewer"
	out = invokeTool(t, m, "graph_validate", map[string]any{"spec": wire})
	if !strings.Contains(out, `role "reviewer" is not supported`) {
		t.Fatalf("expected the validator's message, got:\n%s", out)
	}
}

// toolLoopModel calls one tool, then reports what it saw. If a node agent
// cannot see its own tool result, the model gets the same conversation again
// and calls the tool forever — which is exactly what a live run did before the
// graph drove its nodes through a runner.
type toolLoopModel struct {
	tool  string
	max   int
	calls int
}

func (m *toolLoopModel) Name() string { return "tool-loop" }

func (m *toolLoopModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					yield(&model.LLMResponse{Content: genai.NewContentFromText("saw: "+fmt.Sprint(p.FunctionResponse.Response), genai.RoleModel)}, nil)
					return
				}
			}
		}
		if m.calls > m.max {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("LOOPED", genai.RoleModel)}, nil)
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID:   fmt.Sprintf("call-%d", m.calls),
				Name: m.tool,
				Args: map[string]any{"what": "first"},
			}}},
		}}, nil)
	}
}

func TestNodeAgentSeesItsOwnToolResults(t *testing.T) {
	loop := &toolLoopModel{tool: "fake_probe", max: 3}
	m := NewManager(t.TempDir(), BuilderConfig{
		Models:        stubModels{"m_loop": loop},
		ReadOnlyTools: []tool.Tool{newFakeProbe(t)},
	})
	spec := &Spec{
		V: 1, Name: "loop", MaxConcurrency: 1,
		Budgets: &Budgets{MaxNodes: 3, MaxWallSeconds: 60, MaxTokens: 50_000},
		Nodes: []Node{
			{ID: "probe", Kind: KindAgent, Role: RoleResearcher, Tools: ToolsRead, Instruction: "probe once", Model: "m_loop"},
		},
		Edges: [][]string{{"START", "probe"}},
	}
	out := invokeGraphRun(t, m, map[string]any{"spec": spec, "input": "go"})
	if strings.Contains(out, "LOOPED") {
		t.Fatalf("the node never saw its own tool result; it looped %d model calls", loop.calls)
	}
	if !strings.Contains(out, "probe saw first") {
		t.Fatalf("tool result did not reach the model:\n%s", out)
	}
	if loop.calls != 2 {
		t.Fatalf("model called %d times, want 2 (one tool call, one answer)", loop.calls)
	}
}

// A wall-clock kill must say which nodes were still running: that is the
// difference between "narrow the graph" and "guess".
func TestWallClockErrorNamesTheStuckNodes(t *testing.T) {
	budget := newRunBudget(&Spec{Budgets: &Budgets{MaxTokens: 100_000}}, nil)
	budget.startNode("g", Node{ID: "draft", Role: RoleGeneral}, "m")
	budget.startNode("g", Node{ID: "critic", Role: RoleCritic}, "m")
	budget.finishNode("g", Node{ID: "draft", Role: RoleGeneral}, "m")

	msg := describeRunError(context.DeadlineExceeded, 30*time.Second, budget)
	if !strings.Contains(msg, "still running: critic") {
		t.Fatalf("in-flight nodes not reported: %s", msg)
	}
	if strings.Contains(msg, "draft") {
		t.Fatalf("a finished node was reported as stuck: %s", msg)
	}
}

// A node that needs a human is a dead end inside a graph: nobody is watching,
// and the engine's pause is otherwise silent (the iterator just ends).
func TestPausedNodeIsReportedAsUnattended(t *testing.T) {
	confirming, err := functiontool.New(functiontool.Config{Name: "needs_ok", Description: "asks a human"},
		func(ctx agent.Context, args map[string]any) (map[string]any, error) {
			if err := ctx.RequestConfirmation("approve?", args); err != nil {
				return nil, err
			}
			return map[string]any{"result": "approved"}, nil
		})
	if err != nil {
		t.Fatalf("functiontool.New: %v", err)
	}
	loop := &toolLoopModel{tool: "needs_ok", max: 3}
	m := NewManager(t.TempDir(), BuilderConfig{
		Models:        stubModels{"m_loop": loop},
		ReadOnlyTools: []tool.Tool{confirming},
	})
	spec := &Spec{
		V: 1, Name: "gated", MaxConcurrency: 1,
		Budgets: &Budgets{MaxNodes: 3, MaxWallSeconds: 60, MaxTokens: 50_000},
		Nodes: []Node{
			{ID: "gate", Kind: KindAgent, Role: RoleCoder, Tools: ToolsRead, Instruction: "run the gated tool", Model: "m_loop"},
		},
		Edges: [][]string{{"START", "gate"}},
	}
	out := invokeGraphRun(t, m, map[string]any{"spec": spec, "input": "go"})
	if !strings.Contains(out, "paused waiting for a human") {
		t.Fatalf("a paused node was not reported as unattended:\n%s", out)
	}
}
