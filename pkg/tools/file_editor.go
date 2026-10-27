package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ErrPathOutOfBounds is returned when a requested file path escapes the allowed root directory.
var ErrPathOutOfBounds = errors.New("file path is outside allowed directory")

// resolveSafePath ensures path is evaluated relative to baseDir (defaulting to current working dir)
// and does not escape baseDir via directory traversal.
func resolveSafePath(baseDir, path string) (string, error) {
	if baseDir == "" {
		var err error
		baseDir, err = os.Getwd()
		if err != nil {
			return "", errors.Wrap(err, "getting working directory")
		}
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", errors.Wrapf(err, "resolving absolute path for base %s", baseDir)
	}

	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(absBase, target)
	}
	cleanedTarget := filepath.Clean(target)

	rel, err := filepath.Rel(absBase, cleanedTarget)
	if err != nil {
		return "", errors.Wrapf(err, "computing relative path from %s to %s", absBase, cleanedTarget)
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.Wrapf(ErrPathOutOfBounds, "path %q escapes workspace root %q", path, absBase)
	}

	return cleanedTarget, nil
}

// ReadFileArgs defines input for reading a file.
type ReadFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"The path to the file to read (must be within workspace)"`
}

// ReadFileResult defines output from reading a file.
type ReadFileResult struct {
	Content string `json:"content" jsonschema:"The text content of the file"`
	Bytes   int    `json:"bytes" jsonschema:"Size of the file in bytes"`
}

// ReadFile reads the full contents of a file at path ensuring it remains within baseDir.
func ReadFile(baseDir, path string) (string, error) {
	safePath, err := resolveSafePath(baseDir, path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(safePath)
	if err != nil {
		return "", errors.Wrapf(err, "reading file %s", safePath)
	}
	return string(b), nil
}

// NewReadFileTool returns an ADK tool for reading files with an optional baseDir sandbox.
func NewReadFileTool(baseDir string, opts ...Option) (tool.Tool, error) {
	o := newOptions(opts)
	t, err := functiontool.New(functiontool.Config{
		Name:        "read_file",
		Description: "Reads and returns the complete text content of a file at the given relative or absolute path within the workspace.",
	}, func(ctx agent.Context, args ReadFileArgs) (ReadFileResult, error) {
		if err := o.checkRead("read_file", args.FilePath); err != nil {
			return ReadFileResult{}, err
		}
		content, err := ReadFile(baseDir, args.FilePath)

		if err != nil {
			return ReadFileResult{}, errors.Wrap(err, "reading file")
		}
		return ReadFileResult{
			Content: content,
			Bytes:   len(content),
		}, nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating read_file tool")
	}
	return t, nil
}

// WriteFileArgs defines input for writing to a file.
type WriteFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"The path to the file to write (must be within workspace)"`
	Content  string `json:"content" jsonschema:"The content to write into the file"`
}

// WriteFileResult defines output after writing a file.
type WriteFileResult struct {
	BytesWritten int  `json:"bytes_written" jsonschema:"Number of bytes written"`
	Success      bool `json:"success" jsonschema:"Whether the write succeeded"`
}

// WriteFile writes the given content to path, creating or truncating it, ensuring it remains within baseDir.
func WriteFile(baseDir, path string, content string) error {
	safePath, err := resolveSafePath(baseDir, path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(safePath, []byte(content), 0644); err != nil {
		return errors.Wrapf(err, "writing file %s", safePath)
	}
	return nil
}

// NewWriteFileTool returns an ADK tool for writing files with an optional baseDir sandbox.
func NewWriteFileTool(baseDir string, opts ...Option) (tool.Tool, error) {
	o := newOptions(opts)
	t, err := functiontool.New(functiontool.Config{
		Name:        "write_file",
		Description: "Writes content to a file at the specified path within the workspace, creating it if needed or overwriting if it exists.",
	}, func(ctx agent.Context, args WriteFileArgs) (WriteFileResult, error) {
		if err := o.checkWrite("write_file", args.FilePath); err != nil {
			return WriteFileResult{}, err
		}
		if err := WriteFile(baseDir, args.FilePath, args.Content); err != nil {
			return WriteFileResult{}, errors.Wrap(err, "writing file")
		}
		return WriteFileResult{
			BytesWritten: len(args.Content),
			Success:      true,
		}, nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating write_file tool")
	}
	return t, nil
}

// ReplaceRegexArgs defines input for regex replacement in a file.
type ReplaceRegexArgs struct {
	FilePath     string `json:"file_path" jsonschema:"The path to the file (must be within workspace)"`
	Pattern      string `json:"pattern" jsonschema:"The regular expression pattern to search for"`
	Replacement  string `json:"replacement" jsonschema:"The replacement text"`
}

// ReplaceRegexResult defines output after regex replacement.
type ReplaceRegexResult struct {
	Matched bool `json:"matched" jsonschema:"Whether any matches were replaced"`
	Success bool `json:"success" jsonschema:"Whether the operation completed successfully"`
}

// ReplaceStringWithRegex replaces all occurrences of pattern in path with replacement, ensuring path is within baseDir.
func ReplaceStringWithRegex(baseDir, path string, pattern string, replacement string) (bool, error) {
	safePath, err := resolveSafePath(baseDir, path)
	if err != nil {
		return false, err
	}

	content, err := ReadFile(baseDir, safePath)
	if err != nil {
		return false, errors.Wrapf(err, "reading file %s for regex replace", safePath)
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, errors.Wrapf(err, "compiling regex %q (please provide a valid Go regexp pattern)", pattern)
	}

	matched := re.MatchString(content)
	if !matched {
		return false, nil
	}

	newContent := re.ReplaceAllString(content, replacement)
	if err := WriteFile(baseDir, safePath, newContent); err != nil {
		return false, errors.Wrapf(err, "writing regex replaced content to %s", safePath)
	}

	return true, nil
}

// NewReplaceRegexTool returns an ADK tool for replacing content with regex with an optional baseDir sandbox.
func NewReplaceRegexTool(baseDir string, opts ...Option) (tool.Tool, error) {
	o := newOptions(opts)
	t, err := functiontool.New(functiontool.Config{
		Name:        "replace_regex",
		Description: "Replaces occurrences of a regex pattern with replacement text in a specified file within the workspace.",
	}, func(ctx agent.Context, args ReplaceRegexArgs) (ReplaceRegexResult, error) {
		if err := o.checkWrite("replace_regex", args.FilePath); err != nil {
			return ReplaceRegexResult{}, err
		}
		matched, err := ReplaceStringWithRegex(baseDir, args.FilePath, args.Pattern, args.Replacement)
		if err != nil {
			return ReplaceRegexResult{}, errors.Wrap(err, "replacing regex in file")
		}
		return ReplaceRegexResult{
			Matched: matched,
			Success: true,
		}, nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating replace_regex tool")
	}
	return t, nil
}
