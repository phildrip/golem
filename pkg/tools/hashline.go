package tools

import (
	"fmt"
	"os"
	"path/filepath"
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

// ParseAnchor parses "42:a3", "42:a3:", or just "42" into a LineAnchor.
func ParseAnchor(raw string) (LineAnchor, error) {
	raw = strings.TrimSpace(raw)
	// Strip optional trailing colon (matches hashline tokenizer consume_optional_colon)
	raw = strings.TrimSuffix(raw, ":")
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
	Op       string `json:"op" jsonschema:"Operation type: 'SWAP', 'DEL', 'INS.PRE', 'INS.POST', 'SWAP.BLK', 'DEL.BLK', 'INS.BLK.PRE', or 'INS.BLK.POST'"`
	Anchor   string `json:"anchor" jsonschema:"Target anchor: e.g. '42:a3', range '42:a3..45:f1', two-anchor '42:a3 45:f1', block anchor '42:a3', or '0' for empty files"`
	Content  string `json:"content,omitempty" jsonschema:"New replacement or inserted content (lines can optionally begin with '+ ')"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"If true, checks anchors without writing changes to disk"`
}

// HashlinePatchResult defines output after applying a patch.
type HashlinePatchResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Diff    string `json:"diff,omitempty"`
}

// HashlineFindBlockArgs defines input for finding the structural block around an anchor.
type HashlineFindBlockArgs struct {
	FilePath string `json:"file_path" jsonschema:"Path to the file (must be within workspace)"`
	Anchor   string `json:"anchor" jsonschema:"Anchor inside or at the header of the target block, e.g. '42:a3'"`
}

// HashlineFindBlockResult defines output of finding a structural block.
type HashlineFindBlockResult struct {
	FilePath   string             `json:"file_path"`
	TotalLines int                `json:"total_lines"`
	Language   string             `json:"language"`
	BlockStart int                `json:"block_start" jsonschema:"First line of block (1-based, inclusive)"`
	BlockEnd   int                `json:"block_end" jsonschema:"Last line of block (1-based, inclusive)"`
	Lines      []HashlineReadLine `json:"lines"`
	Rendered   string             `json:"rendered"`
}

