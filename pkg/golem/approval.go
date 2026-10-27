package golem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/provider/openai"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// ErrAwaitingApproval means the turn stopped on a sandbox confirmation: the
// policy wants a human to approve a tool call before the turn can continue.
// The caller answers with RespondToApproval on the same Agent.
var ErrAwaitingApproval = errors.New("awaiting approval for a tool call")

// ApprovalStreamHandler optionally extends StreamHandler to receive the
// confirmation prompt of the turn it is streaming. A handler that does not
// implement it still works: the turn pauses and PendingApproval reports the
// request instead.
type ApprovalStreamHandler interface {
	StreamHandler
	OnApprovalRequest(req approval.Request)
}

// streamPass runs one ADK pass over sessionID and returns the assistant text.
//
// It is the only place the runner is driven, so a fresh turn and a resumed
// confirmation share the retry policy, the streaming plumbing and the
// confirmation detection. A confirmation request does not fail the pass: the
// flow simply stops after emitting adk_request_confirmation, so the text is
// empty and pendingApprovalRequest() reports what is waiting.
func (a *Agent) streamPass(ctx context.Context, sessionID string, content *genai.Content, handler StreamHandler) (string, error) {
	maxRetries := 3
	backoff := 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		var responseBuilder strings.Builder
		var runErr error

		runCtx := ctx
		if a.sessionID != "" {
			runCtx = openai.WithSessionID(runCtx, a.sessionID)
		}

		eventsSeq := a.runner.Run(runCtx, a.userID, sessionID, content, agent.RunConfig{
			StreamingMode: agent.StreamingModeSSE,
		})
		for ev, err := range eventsSeq {
			if err != nil {
				runErr = err
				break
			}
			if ev == nil || ev.Content == nil {
				continue
			}
			a.noteConfirmationRequest(ev, handler)
			if ev.Partial || responseBuilder.Len() == 0 {
				for _, part := range ev.Content.Parts {
					if part.Thought || part.Text == "" {
						continue
					}
					responseBuilder.WriteString(part.Text)
					if handler != nil {
						handler.OnChunk(part.Text)
					}
				}
			}
		}

		if runErr == nil {
			return responseBuilder.String(), nil
		}

		// Only restart a turn from scratch when nothing has executed yet. Once a
		// tool has run, a restart would re-execute its side effects and discard
		// all work, so surface the error (with any partial text) instead.
		if shouldRestartTurn(runErr, attempt, maxRetries, a.turnToolCallCount()) {
			if a.cfg.OnRetry != nil {
				a.cfg.OnRetry(attempt, maxRetries, backoff)
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
				continue
			}
		}

		return responseBuilder.String(), errors.Wrap(runErr, "running adk agent")
	}
	return "", nil
}

// noteConfirmationRequest records an adk_request_confirmation event and tells
// the transport about it. The event is the ADK's Human-in-the-Loop prompt: the
// tool that asked is held in the flow until a FunctionResponse answers it.
func (a *Agent) noteConfirmationRequest(ev *session.Event, handler StreamHandler) {
	for _, part := range ev.Content.Parts {
		fc := part.FunctionCall
		if fc == nil || fc.Name != toolconfirmation.FunctionCallName {
			continue
		}
		req := approval.RequestFrom(fc.ID, fc.Args)
		a.approvalMu.Lock()
		a.pendingApproval = &req
		a.approvalMu.Unlock()
		if sh, ok := handler.(ApprovalStreamHandler); ok {
			sh.OnApprovalRequest(req)
		}
	}
}

// pendingApprovalRequest returns the confirmation the current pass raised, if any.
func (a *Agent) pendingApprovalRequest() *approval.Request {
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	return a.pendingApproval
}

// setPendingApproval remembers the session the answer must resume.
func (a *Agent) setPendingApproval(sessionID string, req *approval.Request) {
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	a.pendingApproval = req
	a.pendingSession = sessionID
}

// PendingApproval reports the tool call waiting for a human, or nil. Transports
// call it after a reconnect to re-render a prompt nobody answered yet.
func (a *Agent) PendingApproval() (approval.Request, bool) {
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	if a.pendingApproval == nil {
		return approval.Request{}, false
	}
	return *a.pendingApproval, true
}

// RespondToApproval answers the pending confirmation and resumes the turn.
//
// approved runs the held call and lets the model continue; rejected returns a
// tool error the model can route around. The resumed pass runs on the same
// session, which is what lets the ADK re-dispatch the original function call
// with the answer attached. It returns ErrAwaitingApproval again when the same
// turn raises a second confirmation.
func (a *Agent) RespondToApproval(ctx context.Context, callID string, approved bool, handler StreamHandler) (string, error) {
	a.approvalMu.Lock()
	pending := a.pendingApproval
	sessionID := a.pendingSession
	a.approvalMu.Unlock()

	if pending == nil {
		return "", errors.New("no tool call is waiting for approval")
	}
	if callID != "" && callID != pending.CallID {
		return "", fmt.Errorf("approval for %q does not match the pending call %q", callID, pending.CallID)
	}

	a.activeHandlerMu.Lock()
	a.activeHandler = handler
	a.activeHandlerMu.Unlock()
	defer func() {
		a.activeHandlerMu.Lock()
		a.activeHandler = nil
		a.activeHandlerMu.Unlock()
	}()

	// Clear the request before resuming so a second prompt in the same turn is
	// not confused with this one.
	a.approvalMu.Lock()
	a.pendingApproval = nil
	a.approvalMu.Unlock()

	content := genai.NewContentFromParts([]*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID:   pending.CallID,
			Name: toolconfirmation.FunctionCallName,
			Response: map[string]any{
				"confirmed": approved,
			},
		},
	}}, genai.RoleUser)

	finalResponse, err := a.streamPass(ctx, sessionID, content, handler)
	if err != nil {
		return "", err
	}
	if next := a.pendingApprovalRequest(); next != nil {
		a.setPendingApproval(sessionID, next)
		return "", ErrAwaitingApproval
	}

	// The turn is over: log it and close it out exactly as a normal turn would.
	if finalResponse != "" {
		if _, err := a.store.AppendEvent(fractal.KindTalk, finalResponse); err != nil {
			return finalResponse, errors.Wrap(err, "logging agent response event")
		}
	}
	a.view.Sync()
	a.endTurn()
	a.triggerCompactor()
	return finalResponse, nil
}
