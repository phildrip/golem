package graph

import (
	"encoding/json"
	"fmt"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/adk/v2/tool/toolutils"
	"google.golang.org/genai"
)

// CallKey identifies one concrete tool call — the tool's name plus its
// arguments, canonically encoded. It is what a human approves: not "writes are
// allowed", but "this write, of this file, with this content".
func CallKey(name string, args map[string]any) string {
	if len(args) == 0 {
		return name
	}
	// encoding/json sorts map keys, so the same call always yields the same key.
	data, err := json.Marshal(args)
	if err != nil {
		return name
	}
	return name + "\x00" + string(data)
}

// Decisions maps a CallKey to the human's answer for it. A key that is absent
// has not been decided, so the tool's own gate still asks.
type Decisions map[string]bool

// ApprovalFunc answers whether a call has been decided and, if so, whether it
// may run. It is nil on a fresh run: nothing is decided, every gated call asks.
type ApprovalFunc func(tool string, args map[string]any) (allowed, decided bool)

// decide resolves one call against d.
func (d Decisions) decide(name string, args map[string]any) (bool, bool) {
	if len(d) == 0 {
		return false, false
	}
	allowed, ok := d[CallKey(name, args)]
	return allowed, ok
}

// withApprovals returns a copy of b whose tools consult d. The Manager's own
// builder is never mutated: a run is one turn, and two runs may be in flight.
func (b *Builder) withApprovals(d Decisions) *Builder {
	if len(d) == 0 {
		return b
	}
	cfg := b.cfg
	cfg.Approvals = d.decide
	return &Builder{cfg: cfg}
}

// approvalTool wraps a tool so a call a human already decided neither asks
// again nor slips through unasked.
//
// Graph nodes run unattended, so a call the sandbox policy wants a human for
// ends the turn: the node parks and the run waits. On resume the answer is in
// Decisions, and this wrapper is what makes it effective — an approved call is
// re-run under a confirmed tool context (the gate reads ToolConfirmation and
// would otherwise ask a second time), a refused one returns an error the model
// can read.
type approvalTool struct {
	inner  tool.Tool
	decide ApprovalFunc
}

// wrapTools wraps every tool in tools with the approval gate.
func wrapTools(tools []tool.Tool, decide ApprovalFunc) []tool.Tool {
	if decide == nil || len(tools) == 0 {
		return tools
	}
	out := make([]tool.Tool, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		out = append(out, &approvalTool{inner: t, decide: decide})
	}
	return out
}

func (t *approvalTool) Name() string        { return t.inner.Name() }
func (t *approvalTool) Description() string { return t.inner.Description() }
func (t *approvalTool) IsLongRunning() bool { return t.inner.IsLongRunning() }

// Declaration forwards the schema the model sees. The ADK reaches these through
// structural interfaces on the tool, so a wrapper that forgets one silently
// drops the tool from the request.
func (t *approvalTool) Declaration() *genai.FunctionDeclaration {
	if d, ok := t.inner.(interface {
		Declaration() *genai.FunctionDeclaration
	}); ok {
		return d.Declaration()
	}
	return nil
}

// ProcessRequest packs the wrapper itself into the request, not the tool it
// wraps: the flow dispatches a model's call to whatever object is registered
// under the tool's name, so registering the inner tool would bypass the gate.
func (t *approvalTool) ProcessRequest(_ agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, t)
}

func (t *approvalTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	name := t.inner.Name()
	if m, ok := argsAsMap(args); ok && t.decide != nil {
		if allowed, decided := t.decide(name, m); decided {
			if !allowed {
				return nil, fmt.Errorf("%s: the human refused this call for this run; "+
					"do not retry it unchanged, and report what you could not do", name)
			}
			// Re-run under a confirmed context: the tool's own approval gate
			// reads ToolConfirmation and would otherwise ask again, and the
			// run would pause on a decision the human already made.
			ctx = agent.NewToolContext(ctx, ctx.FunctionCallID(), ctx.Actions(),
				&toolconfirmation.ToolConfirmation{
					Confirmed: true,
					Hint:      "approved by the human for this graph run",
				})
		}
	}
	runner, ok := t.inner.(interface {
		Run(agent.Context, any) (map[string]any, error)
	})
	if !ok {
		return nil, fmt.Errorf("tool %q cannot be run as a graph node tool", name)
	}
	return runner.Run(ctx, args)
}

// argsAsMap normalises the argument shapes the flow may hand a tool.
func argsAsMap(args any) (map[string]any, bool) {
	switch v := args.(type) {
	case nil:
		return map[string]any{}, true
	case map[string]any:
		return v, true
	case *map[string]any:
		if v == nil {
			return map[string]any{}, true
		}
		return *v, true
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, false
		}
		return m, true
	}
}
