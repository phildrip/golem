package graph

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

// gatedModel asks for one confirmation-gated tool call, then answers whatever
// the tool reported. It records what it was told, which is how a test sees
// whether the parked call actually ran.
type gatedModel struct {
	mu  sync.Mutex
	saw []string
}

func (m *gatedModel) Name() string { return "gated" }

func (m *gatedModel) note(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saw = append(m.saw, s)
}

func (m *gatedModel) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.saw...)
}

func (m *gatedModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse == nil {
					continue
				}
				if _, failed := p.FunctionResponse.Response["error"]; failed {
					m.note("rejected")
					yield(&model.LLMResponse{Content: genai.NewContentFromText("could not write it", genai.RoleModel)}, nil)
					return
				}
				m.note("ran")
				yield(&model.LLMResponse{Content: genai.NewContentFromText("wrote the note", genai.RoleModel)}, nil)
				return
			}
		}
		yield(&model.LLMResponse{Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
				ID:   "call-1",
				Name: "gated_write",
				Args: map[string]any{"path": "notes.txt"},
			}}},
		}}, nil)
	}
}

// gatedManager returns a manager whose only tool needs a human's approval.
func gatedManager(t *testing.T, m model.LLM) *Manager {
	t.Helper()
	gated, err := functiontool.New(functiontool.Config{
		Name:                "gated_write",
		Description:         "writes a note, but only with a human's approval",
		RequireConfirmation: true,
	}, func(_ agent.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"result": "wrote " + fmt.Sprint(args["path"])}, nil
	})
	if err != nil {
		t.Fatalf("functiontool.New: %v", err)
	}
	return NewManager(t.TempDir(), BuilderConfig{
		Models:        stubModels{"m": m},
		ReadOnlyTools: []tool.Tool{gated},
	})
}

// gatedSpec is one coder node whose work needs an approval.
func gatedSpec() *Spec {
	return &Spec{
		V: 1, Name: "gate", MaxConcurrency: 1,
		Budgets: &Budgets{MaxNodes: 4, MaxWallSeconds: 60, MaxTokens: 50_000},
		Nodes: []Node{
			{ID: "writer", Kind: KindAgent, Role: RoleCoder, Tools: ToolsRead, Instruction: "write the note", Model: "m"},
		},
		Edges: [][]string{{"START", "writer"}},
	}
}

// A graph that hits a gated tool call parks durably; approving it from a later
// tool call runs the call and finishes the run.
func TestPausedRunResumesAfterApproval(t *testing.T) {
	m := gatedManager(t, &gatedModel{})
	out := invokeGraphRun(t, m, map[string]any{"spec": gatedSpec(), "input": "write notes.txt"})
	for _, want := range []string{"PAUSED", "gated_write", "graph_resume"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pause report missing %q:\n%s", want, out)
		}
	}

	runs, err := m.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	rec := runs[0]
	if rec.Status != RunWaiting {
		t.Fatalf("record status = %q, want waiting (err %q)", rec.Status, rec.Err)
	}
	if len(rec.Waiting) != 1 || rec.Waiting[0].Kind != InterruptToolConfirmation ||
		rec.Waiting[0].ToolName != "gated_write" || rec.Waiting[0].Node != "writer" {
		t.Fatalf("waiting interrupts = %+v, want one tool confirmation for writer/gated_write", rec.Waiting)
	}

	resumed := invokeTool(t, m, "graph_resume", map[string]any{"run_id": rec.RunID, "approve": true})
	if !strings.Contains(resumed, "wrote the note") {
		t.Fatalf("resume did not finish the run:\n%s", resumed)
	}

	rec, err = m.LoadRun(rec.RunID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if rec.Status != RunCompleted {
		t.Fatalf("after approval the run is %q, want completed (err %q)", rec.Status, rec.Err)
	}
	if len(rec.Waiting) != 0 {
		t.Fatalf("completed run still reports waiting interrupts: %+v", rec.Waiting)
	}
}

// Refusing the confirmation is an answer too: the parked call must not run, and
// the run must still finish rather than hang in waiting.
func TestPausedRunResumesAfterRefusal(t *testing.T) {
	gm := &gatedModel{}
	m := gatedManager(t, gm)
	invokeGraphRun(t, m, map[string]any{"spec": gatedSpec(), "input": "write notes.txt"})

	runs, err := m.ListRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: %v (runs %d)", err, len(runs))
	}
	rec := runs[0]

	invokeTool(t, m, "graph_resume", map[string]any{"run_id": rec.RunID, "approve": false})

	seen := gm.seen()
	if len(seen) == 0 || seen[len(seen)-1] != "rejected" {
		t.Fatalf("model saw %v, want the tool call rejected", seen)
	}
	rec, err = m.LoadRun(rec.RunID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if rec.Status == RunWaiting {
		t.Fatalf("refusal left the run waiting; it should have finished")
	}
}

// Resuming a run that is not waiting is a clear error, not a silent re-run.
func TestResumeRejectsFinishedRun(t *testing.T) {
	m, _ := testManager(t, 10)
	invokeGraphRun(t, m, map[string]any{"spec": fanOutSpec(), "input": "task"})
	runs, err := m.ListRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: %v (runs %d)", err, len(runs))
	}
	if _, err := m.Resume(nil, runs[0].RunID, Decision{Approve: true}); err == nil {
		t.Fatal("resuming a finished run should fail")
	}
}

// A run's record survives on disk: a fresh Manager over the same directory can
// still see it, which is what makes resume work after a restart.
func TestRunRecordSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	gated, err := functiontool.New(functiontool.Config{
		Name: "gated_write", Description: "writes", RequireConfirmation: true,
	}, func(_ agent.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"result": "ok"}, nil
	})
	if err != nil {
		t.Fatalf("functiontool.New: %v", err)
	}
	m := NewManager(dir, BuilderConfig{Models: stubModels{"m": &gatedModel{}}, ReadOnlyTools: []tool.Tool{gated}})
	invokeGraphRun(t, m, map[string]any{"spec": gatedSpec(), "input": "write notes.txt"})

	// A new Manager over the same directory is a new process, as far as the
	// store is concerned.
	reopened := NewManager(dir, BuilderConfig{Models: stubModels{"m": &gatedModel{}}, ReadOnlyTools: []tool.Tool{gated}})
	runs, err := reopened.ListRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns after reopen: %v (runs %d)", err, len(runs))
	}
	if runs[0].Status != RunWaiting || runs[0].Spec == nil || len(runs[0].Waiting) != 1 {
		t.Fatalf("reopened record lost its pause: %+v", runs[0])
	}
}

// graph_stat is how a later turn finds a run that an earlier turn left
// paused — including one from before a restart.
func TestGraphStatListsRuns(t *testing.T) {
	m := gatedManager(t, &gatedModel{})
	invokeGraphRun(t, m, map[string]any{"spec": gatedSpec(), "input": "write notes.txt"})

	out := invokeTool(t, m, "graph_stat", map[string]any{})
	for _, want := range []string{"gate", "waiting", "gated_write", "graph_resume"} {
		if !strings.Contains(out, want) {
			t.Fatalf("graph_stat output missing %q:\n%s", want, out)
		}
	}

	runs, err := m.ListRuns()
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: %v (runs %d)", err, len(runs))
	}
	detail := invokeTool(t, m, "graph_stat", map[string]any{"run_id": runs[0].RunID})
	for _, want := range []string{runs[0].RunID, "nodes:", "input: write notes.txt"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("graph_stat detail missing %q:\n%s", want, detail)
		}
	}
}
