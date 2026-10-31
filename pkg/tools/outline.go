package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// SymbolOutlineArgs defines input for the symbol_outline tool.
type SymbolOutlineArgs struct {
	FilePath string `json:"file_path" jsonschema:"Path to the file to outline (must be within workspace)"`
}

// SymbolEntry represents a single top-level symbol or method in a file.
type SymbolEntry struct {
	Kind        string `json:"kind" jsonschema:"Symbol kind: func, method, type, const, var, class, interface"`
	Name        string `json:"name" jsonschema:"Symbol identifier"`
	Signature   string `json:"signature" jsonschema:"Declaration header line"`
	StartLine   int    `json:"start_line" jsonschema:"1-based starting line number"`
	EndLine     int    `json:"end_line" jsonschema:"1-based ending line number"`
	StartAnchor string `json:"start_anchor" jsonschema:"Hashline start anchor (e.g. '42:a3')"`
	EndAnchor   string `json:"end_anchor" jsonschema:"Hashline end anchor (e.g. '68:f0')"`
}

// SymbolOutlineResult is the output of the symbol_outline tool.
type SymbolOutlineResult struct {
	FilePath   string        `json:"file_path"`
	Language   string        `json:"language"`
	TotalLines int           `json:"total_lines"`
	Symbols    []SymbolEntry `json:"symbols"`
	Rendered   string        `json:"rendered" jsonschema:"Compact hashline-anchored symbol table ready for hashline_find_block or hashline_patch"`
}

// ExtractSymbolOutline parses a file and returns its declarations with exact
// start..end hashline anchors so the agent can survey a large file in ~100 tokens
// and jump straight to hashline_patch(SWAP.BLK) or hashline_read.
func ExtractSymbolOutline(baseDir, filePath string) (SymbolOutlineResult, error) {
	safePath, err := resolveSafePath(baseDir, filePath)
	if err != nil {
		return SymbolOutlineResult{}, err
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		return SymbolOutlineResult{}, errors.Wrapf(err, "reading file %s", safePath)
	}

	lines := SplitFileLines(string(data))
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filePath), "."))
	lang := LanguageForExtension(ext)

	var symbols []SymbolEntry
	if ext == "go" {
		symbols = extractGoSymbols(filePath, data, lines)
	}
	if len(symbols) == 0 {
		symbols = extractHeuristicSymbols(filePath, string(data), lines)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s (%s, %d lines, %d symbols)]\n", filePath, lang, len(lines), len(symbols))
	for _, s := range symbols {
		rangeStr := s.StartAnchor
		if s.EndLine > s.StartLine && s.EndAnchor != "" {
			rangeStr = s.StartAnchor + ".." + s.EndAnchor
		}
		fmt.Fprintf(&sb, "%s | %-6s %s\n", rangeStr, s.Kind, s.Signature)
	}

	return SymbolOutlineResult{
		FilePath:   filePath,
		Language:   lang,
		TotalLines: len(lines),
		Symbols:    symbols,
		Rendered:   strings.TrimRight(sb.String(), "\n"),
	}, nil
}

func extractGoSymbols(filePath string, src []byte, lines []string) []SymbolEntry {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, src, parser.SkipObjectResolution)
	if err != nil || f == nil {
		return nil
	}

	anchorAt := func(line int) string {
		if line < 1 || line > len(lines) {
			return fmt.Sprintf("%d:00", line)
		}
		return fmt.Sprintf("%d:%s", line, LineHash(lines[line-1]))
	}
	sigAt := func(line int) string {
		if line < 1 || line > len(lines) {
			return ""
		}
		return strings.TrimSpace(lines[line-1])
	}

	var out []SymbolEntry
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			start := fset.Position(d.Pos()).Line
			end := fset.Position(d.End()).Line
			kind := "func"
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = "method"
			}
			out = append(out, SymbolEntry{
				Kind:        kind,
				Name:        name,
				Signature:   sigAt(start),
				StartLine:   start,
				EndLine:     end,
				StartAnchor: anchorAt(start),
				EndAnchor:   anchorAt(end),
			})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					start := fset.Position(sp.Pos()).Line
					end := fset.Position(sp.End()).Line
					if len(d.Specs) == 1 {
						start = fset.Position(d.Pos()).Line
						end = fset.Position(d.End()).Line
					}
					out = append(out, SymbolEntry{
						Kind:        "type",
						Name:        sp.Name.Name,
						Signature:   sigAt(start),
						StartLine:   start,
						EndLine:     end,
						StartAnchor: anchorAt(start),
						EndAnchor:   anchorAt(end),
					})
				case *ast.ValueSpec:
					if len(sp.Names) == 0 {
						continue
					}
					start := fset.Position(sp.Pos()).Line
					end := fset.Position(sp.End()).Line
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					out = append(out, SymbolEntry{
						Kind:        kind,
						Name:        sp.Names[0].Name,
						Signature:   sigAt(start),
						StartLine:   start,
						EndLine:     end,
						StartAnchor: anchorAt(start),
						EndAnchor:   anchorAt(end),
					})
				}
			}
		}
	}
	return out
}

var heuristicDeclRe = regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?(?:pub(?:\([^)]*\))?\s+)?(func|fn|def|class|struct|interface|enum|trait|type|impl)\s+([A-Za-z_][A-Za-z0-9_]*)`)

func extractHeuristicSymbols(filePath, text string, lines []string) []SymbolEntry {
	resolver := NewBuiltinBlockResolver()
	var out []SymbolEntry
	for idx, line := range lines {
		m := heuristicDeclRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lNum := idx + 1
		endLine := lNum
		if span, err := resolver.Resolve(filePath, text, lNum); err == nil && span != nil && span.End >= lNum {
			endLine = span.End
		}
		startAnchor := fmt.Sprintf("%d:%s", lNum, LineHash(line))
		endAnchor := startAnchor
		if endLine >= 1 && endLine <= len(lines) {
			endAnchor = fmt.Sprintf("%d:%s", endLine, LineHash(lines[endLine-1]))
		}
		out = append(out, SymbolEntry{
			Kind:        m[1],
			Name:        m[2],
			Signature:   strings.TrimSpace(line),
			StartLine:   lNum,
			EndLine:     endLine,
			StartAnchor: startAnchor,
			EndAnchor:   endAnchor,
		})
	}
	return out
}

// NewSymbolOutlineTool creates the ADK symbol_outline tool.
func NewSymbolOutlineTool(baseDir string, opts ...Option) (tool.Tool, error) {
	o := newOptions(opts)
	return functiontool.New(functiontool.Config{
		Name:        "symbol_outline",
		Description: "Returns a compact structural outline of all functions, methods, types, and classes in a file with their exact start..end hashline anchors (e.g. '42:a3..68:f0'). Use to survey a file or target SWAP.BLK/DEL.BLK without reading the entire file.",
	}, func(ctx agent.Context, args SymbolOutlineArgs) (SymbolOutlineResult, error) {
		if err := o.checkRead(ctx, "symbol_outline", args.FilePath); err != nil {
			return SymbolOutlineResult{}, err
		}
		return ExtractSymbolOutline(o.effectiveBaseDir(baseDir, args.FilePath, true), args.FilePath)
	})
}
