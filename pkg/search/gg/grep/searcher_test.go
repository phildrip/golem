package grep

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSearchDataLiteral(t *testing.T) {
	content := []byte("line 1\nTarget line here\nline 3\nanother target\n")

	opts := GrepOptions{
		Pattern:       "target",
		CaseSensitive: false,
		ContextBefore: 1,
		ContextAfter:  1,
	}

	matches := SearchData(content, "test.txt", opts, []byte("target"), nil)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}

	// First match
	m1 := matches[0]
	if m1.LineNum != 2 || m1.Content != "Target line here" {
		t.Errorf("m1 mismatch: %+v", m1)
	}
	if len(m1.ContextBefore) != 1 || m1.ContextBefore[0] != "line 1" {
		t.Errorf("m1 context before mismatch: %v", m1.ContextBefore)
	}
	if len(m1.ContextAfter) != 1 || m1.ContextAfter[0] != "line 3" {
		t.Errorf("m1 context after mismatch: %v", m1.ContextAfter)
	}
}

// TestSearchDataLines_FFFPorted ports line boundary and CRLF tests from fff-grep lines.rs.
func TestSearchDataLines_FFFPorted(t *testing.T) {
	// 1. CRLF line endings
	crlf := []byte("first\r\nsecond match\r\nthird\r\n")
	opts := GrepOptions{
		Pattern:       "match",
		ContextBefore: 1,
		ContextAfter:  1,
	}
	matchesCRLF := SearchData(crlf, "crlf.txt", opts, []byte("match"), nil)
	if len(matchesCRLF) != 1 {
		t.Fatalf("expected 1 match for CRLF, got %d", len(matchesCRLF))
	}
	if matchesCRLF[0].Content != "second match" {
		t.Errorf("expected clean stripped content, got: %q", matchesCRLF[0].Content)
	}
	if len(matchesCRLF[0].ContextBefore) != 1 || matchesCRLF[0].ContextBefore[0] != "first" {
		t.Errorf("expected context before 'first', got: %v", matchesCRLF[0].ContextBefore)
	}
	if len(matchesCRLF[0].ContextAfter) != 1 || matchesCRLF[0].ContextAfter[0] != "third" {
		t.Errorf("expected context after 'third', got: %v", matchesCRLF[0].ContextAfter)
	}

	// 2. Boundary edge cases: first line match (no panic for ContextBefore)
	firstLineContent := []byte("match at line 1\nline 2\n")
	optsEdge := GrepOptions{
		Pattern:       "match",
		ContextBefore: 5,
		ContextAfter:  5,
	}
	mFirst := SearchData(firstLineContent, "edge.txt", optsEdge, []byte("match"), nil)
	if len(mFirst) != 1 || len(mFirst[0].ContextBefore) != 0 {
		t.Errorf("expected 0 context before for line 1 match, got: %v", mFirst[0].ContextBefore)
	}

	// 3. Boundary edge cases: last line match without trailing newline
	lastLineContent := []byte("line 1\nmatch at last line")
	mLast := SearchData(lastLineContent, "edge.txt", optsEdge, []byte("match"), nil)
	if len(mLast) != 1 || len(mLast[0].ContextAfter) != 0 {
		t.Errorf("expected 0 context after for last line match, got: %v", mLast[0].ContextAfter)
	}

	// 4. Empty content
	mEmpty := SearchData([]byte(""), "empty.txt", opts, []byte("match"), nil)
	if len(mEmpty) != 0 {
		t.Errorf("expected 0 matches for empty content, got %d", len(mEmpty))
	}
}

func TestSearchDataBinary(t *testing.T) {
	content := []byte{0x48, 0x65, 0x6c, 0x6c, 0x6f, 0x00, 0x57, 0x6f, 0x72, 0x6c, 0x64}
	opts := GrepOptions{
		Pattern: "Hello",
	}

	matches := SearchData(content, "bin.dat", opts, []byte("hello"), nil)
	if len(matches) != 0 {
		t.Errorf("expected binary data to be skipped, got: %v", matches)
	}
}

func TestSearchFiles(t *testing.T) {
	tempDir := t.TempDir()

	files := map[string]string{
		"a.go":   "package main\n\nfunc Run() {\n\t// TODO: implement\n}\n",
		"b.go":   "package main\n\nfunc Helper() {\n\tprintln(\"helper\")\n}\n",
		"c.txt":  "plain text with TODO comment here\n",
		"bin.so": "ELF\x00\x00binary TODO content",
	}

	for f, content := range files {
		p := filepath.Join(tempDir, f)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	allPaths := []string{"a.go", "b.go", "c.txt", "bin.so"}

	// Search TODO only in *.go files
	opts := GrepOptions{
		Pattern:     "TODO",
		FilePattern: "*.go",
		Concurrency: 2,
	}

	matches, err := SearchFiles(context.Background(), tempDir, allPaths, opts)
	if err != nil {
		t.Fatalf("search files failed: %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("expected 1 match in *.go, got %d: %+v", len(matches), matches)
	}
	if matches[0].File != "a.go" {
		t.Errorf("expected match in a.go, got: %s", matches[0].File)
	}

	// Regex search
	optsRegex := GrepOptions{
		Pattern:     `func\s+[A-Z]\w+`,
		IsRegex:     true,
		Concurrency: 2,
	}

	matchesRegex, err := SearchFiles(context.Background(), tempDir, allPaths, optsRegex)
	if err != nil {
		t.Fatalf("regex search failed: %v", err)
	}

	if len(matchesRegex) != 2 {
		t.Fatalf("expected 2 regex matches (Run, Helper), got %d: %+v", len(matchesRegex), matchesRegex)
	}

	// Hyphenated pattern test
	hyphenFile := filepath.Join(tempDir, "flags.txt")
	_ = os.WriteFile(hyphenFile, []byte("enable --debug mode\n"), 0644)
	allWithHyphen := append(allPaths, "flags.txt")

	optsHyphen := GrepOptions{
		Pattern:     "--debug",
		Concurrency: 2,
	}
	matchesHyphen, err := SearchFiles(context.Background(), tempDir, allWithHyphen, optsHyphen)
	if err != nil {
		t.Fatalf("hyphen search failed: %v", err)
	}
	if len(matchesHyphen) != 1 || matchesHyphen[0].File != "flags.txt" {
		t.Errorf("expected match for --debug in flags.txt, got: %+v", matchesHyphen)
	}

	// Test literal pattern with parentheses: "Run()"
	optsParens := GrepOptions{
		Pattern:     "Run()",
		Concurrency: 2,
	}
	matchesParens, err := SearchFiles(context.Background(), tempDir, allPaths, optsParens)
	if err != nil {
		t.Fatalf("literal parens search failed: %v", err)
	}
	if len(matchesParens) != 1 || matchesParens[0].File != "a.go" {
		t.Errorf("expected match for 'Run()' in a.go, got: %+v", matchesParens)
	}
}
