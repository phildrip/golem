package tools

import (
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// BlockSpan represents a 1-indexed inclusive line range [Start, End].
type BlockSpan struct {
	Start int
	End   int
}

// BlockResolver resolves a line number within a file into its enclosing syntactic block.
type BlockResolver interface {
	Resolve(path, text string, line int) (*BlockSpan, error)
}

// BuiltinBlockResolver is a heuristic block resolver matching quangdang46/hashline.
// It uses:
// - Brace-matching for C-family languages (Go, Rust, JS/TS, C/C++, Java, C#, Swift, etc.)
// - Indentation analysis for Python, Verse, and unknown languages
// - End-keyword balancing for Ruby
type BuiltinBlockResolver struct{}

// NewBuiltinBlockResolver creates a new BuiltinBlockResolver.
func NewBuiltinBlockResolver() *BuiltinBlockResolver {
	return &BuiltinBlockResolver{}
}

// Resolve resolves the enclosing block for the given 1-indexed line.
func (r *BuiltinBlockResolver) Resolve(path, text string, line int) (*BlockSpan, error) {
	if text == "" {
		return nil, errors.Errorf("empty text")
	}

	rawLines := strings.Split(text, "\n")
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" && strings.HasSuffix(text, "\n") {
		rawLines = rawLines[:len(rawLines)-1]
	}

	anchorIdx := line - 1
	if anchorIdx < 0 || anchorIdx >= len(rawLines) {
		return nil, errors.Errorf("line %d out of bounds (file has %d lines)", line, len(rawLines))
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))

	var span *BlockSpan
	switch ext {
	case "py", "verse":
		if s := resolveBraceBlock(rawLines, anchorIdx, ext); s != nil {
			span = s
		} else {
			span = resolveIndentBlock(rawLines, anchorIdx)
		}
	case "rb":
		span = resolveRubyBlock(rawLines, anchorIdx)
	case "rs", "js", "ts", "tsx", "jsx", "go", "java", "c", "cpp", "h", "hpp",
		"cs", "kt", "kts", "swift", "scala", "dart", "zig", "m", "mm":
		span = resolveBraceBlock(rawLines, anchorIdx, ext)
	default:
		if s := resolveBraceBlock(rawLines, anchorIdx, ext); s != nil {
			span = s
		} else {
			span = resolveIndentBlock(rawLines, anchorIdx)
		}
	}

	if span == nil {
		return nil, errors.Errorf("cannot resolve syntactic block around line %d", line)
	}
	return span, nil
}

// resolveBraceBlock scans brace pairs '{' and '}' ignoring strings and comments.
// Returns 1-indexed [Start, End] BlockSpan if found.
func resolveBraceBlock(lines []string, anchorIdx int, ext string) *BlockSpan {
	pairs := findBracePairs(lines, ext)
	var bestStart, bestEnd int
	found := false

	for _, p := range pairs {
		if p[0] <= anchorIdx && p[1] >= anchorIdx {
			if !found || p[0] > bestStart {
				bestStart = p[0]
				bestEnd = p[1]
				found = true
			}
		}
	}

	if !found {
		return nil
	}
	return &BlockSpan{
		Start: bestStart + 1,
		End:   bestEnd + 1,
	}
}

func findBracePairs(lines []string, ext string) [][2]int {
	var pairs [][2]int
	var stack []int

	lineComment := "//"
	if ext == "py" || ext == "rb" {
		lineComment = "#"
	}
	useBlockComments := ext != "py" && ext != "rb"

	inBlockComment := false
	inTemplateLiteral := false
	templateEscape := false

	for lineIdx, line := range lines {
		bytes := []byte(line)
		i := 0
		inSingleQuote := false
		inDoubleQuote := false
		prevEscape := false

		for i < len(bytes) {
			if inTemplateLiteral {
				if templateEscape {
					templateEscape = false
				} else if bytes[i] == '\\' {
					templateEscape = true
				} else if bytes[i] == '`' {
					inTemplateLiteral = false
				}
				i++
				continue
			}

			if prevEscape {
				prevEscape = false
				i++
				continue
			}
			if (inSingleQuote || inDoubleQuote) && bytes[i] == '\\' {
				prevEscape = true
				i++
				continue
			}

			if inBlockComment {
				if i+1 < len(bytes) && bytes[i] == '*' && bytes[i+1] == '/' {
					inBlockComment = false
					i += 2
					continue
				}
				i++
				continue
			}

			if !inSingleQuote && !inDoubleQuote && strings.HasPrefix(string(bytes[i:]), lineComment) {
				break
			}

			if useBlockComments && !inSingleQuote && !inDoubleQuote && i+1 < len(bytes) && bytes[i] == '/' && bytes[i+1] == '*' {
				inBlockComment = true
				i += 2
				continue
			}

			if inSingleQuote && bytes[i] == '\'' {
				inSingleQuote = false
				i++
				continue
			}
			if inDoubleQuote && bytes[i] == '"' {
				inDoubleQuote = false
				i++
				continue
			}

			if !inSingleQuote && !inDoubleQuote && bytes[i] == '\'' {
				inSingleQuote = true
				i++
				continue
			}
			if !inSingleQuote && !inDoubleQuote && bytes[i] == '"' {
				inDoubleQuote = true
				i++
				continue
			}
			if !inSingleQuote && !inDoubleQuote && bytes[i] == '`' {
				inTemplateLiteral = true
				i++
				continue
			}

			if !inSingleQuote && !inDoubleQuote && !inBlockComment {
				if bytes[i] == '{' {
					stack = append(stack, lineIdx)
				} else if bytes[i] == '}' {
					if len(stack) > 0 {
						start := stack[len(stack)-1]
						stack = stack[:len(stack)-1]
						pairs = append(pairs, [2]int{start, lineIdx})
					}
				}
			}

			i++
		}
	}

	return pairs
}

