package tools

import (
	"context"
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
}
