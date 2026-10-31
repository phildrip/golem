package tools

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// PostEditDiagnostics checks the syntax of a file after a write or patch so the
// agent discovers syntax errors in the same tool round-trip instead of spending
// an extra turn running a compiler or linter.
func PostEditDiagnostics(filePath string, content []byte) []string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filePath), "."))
	switch ext {
	case "go":
		return checkGoSyntax(filePath, content)
	case "json":
		return checkJSONSyntax(filePath, content)
	default:
		return nil
	}
}

func checkGoSyntax(filePath string, content []byte) []string {
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, filePath, content, parser.AllErrors)
	if err == nil {
		return nil
	}
	var diags []string
	if list, ok := err.(scanner.ErrorList); ok {
		for i, e := range list {
			if i >= 10 {
				diags = append(diags, fmt.Sprintf("... (%d more syntax errors)", len(list)-10))
				break
			}
			diags = append(diags, e.Error())
		}
		return diags
	}
	return []string{err.Error()}
}

func checkJSONSyntax(filePath string, content []byte) []string {
	if len(strings.TrimSpace(string(content))) == 0 {
		return []string{fmt.Sprintf("%s: empty JSON document", filePath)}
	}
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		return []string{fmt.Sprintf("%s: %v", filePath, err)}
	}
	return nil
}

// RenderHashlineWindow formats a 1-based inclusive line window [startLine, endLine]
// from lines using "line:hash| content" notation so a follow-up patch can chain
// immediately without calling hashline_read.
func RenderHashlineWindow(filePath string, lines []string, startLine, endLine int) string {
	total := len(lines)
	if total == 0 {
		return fmt.Sprintf("[%s#lines 0-0 of 0 (empty file)]", filePath)
	}
	if startLine < 1 {
		startLine = 1
	}
	if endLine > total {
		endLine = total
	}
	if startLine > endLine {
		startLine = endLine
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s#lines %d-%d of %d]\n", filePath, startLine, endLine, total)
	for i := startLine - 1; i < endLine && i < total; i++ {
		lNum := i + 1
		row := lines[i]
		h := LineHash(row)
		fmt.Fprintf(&sb, "%d:%s| %s\n", lNum, h, row)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// SplitFileLines splits file text into lines using the same trailing-newline
// convention as ReadFileWithHashlines.
func SplitFileLines(text string) []string {
	if len(text) == 0 {
		return nil
	}
	rawLines := strings.Split(text, "\n")
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" && strings.HasSuffix(text, "\n") {
		rawLines = rawLines[:len(rawLines)-1]
	}
	return rawLines
}

// CheckFileDiagnosticsOnDisk reads safePath and runs PostEditDiagnostics.
func CheckFileDiagnosticsOnDisk(displayPath, safePath string) []string {
	data, err := os.ReadFile(safePath)
	if err != nil {
		return nil
	}
	return PostEditDiagnostics(displayPath, data)
}