// leadingWhitespaceCount returns the number of leading spaces/tabs.
func leadingWhitespaceCount(s string) int {
	return len(s) - len(strings.TrimLeft(s, " \t\r\n"))
}

// resolveIndentBlock resolves blocks using indentation (e.g. Python).
func resolveIndentBlock(lines []string, anchorIdx int) *BlockSpan {
	anchorIndent := leadingWhitespaceCount(lines[anchorIdx])

	if anchorIndent == 0 {
		end := len(lines) - 1
		for i := anchorIdx + 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if leadingWhitespaceCount(lines[i]) <= anchorIndent {
				end = i - 1
				break
			}
		}
		return &BlockSpan{
			Start: anchorIdx + 1,
			End:   end + 1,
		}
	}

	start := -1
	for i := anchorIdx - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t != "" && leadingWhitespaceCount(lines[i]) < anchorIndent {
			start = i
			break
		}
	}
	if start == -1 {
		return nil
	}

	startIndent := leadingWhitespaceCount(lines[start])
	end := len(lines) - 1
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if leadingWhitespaceCount(lines[i]) <= startIndent {
			end = i - 1
			break
		}
	}

	return &BlockSpan{
		Start: start + 1,
		End:   end + 1,
	}
}

var rubyOpeners = []string{
	"def ", "class ", "module ", "do ", "do|", "if ", "unless ", "while ", "until ", "for ",
	"begin ", "case ",
}

func rubyOpenerCount(trimmed string) int {
	for _, opener := range rubyOpeners {
		if strings.HasPrefix(trimmed, opener) {
			return 1
		}
	}
	return 0
}

// resolveRubyBlock resolves Ruby blocks matching def/class/if...end.
func resolveRubyBlock(lines []string, anchorIdx int) *BlockSpan {
	depth := 0
	start := -1
	for i := anchorIdx; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		endCount := 0
		if trimmed == "end" {
			endCount = 1
		}
		openCount := rubyOpenerCount(trimmed)
		depth += endCount
		depth -= openCount
		if openCount > 0 && depth <= 0 {
			start = i
			break
		}
	}
	if start == -1 {
		return nil
	}

	depth = 0
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		openCount := rubyOpenerCount(trimmed)
		endCount := 0
		if trimmed == "end" {
			endCount = 1
		}
		depth += openCount
		depth -= endCount
		if i > start && depth <= 0 && trimmed == "end" {
			return &BlockSpan{Start: start + 1, End: i + 1}
		}
		if i == start && depth <= 0 {
			return &BlockSpan{Start: start + 1, End: i + 1}
		}
	}

	return nil
}

// LanguageForExtension returns human-readable language name for extension.
func LanguageForExtension(ext string) string {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "rs":
		return "Rust"
	case "py":
		return "Python"
	case "js":
		return "JavaScript"
	case "ts":
		return "TypeScript"
	case "tsx":
		return "TSX"
	case "jsx":
		return "JSX"
	case "go":
		return "Go"
	case "rb":
		return "Ruby"
	case "verse":
		return "Verse"
	case "java":
		return "Java"
	case "c":
		return "C"
	case "cpp", "hpp":
		return "C++"
	case "h":
		return "C/C++ Header"
	case "cs":
		return "C#"
	case "kt", "kts":
		return "Kotlin"
	case "swift":
		return "Swift"
	case "scala":
		return "Scala"
	case "dart":
		return "Dart"
	case "zig":
		return "Zig"
	case "m":
		return "Objective-C"
	case "mm":
		return "Objective-C++"
	default:
		return "Unknown"
	}
}
