package engine

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/user/golem/pkg/search/gg/parser"
	"github.com/user/golem/pkg/search/gg/scoring"
)

// SearchResult represents a scored file search result.
type SearchResult struct {
	Path      string
	Score     int
	Positions []int
}

// FilePicker maintains the in-memory file list, inverted index, and optional filesystem watcher.
type FilePicker struct {
	rootDir  string
	mu       sync.RWMutex
	paths    []string
	pathToID map[string]int
	freeIDs  []int
	index    *InvertedIndex
	scanner  *Scanner
	watcher  *Watcher
	frecency *scoring.FrecencyTracker
}

// FilePickerOptions configures the FilePicker.
type FilePickerOptions struct {
	RootDir        string
	EnableWatcher  bool
	DefaultIgnores []string
	Concurrency    int
	MaxFiles       int
	FrecencyDbPath string
}

// NewFilePicker initializes a FilePicker, scans the root directory, and optionally starts the background watcher.
func NewFilePicker(ctx context.Context, opts FilePickerOptions) (*FilePicker, error) {
	absRoot, err := filepath.Abs(opts.RootDir)
	if err != nil {
		return nil, err
	}

	scanner := NewScanner(ScanOptions{
		RootDir:        absRoot,
		DefaultIgnores: opts.DefaultIgnores,
		Concurrency:    opts.Concurrency,
		MaxFiles:       opts.MaxFiles,
	})

	files, err := scanner.Scan(ctx)
	if err != nil {
		return nil, err
	}

	fp := &FilePicker{
		rootDir:  absRoot,
		pathToID: make(map[string]int, len(files)),
		paths:    make([]string, 0, len(files)),
		index:    NewInvertedIndex(),
		scanner:  scanner,
		frecency: scoring.NewFrecencyTracker(opts.FrecencyDbPath),
	}

	for _, f := range files {
		fp.AddFile(f)
	}

	if opts.EnableWatcher {
		w, err := NewWatcher(fp, absRoot)
		if err == nil {
			fp.watcher = w
		}
	}

	return fp, nil
}

// RootDir returns the root directory of the picker.
func (p *FilePicker) RootDir() string {
	return p.rootDir
}

// AddFile registers a file path into the picker and inverted index.
func (p *FilePicker) AddFile(relPath string) int {
	cleanPath := filepath.ToSlash(filepath.Clean(relPath))
	if cleanPath == "." || cleanPath == "" {
		return -1
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if id, exists := p.pathToID[cleanPath]; exists {
		return id
	}

	var id int
	if len(p.freeIDs) > 0 {
		id = p.freeIDs[len(p.freeIDs)-1]
		p.freeIDs = p.freeIDs[:len(p.freeIDs)-1]
		p.paths[id] = cleanPath
	} else {
		id = len(p.paths)
		p.paths = append(p.paths, cleanPath)
	}

	p.pathToID[cleanPath] = id
	p.index.AddDocument(id, cleanPath)
	return id
}

// RemoveFile removes a file path from the picker and inverted index.
func (p *FilePicker) RemoveFile(relPath string) {
	cleanPath := filepath.ToSlash(filepath.Clean(relPath))

	p.mu.Lock()
	defer p.mu.Unlock()

	id, exists := p.pathToID[cleanPath]
	if !exists {
		return
	}

	delete(p.pathToID, cleanPath)
	p.paths[id] = ""
	p.freeIDs = append(p.freeIDs, id)
	p.index.RemoveDocument(id, cleanPath)
}

// HasFile reports whether the file is indexed.
func (p *FilePicker) HasFile(relPath string) bool {
	cleanPath := filepath.ToSlash(filepath.Clean(relPath))
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, exists := p.pathToID[cleanPath]
	return exists
}

// Files returns a copy of all indexed file paths.
func (p *FilePicker) Files() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	res := make([]string, 0, len(p.pathToID))
	for _, pth := range p.paths {
		if pth != "" {
			res = append(res, pth)
		}
	}
	return res
}

// Len returns the count of active indexed files.
func (p *FilePicker) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.pathToID)
}

// QueryCandidates returns candidate paths satisfying the query constraints and bigram index.
func (p *FilePicker) QueryCandidates(queryStr string) []string {
	q := parser.Parse(queryStr)

	candidateIDs := p.index.FilterWords(q.FuzzyWords)
	if len(candidateIDs) == 0 {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	var matches []string
	for _, id := range candidateIDs {
		if id < 0 || id >= len(p.paths) {
			continue
		}
		path := p.paths[id]
		if path == "" {
			continue
		}
		if q.Matches(path) {
			matches = append(matches, path)
		}
	}

	return matches
}

// RecordAccess records that relPath was accessed, boosting its frecency score.
func (p *FilePicker) RecordAccess(relPath string) {
	if p.frecency != nil {
		p.frecency.RecordAccess(relPath, time.Now())
	}
}

// Search filters candidates by constraints and inverted index, then ranks them via parallel fuzzy scoring and frecency.
func (p *FilePicker) Search(queryStr string, maxResults int) []SearchResult {
	q := parser.Parse(queryStr)
	candidates := p.QueryCandidates(queryStr)
	if len(candidates) == 0 {
		return nil
	}

	now := time.Now()
	var results []SearchResult

	if q.FuzzyText == "" {
		// Pure constraint search: score primarily by frecency
		results = make([]SearchResult, len(candidates))
		for i, cand := range candidates {
			fScore := 0
			if p.frecency != nil {
				fScore = int(p.frecency.Score(cand, now) * 10)
			}
			results[i] = SearchResult{
				Path:  cand,
				Score: fScore,
			}
		}
	} else {
		// Fuzzy search across candidates
		scores := scoring.ParallelMatchScores(q.FuzzyText, candidates, p.scanner.opts.Concurrency)
		for _, m := range scores {
			if !m.Matched {
				continue
			}
			totalScore := m.Score
			if p.frecency != nil {
				totalScore += int(p.frecency.Score(m.Path, now) * 10)
			}
			results = append(results, SearchResult{
				Path:      m.Path,
				Score:     totalScore,
				Positions: m.Positions,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Path < results[j].Path
		}
		return results[i].Score > results[j].Score
	})

	if maxResults > 0 && len(results) > maxResults {
		results = results[:maxResults]
	}

	return results
}

// Rescan re-scans the root directory and updates the index.
func (p *FilePicker) Rescan(ctx context.Context) error {
	newFiles, err := p.scanner.Scan(ctx)
	if err != nil {
		return err
	}

	newSet := make(map[string]bool, len(newFiles))
	for _, f := range newFiles {
		newSet[f] = true
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// 1. Remove files that no longer exist
	for path, id := range p.pathToID {
		if !newSet[path] {
			delete(p.pathToID, path)
			p.paths[id] = ""
			p.freeIDs = append(p.freeIDs, id)
			p.index.RemoveDocument(id, path)
		}
	}

	// 2. Add new files
	for _, f := range newFiles {
		if _, exists := p.pathToID[f]; !exists {
			var id int
			if len(p.freeIDs) > 0 {
				id = p.freeIDs[len(p.freeIDs)-1]
				p.freeIDs = p.freeIDs[:len(p.freeIDs)-1]
				p.paths[id] = f
			} else {
				id = len(p.paths)
				p.paths = append(p.paths, f)
			}
			p.pathToID[f] = id
			p.index.AddDocument(id, f)
		}
	}

	return nil
}

// Close shuts down the background watcher if active.
func (p *FilePicker) Close() error {
	if p.watcher != nil {
		return p.watcher.Close()
	}
	return nil
}
