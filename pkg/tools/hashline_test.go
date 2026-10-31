package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinBlockResolver_BraceMatching(t *testing.T) {
	resolver := NewBuiltinBlockResolver()

	rustCode := `fn hello() {
    let x = 1;
    if true {
        println!("ok");
    }
}
`
	// Anchor line 3: "    if true {" -> inner block lines 3..5
	span, err := resolver.Resolve("test.rs", rustCode, 3)
	if err != nil {
		t.Fatalf("unexpected error resolving line 3: %v", err)
	}
	if span.Start != 3 || span.End != 5 {
		t.Errorf("expected inner span [3, 5], got [%d, %d]", span.Start, span.End)
	}

	// Anchor line 1: "fn hello() {" -> outer function lines 1..6
	spanOuter, err := resolver.Resolve("test.rs", rustCode, 1)
	if err != nil {
		t.Fatalf("unexpected error resolving line 1: %v", err)
	}
	if spanOuter.Start != 1 || spanOuter.End != 6 {
		t.Errorf("expected outer span [1, 6], got [%d, %d]", spanOuter.Start, spanOuter.End)
	}
}

func TestBuiltinBlockResolver_GoBrace(t *testing.T) {
	resolver := NewBuiltinBlockResolver()

	goCode := `func main() {
	if true {
		println("ok")
	}
}
`
	span, err := resolver.Resolve("main.go", goCode, 2)
	if err != nil {
		t.Fatalf("unexpected error resolving line 2: %v", err)
	}
	if span.Start != 2 || span.End != 4 {
		t.Errorf("expected [2, 4], got [%d, %d]", span.Start, span.End)
	}
}

func TestBuiltinBlockResolver_TemplateLiteralsAndStrings(t *testing.T) {
	resolver := NewBuiltinBlockResolver()

	jsCode := `function greet(name) {
  return ` + "`" + `Hello, ${name}!` + "`" + `;
}
`
	span, err := resolver.Resolve("greet.js", jsCode, 2)
	if err != nil {
		t.Fatalf("unexpected error resolving line 2: %v", err)
	}
	if span.Start != 1 || span.End != 3 {
		t.Errorf("expected [1, 3], got [%d, %d]", span.Start, span.End)
	}

	// Brace inside string literal
	rustStrCode := `fn hello() {
    let s = "{";
}
`
	spanRust, err := resolver.Resolve("hello.rs", rustStrCode, 2)
	if err != nil {
		t.Fatalf("unexpected error resolving line 2: %v", err)
	}
	if spanRust.Start != 1 || spanRust.End != 3 {
		t.Errorf("expected [1, 3], got [%d, %d]", spanRust.Start, spanRust.End)
	}
}

