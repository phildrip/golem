package golem

import (
	"context"
	"strings"
	"testing"

	"github.com/user/golem/pkg/approval"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// fakeApprovalHandler records the prompt a transport would render.
type fakeApprovalHandler struct {
	got []approval.Request
}

func (h *fakeApprovalHandler) OnChunk(string)                 {}
func (h *fakeApprovalHandler) OnToolStart(string, string)     {}
func (h *fakeApprovalHandler) OnToolDone(string, string, int) {}
func (h *fakeApprovalHandler) OnApprovalRequest(req approval.Request) {
	h.got = append(h.got, req)
}

// confirmationEvent builds the adk_request_confirmation event the flow emits.
func confirmationEvent(callID, hint, action string) *session.Event {
	return &session.Event{
		LLMResponse: model.LLMResponse{
			Content: &genai.Content{
				Role: genai.RoleModel,
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					ID:   callID,
					Name: toolconfirmation.FunctionCallName,
					Args: map[string]any{
						"toolConfirmation": &toolconfirmation.ToolConfirmation{
							Hint:    hint,
							Payload: &approval.Payload{Tool: "shell", Action: action, Reason: "because"},
						},
					},
				}}},
			},
		},
	}
}

// TestNoteConfirmationRequestReportsThePrompt is the agent half of the gate:
// the adk_request_confirmation event becomes an approval.Request with the exact
// action, the transport is told, and PendingApproval can hand it out again
// after a reconnect.
func TestNoteConfirmationRequestReportsThePrompt(t *testing.T) {
	a := &Agent{}
	handler := &fakeApprovalHandler{}

	a.noteConfirmationRequest(confirmationEvent("conf-1", "shell wants to run: rm -rf build", "rm -rf build"), handler)

	if len(handler.got) != 1 {
		t.Fatalf("handler saw %d requests, want 1", len(handler.got))
	}
	req := handler.got[0]
	if req.CallID != "conf-1" || req.Tool != "shell" || req.Action != "rm -rf build" {
		t.Fatalf("request = %+v", req)
	}
	if !strings.Contains(req.Hint, "rm -rf build") {
		t.Fatalf("hint = %q", req.Hint)
	}

	pending, ok := a.PendingApproval()
	if !ok || pending.CallID != "conf-1" {
		t.Fatalf("PendingApproval = %+v, %v", pending, ok)
	}
}

// TestNoteConfirmationRequestIgnoresOtherCalls keeps ordinary function calls
// out of the approval path.
func TestNoteConfirmationRequestIgnoresOtherCalls(t *testing.T) {
	a := &Agent{}
	handler := &fakeApprovalHandler{}
	ev := &session.Event{LLMResponse: model.LLMResponse{Content: &genai.Content{
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "c1", Name: "shell"}}},
	}}}
	a.noteConfirmationRequest(ev, handler)
	if len(handler.got) != 0 {
		t.Fatalf("a plain tool call raised an approval: %+v", handler.got)
	}
	if _, ok := a.PendingApproval(); ok {
		t.Fatal("a plain tool call left a pending approval")
	}
}

func TestRespondToApprovalWithoutAPendingCall(t *testing.T) {
	a := &Agent{}
	if _, err := a.RespondToApproval(context.Background(), "", true, nil); err == nil {
		t.Fatal("expected an error when nothing is pending")
	}
}

// TestRespondToApprovalRejectsAMismatchedCall keeps an answer bound to the call
// the user was actually shown.
func TestRespondToApprovalRejectsAMismatchedCall(t *testing.T) {
	a := &Agent{}
	a.setPendingApproval("turn-1", &approval.Request{CallID: "conf-1", Tool: "shell"})

	if _, err := a.RespondToApproval(context.Background(), "conf-2", true, nil); err == nil {
		t.Fatal("expected an error for a mismatched call id")
	}
	pending, ok := a.PendingApproval()
	if !ok || pending.CallID != "conf-1" {
		t.Fatalf("the pending call must survive a rejected answer: %+v", pending)
	}
}

// TestNewTurnIsRefusedWhileAnApprovalIsPending keeps the prompt bound to the
// turn that raised it: starting another turn would orphan the question.
func TestNewTurnIsRefusedWhileAnApprovalIsPending(t *testing.T) {
	a := &Agent{}
	a.setPendingApproval("turn-1", &approval.Request{CallID: "conf-1", Tool: "shell"})

	if _, err := a.ProcessStream(context.Background(), "hello", nil); err == nil {
		t.Fatal("expected a new turn to be refused while an approval is pending")
	}
}
