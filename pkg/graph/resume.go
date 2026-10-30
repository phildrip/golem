package graph

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"
)

// graphAppName namespaces every graph run's ADK session. The sessions
// themselves are per-run directories on disk, so this is only a label.
const graphAppName = "golem-graph"

// The kinds of pause a run can be waiting on.
const (
	InterruptToolConfirmation = "tool_confirmation"
	InterruptRequestInput     = "request_input"
	InterruptLongRunningTool  = "long_running_tool"
)

// Decision is a human's answer to a paused run, applied to every interrupt the
// run is waiting on. A graph pauses because a node needs one approval or one
// answer, and answering the whole run at once is what a caller wants.
type Decision struct {
	// Approve answers tool-confirmation interrupts: true lets the parked tool
	// call run, false refuses it.
	Approve bool `json:"approve"`
	// Answer answers request-input interrupts.
	Answer string `json:"answer,omitempty"`
}

// newRunID returns a sortable, filesystem-safe run ID: the graph's name, the
// UTC time it started, and a short random suffix that keeps two runs started in
// the same second apart.
func newRunID(graph string) string {
	name := strings.TrimSpace(graph)
	if name == "" {
		name = "graph"
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	return fmt.Sprintf("%s-%s-%s", name, time.Now().UTC().Format("20060102-150405"), randHex(3))
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(b)
}

// openInterrupts walks a run's session history and returns the interrupts that
// are still unanswered: the long-running calls the engine parked on, minus the
// ones a later FunctionResponse resolved.
func openInterrupts(events []*session.Event, spec *Spec) []WaitingInterrupt {
	answered := map[string]bool{}
	calls := map[string]*session.Event{}
	var order []string

	for _, ev := range events {
		if ev == nil {
			continue
		}
		for _, fr := range eventFunctionResponses(ev) {
			if fr != nil && fr.ID != "" {
				answered[fr.ID] = true
			}
		}
		for _, id := range ev.LongRunningToolIDs {
			if id == "" {
				continue
			}
			if _, seen := calls[id]; !seen {
				order = append(order, id)
			}
			calls[id] = ev
		}
	}

	var out []WaitingInterrupt
	for _, id := range order {
		if answered[id] {
			continue
		}
		ev := calls[id]
		w := WaitingInterrupt{InterruptID: id, Kind: InterruptLongRunningTool}
		if spec != nil {
			w.Node = eventNodeName(ev, spec)
		}
		for _, fc := range eventFunctionCalls(ev) {
			if fc == nil || fc.ID != id {
				continue
			}
			switch fc.Name {
			case toolconfirmation.FunctionCallName:
				w.Kind = InterruptToolConfirmation
				w.Hint, w.ToolName, w.ToolArgs = describeConfirmation(fc)
			case workflow.WorkflowInputFunctionCallName:
				w.Kind = InterruptRequestInput
				w.Hint, _ = fc.Args["message"].(string)
				w.Payload = fc.Args["payload"]
			default:
				w.ToolName = fc.Name
				w.ToolArgs = fc.Args
			}
		}
		out = append(out, w)
	}
	return out
}

// describeConfirmation pulls the human-facing half out of an
// adk_request_confirmation call: the hint the tool asked for, and the tool call
// the human is being asked to allow.
func describeConfirmation(fc *genai.FunctionCall) (hint, tool string, args map[string]any) {
	if tc, ok := fc.Args["toolConfirmation"].(map[string]any); ok {
		hint, _ = tc["hint"].(string)
	}
	if orig, err := toolconfirmation.OriginalCallFrom(fc); err == nil && orig != nil {
		tool, args = orig.Name, orig.Args
	}
	return hint, tool, args
}

// resumeResponses maps each pending interrupt to the payload the engine expects
// back for it.
func resumeResponses(waiting []WaitingInterrupt, d Decision) map[string]any {
	out := make(map[string]any, len(waiting))
	for _, w := range waiting {
		switch w.Kind {
		case InterruptToolConfirmation:
			out[w.InterruptID] = map[string]any{"confirmed": d.Approve}
		default:
			out[w.InterruptID] = map[string]any{"response": d.Answer}
		}
	}
	return out
}

// resumeText is the user turn that tells a resumed node what the human
// decided.
//
// It is plain text on purpose. A resumed node re-runs from the top, and its
// model is a one-shot placement that sees only the current turn, so the turn
// has to say what happened. FunctionResponse parts would be the other option,
// but the flow drops an event carrying an adk_request_confirmation response
// from the model's contents entirely, so the model would see nothing at all.
func resumeText(waiting []WaitingInterrupt, d Decision) string {
	var sb strings.Builder
	sb.WriteString("A human reviewed this paused graph run and answered:\n")
	for _, w := range waiting {
		node := w.Node
		if node == "" {
			node = "a node"
		}
		switch w.Kind {
		case InterruptToolConfirmation:
			verdict := "REFUSED"
			if d.Approve {
				verdict = "APPROVED"
			}
			fmt.Fprintf(&sb, "- %s: %s %s", node, verdict, w.ToolName)
			if len(w.ToolArgs) > 0 {
				if data, err := marshalJSON(w.ToolArgs); err == nil {
					fmt.Fprintf(&sb, "(%s)", data)
				}
			}
			if w.Hint != "" {
				fmt.Fprintf(&sb, " — %s", w.Hint)
			}
			sb.WriteString("\n")
		case InterruptRequestInput:
			fmt.Fprintf(&sb, "- %s asked %q; the answer is: %s\n", node, w.Hint, d.Answer)
		default:
			fmt.Fprintf(&sb, "- %s was blocked on %s; the answer is: %s\n", node, w.ToolName, d.Answer)
		}
	}
	sb.WriteString("\nContinue the task you were given. An approved call may be made now: " +
		"it will run instead of asking again. A refused call must not be retried unchanged — " +
		"report what you could not do and finish the rest.")
	return sb.String()
}

// appendResumeAnswers writes the human's answers into the run's session as a
// user event before the resumed turn runs.
//
// The engine rebuilds a paused run by scanning history, so an interrupt that no
// user FunctionResponse resolved stays WAITING forever: a second pause in the
// same run would keep answering the first. The event is authored by "user" and
// carries only function responses, which the flow excludes from the model's
// contents — the model reads resumeText instead, so the answer is not stated
// twice.
func appendResumeAnswers(ctx agent.Context, svc *fileSessionService, sess session.Session, waiting []WaitingInterrupt, d Decision) error {
	if len(waiting) == 0 {
		return nil
	}
	ev := session.NewEvent(ctx, ctx.InvocationID())
	ev.Author = "user"
	parts := make([]*genai.Part, 0, len(waiting))
	for _, w := range waiting {
		parts = append(parts, &genai.Part{FunctionResponse: resumeResponse(w, d)})
	}
	ev.LLMResponse = model.LLMResponse{Content: &genai.Content{Role: genai.RoleUser, Parts: parts}}
	return svc.AppendEvent(ctx, sess, ev)
}

// resumeResponse is the FunctionResponse the engine expects for one interrupt.
func resumeResponse(w WaitingInterrupt, d Decision) *genai.FunctionResponse {
	switch w.Kind {
	case InterruptToolConfirmation:
		return &genai.FunctionResponse{
			ID:       w.InterruptID,
			Name:     toolconfirmation.FunctionCallName,
			Response: map[string]any{"confirmed": d.Approve},
		}
	case InterruptRequestInput:
		return &genai.FunctionResponse{
			ID:       w.InterruptID,
			Name:     workflow.WorkflowInputFunctionCallName,
			Response: map[string]any{"response": d.Answer},
		}
	default:
		return &genai.FunctionResponse{
			ID:       w.InterruptID,
			Name:     w.ToolName,
			Response: map[string]any{"result": d.Answer},
		}
	}
}

// decisionsFor turns a run's pending tool calls into the approval table the
// resumed turn's tools consult.
func decisionsFor(waiting []WaitingInterrupt, d Decision) Decisions {
	out := Decisions{}
	for _, w := range waiting {
		if w.ToolName == "" {
			continue
		}
		switch w.Kind {
		case InterruptToolConfirmation, InterruptLongRunningTool:
			out[CallKey(w.ToolName, w.ToolArgs)] = d.Approve
		}
	}
	return out
}

// eventFunctionCalls flattens an event's function calls.
func eventFunctionCalls(ev *session.Event) []*genai.FunctionCall {
	if ev == nil || ev.Content == nil {
		return nil
	}
	var out []*genai.FunctionCall
	for _, p := range ev.Content.Parts {
		if p.FunctionCall != nil {
			out = append(out, p.FunctionCall)
		}
	}
	return out
}

// eventFunctionResponses flattens an event's function responses.
func eventFunctionResponses(ev *session.Event) []*genai.FunctionResponse {
	if ev == nil || ev.Content == nil {
		return nil
	}
	var out []*genai.FunctionResponse
	for _, p := range ev.Content.Parts {
		if p.FunctionResponse != nil {
			out = append(out, p.FunctionResponse)
		}
	}
	return out
}
