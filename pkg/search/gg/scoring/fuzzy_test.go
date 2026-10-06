package scoring

import (
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

	// Non-matching
	_, _, mNone := FuzzyScore("xyz", "main.go")
	if mNone {
		t.Errorf("expected no match for xyz in main.go")
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
