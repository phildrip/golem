package engine

import (
	"testing"
)

func TestInvertedIndex(t *testing.T) {
	idx := NewInvertedIndex()

	files := []string{
		"pkg/search/gg/engine/index.go",
		"pkg/search/gg/parser/parser.go",
		"cmd/golem-cli/main.go",
		"pkg/tools/fff.go",
		"pkg/config/config.go",
	}

	for i, f := range files {
		idx.AddDocument(i, f)
	}

	// 1. Query with 'parser' should match doc 1 only
	matches := idx.FilterCandidates("parser")
	if len(matches) != 1 || matches[0] != 1 {
		t.Errorf("expected doc 1 for 'parser', got: %v", matches)
	}

	// 2. Query with 'config' should match doc 4 only
	matchesConfig := idx.FilterCandidates("config")
	if len(matchesConfig) != 1 || matchesConfig[0] != 4 {
		t.Errorf("expected doc 4 for 'config', got: %v", matchesConfig)
	}

	// 3. Query with '.go' should match all 5 docs
	matchesAll := idx.FilterCandidates(".go")
	if len(matchesAll) != 5 {
		t.Errorf("expected 5 matches for '.go', got: %d", len(matchesAll))
	}

	// 4. Query with non-existent bigram 'zz' should return 0 matches
	matchesNone := idx.FilterCandidates("zz")
	if len(matchesNone) != 0 {
		t.Errorf("expected 0 matches for 'zz', got: %v", matchesNone)
	}

	// 5. Test RemoveDocument
	idx.RemoveDocument(1, files[1])
	matchesAfterRemove := idx.FilterCandidates("parser")
	if len(matchesAfterRemove) != 0 {
		t.Errorf("expected 0 matches for 'parser' after removing doc 1, got: %v", matchesAfterRemove)
	}
}
