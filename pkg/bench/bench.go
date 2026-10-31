package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/user/golem/pkg/checkpoint"
	"github.com/user/golem/pkg/graph"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/tools"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/genai"
)

// ScenarioResult captures the outcome and telemetry of a single benchmark scenario.
type ScenarioResult struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Passed      bool              `json:"passed"`
	Duration    time.Duration     `json:"duration"`
	Steps       []string          `json:"steps"`
	Metrics     map[string]any    `json:"metrics,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// SuiteReport summarizes an entire benchmark suite execution.
type SuiteReport struct {
	StartedAt time.Time        `json:"started_at"`
	Duration  time.Duration    `json:"duration"`
	Total     int              `json:"total"`
	Passed    int              `json:"passed"`
	Failed    int              `json:"failed"`
	Results   []ScenarioResult `json:"results"`
}

// Scenario defines a single reproducible evaluation task.
type Scenario struct {
	Name        string
	Description string
	Run         func(ctx context.Context, workDir string) (ScenarioResult, error)
}

// Suite holds an ordered collection of harness benchmark scenarios.
type Suite struct {
	Scenarios []Scenario
}

// DefaultSuite returns the standard 4-pillar Golem harness evaluation suite:
//  1. search_to_patch_self_heal (1-hop navigation + hashline edit + AST diagnostic recovery)
//  2. fractal_memory_recall (long-horizon compaction + BM25/GUID search + zoom expansion)
//  3. cyclic_graph_verify_fix (bounded cyclic verify->fix workflow + prompt cache telemetry)
//  4. workspace_checkpoint_undo (shadow git index snapshot + diff + atomic rollback)
func DefaultSuite() *Suite {
	return &Suite{
		Scenarios: []Scenario{
			{
				Name:        "search_to_patch_self_heal",
				Description: "1-hop grep/symbol_outline navigation to hashline_patch with syntax diagnostic self-healing and go test verification",
				Run:         runSearchToPatchSelfHeal,
			},
			{
				Name:        "fractal_memory_recall",
				Description: "Long-horizon multi-session fractal compaction with BM25 + TurnID/SessionID search and lossless zoom recall",
				Run:         runFractalMemoryRecall,
			},
			{
				Name:        "cyclic_graph_verify_fix",
				Description: "Bounded cyclic plan->code->verify->fix workflow graph execution with node activation & token cache telemetry",
				Run:         runCyclicGraphVerifyFix,
			},
			{
				Name:        "workspace_checkpoint_undo",
				Description: "Zero-pollution shadow git tree checkpoint creation, untracked+tracked diffing, and byte-identical rollback",
				Run:         runWorkspaceCheckpointUndo,
			},
		},
	}
}

// Run executes all scenarios in the suite using isolated temporary directories under rootDir.
// If rootDir is empty, a temporary directory is created and cleaned up automatically.
func (s *Suite) Run(ctx context.Context, rootDir string) (*SuiteReport, error) {
	cleanup := func() {}
	if rootDir == "" {
		tmp, err := os.MkdirTemp("", "golem-bench-*")
		if err != nil {
			return nil, fmt.Errorf("creating benchmark temp dir: %w", err)
		}
		rootDir = tmp
		cleanup = func() { _ = os.RemoveAll(tmp) }
	}
	defer cleanup()

	start := time.Now()
	report := &SuiteReport{
		StartedAt: start.UTC(),
		Total:     len(s.Scenarios),
		Results:   make([]ScenarioResult, 0, len(s.Scenarios)),
	}

	for _, sc := range s.Scenarios {
		scDir := filepath.Join(rootDir, sc.Name)
		if err := os.MkdirAll(scDir, 0755); err != nil {
			return nil, fmt.Errorf("creating scenario dir %s: %w", scDir, err)
		}

		scStart := time.Now()
		res, err := sc.Run(ctx, scDir)
		res.Name = sc.Name
		res.Description = sc.Description
		res.Duration = time.Since(scStart)
		if err != nil {
			res.Passed = false
			res.Error = err.Error()
			report.Failed++
		} else {
			res.Passed = true
			report.Passed++
		}
		report.Results = append(report.Results, res)
	}

	report.Duration = time.Since(start)
	return report, nil
}

// FormatMarkdown renders the suite report as a concise Markdown table and step log.
func (r *SuiteReport) FormatMarkdown() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Golem Harness Benchmark Report\n\n")
	fmt.Fprintf(&sb, "- **Total Scenarios**: %d (%d passed, %d failed)\n", r.Total, r.Passed, r.Failed)
	fmt.Fprintf(&sb, "- **Total Duration**: `%s`\n\n", r.Duration.Round(time.Millisecond))
	sb.WriteString("| Scenario | Status | Duration | Key Metrics |\n")
	sb.WriteString("| :--- | :--- | :--- | :--- |\n")
	for _, res := range r.Results {
		status := "PASS"
		if !res.Passed {
			status = "FAIL"
		}
		var metricPairs []string
		for k, v := range res.Metrics {
			metricPairs = append(metricPairs, fmt.Sprintf("%s=%v", k, v))
		}
		sb.WriteString(fmt.Sprintf("| `%s` | **%s** | `%s` | %s |\n",
			res.Name, status, res.Duration.Round(time.Millisecond), strings.Join(metricPairs, ", ")))
	}
	return sb.String()
}

// FormatJSON serializes the report as indented JSON.
func (r *SuiteReport) FormatJSON() (string, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// -----------------------------------------------------------------------------
// Scenario 1: 1-Hop Search-to-Patch & Syntax Self-Healing
// -----------------------------------------------------------------------------

func runSearchToPatchSelfHeal(ctx context.Context, workDir string) (ScenarioResult, error) {
	res := ScenarioResult{
		Metrics: make(map[string]any),
	}

	goMod := "module example.com/benchcalc\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte(goMod), 0644); err != nil {
		return res, err
	}

	buggySrc := `package benchcalc

