// Package approval bridges the sandbox policy and the human.
//
// pkg/sandbox decides that a call is dangerous (Deny) or that it needs a human
// (Confirm); this package turns a Confirm into an ADK Human-in-the-Loop
// request and, on the second pass, reads the answer back. It is deliberately
// transport-agnostic: the ADK emits one adk_request_confirmation event and each
// front end (TUI, web cockpit, readline) renders it.
package approval

import (
	"fmt"

	"github.com/user/golem/pkg/sandbox"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
)

// Approver is the slice of agent.Context the gate needs. Keeping it narrow
// means the gate can be tested without a live ADK invocation.
type Approver interface {
	// ToolConfirmation is non-nil on the pass that follows an answer.
	ToolConfirmation() *toolconfirmation.ToolConfirmation
	// RequestConfirmation raises the HITL prompt.
	RequestConfirmation(hint string, payload any) error
	// Actions carries the per-event flags the flow reads.
	Actions() *session.EventActions
}

// Mode says who, if anyone, can answer a confirmation prompt.
type Mode int

const (
	// Interactive asks the human and waits for an answer. It is the default for
	// a turn that a person is watching.
	Interactive Mode = iota
	// Unattended refuses anything that needs approval, with an error the model
	// can read and route around. It is what a subagent and any headless caller
	// gets, so a delegate can never approve on the user's behalf.
	Unattended
)

// Payload is what the transport renders and what the model sees attached to the
// request. Action is the exact thing being approved - the command line or the
// patch summary - never a paraphrase of it.
type Payload struct {
	Tool   string `json:"tool"`
	Action string `json:"action"`
	Rule   string `json:"rule,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Request is one pending confirmation as the transports see it.
type Request struct {
	// CallID identifies the adk_request_confirmation function call; the answer
	// is a FunctionResponse carrying this ID.
	CallID string
	// Tool is the tool that is waiting.
	Tool string
	// Hint is the sentence shown in the prompt.
	Hint string
	// Action is the exact command or patch being approved.
	Action string
	// Reason explains why the policy asked.
	Reason string
}

// Gate enforces d for toolName, asking the human when the policy says Confirm.
//
// It returns nil when the call may proceed. When a human has to answer, it
// raises the confirmation request and returns an error wrapping
// tool.ErrConfirmationRequired, which ends this pass of the turn; the ADK emits
// an adk_request_confirmation event and the transport asks the user. The
// resumed pass re-runs the tool with ToolConfirmation() set.
func Gate(ctx Approver, mode Mode, toolName string, d sandbox.Decision, action string) error {
	switch d.Verdict {
	case sandbox.Allow:
		return nil
	case sandbox.Deny:
		return d.Err(toolName)
	}

	// Confirm: a human decides.
	if mode == Unattended || ctx == nil {
		return &sandbox.BlockedError{
			Tool:    toolName,
			Verdict: sandbox.Deny,
			Rule:    "unattended",
			Reason:  fmt.Sprintf("%s; nobody is attached to this turn to approve it, so it is refused", d.Reason),
		}
	}

	if conf := ctx.ToolConfirmation(); conf != nil {
		if conf.Confirmed {
			return nil
		}
		return &sandbox.BlockedError{
			Tool:    toolName,
			Verdict: sandbox.Deny,
			Rule:    "rejected",
			Reason:  "the user declined this call; do not retry it unchanged",
		}
	}

	hint := Hint(toolName, action, d.Reason)
	if err := ctx.RequestConfirmation(hint, &Payload{
		Tool:   toolName,
		Action: action,
		Rule:   d.Rule,
		Reason: d.Reason,
	}); err != nil {
		return fmt.Errorf("%s: requesting confirmation: %w", toolName, err)
	}
	// The confirmation event is the whole point of this pass; do not let the
	// model narrate it as a finished tool call.
	if actions := ctx.Actions(); actions != nil {
		actions.SkipSummarization = true
	}
	return fmt.Errorf("%s: %w", toolName, tool.ErrConfirmationRequired)
}

// Hint is the sentence a human reads. It names the tool, quotes the exact
// action, and gives the policy's reason, because "approve shell?" is not
// something anyone can answer.
func Hint(toolName, action, reason string) string {
	if action == "" {
		action = "(no arguments)"
	}
	if reason == "" {
		return fmt.Sprintf("%s wants to run: %s", toolName, action)
	}
	return fmt.Sprintf("%s wants to run: %s\nwhy: %s", toolName, action, reason)
}

// RequestFrom builds a Request from the args of an adk_request_confirmation
// function call. It tolerates a typed payload (the in-process case) and a
// decoded map (the case after a session round-trip).
func RequestFrom(callID string, args map[string]any) Request {
	req := Request{CallID: callID}
	if args == nil {
		return req
	}
	switch conf := args["toolConfirmation"].(type) {
	case *toolconfirmation.ToolConfirmation:
		if conf != nil {
			req.Hint = conf.Hint
			req.fillFromPayload(conf.Payload)
		}
	case toolconfirmation.ToolConfirmation:
		req.Hint = conf.Hint
		req.fillFromPayload(conf.Payload)
	case map[string]any:
		if hint, ok := conf["hint"].(string); ok {
			req.Hint = hint
		}
		req.fillFromPayload(conf["payload"])
	}
	if req.Tool == "" {
		if original, ok := args["originalFunctionCall"].(map[string]any); ok {
			if name, ok := original["name"].(string); ok {
				req.Tool = name
			}
		}
	}
	return req
}

func (r *Request) fillFromPayload(payload any) {
	switch p := payload.(type) {
	case *Payload:
		if p != nil {
			r.Tool, r.Action, r.Reason = p.Tool, p.Action, p.Reason
		}
	case Payload:
		r.Tool, r.Action, r.Reason = p.Tool, p.Action, p.Reason
	case map[string]any:
		if s, ok := p["tool"].(string); ok {
			r.Tool = s
		}
		if s, ok := p["action"].(string); ok {
			r.Action = s
		}
		if s, ok := p["reason"].(string); ok {
			r.Reason = s
		}
	}
}
