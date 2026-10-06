package tools

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// LineHash computes a short 2-character hex hash of trimmed line content using xxHash.
// Format matches hashline convention: e.g. "42:a3" (line 42, hash a3).
func LineHash(content string) string {
	// Normalize line for stable hashing across platforms
	trimmed := strings.TrimRight(content, "\r\n")
	h := xxhash.Sum64String(trimmed)
	// Return lowest byte in hex (2 chars)
	return fmt.Sprintf("%02x", byte(h&0xff))
}

// LineAnchor represents a line number and its content hash.
type LineAnchor struct {
	LineNum int    `json:"line_num"`
	Hash    string `json:"hash"`
}

// ParseAnchor parses "42:a3" or just "42" into a LineAnchor.
func ParseAnchor(raw string) (LineAnchor, error) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ":")
	num, err := strconv.Atoi(parts[0])
	if err != nil {
		return LineAnchor{}, errors.Wrapf(err, "invalid line number in anchor %q", raw)
	}
	hash := ""
	if len(parts) > 1 {
		hash = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	return LineAnchor{LineNum: num, Hash: hash}, nil
}

// HashlineReadArgs defines input for reading a file with hashline anchors.
type HashlineReadArgs struct {
	FilePath   string `json:"file_path" jsonschema:"Path to the file to read (must be within workspace)"`
	StartLine  int    `json:"start_line,omitempty" jsonschema:"Optional starting line number (1-based, defaults to 1)"`
	LineCount  int    `json:"line_count,omitempty" jsonschema:"Optional number of lines to read (defaults to full file)"`
}

// HashlineReadLine represents a single line with its anchor.
type HashlineReadLine struct {
	Anchor  string `json:"anchor" jsonschema:"Stable anchor string (e.g. '12:a3')"`
	LineNum int    `json:"line_num" jsonschema:"Line number (1-based)"`
	Hash    string `json:"hash" jsonschema:"2-character xxHash suffix"`
	Content string `json:"content" jsonschema:"Line text content"`
}

// HashlineReadResult defines output from reading a file with hashline anchors.
type HashlineReadResult struct {
	FilePath   string             `json:"file_path"`
	TotalLines int                `json:"total_lines"`
	Lines      []HashlineReadLine `json:"lines"`
	Rendered   string             `json:"rendered" jsonschema:"Human/LLM-readable text formatted with anchors: '42:a3| const x = 1;'"`
}

// ReadFileWithHashlines reads a file and assigns 2-char xxHash anchors to each line.
func ReadFileWithHashlines(baseDir, filePath string, startLine, lineCount int) (HashlineReadResult, error) {
	safePath, err := resolveSafePath(baseDir, filePath)
	if err != nil {
		return HashlineReadResult{}, err
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return HashlineReadResult{}, errors.Wrapf(err, "reading file %s", safePath)
	}

	var rawLines []string
	if len(data) > 0 {
		rawLines = strings.Split(string(data), "\n")
		// Handle trailing empty line from standard UNIX files without counting extra line
		if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" && strings.HasSuffix(string(data), "\n") {
			rawLines = rawLines[:len(rawLines)-1]
		}
	}

	total := len(rawLines)
	if total == 0 {
		return HashlineReadResult{
			FilePath:   filePath,
			TotalLines: 0,
			Lines:      []HashlineReadLine{},
			Rendered:   fmt.Sprintf("[%s#lines 0-0 of 0 (empty file)]\n", filePath),
		}, nil
	}

	if startLine < 1 {
		startLine = 1
	}
	endLine := total
	if lineCount > 0 && (startLine-1+lineCount) < total {
		endLine = startLine - 1 + lineCount
	}

	var parsed []HashlineReadLine
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s#lines %d-%d of %d]\n", filePath, startLine, endLine, total))

	for i := startLine - 1; i < endLine && i < total; i++ {
		lNum := i + 1
		text := rawLines[i]
		h := LineHash(text)
		anchor := fmt.Sprintf("%d:%s", lNum, h)
		parsed = append(parsed, HashlineReadLine{
			Anchor:  anchor,
			LineNum: lNum,
			Hash:    h,
			Content: text,
		})
		sb.WriteString(fmt.Sprintf("%s| %s\n", anchor, text))
	}

	return HashlineReadResult{
		FilePath:   filePath,
		TotalLines: total,
		Lines:      parsed,
		Rendered:   sb.String(),
	}, nil
}

