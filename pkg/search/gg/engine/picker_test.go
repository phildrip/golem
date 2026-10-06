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
	file2 := filepath.Join(tempDir, "pkg", "tools", "gg.go")
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
	if !picker.HasFile("pkg/tools/gg.go") {
		t.Errorf("expected picker to have pkg/tools/gg.go")
	}

	// Test QueryCandidates
	candGo := picker.QueryCandidates("*.go")
	if len(candGo) != 2 {
		t.Errorf("expected 2 .go files, got %d: %v", len(candGo), candGo)
	}

	candTools := picker.QueryCandidates("tools/ *.go")
	if len(candTools) != 1 || candTools[0] != "pkg/tools/gg.go" {
		t.Errorf("expected pkg/tools/gg.go, got: %v", candTools)
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
	// Test multi-word search: "tools gg" should match "pkg/tools/gg.go"
	resMulti := picker.Search("tools gg", 5)
	if len(resMulti) == 0 || resMulti[0].Path != "pkg/tools/gg.go" {
		t.Errorf("expected pkg/tools/gg.go for multi-word search 'tools gg', got: %+v", resMulti)
	}
}

func TestFilePickerWatcher(t *testing.T) {
	tempDir := t.TempDir()

	// Write .gitignore before starting picker
	_ = os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte("*.ignored\n"), 0644)

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

	// Create a normal new file dynamically
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

	// Create a gitignored file dynamically: must NOT be added to picker
	ignoredFilePath := filepath.Join(tempDir, "secret.ignored")
	if err := os.WriteFile(ignoredFilePath, []byte("ignored"), 0644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if picker.HasFile("secret.ignored") {
		t.Errorf("watcher must NOT index gitignored file secret.ignored")
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

// TestFilePickerWatcherDirectoryLifecycle ports FFF's new_directory_watcher_test and fs_delete_handler_test.
func TestFilePickerWatcherDirectoryLifecycle(t *testing.T) {
	tempDir := t.TempDir()

	picker, err := NewFilePicker(context.Background(), FilePickerOptions{
		RootDir:       tempDir,
		EnableWatcher: true,
	})
	if err != nil {
		t.Fatalf("failed to create FilePicker with watcher: %v", err)
	}
	defer picker.Close()

	time.Sleep(50 * time.Millisecond)

	// 1. Create a new subdirectory and file inside it
	subDir := filepath.Join(tempDir, "new_sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	subFile := filepath.Join(subDir, "inner.go")
	if err := os.WriteFile(subFile, []byte("package sub"), 0644); err != nil {
		t.Fatal(err)
	}

	detected := false
	for i := 0; i < 40; i++ {
		if picker.HasFile("new_sub/inner.go") {
			detected = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !detected {
		t.Errorf("expected watcher to detect file inside new subdirectory")
	}

	// 2. Remove the entire subdirectory
	if err := os.RemoveAll(subDir); err != nil {
		t.Fatal(err)
	}

	removed := false
	for i := 0; i < 40; i++ {
		if !picker.HasFile("new_sub/inner.go") {
			removed = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !removed {
		t.Errorf("expected watcher to remove file when parent subdirectory is deleted")
	}
}