import "errors"

// ErrDivideByZero is returned when dividing by zero.
var ErrDivideByZero = errors.New("divide by zero")

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}

// Divide returns a / b or ErrDivideByZero when b == 0.
func Divide(a, b int) (int, error) {
	// BUG: multiplies instead of dividing and ignores zero divisor
	return a * b, nil
}
`
	testSrc := `package benchcalc

import "testing"

func TestDivide(t *testing.T) {
	got, err := Divide(20, 4)
	if err != nil || got != 5 {
		t.Fatalf("Divide(20, 4) = (%d, %v), want (5, nil)", got, err)
	}
	if _, err := Divide(10, 0); err != ErrDivideByZero {
		t.Fatalf("Divide(10, 0) err = %v, want ErrDivideByZero", err)
	}
}
`
	if err := os.WriteFile(filepath.Join(workDir, "calc.go"), []byte(buggySrc), 0644); err != nil {
		return res, err
	}
	if err := os.WriteFile(filepath.Join(workDir, "calc_test.go"), []byte(testSrc), 0644); err != nil {
		return res, err
	}

	// Step 1: Locate buggy line in 1 hop via GrepFiles (verify hashline anchor is emitted directly).
	grepOut, err := tools.GrepFiles(workDir, "", "multiplies instead of dividing", "*.go", 10)
	if err != nil {
		return res, fmt.Errorf("GrepFiles failed: %w", err)
	}
	if len(grepOut.Matches) != 1 || grepOut.Matches[0].Anchor == "" {
		return res, fmt.Errorf("expected 1 hashline-anchored grep match, got %+v", grepOut.Matches)
	}
	res.Steps = append(res.Steps, fmt.Sprintf("grep_files located bug at %s (%s) in 1 hop", grepOut.Matches[0].File, grepOut.Matches[0].Anchor))

	// Step 2: Extract symbol outline to get exact StartAnchor..EndAnchor of Divide.
	outline, err := tools.ExtractSymbolOutline(workDir, "calc.go")
	if err != nil {
		return res, fmt.Errorf("ExtractSymbolOutline failed: %w", err)
	}
	var divideSym *tools.SymbolEntry
	for i := range outline.Symbols {
		if outline.Symbols[i].Name == "Divide" {
			divideSym = &outline.Symbols[i]
			break
		}
	}
	if divideSym == nil || divideSym.StartAnchor == "" || divideSym.EndAnchor == "" {
		return res, fmt.Errorf("Divide symbol not found with anchors in outline: %+v", outline.Symbols)
	}
	rangeAnchor := divideSym.StartAnchor + ".." + divideSym.EndAnchor
	res.Steps = append(res.Steps, fmt.Sprintf("symbol_outline resolved Divide span %s without full file read", rangeAnchor))
	res.Metrics["navigation_hops"] = 1

	// Step 3: Apply a malformed patch (missing closing parenthesis/brace) to verify immediate AST syntax diagnostics.
	malformedCode := `func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, ErrDivideByZero
	}
	return a / b, nil` // missing closing brace
	badPatch, err := tools.PatchFileWithHashline(workDir, "calc.go", "SWAP", rangeAnchor, malformedCode, false)
	if err != nil {
		return res, fmt.Errorf("PatchFileWithHashline (malformed) unexpected error: %w", err)
	}
	if len(badPatch.Diagnostics) == 0 {
		return res, fmt.Errorf("expected post-edit syntax diagnostics on malformed Go patch, got none")
	}
	res.Steps = append(res.Steps, fmt.Sprintf("hashline_patch caught syntax error immediately: %s", badPatch.Diagnostics[0]))
	res.Metrics["diagnostics_caught"] = len(badPatch.Diagnostics)

	// Step 4: Self-heal using fresh anchors from ReadFileWithHashlines around the broken function.
	readBack, err := tools.ReadFileWithHashlines(workDir, "calc.go", divideSym.StartLine, 10)
	if err != nil || len(readBack.Lines) == 0 {
		return res, fmt.Errorf("ReadFileWithHashlines for self-heal failed: %w", err)
	}
	healRange := readBack.Lines[0].Anchor + ".." + readBack.Lines[len(readBack.Lines)-1].Anchor
	fixedCode := `func Divide(a, b int) (int, error) {
	if b == 0 {
		return 0, ErrDivideByZero
	}
	return a / b, nil
}`
	goodPatch, err := tools.PatchFileWithHashline(workDir, "calc.go", "SWAP", healRange, fixedCode, false)
	if err != nil {
		return res, fmt.Errorf("PatchFileWithHashline (self-heal) failed: %w", err)
	}
	if !goodPatch.Success || len(goodPatch.Diagnostics) != 0 {
		return res, fmt.Errorf("expected clean self-healed patch, got diagnostics: %v", goodPatch.Diagnostics)
	}
	res.Steps = append(res.Steps, "self-healed Divide with clean AST diagnostics")

	// Step 5: Run `go test` in the workspace to verify behavioral correctness.
	cmd := exec.CommandContext(ctx, "go", "test", "./...")
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return res, fmt.Errorf("go test failed after self-heal: %w\n%s", err, string(out))
	}
	res.Steps = append(res.Steps, "verified `go test ./...` passes in patched workspace")
	res.Metrics["self_healed"] = true
	return res, nil
}

// -----------------------------------------------------------------------------
// Scenario 2: Long-Horizon Multi-Session Fractal Memory Recall
// -----------------------------------------------------------------------------

func runFractalMemoryRecall(_ context.Context, workDir string) (ScenarioResult, error) {
	res := ScenarioResult{
		Metrics: make(map[string]any),
	}

	storeDir := filepath.Join(workDir, "memory")
	store, err := fractal.OpenStore(storeDir)
	if err != nil {
		return res, fmt.Errorf("OpenStore: %w", err)
	}
	defer store.Close()

	baseTime := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	sessions := []string{"sess-arch", "sess-impl", "sess-perf"}

	// Seed 48 events across 3 sessions (16 turns of 3 events each).
	// Event #4..6 (Turn "turn-01" in "sess-arch") holds our needle invariant.
	totalEvents := 48
	needleTurn := "turn-01"
	needleSession := "sess-arch"
	needlePhrase := "INVARIANT-7749: All cross-node RPC frames must carry a 64-bit monotonic Lamport clock in header X-Golem-Lamport."

	for i := 0; i < totalEvents; i++ {
		turnIdx := i / 3
		sessID := sessions[turnIdx%len(sessions)]
		turnID := fmt.Sprintf("turn-%02d", turnIdx)
		ts := baseTime.Add(time.Duration(i) * 15 * time.Minute)

		kind := fractal.KindUser
		text := fmt.Sprintf("Routine discussion message #%d in %s (%s) covering general repository scaffolding.", i, sessID, turnID)
		if i%3 == 1 {
			kind = fractal.KindTool
			text = fmt.Sprintf("hashline_read: file_%d.go lines 1..40", i)
		} else if i%3 == 2 {
			kind = fractal.KindTalk
			text = fmt.Sprintf("Completed routine step #%d in %s.", i, turnID)
		}

		if i == 5 {
			kind = fractal.KindTalk
			sessID = needleSession
			turnID = needleTurn
			text = needlePhrase
		}

		if _, err := store.AppendEvent(kind, text, fractal.AppendEventOpts{
			Timestamp: ts,
			SessionID: sessID,
			TurnID:    turnID,
		}); err != nil {
			return res, fmt.Errorf("AppendEvent(%d): %w", i, err)
		}
	}

	// Populate binary summary tree nodes covering the older events [0..32) so the view tiles them.
	for idx := 0; idx < 16; idx++ {
		_ = store.SaveNode(fractal.Node{
			Level: 1,
			Index: idx,
			Text:  fmt.Sprintf("L1 summary for events %d..%d", idx*2, idx*2+1),
		})
	}
	for idx := 0; idx < 8; idx++ {
		summary := fmt.Sprintf("L2 summary for events %d..%d", idx*4, idx*4+3)
		if idx == 1 {
			summary = "Architecture decision: RPC frames require X-Golem-Lamport header (see 4+4)"
		}
		_ = store.SaveNode(fractal.Node{
			Level: 2,
			Index: idx,
			Text:  summary,
		})
	}

	viewCfg := fractal.ViewConfig{
		ViewBudgetBytes: 4096,
		LowWaterBytes:   2048,
		LiveHeadCount:   8,
	}
	view := fractal.NewView(store, viewCfg)
	view.Sync()

	// Verify event #5 is compacted out of the live head (live head only contains recent events >= 40).
	liveHead := view.LiveHeadEvents()
	for _, ev := range liveHead {
		if ev.ID == 5 {
			return res, fmt.Errorf("expected needle event #5 to be compacted out of live head, but found in LiveHeadEvents")
		}
	}
	res.Steps = append(res.Steps, fmt.Sprintf("seeded %d events across 3 sessions; needle event #5 compacted out of live head (%d live events)", totalEvents, len(liveHead)))

	// Close and reopen store to simulate cross-session persistence.
	if err := store.Close(); err != nil {
		return res, fmt.Errorf("closing store before reopen: %w", err)
	}
	reopened, err := fractal.OpenStore(storeDir)
	if err != nil {
		return res, fmt.Errorf("reopening fractal store: %w", err)
	}
	defer reopened.Close()

	reopenedView := fractal.NewView(reopened, viewCfg)
	mgr := fractal.NewMemoryManager(reopened, reopenedView)

	// Step 1: BM25 search with IncludeNodes=true to locate both the verbatim needle and L2 summary node.
	searchRes, err := fractal.Search(mgr, fractal.SearchArgs{
		Query:        "INVARIANT-7749 Lamport clock X-Golem-Lamport",
		IncludeNodes: true,
		Limit:        5,
	})
	if err != nil {
		return res, fmt.Errorf("fractal.Search failed: %w", err)
	}
	if len(searchRes.Matches) == 0 {
		return res, fmt.Errorf("expected BM25 matches for needle query, got 0")
	}
	topHit := searchRes.Matches[0]
	if topHit.Address != "5+1" || topHit.TurnID != needleTurn || topHit.SessionID != needleSession {
		return res, fmt.Errorf("top hit = %+v, want Address=5+1 TurnID=%s SessionID=%s", topHit, needleTurn, needleSession)
	}
	res.Steps = append(res.Steps, fmt.Sprintf("memory_search ranked needle 5+1 #1 (score=%.2f, turn=%s, session=%s)", topHit.Score, topHit.TurnID, topHit.SessionID))

	// Step 2: Zoom into 4+4 -> 4+2 & 6+2 -> 5+1 to verify hierarchical tree unpacking.
	zoomL2, err := fractal.Zoom(mgr, fractal.ZoomArgs{ID: 4, N: 4})
	if err != nil || !strings.Contains(zoomL2.Result, "4+2|") {
		return res, fmt.Errorf("Zoom(4+4) failed: %v (%+v)", err, zoomL2)
	}
	zoomLeaf, err := fractal.Zoom(mgr, fractal.ZoomArgs{ID: 5, N: 1})
	if err != nil || !strings.Contains(zoomLeaf.Result, needlePhrase) {
		return res, fmt.Errorf("Zoom(5+1) failed to recover verbatim needle: %v (%+v)", err, zoomLeaf)
	}
	res.Steps = append(res.Steps, "zoom unpacked 4+4 -> 5+1 and recovered exact verbatim invariant")

	// Step 3: Verify O(1) TurnID transaction lookup recovers all 3 events (user, tool, talk) of turn-01.
	turnEvents := reopened.GetEventsByTurn(needleTurn)
	if len(turnEvents) != 3 {
		return res, fmt.Errorf("expected 3 events in %s, got %d", needleTurn, len(turnEvents))
	}
	res.Steps = append(res.Steps, fmt.Sprintf("turn index recovered all %d events for transaction %s across restart", len(turnEvents), needleTurn))

	res.Metrics["total_events"] = totalEvents
	res.Metrics["top_hit_address"] = topHit.Address
	res.Metrics["turn_events_recovered"] = len(turnEvents)
	return res, nil
}

// -----------------------------------------------------------------------------
// Scenario 3: Bounded Cyclic Verify-Fix Workflow Graph with Cache Telemetry
// -----------------------------------------------------------------------------

type benchSeqModel struct {
	name      string
	responses []string
	tokens    int32
	cached    int32
	calls     int
}

func (m *benchSeqModel) Name() string { return m.name }

func (m *benchSeqModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		idx := m.calls
		m.calls++
		if idx >= len(m.responses) {
			idx = len(m.responses) - 1
		}
		resp := &model.LLMResponse{
			Content: genai.NewContentFromText(m.responses[idx], genai.RoleModel),
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
				TotalTokenCount:         m.tokens,
				PromptTokenCount:        m.tokens,
				CachedContentTokenCount: m.cached,
			},
		}
		yield(resp, nil)
	}
}

type benchStubModels map[string]model.LLM

func (s benchStubModels) GetModel(_ context.Context, requested string) (model.LLM, string, error) {
	m, ok := s[requested]
	if !ok {
		return nil, "", fmt.Errorf("unknown benchmark model %q", requested)
	}
	return m, requested, nil
}

type benchToolCaller struct {
	tool string
	args map[string]any
}

func (m *benchToolCaller) Name() string { return "bench-orchestrator" }

func (m *benchToolCaller) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
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
				ID:   "call-graph-run",
				Name: m.tool,
				Args: m.args,
			}}},
		}}, nil)
	}
}

func runCyclicGraphVerifyFix(ctx context.Context, workDir string) (ScenarioResult, error) {
	res := ScenarioResult{
		Metrics: make(map[string]any),
	}

	planner := &benchSeqModel{
		name:      "planner",
		responses: []string{"PLAN: implement rate limiter with token bucket"},
		tokens:    120,
		cached:    80,
	}
	coder := &benchSeqModel{
		name:      "coder",
		responses: []string{"IMPL v1: unlocked bucket counter", "IMPL v2: mutex-guarded bucket counter"},
		tokens:    200,
		cached:    150,
	}
	verifier := &benchSeqModel{
		name: "verifier",
		responses: []string{
			"FAIL: race detected on bucket counter\nROUTE: retry",
			"PASS: go test -race clean\nROUTE: pass",
		},
		tokens: 160,
		cached: 120,
	}
	summarizer := &benchSeqModel{
		name:      "summarizer",
		responses: []string{"SUMMARY: mutex-guarded token bucket verified race-free"},
		tokens:    100,
		cached:    70,
	}

	gm := graph.NewManager(workDir, graph.BuilderConfig{
		Models: benchStubModels{
			"m_plan":      planner,
			"m_code":      coder,
			"m_verify":    verifier,
			"m_summarize": summarizer,
		},
	})

	spec := &graph.Spec{
		V:              1,
		Name:           "bench_verify_fix_cycle",
		MaxConcurrency: 1,
		Budgets: &graph.Budgets{
			MaxNodes:       8,
			MaxWallSeconds: 60,
			MaxTokens:      50_000,
		},
		Nodes: []graph.Node{
			{ID: "plan", Kind: graph.KindAgent, Role: graph.RoleGeneral, Tools: graph.ToolsNone, Instruction: "plan", Model: "m_plan"},
			{ID: "coder", Kind: graph.KindAgent, Role: graph.RoleCoder, Tools: graph.ToolsNone, Instruction: "code", Model: "m_code"},
			{ID: "verifier", Kind: graph.KindAgent, Role: graph.RoleCritic, Tools: graph.ToolsNone, Instruction: "verify", Model: "m_verify"},
			{ID: "summarizer", Kind: graph.KindAgent, Role: graph.RoleGeneral, Tools: graph.ToolsNone, Instruction: "summarize", Model: "m_summarize"},
		},
		Edges: [][]string{
			{"START", "plan"},
			{"plan", "coder"},
			{"coder", "verifier"},
			{"verifier", "coder", "retry"},
			{"verifier", "summarizer", "pass"},
		},
	}

	graphTools, err := graph.NewTools(gm)
	if err != nil {
		return res, fmt.Errorf("graph.NewTools: %w", err)
	}
	parent, err := llmagent.New(llmagent.Config{
		Name:  "bench_parent",
		Model: &benchToolCaller{tool: "graph_run", args: map[string]any{"spec": spec, "input": "build rate limiter"}},
		Tools: graphTools,
	})
	if err != nil {
		return res, fmt.Errorf("llmagent.New: %w", err)
	}
	r, err := runner.NewInMemory("bench-graph", parent)
	if err != nil {
		return res, fmt.Errorf("runner.NewInMemory: %w", err)
	}

	for _, err := range r.Run(ctx, "bench-user", "bench-sess", genai.NewContentFromText("go", genai.RoleUser), agent.RunConfig{StreamingMode: agent.StreamingModeNone}) {
		if err != nil {
			return res, fmt.Errorf("runner.Run: %w", err)
		}
	}

	runs, err := gm.ListRuns()
	if err != nil || len(runs) != 1 {
		return res, fmt.Errorf("expected 1 persisted graph RunRecord, got %d (err=%v)", len(runs), err)
	}
	rec := runs[0]
	if rec.Status != graph.RunCompleted {
		return res, fmt.Errorf("expected RunCompleted, got %s (err=%s)", rec.Status, rec.Err)
	}
	if coder.calls != 2 || verifier.calls != 2 {
		return res, fmt.Errorf("expected coder=2 and verifier=2 calls in retry loop, got coder=%d verifier=%d", coder.calls, verifier.calls)
	}
	if rec.Activations != 6 {
		return res, fmt.Errorf("expected 6 total node activations (1+2+2+1), got %d", rec.Activations)
	}
	if rec.CachedTokens <= 0 || rec.CacheHitRatio <= 0 {
		return res, fmt.Errorf("expected positive cache telemetry, got cached=%d ratio=%.2f", rec.CachedTokens, rec.CacheHitRatio)
	}

	res.Steps = append(res.Steps,
		"validated cyclic spec (verifier -> coder on route=retry, verifier -> summarizer on route=pass)",
		fmt.Sprintf("executed 2-pass verify/fix loop: %d node activations across 4 distinct nodes", rec.Activations),
		fmt.Sprintf("recorded prompt cache telemetry: %d/%d cached tokens (%.1f%% hit ratio)", rec.CachedTokens, rec.Tokens, rec.CacheHitRatio*100),
	)
	res.Metrics["activations"] = rec.Activations
	res.Metrics["tokens"] = rec.Tokens
	res.Metrics["cached_tokens"] = rec.CachedTokens
	res.Metrics["cache_hit_ratio"] = fmt.Sprintf("%.1f%%", rec.CacheHitRatio*100)
	return res, nil
}

// -----------------------------------------------------------------------------
// Scenario 4: Shadow Git Workspace Checkpoint Diff & Restore
// -----------------------------------------------------------------------------

func runWorkspaceCheckpointUndo(ctx context.Context, workDir string) (ScenarioResult, error) {
	res := ScenarioResult{
		Metrics: make(map[string]any),
	}

	runGit := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	if err := runGit("init"); err != nil {
		return res, err
	}
	if err := runGit("config", "user.email", "bench@golem.local"); err != nil {
		return res, err
	}
	if err := runGit("config", "user.name", "Golem Bench"); err != nil {
		return res, err
	}

	origMain := "package main\n\nfunc main() {\n\tprintln(\"clean v1\")\n}\n"
	origConfig := "env: production\nworkers: 4\n"
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte(origMain), 0644); err != nil {
		return res, err
	}
	if err := os.WriteFile(filepath.Join(workDir, "config.yaml"), []byte(origConfig), 0644); err != nil {
		return res, err
	}
	if err := runGit("add", "main.go", "config.yaml"); err != nil {
		return res, err
	}
	if err := runGit("commit", "-m", "initial commit"); err != nil {
		return res, err
	}

	mgr := checkpoint.NewManager(workDir)
	if !mgr.Available() {
		return res, fmt.Errorf("checkpoint.Manager reported unavailable in initialized git repo")
	}

	// Step 1: Create pre-turn shadow snapshot.
	snap, err := mgr.Create("turn-risky-refactor", "pre-refactor")
	if err != nil {
		return res, fmt.Errorf("mgr.Create failed: %w", err)
	}
	res.Steps = append(res.Steps, fmt.Sprintf("created shadow checkpoint %s (tree %s)", snap.ID, snap.TreeHash[:12]))

	// Step 2: Destructively mutate workspace (corrupt main.go, delete config.yaml, add untracked rogue file).
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n// corrupted\n"), 0644); err != nil {
		return res, err
	}
	if err := os.Remove(filepath.Join(workDir, "config.yaml")); err != nil {
		return res, err
	}
	roguePath := filepath.Join(workDir, "rogue_generated.tmp")
	if err := os.WriteFile(roguePath, []byte("untracked garbage\n"), 0644); err != nil {
		return res, err
	}

	// Step 3: Verify Diff captures all 3 changes (modified main.go, deleted config.yaml, added rogue_generated.tmp).
	diffText, _, err := mgr.Diff(snap.ID)
	if err != nil {
		return res, fmt.Errorf("mgr.Diff failed: %w", err)
	}
	for _, wantFile := range []string{"main.go", "config.yaml", "rogue_generated.tmp"} {
		if !strings.Contains(diffText, wantFile) {
			return res, fmt.Errorf("expected diff to mention %q, got:\n%s", wantFile, diffText)
		}
	}
	res.Steps = append(res.Steps, "verified shadow diff captured modified tracked file, deleted tracked file, and new untracked file")

	// Step 4: Restore checkpoint and verify byte-identical recovery + untracked file cleanup.
	if _, err := mgr.Restore(snap.ID); err != nil {
		return res, fmt.Errorf("mgr.Restore failed: %w", err)
	}

	restoredMain, err := os.ReadFile(filepath.Join(workDir, "main.go"))
	if err != nil || string(restoredMain) != origMain {
		return res, fmt.Errorf("main.go not restored byte-for-byte: got %q, want %q", string(restoredMain), origMain)
	}
	restoredConfig, err := os.ReadFile(filepath.Join(workDir, "config.yaml"))
	if err != nil || string(restoredConfig) != origConfig {
		return res, fmt.Errorf("config.yaml not restored byte-for-byte: got %q, want %q", string(restoredConfig), origConfig)
	}
	if _, err := os.Stat(roguePath); !os.IsNotExist(err) {
		return res, fmt.Errorf("expected untracked file %s to be removed on restore", roguePath)
	}

	postDiff, _, err := mgr.Diff(snap.ID)
	if err != nil || postDiff != "" {
		return res, fmt.Errorf("expected empty diff after restore, got: %q (err=%v)", postDiff, err)
	}
	res.Steps = append(res.Steps, "restored workspace byte-for-byte and pruned untracked artifact without touching git commit history")
	res.Metrics["files_restored"] = 3
	return res, nil
}