// FindBlockWithHashline locates the enclosing syntactic block around an anchor.
func FindBlockWithHashline(baseDir, filePath, anchorStr string) (HashlineFindBlockResult, error) {
	safePath, err := resolveSafePath(baseDir, filePath)
	if err != nil {
		return HashlineFindBlockResult{}, err
	}

	data, err := os.ReadFile(safePath)
	if err != nil {
		return HashlineFindBlockResult{}, errors.Wrapf(err, "reading file %s for find_block", safePath)
	}

	text := string(data)
	var rawLines []string
	if len(data) > 0 {
		rawLines = strings.Split(text, "\n")
		if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" && strings.HasSuffix(text, "\n") {
			rawLines = rawLines[:len(rawLines)-1]
		}
	}

	if len(rawLines) == 0 {
		return HashlineFindBlockResult{}, errors.Errorf("file is empty")
	}

	anchor, err := ParseAnchor(anchorStr)
	if err != nil {
		return HashlineFindBlockResult{}, err
	}

	if anchor.LineNum < 1 || anchor.LineNum > len(rawLines) {
		return HashlineFindBlockResult{}, errors.Errorf("anchor line %d out of bounds (file has %d lines)", anchor.LineNum, len(rawLines))
	}

	if anchor.Hash != "" {
		actualHash := LineHash(rawLines[anchor.LineNum-1])
		if actualHash != anchor.Hash {
			return HashlineFindBlockResult{}, errors.Errorf("stale read: line %d expected hash %s, but found %s",
				anchor.LineNum, anchor.Hash, actualHash)
		}
	}

	resolver := NewBuiltinBlockResolver()
	span, err := resolver.Resolve(filePath, text, anchor.LineNum)
	if err != nil {
		return HashlineFindBlockResult{}, err
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filePath), "."))
	lang := LanguageForExtension(ext)

	var blockLines []HashlineReadLine
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("OK file=%s lang=%s lines=%d block=%d..%d\n", filePath, lang, len(rawLines), span.Start, span.End))

	for i := span.Start - 1; i <= span.End-1 && i < len(rawLines); i++ {
		lNum := i + 1
		row := rawLines[i]
		h := LineHash(row)
		aStr := fmt.Sprintf("%d:%s", lNum, h)
		blockLines = append(blockLines, HashlineReadLine{
			Anchor:  aStr,
			LineNum: lNum,
			Hash:    h,
			Content: row,
		})
		sb.WriteString(fmt.Sprintf("%s|%s\n", aStr, row))
	}

	return HashlineFindBlockResult{
		FilePath:   filePath,
		TotalLines: len(rawLines),
		Language:   lang,
		BlockStart: span.Start,
		BlockEnd:   span.End,
		Lines:      blockLines,
		Rendered:   sb.String(),
	}, nil
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
		if normOp == "SWAP" || normOp == "REPLACE" || normOp == "INS.PRE" || normOp == "INS.POST" ||
			normOp == "INSERT_BEFORE" || normOp == "INSERT_AFTER" {
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
	// Supports:
	// - Range format: "42:a3..45:f1"
	// - Two-anchor space format: "42:a3 45:f1"
	// - Single anchor: "42:a3"
	trimmedAnchor := strings.TrimSpace(anchorStr)
	var startAnchor, endAnchor LineAnchor
	isTwoAnchor := false

	if strings.Contains(trimmedAnchor, "..") {
		parts := strings.SplitN(trimmedAnchor, "..", 2)
		var err error
		startAnchor, err = ParseAnchor(parts[0])
		if err != nil {
			return HashlinePatchResult{}, err
		}
		endAnchor, err = ParseAnchor(parts[1])
		if err != nil {
			return HashlinePatchResult{}, err
		}
		isTwoAnchor = true
	} else if fields := strings.Fields(trimmedAnchor); len(fields) == 2 {
		var err error
		startAnchor, err = ParseAnchor(fields[0])
		if err != nil {
			return HashlinePatchResult{}, err
		}
		endAnchor, err = ParseAnchor(fields[1])
		if err != nil {
			return HashlinePatchResult{}, err
		}
		isTwoAnchor = true
	} else {
		var err error
		startAnchor, err = ParseAnchor(trimmedAnchor)
		if err != nil {
			return HashlinePatchResult{}, err
		}
		endAnchor = startAnchor
	}

	isBlockOp := normOp == "SWAP.BLK" || normOp == "DEL.BLK" || normOp == "INS.BLK.PRE" || normOp == "INS.BLK.POST"

	// If it's a block operation and only a single anchor was provided, resolve the structural block
	if isBlockOp && !isTwoAnchor {
		// Validate start anchor line first
		if startAnchor.LineNum < 1 || startAnchor.LineNum > len(rawLines) {
			return HashlinePatchResult{}, errors.Errorf("start line %d out of bounds (file has %d lines)", startAnchor.LineNum, len(rawLines))
		}
		// Enforce start hash presence and validity BEFORE block resolution to prevent stale-read bypass
		if startAnchor.Hash == "" {
			return HashlinePatchResult{}, errors.Errorf("missing line hash in start anchor %q (hashline requires format 'line:hash', e.g. '%d:%s')",
				anchorStr, startAnchor.LineNum, LineHash(rawLines[startAnchor.LineNum-1]))
		}
		actualStartHash := LineHash(rawLines[startAnchor.LineNum-1])
		if actualStartHash != startAnchor.Hash {
			return HashlinePatchResult{}, errors.Errorf("stale read: line %d expected hash %s, but found %s (%q)",
				startAnchor.LineNum, startAnchor.Hash, actualStartHash, rawLines[startAnchor.LineNum-1])
		}

		resolver := NewBuiltinBlockResolver()
		span, err := resolver.Resolve(filePath, string(data), startAnchor.LineNum)
		if err != nil {
			return HashlinePatchResult{}, err
		}

		// Reject single-line blocks (bare statements) per hashline reference
		if span.Start == span.End {
			return HashlinePatchResult{}, errors.Errorf("line %d: %s resolved a single-line block (line %d) — bare statement, not the opening line of a multi-line construct. Use a concrete line range instead.",
				startAnchor.LineNum, normOp, span.Start)
		}

		startAnchor = LineAnchor{
			LineNum: span.Start,
			Hash:    LineHash(rawLines[span.Start-1]),
		}
		endAnchor = LineAnchor{
			LineNum: span.End,
			Hash:    LineHash(rawLines[span.End-1]),
		}
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
	case "SWAP", "REPLACE", "SWAP.BLK":
		resultLines = append(resultLines, rawLines[:startIdx]...)
		resultLines = append(resultLines, newBodyLines...)
		resultLines = append(resultLines, rawLines[endIdx:]...)

	case "DEL", "DELETE", "DEL.BLK":
		resultLines = append(resultLines, rawLines[:startIdx]...)
		resultLines = append(resultLines, rawLines[endIdx:]...)

	case "INS.PRE", "INSERT_BEFORE", "INS.BLK.PRE":
		resultLines = append(resultLines, rawLines[:startIdx]...)
		resultLines = append(resultLines, newBodyLines...)
		resultLines = append(resultLines, rawLines[startIdx:]...)

	case "INS.POST", "INSERT_AFTER", "INS.BLK.POST":
		resultLines = append(resultLines, rawLines[:endIdx]...)
		resultLines = append(resultLines, newBodyLines...)
		resultLines = append(resultLines, rawLines[endIdx:]...)

	default:
		return HashlinePatchResult{}, errors.Errorf("unsupported hashline operation %q (use SWAP, DEL, INS.PRE, INS.POST, SWAP.BLK, DEL.BLK, INS.BLK.PRE, or INS.BLK.POST)", op)
	}

	var outputStr string
	if len(resultLines) > 0 {
		outputStr = strings.Join(resultLines, "\n")
		if hasTrailingNewline {
			outputStr += "\n"
		}
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
func NewHashlineReadTool(baseDir string, opts ...Option) (tool.Tool, error) {
	o := newOptions(opts)
	return functiontool.New(functiontool.Config{
		Name:        "hashline_read",
		Description: "Reads a file with stable content-hashed line anchors (e.g. '42:a3'). Use before hashline_patch to prevent edit collision and hallucinated line modifications.",
	}, func(ctx agent.Context, args HashlineReadArgs) (HashlineReadResult, error) {
		if err := o.checkRead("hashline_read", args.FilePath); err != nil {
			return HashlineReadResult{}, err
		}
		return ReadFileWithHashlines(baseDir, args.FilePath, args.StartLine, args.LineCount)
	})
}

// NewHashlineFindBlockTool creates the ADK tool for locating structural code blocks around anchors.
func NewHashlineFindBlockTool(baseDir string, opts ...Option) (tool.Tool, error) {
	o := newOptions(opts)
	return functiontool.New(functiontool.Config{
		Name:        "hashline_find_block",
		Description: "Finds the enclosing syntactic code block (function, struct, class, if/for block) around a line:hash anchor. Useful before SWAP.BLK or DEL.BLK.",
	}, func(ctx agent.Context, args HashlineFindBlockArgs) (HashlineFindBlockResult, error) {
		if err := o.checkRead("hashline_find_block", args.FilePath); err != nil {
			return HashlineFindBlockResult{}, err
		}
		return FindBlockWithHashline(baseDir, args.FilePath, args.Anchor)
	})
}

// NewHashlinePatchTool creates the ADK tool for hash-anchored file patching.
func NewHashlinePatchTool(baseDir string, opts ...Option) (tool.Tool, error) {
	o := newOptions(opts)
	return functiontool.New(functiontool.Config{
		Name:        "hashline_patch",
		Description: "Applies a robust content-hashed patch ('SWAP', 'DEL', 'INS.PRE', 'INS.POST', 'SWAP.BLK', 'DEL.BLK', 'INS.BLK.PRE', 'INS.BLK.POST') using verified anchors like '42:a3', range '42:a3..45:f1', or block anchor. Rejects stale edits if lines changed.",
	}, func(ctx agent.Context, args HashlinePatchArgs) (HashlinePatchResult, error) {
		if !args.DryRun {
			if err := o.checkWrite("hashline_patch", args.FilePath); err != nil {
				return HashlinePatchResult{}, err
			}
		}
		return PatchFileWithHashline(baseDir, args.FilePath, args.Op, args.Anchor, args.Content, args.DryRun)
	})
}
