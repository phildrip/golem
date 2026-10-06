package gg

import (
	"context"

	"github.com/user/golem/pkg/search/gg/engine"
	"github.com/user/golem/pkg/search/gg/grep"
	"github.com/user/golem/pkg/search/gg/parser"
	"github.com/user/golem/pkg/search/gg/scoring"
)

// Re-export common types
type (
	FilePicker        = engine.FilePicker
	FilePickerOptions = engine.FilePickerOptions
	SearchResult      = engine.SearchResult
	GrepMatch         = grep.GrepMatch
	GrepOptions       = grep.GrepOptions
	Query             = parser.Query
	MatchResult       = scoring.MatchResult
)

// FindFiles performs a one-shot gitignore-aware fuzzy file search in rootDir.
func FindFiles(ctx context.Context, rootDir, query string, maxResults int) ([]SearchResult, error) {
	picker, err := engine.NewFilePicker(ctx, engine.FilePickerOptions{
		RootDir:       rootDir,
		EnableWatcher: false,
		MaxFiles:      0,
	})
	if err != nil {
		return nil, err
	}
	defer picker.Close()

	return picker.Search(query, maxResults), nil
}

// GrepFiles performs a one-shot gitignore-aware content search in rootDir.
func GrepFiles(ctx context.Context, rootDir string, opts grep.GrepOptions) ([]grep.GrepMatch, error) {
	scanner := engine.NewScanner(engine.ScanOptions{
		RootDir: rootDir,
	})

	files, err := scanner.Scan(ctx)
	if err != nil {
		return nil, err
	}

	return grep.SearchFiles(ctx, rootDir, files, opts)
}
