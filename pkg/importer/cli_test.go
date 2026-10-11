package importer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/golem/pkg/memory/fractal"
)

func TestHandleImportOption(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "golem-test-cli-import-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Isolate discovery from the host: HOME/XDG_DATA_HOME point at tempDir so the
	// Cursor and OpenCode sources find nothing, leaving only the AGY fixture.
	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "xdg"))

	storeDir := filepath.Join(tempDir, "store")
	store, err := fractal.OpenStore(storeDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// 1. Empty import argument -> no-op
	handled, err := HandleImportOption("", store, tempDir, strings.NewReader(""), &bytes.Buffer{})
	if err != nil || handled {
		t.Errorf("expected handled=false on empty, got %v (err: %v)", handled, err)
	}

	// 2. Direct file import with AGY transcript
	agyFile := filepath.Join(tempDir, "transcript.jsonl")
	data := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-10-05T15:46:40Z","content":"<USER_REQUEST>\nFix database race\n</USER_REQUEST>"}` + "\n"
	if err := os.WriteFile(agyFile, []byte(data), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	var out bytes.Buffer
	handled, err = HandleImportOption(agyFile, store, tempDir, strings.NewReader(""), &out)
	if err != nil || !handled {
		t.Fatalf("HandleImportOption direct file failed: %v", err)
	}
	if !strings.Contains(out.String(), "Imported 1 events") {
		t.Errorf("expected output to mention imported 1 events, got: %s", out.String())
	}
	if store.CountEvents() != 1 {
		t.Errorf("expected store to contain 1 event, got %d", store.CountEvents())
	}

	// 3. Interactive picker selection
	t.Setenv("AGY_BRAIN_DIR", tempDir)
	convDir := filepath.Join(tempDir, "conv-picker", ".system_generated", "logs")
	_ = os.MkdirAll(convDir, 0755)
	_ = os.WriteFile(filepath.Join(convDir, "transcript.jsonl"), []byte(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-10-05T15:46:40Z","content":"<USER_REQUEST>\nPicked task\n</USER_REQUEST>"}`+"\n"), 0644)

	var pickOut bytes.Buffer
	handled, err = HandleImportOption("pick", store, tempDir, strings.NewReader("1\n"), &pickOut)
	if err != nil || !handled {
		t.Fatalf("HandleImportOption pick failed: %v", err)
	}
	if !strings.Contains(pickOut.String(), "Imported 1 events") {
		t.Errorf("expected pick output to mention imported 1 events, got: %s", pickOut.String())
	}
}
