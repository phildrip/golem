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

	// 1. Test FindFiles
	results, err := FindFiles(context.Background(), tempDir, "server", 10)
	if err != nil {
		t.Fatalf("FindFiles failed: %v", err)
	}
	if len(results) != 1 || results[0].Path != "pkg/core/server.go" {
		t.Fatalf("expected pkg/core/server.go, got: %+v", results)
	}

	// 2. Test GrepFiles
	matches, err := GrepFiles(context.Background(), tempDir, GrepOptions{
		Pattern: "Entrypoint",
	})
	if err != nil {
		t.Fatalf("GrepFiles failed: %v", err)
	}
	if len(matches) != 1 || matches[0].File != "main.go" {
		t.Fatalf("expected main.go match, got: %+v", matches)
	}
}