// HashlinePatchArgs defines input for applying a content-hash anchored patch.
type HashlinePatchArgs struct {
	FilePath string `json:"file_path" jsonschema:"Path to the file to patch (must be within workspace)"`
	Op       string `json:"op" jsonschema:"Operation type: 'SWAP', 'DEL', 'INS.PRE', or 'INS.POST'"`
	Anchor   string `json:"anchor" jsonschema:"Target anchor: e.g. '42:a3' or range '42:a3..45:f1' (or '0' for empty files)"`
	Content  string `json:"content,omitempty" jsonschema:"New replacement or inserted content (lines can optionally begin with '+ ')"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"If true, checks anchors without writing changes to disk"`
}

// HashlinePatchResult defines output after applying a patch.
type HashlinePatchResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Diff    string `json:"diff,omitempty"`
}

// PatchFileWithHashline applies a hash-verified edit to a file.
func PatchFileWithHashline(baseDir, filePath, op, anchorStr, content string, dryRun bool) (HashlinePatchResult, error) {
	safePath, err := resolveSafePath(baseDir, filePath)
	if err != nil {
		return HashlinePatchResult{}, err
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return HashlinePatchResult{}, errors.Wrapf(err, "reading file %s for patch", safePath)
	}

	var rawLines []string
	hasTrailingNewline := strings.HasSuffix(string(data), "\n")
	if len(data) > 0 {
		rawLines = strings.Split(string(data), "\n")
		if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" && hasTrailingNewline {
			rawLines = rawLines[:len(rawLines)-1]
		}
	}

	// Clean content lines (strip optional "+ " prefixes from unified diff / hashline bodies)
	var newBodyLines []string
	if content != "" {
		for _, row := range strings.Split(content, "\n") {
			if strings.HasPrefix(row, "+ ") {
				newBodyLines = append(newBodyLines, strings.TrimPrefix(row, "+ "))
			} else if strings.HasPrefix(row, "+") {
				newBodyLines = append(newBodyLines, strings.TrimPrefix(row, "+"))
			} else {
				newBodyLines = append(newBodyLines, row)
			}
		}
	}

	normOp := strings.ToUpper(strings.TrimSpace(op))

	// Handle empty file initialization
	if len(rawLines) == 0 {
		if normOp == "SWAP" || normOp == "REPLACE" || normOp == "INS.PRE" || normOp == "INS.POST" || normOp == "INSERT_BEFORE" || normOp == "INSERT_AFTER" {
			outputStr := strings.Join(newBodyLines, "\n")
			if len(newBodyLines) > 0 {
				outputStr += "\n"
			}
			msg := fmt.Sprintf("Successfully initialized empty file with %d lines", len(newBodyLines))
			if dryRun {
				return HashlinePatchResult{
					Success: true,
					Message: "[DRY-RUN] " + msg,
					Diff:    fmt.Sprintf("Lines before: 0, lines after: %d", len(newBodyLines)),
				}, nil
			}
			if err := os.WriteFile(safePath, []byte(outputStr), 0644); err != nil {
				return HashlinePatchResult{}, errors.Wrapf(err, "writing patched file %s", safePath)
			}
			return HashlinePatchResult{Success: true, Message: msg}, nil
		}
		return HashlinePatchResult{}, errors.Errorf("cannot apply %s on empty file", op)
	}

	// Parse anchor(s)
	// Supports "42:a3..45:f1" or "42:a3" or "42..45"
	var startAnchor, endAnchor LineAnchor
	if strings.Contains(anchorStr, "..") {
		parts := strings.SplitN(anchorStr, "..", 2)
		var err error
		startAnchor, err = ParseAnchor(parts[0])
		if err != nil {
			return HashlinePatchResult{}, err
		}
		endAnchor, err = ParseAnchor(parts[1])
		if err != nil {
			return HashlinePatchResult{}, err
		}
	} else {
		var err error
		startAnchor, err = ParseAnchor(anchorStr)
		if err != nil {
			return HashlinePatchResult{}, err
		}
		endAnchor = startAnchor
	}

	// Validate bounds (1-indexed)
	if startAnchor.LineNum < 1 || startAnchor.LineNum > len(rawLines) {
		return HashlinePatchResult{}, errors.Errorf("start line %d out of bounds (file has %d lines)", startAnchor.LineNum, len(rawLines))
	}
	if endAnchor.LineNum < startAnchor.LineNum || endAnchor.LineNum > len(rawLines) {
		return HashlinePatchResult{}, errors.Errorf("end line %d out of bounds (file has %d lines)", endAnchor.LineNum, len(rawLines))
	}

	// Verify content hashes (enforce hash presence to prevent bypassing stale-read safety)
	if startAnchor.Hash == "" {
		return HashlinePatchResult{}, errors.Errorf("missing line hash in start anchor %q (hashline requires format 'line:hash', e.g. '%d:%s')",
			anchorStr, startAnchor.LineNum, LineHash(rawLines[startAnchor.LineNum-1]))
	}
	actualStartHash := LineHash(rawLines[startAnchor.LineNum-1])
	if actualStartHash != startAnchor.Hash {
		return HashlinePatchResult{}, errors.Errorf("stale read: line %d expected hash %s, but found %s (%q)",
			startAnchor.LineNum, startAnchor.Hash, actualStartHash, rawLines[startAnchor.LineNum-1])
	}

	if endAnchor.Hash == "" {
		return HashlinePatchResult{}, errors.Errorf("missing line hash in end anchor %q (hashline requires format 'line:hash', e.g. '%d:%s')",
			anchorStr, endAnchor.LineNum, LineHash(rawLines[endAnchor.LineNum-1]))
	}
	actualEndHash := LineHash(rawLines[endAnchor.LineNum-1])
	if actualEndHash != endAnchor.Hash {
		return HashlinePatchResult{}, errors.Errorf("stale read: line %d expected hash %s, but found %s (%q)",
			endAnchor.LineNum, endAnchor.Hash, actualEndHash, rawLines[endAnchor.LineNum-1])
	}

	// Apply operation
	startIdx := startAnchor.LineNum - 1
	endIdx := endAnchor.LineNum // exclusive slice bound

	var resultLines []string

	switch normOp {
	case "SWAP", "REPLACE":
		resultLines = append(resultLines, rawLines[:startIdx]...)
		resultLines = append(resultLines, newBodyLines...)
		resultLines = append(resultLines, rawLines[endIdx:]...)

	case "DEL", "DELETE":
		resultLines = append(resultLines, rawLines[:startIdx]...)
		resultLines = append(resultLines, rawLines[endIdx:]...)

	case "INS.PRE", "INSERT_BEFORE":
		resultLines = append(resultLines, rawLines[:startIdx]...)
		resultLines = append(resultLines, newBodyLines...)
		resultLines = append(resultLines, rawLines[startIdx:]...)

	case "INS.POST", "INSERT_AFTER":
		resultLines = append(resultLines, rawLines[:endIdx]...)
		resultLines = append(resultLines, newBodyLines...)
		resultLines = append(resultLines, rawLines[endIdx:]...)

	default:
		return HashlinePatchResult{}, errors.Errorf("unsupported hashline operation %q (use SWAP, DEL, INS.PRE, or INS.POST)", op)
	}

	outputStr := strings.Join(resultLines, "\n")
	if hasTrailingNewline || len(resultLines) > 0 {
		outputStr += "\n"
	}

	msg := fmt.Sprintf("Successfully applied %s at anchor %s (%d lines affected)", normOp, anchorStr, len(resultLines)-len(rawLines))
	if dryRun {
		return HashlinePatchResult{
			Success: true,
			Message: "[DRY-RUN] " + msg,
			Diff:    fmt.Sprintf("Lines before: %d, lines after: %d", len(rawLines), len(resultLines)),
		}, nil
	}

	if err := os.WriteFile(safePath, []byte(outputStr), 0644); err != nil {
		return HashlinePatchResult{}, errors.Wrapf(err, "writing patched file %s", safePath)
	}

	return HashlinePatchResult{
		Success: true,
		Message: msg,
	}, nil
}

// NewHashlineReadTool creates the ADK tool for hash-anchored file reading.
func NewHashlineReadTool(baseDir string) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "hashline_read",
		Description: "Reads a file with stable content-hashed line anchors (e.g. '42:a3'). Use before hashline_patch to prevent edit collision and hallucinated line modifications.",
	}, func(ctx agent.Context, args HashlineReadArgs) (HashlineReadResult, error) {
		return ReadFileWithHashlines(baseDir, args.FilePath, args.StartLine, args.LineCount)
	})
}

// NewHashlinePatchTool creates the ADK tool for hash-anchored file patching.
func NewHashlinePatchTool(baseDir string) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "hashline_patch",
		Description: "Applies a robust content-hashed patch ('SWAP', 'DEL', 'INS.PRE', 'INS.POST') using verified anchors like '42:a3' or '42:a3..45:f1'. Rejects stale edits if lines changed.",
	}, func(ctx agent.Context, args HashlinePatchArgs) (HashlinePatchResult, error) {
		return PatchFileWithHashline(baseDir, args.FilePath, args.Op, args.Anchor, args.Content, args.DryRun)
	})
}
