package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashlinePatchReturnsUpdatedContextAndDiagnostics(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "sample.go")
	initial := "package sample\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	if err := os.WriteFile(goFile, []byte(initial), 0644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	readRes, err := ReadFileWithHashlines(dir, "sample.go", 1, 10)
	if err != nil {
		t.Fatalf("ReadFileWithHashlines: %v", err)
	}
	line4Anchor := readRes.Lines[3].Anchor // "\treturn a + b"

	// 1. Valid Go edit: no diagnostics, UpdatedContext contains new line anchor
	patchRes, err := PatchFileWithHashline(dir, "sample.go", "SWAP", line4Anchor, "\treturn a + b + 1", false)
	if err != nil {
		t.Fatalf("PatchFileWithHashline valid: %v", err)
	}
	if len(patchRes.Diagnostics) != 0 {
		t.Fatalf("expected 0 diagnostics for valid Go edit, got %v", patchRes.Diagnostics)
	}
	wantNewAnchor := "4:" + LineHash("\treturn a + b + 1")
	if !strings.Contains(patchRes.UpdatedContext, wantNewAnchor) {
		t.Fatalf("expected UpdatedContext to contain new anchor %q, got:\n%s", wantNewAnchor, patchRes.UpdatedContext)
	}

	// 2. Chain a second edit directly using the anchor from UpdatedContext without calling ReadFileWithHashlines!
	badPatchRes, err := PatchFileWithHashline(dir, "sample.go", "SWAP", wantNewAnchor, "\treturn a +", false)
	if err != nil {
		t.Fatalf("PatchFileWithHashline syntax-broken: %v", err)
	}
	if len(badPatchRes.Diagnostics) == 0 {
		t.Fatalf("expected post-edit syntax diagnostic for broken Go expression 'return a +'")
	}
	if !strings.Contains(badPatchRes.Message, "WARNING:") {
		t.Fatalf("expected warning in patch message, got: %s", badPatchRes.Message)
	}
}

func TestPostEditDiagnosticsJSON(t *testing.T) {
	validDiags := PostEditDiagnostics("config.json", []byte(`{"ok": true}`))
	if len(validDiags) != 0 {
		t.Fatalf("expected no diagnostics for valid JSON, got %v", validDiags)
	}

	invalidDiags := PostEditDiagnostics("config.json", []byte(`{"ok": true,}`))
	if len(invalidDiags) == 0 {
		t.Fatalf("expected syntax diagnostic for invalid JSON")
	}
}
