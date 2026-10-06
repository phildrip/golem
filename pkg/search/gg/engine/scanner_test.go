package engine

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestScanner(t *testing.T) {
	tempDir := t.TempDir()

	// Setup directory structure:
	// root/
	//   .git/config
	//   node_modules/pkg/index.js
	//   .gitignore (contains: ignored.txt, build/)
	//   ignored.txt
	//   main.go
	//   src/
	//     app.go
	//     temp.log
	//   build/
	//     output.bin
	//   nested/
	//     .gitignore (contains: *.secret, !keep.secret)
	//     foo.secret
	//     keep.secret
	//     ok.txt

	dirs := []string{
		filepath.Join(tempDir, ".git"),
		filepath.Join(tempDir, "node_modules", "pkg"),
		filepath.Join(tempDir, "src"),
		filepath.Join(tempDir, "build"),
		filepath.Join(tempDir, "nested"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	files := map[string]string{
		".git/config":               "gitconfig",
		"node_modules/pkg/index.js": "console.log('hi')",
		".gitignore":                "ignored.txt\nbuild/\n*.log\n",
		"ignored.txt":               "should be ignored",
		"main.go":                   "package main",
		"src/app.go":                "package src",
		"src/temp.log":              "debug log",
		"build/output.bin":          "binary",
		"nested/.gitignore":         "*.secret\n!keep.secret\n",
		"nested/foo.secret":         "secret1",
		"nested/keep.secret":        "secret2",
		"nested/ok.txt":             "ok",
	}

	for f, content := range files {
		p := filepath.Join(tempDir, f)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	scanner := NewScanner(ScanOptions{
		RootDir:     tempDir,
		Concurrency: 4,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	sort.Strings(results)

	// Expected results:
	// .gitignore
	// main.go
	// nested/.gitignore
	// nested/keep.secret
	// nested/ok.txt
	// src/app.go
	expected := []string{
		".gitignore",
		"main.go",
		"nested/.gitignore",
		"nested/keep.secret",
		"nested/ok.txt",
		"src/app.go",
	}

	if len(results) != len(expected) {
		t.Fatalf("expected %d results %v, got %d %v", len(expected), expected, len(results), results)
	}

	for i := range expected {
		if results[i] != expected[i] {
			t.Errorf("at index %d: expected %s, got %s", i, expected[i], results[i])
		}
	}
}

func TestScannerMaxFiles(t *testing.T) {
	tempDir := t.TempDir()

	for i := 0; i < 20; i++ {
		p := filepath.Join(tempDir, "file_"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(p, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	scanner := NewScanner(ScanOptions{
		RootDir:     tempDir,
		MaxFiles:    5,
		Concurrency: 2,
	})

	results, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}

	if len(results) != 5 {
		t.Fatalf("expected exactly 5 results, got %d", len(results))
	}
}

func TestScannerEdgeCases(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Empty directory
	emptyDir := filepath.Join(tempDir, "empty")
	_ = os.MkdirAll(emptyDir, 0755)
	scannerEmpty := NewScanner(ScanOptions{RootDir: emptyDir})
	resEmpty, err := scannerEmpty.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan empty dir failed: %v", err)
	}
	if len(resEmpty) != 0 {
		t.Errorf("expected 0 files in empty dir, got %d", len(resEmpty))
	}

	// 2. Deeply nested directories
	deepDir := filepath.Join(tempDir, "a", "b", "c", "d", "e")
	_ = os.MkdirAll(deepDir, 0755)
	deepFile := filepath.Join(deepDir, "deep.txt")
	_ = os.WriteFile(deepFile, []byte("deep"), 0644)

	scannerDeep := NewScanner(ScanOptions{RootDir: filepath.Join(tempDir, "a")})
	resDeep, err := scannerDeep.Scan(context.Background())
	if err != nil {
		t.Fatalf("scan deep dir failed: %v", err)
	}
	if len(resDeep) != 1 || resDeep[0] != "b/c/d/e/deep.txt" {
		t.Errorf("expected b/c/d/e/deep.txt, got: %v", resDeep)
	}

	// 3. Invalid non-directory root
	scannerInvalid := NewScanner(ScanOptions{RootDir: deepFile})
	_, err = scannerInvalid.Scan(context.Background())
	if err == nil {
		t.Errorf("expected error when scanning a file instead of directory")
	}
}
