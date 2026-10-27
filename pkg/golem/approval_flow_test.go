package golem

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"testing"

	"github.com/user/golem/pkg/sandbox"
	"github.com/user/golem/pkg/tools"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

// scriptedModel replays canned responses: first the tool call, then a text
// answer once the tool result is in the conversation.
type scriptedModel struct {
	calls int
}

func (m *scriptedModel) Name() string { return "scripted" }

func (m *scriptedModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		// Once a function response is present, the tool has run: answer in text.
		if hasFunctionResponse(req.Contents) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("done", genai.RoleModel)}, nil)
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID:   "call-1",
				Name: "shell",
				Args: map[string]any{"command": "echo approved > ran.txt"},
			}}},
		}}, nil)
	}
}

func hasFunctionResponse(contents []*genai.Content) bool {
	for _, c := range contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				return true
			}
		}
	}
	return false
}

// runEvents collects the confirmation call ID (if any) and the text.
func runEvents(t *testing.T, seq iter.Seq2[*session.Event, error]) (text, callID string) {
	t.Helper()
	for ev, err := range seq {
		if err != nil {
			t.Fatalf("run error: %v", err)
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionCall != nil && p.FunctionCall.Name == toolconfirmation.FunctionCallName {
				callID = p.FunctionCall.ID
			}
			if p.Text != "" && !p.Thought {
				text += p.Text
			}
		}
	}
	return text, callID
}

// TestConfirmationGateRoundTripWithADK is the end-to-end proof that the gate
// and the ADK Human-in-the-Loop flow agree: with shell in require_confirm the
// first pass stops on adk_request_confirmation without running anything, and
// the resumed pass - a FunctionResponse with confirmed=true - runs the command
// and lets the model finish.
func TestConfirmationGateRoundTripWithADK(t *testing.T) {
	ws := t.TempDir()
	p, err := sandbox.New(sandbox.Config{
		Mode:           sandbox.ModePolicy,
		RequireConfirm: []string{"shell"},
	}, ws)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}

	shellTool, err := tools.NewShellTool(tools.WithPolicy(p))
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}

	ag, err := llmagent.New(llmagent.Config{
		Name:  "approval_test_agent",
		Model: &scriptedModel{},
		Tools: []tool.Tool{shellTool},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}

	r, err := runner.NewInMemory("approval-app", ag)
	if err != nil {
		t.Fatalf("runner.NewInMemory: %v", err)
	}

	ctx := context.Background()
	sessionID := "turn-approval-1"
	prompt := genai.NewContentFromText("write the file", genai.RoleUser)

	_, callID := runEvents(t, r.Run(ctx, "user", sessionID, prompt, agent.RunConfig{StreamingMode: agent.StreamingModeNone}))
	if callID == "" {
		t.Fatal("the turn did not stop on a confirmation request")
	}
	if _, err := os.Stat(filepath.Join(ws, "ran.txt")); err == nil {
		t.Fatal("the command ran before the human approved it")
	}

	// Approve: the ADK re-dispatches the held call with the answer attached.
	answer := genai.NewContentFromParts([]*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID:       callID,
			Name:     toolconfirmation.FunctionCallName,
			Response: map[string]any{"confirmed": true},
		},
	}}, genai.RoleUser)
	text, _ := runEvents(t, r.Run(ctx, "user", sessionID, answer, agent.RunConfig{StreamingMode: agent.StreamingModeNone}))
	if text != "done" {
		t.Fatalf("resumed turn text = %q, want done", text)
	}
	if _, err := os.Stat(filepath.Join(ws, "ran.txt")); err != nil {
		t.Fatalf("the approved command did not run: %v", err)
	}
}

// TestConfirmationGateRefusalRunsNothing covers the no path: a refusal is a
// tool error the model sees, and the command never runs.
func TestConfirmationGateRefusalRunsNothing(t *testing.T) {
	ws := t.TempDir()
	p, err := sandbox.New(sandbox.Config{
		Mode:           sandbox.ModePolicy,
		RequireConfirm: []string{"shell"},
	}, ws)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	shellTool, err := tools.NewShellTool(tools.WithPolicy(p))
	if err != nil {
		t.Fatalf("NewShellTool: %v", err)
	}
	ag, err := llmagent.New(llmagent.Config{
		Name:  "approval_refusal_agent",
		Model: &scriptedModel{},
		Tools: []tool.Tool{shellTool},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	r, err := runner.NewInMemory("approval-app", ag)
	if err != nil {
		t.Fatalf("runner.NewInMemory: %v", err)
	}

	ctx := context.Background()
	sessionID := "turn-approval-2"
	_, callID := runEvents(t, r.Run(ctx, "user", sessionID, genai.NewContentFromText("write the file", genai.RoleUser), agent.RunConfig{StreamingMode: agent.StreamingModeNone}))
	if callID == "" {
		t.Fatal("the turn did not stop on a confirmation request")
	}

	answer := genai.NewContentFromParts([]*genai.Part{{
		FunctionResponse: &genai.FunctionResponse{
			ID:       callID,
			Name:     toolconfirmation.FunctionCallName,
			Response: map[string]any{"confirmed": false},
		},
	}}, genai.RoleUser)
	runEvents(t, r.Run(ctx, "user", sessionID, answer, agent.RunConfig{StreamingMode: agent.StreamingModeNone}))

	if _, err := os.Stat(filepath.Join(ws, "ran.txt")); err == nil {
		t.Fatal("the command ran despite being refused")
	}
}
