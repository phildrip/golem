package scoring

import (
	"fmt"
	"sort"
	"testing"
)

func TestFastSubsequenceCheck(t *testing.T) {
	cases := []struct {
		pattern string
		target  string
		want    bool
	}{
		{"", "anything", true},
		{"abc", "aabbcc", true},
		{"abc", "aXbXc", true},
		{"abc", "acb", false},
		{"main", "cmd/golem/main.go", true},
		{"pkg", "README.md", false},
		{"GO", "main.go", true}, // case-insensitive
		{"コント", "日本語/コントローラー.tsx", true}, // UTF-8 multibyte
	}

	for _, c := range cases {
		got := FastSubsequenceCheck(c.pattern, c.target)
		if got != c.want {
			t.Errorf("FastSubsequenceCheck(%q, %q) = %v, want %v", c.pattern, c.target, got, c.want)
		}
	}
}

func TestFuzzyScore(t *testing.T) {
	// Exact matches should score highest
	sExact, _, mExact := FuzzyScore("main.go", "main.go")
	if !mExact || sExact <= 0 {
		t.Fatalf("expected exact match, got m=%v, s=%d", mExact, sExact)
	}

	sNested, _, mNested := FuzzyScore("main.go", "cmd/tools/main.go")
	if !mNested {
		t.Fatalf("expected match for nested file")
	}

	if sExact <= sNested {
		t.Errorf("exact file match score %d should be higher than nested file score %d", sExact, sNested)
	}

	// Boundary bonuses: "conf" matching "config.go" vs "foo_config.go"
	sBoundary, _, mBoundary := FuzzyScore("conf", "config.go")
	if !mBoundary || sBoundary <= 0 {
		t.Fatalf("expected boundary match")
	}

	// CamelCase bonus: "FB" should match "FooBar.go" with high score
	sCamel, _, mCamel := FuzzyScore("FB", "pkg/core/FooBar.go")
	if !mCamel || sCamel <= 0 {
		t.Fatalf("expected CamelCase match")
	}

	// Non-matching
	_, _, mNone := FuzzyScore("xyz", "main.go")
	if mNone {
		t.Errorf("expected no match for xyz in main.go")
	}
}

// TestFuzzyParallelAndSequentialMatch ports FFF's fuzzy_parallel_and_sequential_scores_match test.
func TestFuzzyParallelAndSequentialMatch(t *testing.T) {
	var paths []string
	for i := 0; i < 2000; i++ {
		paths = append(paths, fmt.Sprintf("src/components/group_%d/controller_é_%d.tsx", i/100, i))
	}

	query := "controller"

	// Sequential scores
	seqResults := make([]MatchResult, len(paths))
	for i, p := range paths {
		score, pos, matched := FuzzyScore(query, p)
		seqResults[i] = MatchResult{
			Path:      p,
			Score:     score,
			Positions: pos,
			Matched:   matched,
		}
	}

	// Parallel scores (4 workers)
	parResults := ParallelMatchScores(query, paths, 4)

	if len(seqResults) != len(parResults) {
		t.Fatalf("length mismatch: seq %d, par %d", len(seqResults), len(parResults))
	}

	for i := range seqResults {
		if seqResults[i].Matched != parResults[i].Matched {
			t.Errorf("at index %d: matched mismatch (seq %v, par %v)", i, seqResults[i].Matched, parResults[i].Matched)
		}
		if seqResults[i].Score != parResults[i].Score {
			t.Errorf("at index %d: score mismatch (seq %d, par %d)", i, seqResults[i].Score, parResults[i].Score)
		}
		if len(seqResults[i].Positions) != len(parResults[i].Positions) {
			t.Errorf("at index %d: positions len mismatch", i)
		}
	}
}

func TestParallelMatchScores(t *testing.T) {
	paths := []string{
		"cmd/main.go",
		"pkg/search/gg/scoring/fuzzy.go",
		"pkg/search/gg/engine/picker.go",
		"pkg/config/config.go",
		"README.md",
	}

	results := ParallelMatchScores("scor", paths, 2)
	var matched []MatchResult
	for _, r := range results {
		if r.Matched {
			matched = append(matched, r)
		}
	}

	if len(matched) != 1 {
		t.Fatalf("expected 1 match for 'scor', got %d", len(matched))
	}
	if matched[0].Path != "pkg/search/gg/scoring/fuzzy.go" {
		t.Errorf("expected fuzzy.go, got: %s", matched[0].Path)
	}

	// Score ordering test
	resultsGo := ParallelMatchScores("main", paths, 4)
	sort.Slice(resultsGo, func(i, j int) bool {
		return resultsGo[i].Score > resultsGo[j].Score
	})

	if !resultsGo[0].Matched || resultsGo[0].Path != "cmd/main.go" {
		t.Errorf("expected top result to be cmd/main.go, got: %v", resultsGo[0])
	}
}
