package bench

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultHarnessBenchmarkSuite(t *testing.T) {
	suite := DefaultSuite()
	report, err := suite.Run(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("DefaultSuite().Run failed: %v", err)
	}

	if report.Total != 4 || report.Passed != 4 || report.Failed != 0 {
		for _, r := range report.Results {
			t.Logf("Scenario %s: passed=%v err=%s steps=%v", r.Name, r.Passed, r.Error, r.Steps)
		}
		t.Fatalf("expected 4/4 scenarios to pass, got passed=%d failed=%d", report.Passed, report.Failed)
	}

	md := report.FormatMarkdown()
	for _, want := range []string{
		"# Golem Harness Benchmark Report",
		"search_to_patch_self_heal",
		"fractal_memory_recall",
		"cyclic_graph_verify_fix",
		"workspace_checkpoint_undo",
		"**PASS**",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("expected Markdown report to contain %q, got:\n%s", want, md)
		}
	}

	jsonStr, err := report.FormatJSON()
	if err != nil {
		t.Fatalf("FormatJSON failed: %v", err)
	}
	if !strings.Contains(jsonStr, `"passed": 4`) {
		t.Fatalf("expected JSON report to include '\"passed\": 4', got:\n%s", jsonStr)
	}
}

func BenchmarkFractalMemoryRecall(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		res, err := runFractalMemoryRecall(ctx, b.TempDir())
		if err != nil || len(res.Steps) == 0 {
			b.Fatalf("runFractalMemoryRecall failed: %v", err)
		}
	}
}

func BenchmarkCyclicGraphVerifyFix(b *testing.B) {
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		res, err := runCyclicGraphVerifyFix(ctx, b.TempDir())
		if err != nil || len(res.Steps) == 0 {
			b.Fatalf("runCyclicGraphVerifyFix failed: %v", err)
		}
	}
}