func TestBuiltinBlockResolver_PythonIndentation(t *testing.T) {
	resolver := NewBuiltinBlockResolver()

	pyCode := `def hello():
    x = 1
    if True:
        print('ok')
    return x
`
	// Anchor line 1: top-level def -> full function lines 1..5
	spanTop, err := resolver.Resolve("script.py", pyCode, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spanTop.Start != 1 || spanTop.End != 5 {
		t.Errorf("expected [1, 5], got [%d, %d]", spanTop.Start, spanTop.End)
	}

	// Anchor line 4: nested statement inside if -> scans back to line 3 (if True:)
	spanNested, err := resolver.Resolve("script.py", pyCode, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spanNested.Start != 3 || spanNested.End != 4 {
		t.Errorf("expected [3, 4], got [%d, %d]", spanNested.Start, spanNested.End)
	}
}

func TestBuiltinBlockResolver_Ruby(t *testing.T) {
	resolver := NewBuiltinBlockResolver()

	rbCode := `def hello
  x = 1
  if true
    puts 'ok'
  end
end
`
	span, err := resolver.Resolve("test.rb", rbCode, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if span.Start != 1 || span.End != 6 {
		t.Errorf("expected [1, 6], got [%d, %d]", span.Start, span.End)
	}
}

func TestHashline_FindBlockTool(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "math.go"
	fullPath := filepath.Join(tempDir, filePath)

	content := `package math

func Add(a, b int) int {
	return a + b
}

func Sub(a, b int) int {
	return a - b
}
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	readRes, err := ReadFileWithHashlines(tempDir, filePath, 1, 15)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	// Find block for line 4 (inside Add)
	addAnchor := readRes.Lines[3].Anchor // line 4
	findRes, err := FindBlockWithHashline(tempDir, filePath, addAnchor)
	if err != nil {
		t.Fatalf("find block failed: %v", err)
	}
	if findRes.Language != "Go" {
		t.Errorf("expected language Go, got %s", findRes.Language)
	}
	if findRes.BlockStart != 3 || findRes.BlockEnd != 5 {
		t.Errorf("expected block [3, 5], got [%d, %d]", findRes.BlockStart, findRes.BlockEnd)
	}
}

func TestHashline_SwapBlock_Rust(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "test.rs"
	fullPath := filepath.Join(tempDir, filePath)

	content := `fn hello() {
    let x = 1;
    if true {
        println!("ok");
    }
}
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readRes, _ := ReadFileWithHashlines(tempDir, filePath, 1, 10)
	anchor1 := readRes.Lines[0].Anchor // line 1: fn hello() {

	// 1. SWAP.BLK on line 1 replaces entire 6-line function
	patchRes, err := PatchFileWithHashline(tempDir, filePath, "SWAP.BLK", anchor1, "fn replaced() {\n    // new body\n}", false)
	if err != nil {
		t.Fatalf("SWAP.BLK failed: %v", err)
	}
	if !patchRes.Success {
		t.Errorf("expected success: %+v", patchRes)
	}

	updated, _ := os.ReadFile(fullPath)
	expected := "fn replaced() {\n    // new body\n}\n"
	if string(updated) != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, string(updated))
	}
}

func TestHashline_SwapBlock_Inner(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "test.rs"
	fullPath := filepath.Join(tempDir, filePath)

	content := `fn hello() {
    let x = 1;
    if true {
        println!("ok");
    }
}
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readRes, _ := ReadFileWithHashlines(tempDir, filePath, 1, 10)
	anchor3 := readRes.Lines[2].Anchor // line 3: if true {

	patchRes, err := PatchFileWithHashline(tempDir, filePath, "SWAP.BLK", anchor3, "    if false {\n        // nothing\n    }", false)
	if err != nil {
		t.Fatalf("SWAP.BLK failed: %v", err)
	}
	if !patchRes.Success {
		t.Errorf("expected success: %+v", patchRes)
	}

	updated, _ := os.ReadFile(fullPath)
	expected := `fn hello() {
    let x = 1;
    if false {
        // nothing
    }
}
`
	if string(updated) != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, string(updated))
	}
}

func TestHashline_DeleteBlock_Rust(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "test.rs"
	fullPath := filepath.Join(tempDir, filePath)

	content := `fn hello() {
    let x = 1;
}
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readRes, _ := ReadFileWithHashlines(tempDir, filePath, 1, 5)
	anchor1 := readRes.Lines[0].Anchor

	patchRes, err := PatchFileWithHashline(tempDir, filePath, "DEL.BLK", anchor1, "", false)
	if err != nil {
		t.Fatalf("DEL.BLK failed: %v", err)
	}
	if !patchRes.Success {
		t.Errorf("expected success: %+v", patchRes)
	}

	updated, _ := os.ReadFile(fullPath)
	if string(updated) != "" {
		t.Errorf("expected empty file after DEL.BLK, got: %q", string(updated))
	}
}

func TestHashline_InsertAfterBlock_Rust(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "test.rs"
	fullPath := filepath.Join(tempDir, filePath)

	content := `fn hello() {
    let x = 1;
}
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readRes, _ := ReadFileWithHashlines(tempDir, filePath, 1, 5)
	anchor1 := readRes.Lines[0].Anchor

	patchRes, err := PatchFileWithHashline(tempDir, filePath, "INS.BLK.POST", anchor1, "fn world() {\n    let y = 2;\n}", false)
	if err != nil {
		t.Fatalf("INS.BLK.POST failed: %v", err)
	}
	if !patchRes.Success {
		t.Errorf("expected success: %+v", patchRes)
	}

	updated, _ := os.ReadFile(fullPath)
	expected := `fn hello() {
    let x = 1;
}
fn world() {
    let y = 2;
}
`
	if string(updated) != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, string(updated))
	}
}

func TestHashline_SwapBlock_Python(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "test.py"
	fullPath := filepath.Join(tempDir, filePath)

	content := `def hello():
    x = 1
    if True:
        print('ok')
    return x
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readRes, _ := ReadFileWithHashlines(tempDir, filePath, 1, 10)
	anchor1 := readRes.Lines[0].Anchor

	patchRes, err := PatchFileWithHashline(tempDir, filePath, "SWAP.BLK", anchor1, "def hi():\n    pass", false)
	if err != nil {
		t.Fatalf("SWAP.BLK on python failed: %v", err)
	}
	if !patchRes.Success {
		t.Errorf("expected success: %+v", patchRes)
	}

	updated, _ := os.ReadFile(fullPath)
	expected := "def hi():\n    pass\n"
	if string(updated) != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, string(updated))
	}
}

