package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellCommand(t *testing.T) {
	tests := []struct {
		name         string
		command      string
		wantExitCode int
		wantContains string
	}{
		{
			name:         "echo command",
			command:      "echo 'hello golem'",
			wantExitCode: 0,
			wantContains: "hello golem",
		},
		{
			name:         "failed command with non-zero exit code",
			command:      "exit 42",
			wantExitCode: 42,
			wantContains: "",
		},
		{
			name:         "command with stderr",
			command:      "echo 'err output' >&2",
			wantExitCode: 0,
			wantContains: "err output",
		},
	}

	ctx := context.Background()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := ExecuteShellCommand(ctx, tc.command)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.ExitCode != tc.wantExitCode {
				t.Errorf("exit code = %d, want %d", res.ExitCode, tc.wantExitCode)
			}
			if tc.wantContains != "" && !strings.Contains(res.Output, tc.wantContains) {
				t.Errorf("output %q does not contain %q", res.Output, tc.wantContains)
			}
		})
	}
}

func TestFileOperations(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "sample.txt")

	tests := []struct {
		name        string
		setup       func(t *testing.T)
		action      func(t *testing.T)
		verify      func(t *testing.T)
		expectError bool
	}{
		{
			name: "write new file",
			setup: func(t *testing.T) {},
			action: func(t *testing.T) {
				err := WriteFile(tempDir, testFile, "initial content")
				if err != nil {
					t.Fatalf("WriteFile failed: %v", err)
				}
			},
			verify: func(t *testing.T) {
				data, err := ReadFile(tempDir, testFile)
				if err != nil {
					t.Fatalf("ReadFile failed: %v", err)
				}
				if data != "initial content" {
					t.Errorf("got %q, want %q", data, "initial content")
				}
			},
		},
		{
			name: "regex replace existing content",
			setup: func(t *testing.T) {
				_ = WriteFile(tempDir, testFile, "version: 1.0.0")
			},
			action: func(t *testing.T) {
				matched, err := ReplaceStringWithRegex(tempDir, testFile, `version: \d+\.\d+\.\d+`, "version: 2.0.0")
				if err != nil {
					t.Fatalf("ReplaceStringWithRegex failed: %v", err)
				}
				if !matched {
					t.Errorf("expected matched=true")
				}
			},
			verify: func(t *testing.T) {
				data, err := ReadFile(tempDir, testFile)
				if err != nil {
					t.Fatalf("ReadFile failed: %v", err)
				}
				if data != "version: 2.0.0" {
					t.Errorf("got %q, want %q", data, "version: 2.0.0")
				}
			},
		},
		{
			name: "read non-existent file",
			setup: func(t *testing.T) {},
			action: func(t *testing.T) {
				_, err := ReadFile(tempDir, filepath.Join(tempDir, "missing.txt"))
				if err == nil {
					t.Errorf("expected error for non-existent file")
				}
			},
			verify: func(t *testing.T) {},
		},
		{
			name: "prevent path traversal outside workspace",
			setup: func(t *testing.T) {},
			action: func(t *testing.T) {
				_, err := ReadFile(tempDir, "../../etc/passwd")
				if err == nil {
					t.Errorf("expected error escaping baseDir, got nil")
				}
				err = WriteFile(tempDir, "../escape.txt", "pwned")
				if err == nil {
					t.Errorf("expected error writing outside baseDir, got nil")
				}
			},
			verify: func(t *testing.T) {},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			tc.action(t)
			tc.verify(t)
		})
	}
}

func TestADKToolInstantiation(t *testing.T) {
	tempDir := t.TempDir()
	shellTool, err := NewShellTool()
	if err != nil {
		t.Fatalf("failed to create shell tool: %v", err)
	}
	if shellTool.Name() != "shell" {
		t.Errorf("expected shell tool name 'shell', got %q", shellTool.Name())
	}

	readTool, err := NewReadFileTool(tempDir)
	if err != nil {
		t.Fatalf("failed to create read_file tool: %v", err)
	}
	if readTool.Name() != "read_file" {
		t.Errorf("expected read tool name 'read_file', got %q", readTool.Name())
	}

	writeTool, err := NewWriteFileTool(tempDir)
	if err != nil {
		t.Fatalf("failed to create write_file tool: %v", err)
	}
	if writeTool.Name() != "write_file" {
		t.Errorf("expected write tool name 'write_file', got %q", writeTool.Name())
	}

	replaceTool, err := NewReplaceRegexTool(tempDir)
	if err != nil {
		t.Fatalf("failed to create replace_regex tool: %v", err)
	}
	if replaceTool.Name() != "replace_regex" {
		t.Errorf("expected replace tool name 'replace_regex', got %q", replaceTool.Name())
	}

	findFilesTool, err := NewFindFilesTool(tempDir)
	if err != nil {
		t.Fatalf("failed to create find_files tool: %v", err)
	}
	if findFilesTool.Name() != "find_files" {
		t.Errorf("expected find_files tool name 'find_files', got %q", findFilesTool.Name())
	}

	grepFilesTool, err := NewGrepFilesTool(tempDir)
	if err != nil {
		t.Fatalf("failed to create grep_files tool: %v", err)
	}
	if grepFilesTool.Name() != "grep_files" {
		t.Errorf("expected grep_files tool name 'grep_files', got %q", grepFilesTool.Name())
	}

	hashReadTool, err := NewHashlineReadTool(tempDir)
	if err != nil {
		t.Fatalf("failed to create hashline_read tool: %v", err)
	}
	if hashReadTool.Name() != "hashline_read" {
		t.Errorf("expected hashline_read tool name 'hashline_read', got %q", hashReadTool.Name())
	}

	hashPatchTool, err := NewHashlinePatchTool(tempDir)
	if err != nil {
		t.Fatalf("failed to create hashline_patch tool: %v", err)
	}
	if hashPatchTool.Name() != "hashline_patch" {
		t.Errorf("expected hashline_patch tool name 'hashline_patch', got %q", hashPatchTool.Name())
	}
}

