package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepFilesEmitsHashlineAnchorsAndSnippets(t *testing.T) {
	dir := t.TempDir()
	src := "package demo\n\nfunc Greet(name string) string {\n\treturn \"hello \" + name\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "greet.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	res, err := GrepFiles(dir, "", "hello", "*.go", 10)
	if err != nil {
		t.Fatalf("GrepFiles: %v", err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(res.Matches))
	}
	m := res.Matches[0]
	wantAnchor := "4:" + LineHash("\treturn \"hello \" + name")
	if m.Anchor != wantAnchor {
		t.Fatalf("GrepMatch.Anchor = %q, want %q", m.Anchor, wantAnchor)
	}
	if !strings.Contains(m.Snippet, wantAnchor+"| \treturn \"hello \" + name") {
		t.Fatalf("expected Snippet to contain hashline-anchored match line, got:\n%s", m.Snippet)
	}

	// Verify 1-hop edit directly from GrepFiles -> PatchFileWithHashline without calling ReadFileWithHashlines!
	patchRes, err := PatchFileWithHashline(dir, m.File, "SWAP", m.Anchor, "\treturn \"hi \" + name", false)
	if err != nil {
		t.Fatalf("1-hop PatchFileWithHashline failed: %v", err)
	}
	if !patchRes.Success || len(patchRes.Diagnostics) != 0 {
		t.Fatalf("unexpected patch result: %+v", patchRes)
	}
}

func TestExtractSymbolOutlineGoAndPython(t *testing.T) {
	dir := t.TempDir()
	goSrc := `package demo

type Server struct {
	Addr string
}

func NewServer(addr string) *Server {
	return &Server{Addr: addr}
}

func (s *Server) Start() error {
	return nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "server.go"), []byte(goSrc), 0644); err != nil {
		t.Fatalf("write server.go: %v", err)
	}

	outline, err := ExtractSymbolOutline(dir, "server.go")
	if err != nil {
		t.Fatalf("ExtractSymbolOutline(server.go): %v", err)
	}
	if len(outline.Symbols) != 3 {
		t.Fatalf("expected 3 Go symbols (type Server, func NewServer, method Start), got %d: %+v", len(outline.Symbols), outline.Symbols)
	}
	if outline.Symbols[0].Kind != "type" || outline.Symbols[0].Name != "Server" {
		t.Fatalf("symbol 0 = %+v, want type Server", outline.Symbols[0])
	}
	if outline.Symbols[1].Kind != "func" || outline.Symbols[1].Name != "NewServer" {
		t.Fatalf("symbol 1 = %+v, want func NewServer", outline.Symbols[1])
	}
	if outline.Symbols[2].Kind != "method" || outline.Symbols[2].Name != "Start" {
		t.Fatalf("symbol 2 = %+v, want method Start", outline.Symbols[2])
	}

	// Verify we can use the symbol's StartAnchor..EndAnchor range directly with PatchFileWithHashline
	startRange := outline.Symbols[2].StartAnchor + ".." + outline.Symbols[2].EndAnchor
	patchRes, err := PatchFileWithHashline(dir, "server.go", "SWAP", startRange, "func (s *Server) Start() error {\n\treturn nil // updated\n}", false)
	if err != nil {
		t.Fatalf("PatchFileWithHashline using symbol_outline anchors failed: %v", err)
	}
	if !patchRes.Success || len(patchRes.Diagnostics) != 0 {
		t.Fatalf("unexpected patch result: %+v", patchRes)
	}

	// Verify heuristic outline for Python
	pySrc := "class Worker:\n    def run(self):\n        return True\n\ndef helper(x):\n    return x + 1\n"
	if err := os.WriteFile(filepath.Join(dir, "worker.py"), []byte(pySrc), 0644); err != nil {
		t.Fatalf("write worker.py: %v", err)
	}
	pyOutline, err := ExtractSymbolOutline(dir, "worker.py")
	if err != nil {
		t.Fatalf("ExtractSymbolOutline(worker.py): %v", err)
	}
	if len(pyOutline.Symbols) < 2 {
		t.Fatalf("expected Python symbols to be extracted, got %+v", pyOutline.Symbols)
	}
}