func TestHashline_TwoAnchorBlock(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "test.txt"
	fullPath := filepath.Join(tempDir, filePath)

	content := `line1
line2
line3
line4
line5
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readRes, _ := ReadFileWithHashlines(tempDir, filePath, 1, 5)
	h2 := readRes.Lines[1].Anchor
	h4 := readRes.Lines[3].Anchor

	// Space-delimited two anchors: "2:xx 4:yy"
	twoAnchor := h2 + " " + h4
	patchRes, err := PatchFileWithHashline(tempDir, filePath, "SWAP.BLK", twoAnchor, "replacement", false)
	if err != nil {
		t.Fatalf("SWAP.BLK with two anchors failed: %v", err)
	}
	if !patchRes.Success {
		t.Errorf("expected success: %+v", patchRes)
	}

	updated, _ := os.ReadFile(fullPath)
	expected := "line1\nreplacement\nline5\n"
	if string(updated) != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, string(updated))
	}
}

func TestHashline_BlockOp_RejectsMissingHash(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "test.rs"
	fullPath := filepath.Join(tempDir, filePath)

	content := `fn hello() {
    let x = 1;
}
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Attempting SWAP.BLK with bare line number "1" (no hash) must fail
	_, err := PatchFileWithHashline(tempDir, filePath, "SWAP.BLK", "1", "fn foo() {}", false)
	if err == nil {
		t.Fatalf("expected missing line hash error, got nil")
	}
	if !strings.Contains(err.Error(), "missing line hash") {
		t.Errorf("expected 'missing line hash' in error, got: %v", err)
	}
}

func TestHashline_BlockOp_RejectsBareStatement(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "test.py"
	fullPath := filepath.Join(tempDir, filePath)

	content := `x = 1
y = 2
`
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readRes, _ := ReadFileWithHashlines(tempDir, filePath, 1, 2)
	anchor1 := readRes.Lines[0].Anchor // line 1: x = 1

	// Indentation analysis on line 1 of flat statements will resolve start=1, end=1 (single-line)
	// Must be rejected as bare statement!
	_, err := PatchFileWithHashline(tempDir, filePath, "SWAP.BLK", anchor1, "x = 99", false)
	if err == nil {
		t.Fatalf("expected error for bare statement block, got nil")
	}
	if !strings.Contains(err.Error(), "bare statement") {
		t.Errorf("expected error to mention 'bare statement', got: %v", err)
	}
}

func TestHashline_ParseAnchor_TrailingColon(t *testing.T) {
	a, err := ParseAnchor("42:a3:")
	if err != nil {
		t.Fatalf("unexpected error parsing anchor with trailing colon: %v", err)
	}
	if a.LineNum != 42 || a.Hash != "a3" {
		t.Errorf("expected 42:a3, got %d:%s", a.LineNum, a.Hash)
	}
	if _, err := ParseAnchor("42:a3:45:f1"); err == nil {
		t.Errorf("expected error for anchor with multiple colons, got nil")
	}
}

func TestHashline_PreservesLeadingPlusInRawCode(t *testing.T) {
	tempDir := t.TempDir()
	filePath := "math.js"
	fullPath := filepath.Join(tempDir, filePath)
	if err := os.WriteFile(fullPath, []byte("let x = 1;\n"), 0644); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	readRes, _ := ReadFileWithHashlines(tempDir, filePath, 1, 5)
	anchor := readRes.Lines[0].Anchor

	// Raw code where a line starts with "+" must not have "+" stripped.
	rawReplacement := "let x = 1;\n+x;\n"
	if _, err := PatchFileWithHashline(tempDir, filePath, "SWAP", anchor, rawReplacement, false); err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	got, _ := os.ReadFile(fullPath)
	if string(got) != "let x = 1;\n+x;\n" {
		t.Errorf("expected '+x;' preserved without extra blank line, got %q", string(got))
	}

	// Unified diff style where all lines start with "+ " should still strip "+ ".
	readRes2, _ := ReadFileWithHashlines(tempDir, filePath, 1, 5)
	rangeAnchor := readRes2.Lines[0].Anchor + ".." + readRes2.Lines[1].Anchor
	if _, err := PatchFileWithHashline(tempDir, filePath, "SWAP", rangeAnchor, "+ const a = 1;\n+ const b = 2;", false); err != nil {
		t.Fatalf("diff-prefixed patch failed: %v", err)
	}
	got2, _ := os.ReadFile(fullPath)
	if string(got2) != "const a = 1;\nconst b = 2;\n" {
		t.Errorf("expected '+ ' stripped on unified diff block, got %q", string(got2))
	}
}
