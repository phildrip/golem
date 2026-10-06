package engine

import (
	"fmt"
	"testing"
)

func TestInvertedIndex(t *testing.T) {
	idx := NewInvertedIndex()

	files := []string{
		"pkg/search/gg/engine/index.go",
		"pkg/search/gg/parser/parser.go",
		"cmd/golem-cli/main.go",
		"pkg/tools/gg.go",
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

	// 5. Short query (< 2 chars) returns all active documents
	matchesShort := idx.FilterCandidates("a")
	if len(matchesShort) != 5 {
		t.Errorf("expected all 5 docs for 1-char query, got: %d", len(matchesShort))
	}

	// 6. Multi-word query: "search index" matches doc 0
	matchesMulti := idx.FilterCandidates("search index")
	if len(matchesMulti) != 1 || matchesMulti[0] != 0 {
		t.Errorf("expected doc 0 for multi-word query 'search index', got: %v", matchesMulti)
	}

	// 7. Test RemoveDocument
	idx.RemoveDocument(1, files[1])
	matchesAfterRemove := idx.FilterCandidates("parser")
	if len(matchesAfterRemove) != 0 {
		t.Errorf("expected 0 matches for 'parser' after removing doc 1, got: %v", matchesAfterRemove)
	}

	// Short query after remove: must only return the 4 active docs (not doc 1)
	matchesShortAfterRemove := idx.FilterCandidates("")
	if len(matchesShortAfterRemove) != 4 {
		t.Errorf("expected 4 active docs after remove, got: %d", len(matchesShortAfterRemove))
	}
	for _, id := range matchesShortAfterRemove {
		if id == 1 {
			t.Errorf("removed doc 1 must not be returned in short query")
		}
	}

	// 8. Test Reset
	idx.Reset()
	if len(idx.FilterCandidates("config")) != 0 {
		t.Errorf("expected 0 matches after Reset")
	}
}

// TestRareBigramsRejectAbsentQueries ports FFF's rare_bigrams_reject_absent_queries_without_losing_matches test.
func TestRareBigramsRejectAbsentQueries(t *testing.T) {
	idx := NewInvertedIndex()
	const totalDocs = 4096

	for docID := 0; docID < totalDocs; docID++ {
		var path string
		switch docID {
		case 31:
			path = "marker_qq_blah.txt"
		case 500:
			path = "marker_qz_blah.txt"
		case 1000:
			path = "marker_zy_blah.txt"
		case 3000:
			path = "marker_yx_blah.txt"
		default:
			path = fmt.Sprintf("ordinary_content_%d.txt", docID)
		}
		idx.AddDocument(docID, path)
	}

	// Query containing absent combination of bigrams
	absentMatches := idx.FilterCandidates("qqzyx")
	if len(absentMatches) != 0 {
		t.Errorf("expected 0 matches for absent combined query, got: %v", absentMatches)
	}

	// Query containing present rare bigram "qq"
	presentMatches := idx.FilterCandidates("qq")
	if len(presentMatches) != 1 || presentMatches[0] != 31 {
		t.Errorf("expected exactly doc 31 for rare bigram 'qq', got: %v", presentMatches)
	}

	// Query for doc 3000 rare bigram "yx"
	yxMatches := idx.FilterCandidates("yx")
	if len(yxMatches) != 1 || yxMatches[0] != 3000 {
		t.Errorf("expected exactly doc 3000 for 'yx', got: %v", yxMatches)
	}
}
