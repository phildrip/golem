package gg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGGEndToEnd(t *testing.T) {
	tempDir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(tempDir, "pkg", "core"), 0755)
	_ = os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main\n\nfunc main() {\n\t// Entrypoint\n}\n"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "pkg", "core", "server.go"), []byte("package core\n\nfunc Server() {\n\t// Server logic\n}\n"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "pkg", "core", "server_test.go"), []byte("package core\n\nfunc TestServer() {}\n"), 0644)

	// 1. Test FindFiles with fuzzy name
	results, err := FindFiles(context.Background(), tempDir, "server", 10)
	if err != nil {
		t.Fatalf("FindFiles failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 server files, got: %+v", results)
	}

	// 2. Test FindFiles with constraints: "*.go !test server"
	filtered, err := FindFiles(context.Background(), tempDir, "*.go !test server", 10)
	if err != nil {
		t.Fatalf("FindFiles filtered failed: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Path != "pkg/core/server.go" {
		t.Fatalf("expected pkg/core/server.go, got: %+v", filtered)
	}

	// 3. Test GrepFiles literal
	matches, err := GrepFiles(context.Background(), tempDir, GrepOptions{
		Pattern: "Entrypoint",
	})
	if err != nil {
		t.Fatalf("GrepFiles failed: %v", err)
	}
	if len(matches) != 1 || matches[0].File != "main.go" {
		t.Fatalf("expected main.go match, got: %+v", matches)
	}

	// 4. Test GrepFiles regex with context
	reMatches, err := GrepFiles(context.Background(), tempDir, GrepOptions{
		Pattern:       `func\s+Test\w+`,
		IsRegex:       true,
		ContextBefore: 1,
	})
	if err != nil {
		t.Fatalf("GrepFiles regex failed: %v", err)
	}
	if len(reMatches) != 1 || reMatches[0].File != "pkg/core/server_test.go" {
		t.Fatalf("expected match in server_test.go, got: %+v", reMatches)
	}
}
