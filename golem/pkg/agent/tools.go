package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/user/golem/pkg/tools"
)

// ToolDescriptor represents the metadata an LLM needs to understand and call a tool.
type ToolDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters defines the expected input parameters for the tool.
	// This should be a JSON schema object describing the input.
	Parameters string `json:"parameters"`
}

// Tool implements the Tool interface for executing shell commands.
type ShellTool struct{}

func (s *ShellTool) Name() string {
	return "shell"
}

func (s *ShellTool) Description() string {
	return "Executes a shell command on the underlying operating system. Useful for listing files, checking directories, or running scripts."
}

func (s *ShellTool) Run(ctx context.Context, input string) (string, error) {
	return tools.ExecuteShellCommand(input)
}

// Descriptor returns the ToolDescriptor for the ShellTool.
func (s *ShellTool) Descriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:        s.Name(),
		Description: s.Description(),
		Parameters: `{
			"type": "object",
			"properties": {
				"command": {
					"type": "string",
					"description": "The shell command to execute."
				}
			},
			"required": ["command"]
		}`,
	}
}

// FileReadTool implements the Tool interface for reading files.
type FileReadTool struct{}

func (f *FileReadTool) Name() string {
	return "read_file"
}

func (f *FileReadTool) Description() string {
	return "Reads the content of a file from the filesystem. Useful for inspecting code or log files."
}

func (f *FileReadTool) Run(ctx context.Context, input string) (string, error) {
	return tools.ReadFile(input)
}

// Descriptor returns the ToolDescriptor for the FileReadTool.
func (f *FileReadTool) Descriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:        f.Name(),
		Description: f.Description(),
		Parameters: `{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "The absolute path to the file to read."
				}
			},
			"required": ["file_path"]
		}`,
	}
}

// FileWriteTool implements the Tool interface for writing to files.
type FileWriteTool struct{}

func (f *FileWriteTool) Name() string {
	return "write_file"
}

func (f *FileWriteTool) Description() string {
	return "Writes content to a file, overwriting existing content or creating a new file. Use with caution."
}

func (f *FileWriteTool) Run(ctx context.Context, input string) (string, error) {
	// The input string needs to be parsed into file_path and content
	parts := strings.SplitN(input, "::", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid write_file command format. Expected 'file_path::content'.")
	}
	filePath := parts[0]
	content := parts[1]
	return "", tools.WriteFile(filePath, content)
}

// Descriptor returns the ToolDescriptor for the FileWriteTool.
func (f *FileWriteTool) Descriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:        f.Name(),
		Description: f.Description(),
		Parameters: `{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "The absolute path to the file to write."
				},
				"content": {
					"type": "string",
					"description": "The content to write to the file."
				}
			},
			"required": ["file_path", "content"]
		}`,
	}
}

// FileReplaceRegexTool implements the Tool interface for regex replacement in files.
type FileReplaceRegexTool struct{}

func (f *FileReplaceRegexTool) Name() string {
	return "replace_regex"
}

func (f *FileReplaceRegexTool) Description() string {
	return "Replaces all occurrences of a regular expression pattern with a replacement string in a specified file."
}

func (f *FileReplaceRegexTool) Run(ctx context.Context, input string) (string, error) {
	parts := strings.SplitN(input, "::", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid replace_regex command format. Expected 'file_path::regex_pattern::replacement_string'.")
	}
	filePath := parts[0]
	pattern := parts[1]
	replacement := parts[2]
	return "", tools.ReplaceStringWithRegex(filePath, pattern, replacement)
}

// Descriptor returns the ToolDescriptor for the FileReplaceRegexTool.
func (f *FileReplaceRegexTool) Descriptor() ToolDescriptor {
	return ToolDescriptor{
		Name:        f.Name(),
		Description: f.Description(),
		Parameters: `{
			"type": "object",
			"properties": {
				"file_path": {
					"type": "string",
					"description": "The absolute path to the file."
				},
				"regex_pattern": {
					"type": "string",
					"description": "The regular expression pattern to search for."
				},
				"replacement_string": {
					"type": "string",
					"description": "The string to replace the matched pattern with."
				}
			},
			"required": ["file_path", "regex_pattern", "replacement_string"]
		}`,
	}
}
