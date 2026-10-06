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
}
