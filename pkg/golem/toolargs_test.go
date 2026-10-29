package golem

import (
	"context"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/golem/pkg/memory/fractal"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// stringArgsModel calls zoom the way the bug report showed: the numbers are
// JSON strings, not numbers.
type stringArgsModel struct{ calls int }

func (m *stringArgsModel) Name() string { return "string-args" }

func (m *stringArgsModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if hasFunctionResponse(req.Contents) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText("done", genai.RoleModel)}, nil)
			return
		}
		yield(&model.LLMResponse{Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID:   "call-zoom",
				Name: "zoom",
				Args: map[string]any{"id": "0", "n": "1"},
			}}},
		}}, nil)
	}
}

// toolResponses collects every function response the ADK produced.
func toolResponses(t *testing.T, seq iter.Seq2[*session.Event, error]) []map[string]any {
	t.Helper()
	var out []map[string]any
	for ev, err := range seq {
		if err != nil {
			t.Fatalf("run error: %v", err)
		}
		if ev == nil || ev.Content == nil {
			continue
		}
		for _, p := range ev.Content.Parts {
			if p.FunctionResponse != nil {
				out = append(out, p.FunctionResponse.Response)
			}
		}
	}
	return out
}

// TestToolCallWithStringifiedNumbersRuns is the regression test for the TUI
// report "Tool: zoom EXIT 1 ... type: 5441 has type \"string\", want
// \"integer\"": a call the model typed loosely must still run, because the
// schema already says what type each argument is.
func TestToolCallWithStringifiedNumbersRuns(t *testing.T) {
	store, err := fractal.OpenStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	if _, err := store.AppendEvent(fractal.KindUser, "the message the model wanted to reopen"); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	zoomTool, err := fractal.NewZoomTool(store)
	if err != nil {
		t.Fatalf("NewZoomTool: %v", err)
	}

	ag, err := llmagent.New(llmagent.Config{
		Name:                "string_args_agent",
		Model:               &stringArgsModel{},
		Tools:               []tool.Tool{zoomTool},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{repairToolArguments},
	})
	if err != nil {
		t.Fatalf("llmagent.New: %v", err)
	}
	r, err := runner.NewInMemory("string-args-app", ag)
	if err != nil {
		t.Fatalf("runner.NewInMemory: %v", err)
	}

	responses := toolResponses(t, r.Run(
		context.Background(), "user", "string-args-1",
		genai.NewContentFromText("zoom the first message", genai.RoleUser),
		agent.RunConfig{StreamingMode: agent.StreamingModeNone},
	))
	if len(responses) == 0 {
		t.Fatal("the model's zoom call produced no function response")
	}
	if msg, failed := responses[0]["error"]; failed {
		t.Fatalf("zoom still failed: %v", msg)
	}
	result, ok := responses[0]["result"].(string)
	if !ok {
		t.Fatalf("zoom result = %#v, want a string", responses[0]["result"])
	}
	if want := "the message the model wanted to reopen"; !strings.Contains(result, want) {
		t.Fatalf("zoom result = %q, want it to contain %q", result, want)
	}
}
