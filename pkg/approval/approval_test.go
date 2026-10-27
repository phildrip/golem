package approval

import (
	"errors"
	"strings"
	"testing"

	"github.com/user/golem/pkg/sandbox"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
)

// fakeApprover is the narrow slice of agent.Context the gate touches, so the
// decision table can be exercised without a live invocation.
type fakeApprover struct {
	confirmation *toolconfirmation.ToolConfirmation
	actions      *session.EventActions

	asked       bool
	askedHint   string
	askedPayloa any
	requestErr  error
}

func (f *fakeApprover) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return f.confirmation
}

func (f *fakeApprover) RequestConfirmation(hint string, payload any) error {
	f.asked = true
	f.askedHint = hint
	f.askedPayloa = payload
	return f.requestErr
}

func (f *fakeApprover) Actions() *session.EventActions {
	if f.actions == nil {
		f.actions = &session.EventActions{}
	}
	return f.actions
}

func TestGateAllowsAndDeniesWithoutAsking(t *testing.T) {
	ctx := &fakeApprover{}

	if err := Gate(ctx, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Allow}, "ls"); err != nil {
		t.Fatalf("allow: unexpected err %v", err)
	}
	err := Gate(ctx, Interactive, "shell", sandbox.Decision{
		Verdict: sandbox.Deny, Rule: "privilege-escalation", Reason: "no sudo",
	}, "sudo ls")
	if !errors.Is(err, sandbox.ErrBlocked) {
		t.Fatalf("deny: err = %v, want sandbox.ErrBlocked", err)
	}
	if ctx.asked {
		t.Fatal("deny must not raise a confirmation prompt")
	}
}

// TestGateAsksTheHumanAndShowsTheExactAction is the core of the ticket: the
// prompt carries the real command, and this pass ends with the sentinel the ADK
// flow looks for so it can emit adk_request_confirmation.
func TestGateAsksTheHumanAndShowsTheExactAction(t *testing.T) {
	ctx := &fakeApprover{}
	d := sandbox.Decision{Verdict: sandbox.Confirm, Rule: "force-push", Reason: "a force push rewrites shared history"}

	err := Gate(ctx, Interactive, "shell", d, "git push --force origin main")
	if !errors.Is(err, tool.ErrConfirmationRequired) {
		t.Fatalf("err = %v, want tool.ErrConfirmationRequired", err)
	}
	if !ctx.asked {
		t.Fatal("no confirmation was requested")
	}
	if !strings.Contains(ctx.askedHint, "git push --force origin main") {
		t.Fatalf("hint %q does not quote the command", ctx.askedHint)
	}
	if !strings.Contains(ctx.askedHint, "rewrites shared history") {
		t.Fatalf("hint %q does not explain why", ctx.askedHint)
	}
	payload, ok := ctx.askedPayloa.(*Payload)
	if !ok {
		t.Fatalf("payload type = %T, want *Payload", ctx.askedPayloa)
	}
	if payload.Action != "git push --force origin main" || payload.Tool != "shell" {
		t.Fatalf("payload = %+v, want the exact action and tool", payload)
	}
	if !ctx.actions.SkipSummarization {
		t.Fatal("SkipSummarization must be set so the model does not narrate the pending call")
	}
}

func TestGateHonoursTheAnswer(t *testing.T) {
	approved := &fakeApprover{confirmation: &toolconfirmation.ToolConfirmation{Confirmed: true}}
	if err := Gate(approved, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Confirm}, "rm -rf build"); err != nil {
		t.Fatalf("approved call must run: %v", err)
	}
	if approved.asked {
		t.Fatal("an answered call must not ask again")
	}

	rejected := &fakeApprover{confirmation: &toolconfirmation.ToolConfirmation{Confirmed: false}}
	err := Gate(rejected, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Confirm}, "rm -rf build")
	if !errors.Is(err, sandbox.ErrBlocked) {
		t.Fatalf("rejected call: err = %v, want sandbox.ErrBlocked", err)
	}
	var blocked *sandbox.BlockedError
	if !errors.As(err, &blocked) || blocked.Rule != "rejected" {
		t.Fatalf("blocked = %+v, want rule rejected", blocked)
	}
}

// TestGateUnattendedRefusesInsteadOfHanging pins the subagent rule: with nobody
// to ask, a confirmation becomes a tool error the model can route around - never
// a prompt nobody will answer, and never an implicit yes.
func TestGateUnattendedRefusesInsteadOfHanging(t *testing.T) {
	ctx := &fakeApprover{}
	err := Gate(ctx, Unattended, "shell", sandbox.Decision{Verdict: sandbox.Confirm, Reason: "a force push rewrites shared history"}, "git push --force")
	if !errors.Is(err, sandbox.ErrBlocked) {
		t.Fatalf("err = %v, want sandbox.ErrBlocked", err)
	}
	if ctx.asked {
		t.Fatal("an unattended turn must not raise a prompt")
	}
	var blocked *sandbox.BlockedError
	if !errors.As(err, &blocked) || blocked.Rule != "unattended" {
		t.Fatalf("blocked = %+v, want rule unattended", blocked)
	}
}

// TestGateRequestFailureIsReported keeps a transport error from looking like a
// policy denial: the model should be able to tell the difference.
func TestGateRequestFailureIsReported(t *testing.T) {
	boom := errors.New("event bus closed")
	ctx := &fakeApprover{requestErr: boom}
	err := Gate(ctx, Interactive, "shell", sandbox.Decision{Verdict: sandbox.Confirm}, "ls")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the transport error", err)
	}
}

func TestRequestFromTypedPayload(t *testing.T) {
	args := map[string]any{
		"toolConfirmation": &toolconfirmation.ToolConfirmation{
			Hint:    "shell wants to run: ls",
			Payload: &Payload{Tool: "shell", Action: "ls", Reason: "because"},
		},
		"originalFunctionCall": map[string]any{"name": "shell", "id": "call-1"},
	}
	req := RequestFrom("conf-1", args)
	if req.CallID != "conf-1" || req.Tool != "shell" || req.Action != "ls" || req.Reason != "because" {
		t.Fatalf("req = %+v", req)
	}
	if req.Hint != "shell wants to run: ls" {
		t.Fatalf("hint = %q", req.Hint)
	}
}

// TestRequestFromDecodedMap covers the shape that comes back after the session
// round-trip, where the typed payload has been flattened to maps.
func TestRequestFromDecodedMap(t *testing.T) {
	args := map[string]any{
		"toolConfirmation": map[string]any{
			"hint": "shell wants to run: ls",
			"payload": map[string]any{
				"tool": "shell", "action": "ls -la", "reason": "because",
			},
		},
		"originalFunctionCall": map[string]any{"name": "shell"},
	}
	req := RequestFrom("conf-2", args)
	if req.Tool != "shell" || req.Action != "ls -la" || req.Reason != "because" {
		t.Fatalf("req = %+v", req)
	}
}

func TestRequestFromEmptyArgs(t *testing.T) {
	if got := RequestFrom("conf-3", nil); got.CallID != "conf-3" {
		t.Fatalf("req = %+v", got)
	}
}

// TestHintWithoutReason keeps a bare prompt readable.
func TestHintWithoutReason(t *testing.T) {
	if got := Hint("shell", "ls", ""); got != "shell wants to run: ls" {
		t.Fatalf("hint = %q", got)
	}
	if got := Hint("shell", "", ""); !strings.Contains(got, "(no arguments)") {
		t.Fatalf("hint = %q", got)
	}
}
