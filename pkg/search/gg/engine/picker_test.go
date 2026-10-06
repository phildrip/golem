package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilePickerOperations(t *testing.T) {
	tempDir := t.TempDir()

	// Create test files
	file1 := filepath.Join(tempDir, "main.go")
	file2 := filepath.Join(tempDir, "pkg", "tools", "fff.go")
	file3 := filepath.Join(tempDir, "README.md")

	if err := os.MkdirAll(filepath.Dir(file2), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(file1, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file2, []byte("package tools"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file3, []byte("# Readme"), 0644); err != nil {
		t.Fatal(err)
	}

	picker, err := NewFilePicker(context.Background(), FilePickerOptions{
		RootDir:       tempDir,
		EnableWatcher: false,
	})
	if err != nil {
		t.Fatalf("failed to create FilePicker: %v", err)
	}
	defer picker.Close()

	if picker.Len() != 3 {
		t.Fatalf("expected 3 files, got %d", picker.Len())
	}

	if !picker.HasFile("main.go") {
		t.Errorf("expected picker to have main.go")
	}
	if !picker.HasFile("pkg/tools/fff.go") {
		t.Errorf("expected picker to have pkg/tools/fff.go")
	}

	// Test QueryCandidates
	candGo := picker.QueryCandidates("*.go")
	if len(candGo) != 2 {
		t.Errorf("expected 2 .go files, got %d: %v", len(candGo), candGo)
	}

	candTools := picker.QueryCandidates("tools/ *.go")
	if len(candTools) != 1 || candTools[0] != "pkg/tools/fff.go" {
		t.Errorf("expected pkg/tools/fff.go, got: %v", candTools)
	}

	// Test RemoveFile
	picker.RemoveFile("README.md")
	if picker.HasFile("README.md") {
		t.Errorf("expected README.md to be removed")
	}
	if picker.Len() != 2 {
		t.Errorf("expected 2 files after removal, got %d", picker.Len())
	}

	// Test AddFile
	picker.AddFile("cmd/app/main.go")
	if !picker.HasFile("cmd/app/main.go") {
		t.Errorf("expected cmd/app/main.go to be present")
	}
	if picker.Len() != 3 {
		t.Errorf("expected 3 files after AddFile, got %d", picker.Len())
	}

	// Test Search ranking
	res := picker.Search("main", 5)
	if len(res) == 0 {
		t.Fatalf("expected search results for 'main'")
	}
	// Prioritize main.go over cmd/app/main.go due to exact basename match
	if res[0].Path != "main.go" {
		t.Errorf("expected top result to be main.go, got: %s", res[0].Path)
	}

	// Test frecency boosting
	for i := 0; i < 5; i++ {
		picker.RecordAccess("cmd/app/main.go")
	}
	resBoosted := picker.Search("main", 5)
	if len(resBoosted) == 0 {
		t.Fatalf("expected search results for boosted 'main'")
	}
	if resBoosted[0].Path != "cmd/app/main.go" {
		t.Errorf("expected cmd/app/main.go to be boosted to top, got: %s", resBoosted[0].Path)
	}
}

func TestFilePickerWatcher(t *testing.T) {
	tempDir := t.TempDir()

	picker, err := NewFilePicker(context.Background(), FilePickerOptions{
		RootDir:       tempDir,
		EnableWatcher: true,
	})
	if err != nil {
		t.Fatalf("failed to create FilePicker with watcher: %v", err)
	}
	defer picker.Close()

	// Wait briefly for watcher loop to initialize
	time.Sleep(50 * time.Millisecond)

	// Create a new file dynamically
	newFilePath := filepath.Join(tempDir, "dynamic.txt")
	if err := os.WriteFile(newFilePath, []byte("dynamic content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Poll until picker detects the file or times out
	detected := false
	for i := 0; i < 30; i++ {
		if picker.HasFile("dynamic.txt") {
			detected = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !detected {
		t.Errorf("expected watcher to detect dynamic.txt within timeout")
	}

	// Delete file dynamically
	if err := os.Remove(newFilePath); err != nil {
		t.Fatal(err)
	}

	removed := false
	for i := 0; i < 30; i++ {
		if !picker.HasFile("dynamic.txt") {
			removed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !removed {
		t.Errorf("expected watcher to detect removal of dynamic.txt within timeout")
	}
}