func TestFFF_FindAndGrep(t *testing.T) {
	tempDir := t.TempDir()

	// Create test directory tree
	subDir := filepath.Join(tempDir, "pkg", "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	f1 := filepath.Join(tempDir, "main.go")
	f2 := filepath.Join(subDir, "helper.go")
	f3 := filepath.Join(tempDir, "README.md")

	_ = os.WriteFile(f1, []byte("package main\n\nfunc main() {\n\tprintln(\"hello world\")\n}\n"), 0644)
	_ = os.WriteFile(f2, []byte("package sub\n\nfunc Helper() string {\n\treturn \"helper result\"\n}\n"), 0644)
	_ = os.WriteFile(f3, []byte("# Project Readme\nWelcome to the test repo.\n"), 0644)

	// 1. Test FindFiles
	res, err := FindFiles(tempDir, "", "helper", 10)
	if err != nil {
		t.Fatalf("FindFiles failed: %v", err)
	}
	if res.Count != 1 || !strings.Contains(res.Files[0], "helper.go") {
		t.Errorf("expected 1 match containing helper.go, got: %+v", res)
	}

	resAll, err := FindFiles(tempDir, "", ".go", 10)
	if err != nil {
		t.Fatalf("FindFiles .go failed: %v", err)
	}
	if resAll.Count < 2 {
		t.Errorf("expected at least 2 .go files, got %d", resAll.Count)
	}

	// 2. Test GrepFiles
	grepRes, err := GrepFiles(tempDir, "", "hello world", "", 10)
	if err != nil {
		t.Fatalf("GrepFiles failed: %v", err)
	}
	if grepRes.Count != 1 || !strings.Contains(grepRes.Matches[0].File, "main.go") {
		t.Errorf("expected 1 match in main.go, got: %+v", grepRes)
	}

	// 3. Test GrepFiles with file pattern filter
	grepFiltered, err := GrepFiles(tempDir, "", "helper result", "*.go", 10)
	if err != nil {
		t.Fatalf("GrepFiles filtered failed: %v", err)
	}
	if grepFiltered.Count != 1 {
		t.Errorf("expected 1 match with filter, got: %+v", grepFiltered)
	}

	// 4. Test GrepFiles with hyphenated search pattern (e.g. '--flag' / '-v')
	fHyphen := filepath.Join(tempDir, "flags.txt")
	_ = os.WriteFile(fHyphen, []byte("enable --debug mode\nuse -v for verbose\n"), 0644)
	grepHyphen, err := GrepFiles(tempDir, "", "--debug", "", 10)
	if err != nil {
		t.Fatalf("GrepFiles hyphenated pattern failed: %v", err)
	}
	if grepHyphen.Count != 1 || !strings.Contains(grepHyphen.Matches[0].Content, "--debug") {
		t.Errorf("expected 1 match containing --debug, got: %+v", grepHyphen)
	}
}

func TestHashline_ReadAndPatch(t *testing.T) {
	tempDir := t.TempDir()
	targetFile := "service.go"
	fullPath := filepath.Join(tempDir, targetFile)

	initialContent := `package main

func Run() {
	println("v1")
}
`
	if err := os.WriteFile(fullPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 1. Read file with hashline anchors
	readRes, err := ReadFileWithHashlines(tempDir, targetFile, 1, 10)
	if err != nil {
		t.Fatalf("ReadFileWithHashlines failed: %v", err)
	}
	if readRes.TotalLines != 5 {
		t.Fatalf("expected 5 lines, got %d", readRes.TotalLines)
	}

	line4Anchor := readRes.Lines[3].Anchor // line 4: println("v1")
	if !strings.HasPrefix(line4Anchor, "4:") {
		t.Fatalf("expected anchor starting with '4:', got %s", line4Anchor)
	}

	// 2. Test SWAP operation on line 4
	patchRes, err := PatchFileWithHashline(tempDir, targetFile, "SWAP", line4Anchor, "\tprintln(\"v2\")", false)
	if err != nil {
		t.Fatalf("PatchFileWithHashline SWAP failed: %v", err)
	}
	if !patchRes.Success {
		t.Errorf("expected SWAP success, got: %+v", patchRes)
	}

	data, _ := os.ReadFile(fullPath)
	if !strings.Contains(string(data), `println("v2")`) {
		t.Errorf("expected patched file to contain v2, got:\n%s", string(data))
	}

	// 3. Re-read and test Stale Read Rejection
	// Using old anchor from line 4 should fail because line content changed from v1 to v2!
	_, err = PatchFileWithHashline(tempDir, targetFile, "SWAP", line4Anchor, "\tprintln(\"v3\")", false)
	if err == nil {
		t.Fatalf("expected stale read error when reusing old anchor %s, got nil", line4Anchor)
	}
	if !strings.Contains(err.Error(), "stale read") {
		t.Errorf("expected error message to contain 'stale read', got: %v", err)
	}

	// 4. Test INS.POST operation
	readRes2, _ := ReadFileWithHashlines(tempDir, targetFile, 1, 10)
	line4NewAnchor := readRes2.Lines[3].Anchor
	insRes, err := PatchFileWithHashline(tempDir, targetFile, "INS.POST", line4NewAnchor, "\tprintln(\"v2-step2\")", false)
	if err != nil {
		t.Fatalf("PatchFileWithHashline INS.POST failed: %v", err)
	}
	if !insRes.Success {
		t.Errorf("expected INS.POST success")
	}

	data2, _ := os.ReadFile(fullPath)
	if !strings.Contains(string(data2), `println("v2-step2")`) {
		t.Errorf("expected patched file to contain v2-step2, got:\n%s", string(data2))
	}

	// 5. Test DEL operation
	readRes3, _ := ReadFileWithHashlines(tempDir, targetFile, 1, 10)
	line5Anchor := readRes3.Lines[4].Anchor
	delRes, err := PatchFileWithHashline(tempDir, targetFile, "DEL", line5Anchor, "", false)
	if err != nil {
		t.Fatalf("PatchFileWithHashline DEL failed: %v", err)
	}
	if !delRes.Success {
		t.Errorf("expected DEL success")
	}

	data3, _ := os.ReadFile(fullPath)
	if strings.Contains(string(data3), `println("v2-step2")`) {
		t.Errorf("expected v2-step2 to be deleted, got:\n%s", string(data3))
	}

	// 6. Test Dry-Run does not mutate disk
	readRes4, _ := ReadFileWithHashlines(tempDir, targetFile, 1, 10)
	dryRes, err := PatchFileWithHashline(tempDir, targetFile, "SWAP", readRes4.Lines[0].Anchor, "package modified", true)
	if err != nil {
		t.Fatalf("DryRun patch failed: %v", err)
	}
	if !strings.Contains(dryRes.Message, "[DRY-RUN]") {
		t.Errorf("expected dry run message, got: %s", dryRes.Message)
	}
	diskCheck, _ := os.ReadFile(fullPath)
	if strings.Contains(string(diskCheck), "package modified") {
		t.Errorf("dry-run should not have modified file content on disk")
	}

	// 7. Test Empty 0-byte file read and initialization
	emptyFile := "empty.txt"
	emptyPath := filepath.Join(tempDir, emptyFile)
	_ = os.WriteFile(emptyPath, []byte(""), 0644)

	emptyRead, err := ReadFileWithHashlines(tempDir, emptyFile, 1, 10)
	if err != nil {
		t.Fatalf("ReadFileWithHashlines empty file failed: %v", err)
	}
	if emptyRead.TotalLines != 0 || len(emptyRead.Lines) != 0 {
		t.Errorf("expected 0 lines for empty file, got %d", emptyRead.TotalLines)
	}

	initRes, err := PatchFileWithHashline(tempDir, emptyFile, "SWAP", "0", "first line\nsecond line", false)
	if err != nil {
		t.Fatalf("PatchFileWithHashline on empty file failed: %v", err)
	}
	if !initRes.Success {
		t.Errorf("expected init success on empty file")
	}

	dataInit, _ := os.ReadFile(emptyPath)
	if !strings.Contains(string(dataInit), "first line\nsecond line") {
		t.Errorf("expected initialized file content, got: %s", string(dataInit))
	}

	// 8. Test Enforced Hash Anchor requirement (rejecting bare line numbers like "2")
	_, err = PatchFileWithHashline(tempDir, targetFile, "SWAP", "2", "replacement", false)
	if err == nil {
		t.Fatalf("expected error when omitting hash in anchor, got nil")
	}
	if !strings.Contains(err.Error(), "missing line hash in start anchor") {
		t.Errorf("expected 'missing line hash' in error, got: %v", err)
	}
}
