package importer

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	_ "modernc.org/sqlite"
)

// DeduceFormat inspects a given file or directory path and determines its format.
func DeduceFormat(path string) (FormatType, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return FormatUnknown, errors.Wrapf(err, "stat path %q", path)
	}

	if fi.IsDir() {
		return deduceDirFormat(path)
	}
	return deduceFileFormat(path)
}

func deduceDirFormat(dir string) (FormatType, error) {
	// 1. Antigravity brain conversation directory check
	agyLog1 := filepath.Join(dir, ".system_generated", "logs", "transcript.jsonl")
	agyLog2 := filepath.Join(dir, "transcript.jsonl")
	if fileExists(agyLog1) || fileExists(agyLog2) {
		return FormatAGY, nil
	}

	// 2. Cursor workspace storage directory check
	cursorVscdb := filepath.Join(dir, "state.vscdb")
	cursorWorkspace := filepath.Join(dir, "workspace.json")
	if fileExists(cursorVscdb) || fileExists(cursorWorkspace) {
		return FormatCursor, nil
	}

	// 3. OpenCode data directory (contains opencode.db)
	if fileExists(filepath.Join(dir, "opencode.db")) {
		return FormatOpenCode, nil
	}

	return FormatUnknown, errors.Errorf("unable to deduce format for directory %q: no AGY, Cursor, or OpenCode markers found", dir)
}

func deduceFileFormat(filePath string) (FormatType, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	base := strings.ToLower(filepath.Base(filePath))

	// Fast path for known names/extensions
	if ext == ".vscdb" {
		return FormatCursor, nil
	}
	if base == "opencode.db" {
		return FormatOpenCode, nil
	}
	if base == "transcript.jsonl" || base == "transcript_full.jsonl" {
		return FormatAGY, nil
	}

	// Sniff file header
	f, err := os.Open(filePath)
	if err != nil {
		return FormatUnknown, errors.Wrapf(err, "opening file %q", filePath)
	}
	defer f.Close()

	header := make([]byte, 512)
	n, _ := io.ReadFull(f, header)
	header = header[:n]

	// Check for SQLite database magic header
	if len(header) >= 16 && bytes.HasPrefix(header, []byte("SQLite format 3\x00")) {
		// Read-only like the parser: deducing a format must not create or touch
		// a database file (golem-2hp.5).
		db, err := openSQLiteReadOnly(filePath)
		if err == nil {
			defer db.Close()
			var cursorTables int
			_ = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('ItemTable', 'cursorDiskKV')").Scan(&cursorTables)
			if cursorTables > 0 {
				return FormatCursor, nil
			}
			var openCodeTables int
			_ = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('session', 'message', 'part')").Scan(&openCodeTables)
			if openCodeTables == 3 {
				return FormatOpenCode, nil
			}
		}
	}

	// Check JSON / JSONL text contents
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		scanner := bufio.NewScanner(f)
		for i := 0; i < 5 && scanner.Scan(); i++ {
			line := scanner.Text()
			if strings.Contains(line, `"step_index"`) ||
				strings.Contains(line, `"USER_EXPLICIT"`) ||
				strings.Contains(line, `"PLANNER_RESPONSE"`) {
				return FormatAGY, nil
			}
		}
	}

	return FormatUnknown, errors.Errorf("unable to deduce format for file %q", filePath)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
