package engine

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// ScanOptions configures the directory scanner.
type ScanOptions struct {
	RootDir        string
	DefaultIgnores []string
	MaxFiles       int
	Concurrency    int
	FollowSymlinks bool
}

type dirWorkItem struct {
	relPath  string
	patterns []gitignore.Pattern
}

type dirQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []dirWorkItem
	active  int
	stopped bool
}

func newDirQueue() *dirQueue {
	q := &dirQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *dirQueue) push(item dirWorkItem) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return false
	}
	q.items = append(q.items, item)
	q.cond.Signal()
	return true
}

func (q *dirQueue) pop() (dirWorkItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.stopped {
		if q.active == 0 {
			q.stopped = true
			q.cond.Broadcast()
			return dirWorkItem{}, false
		}
		q.cond.Wait()
	}
	if q.stopped && len(q.items) == 0 {
		return dirWorkItem{}, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	q.active++
	return item, true
}

func (q *dirQueue) done() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active--
	if q.active == 0 && len(q.items) == 0 {
		q.stopped = true
		q.cond.Broadcast()
	}
}

func (q *dirQueue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	q.cond.Broadcast()
}

// Scanner performs gitignore-aware concurrent file discovery.
type Scanner struct {
	opts ScanOptions
}

// NewScanner creates a new scanner with the given options.
func NewScanner(opts ScanOptions) *Scanner {
	if opts.Concurrency <= 0 {
		opts.Concurrency = runtime.NumCPU() * 2
	}
	if len(opts.DefaultIgnores) == 0 {
		opts.DefaultIgnores = []string{
			".git",
			"node_modules",
			".hg",
			".svn",
			".DS_Store",
		}
	}
	return &Scanner{opts: opts}
}

// toParts splits a relative filepath into slash-separated components.
func toParts(relPath string) []string {
	clean := filepath.ToSlash(filepath.Clean(relPath))
	if clean == "." || clean == "" {
		return nil
	}
	return strings.Split(clean, "/")
}

// loadGitignore reads .gitignore from dirPath using domain.
func loadGitignore(dirPath string, domain []string) []gitignore.Pattern {
	gitIgnorePath := filepath.Join(dirPath, ".gitignore")
	f, err := os.Open(gitIgnorePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var patterns []gitignore.Pattern
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, domain))
	}
	return patterns
}

// Scan traverses rootDir concurrently and returns all non-ignored relative file paths.
func (s *Scanner) Scan(ctx context.Context) ([]string, error) {
	rootDir, err := filepath.Abs(s.opts.RootDir)
	if err != nil {
		return nil, err
	}

	stat, err := os.Stat(rootDir)
	if err != nil {
		return nil, err
	}
	if !stat.IsDir() {
		return nil, os.ErrInvalid
	}

	var basePatterns []gitignore.Pattern
	for _, ign := range s.opts.DefaultIgnores {
		basePatterns = append(basePatterns, gitignore.ParsePattern(ign, nil))
	}

	queue := newDirQueue()
	queue.push(dirWorkItem{
		relPath:  "",
		patterns: basePatterns,
	})

	var (
		resMu     sync.Mutex
		results   []string
		fileCount int64
		wg        sync.WaitGroup
	)

	// Watch context cancellation
	doneCh := make(chan struct{})
	defer close(doneCh)

	go func() {
		select {
		case <-ctx.Done():
			queue.stop()
		case <-doneCh:
			return
		}
	}()

	numWorkers := s.opts.Concurrency
	wg.Add(numWorkers)

	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				item, ok := queue.pop()
				if !ok {
					return
				}

				absDir := rootDir
				if item.relPath != "" {
					absDir = filepath.Join(rootDir, item.relPath)
				}

				domain := toParts(item.relPath)
				currentPatterns := item.patterns
				if localPatterns := loadGitignore(absDir, domain); len(localPatterns) > 0 {
					combined := make([]gitignore.Pattern, len(currentPatterns)+len(localPatterns))
					copy(combined, currentPatterns)
					copy(combined[len(currentPatterns):], localPatterns)
					currentPatterns = combined
				}

				matcher := gitignore.NewMatcher(currentPatterns)

				entries, err := os.ReadDir(absDir)
				if err != nil {
					queue.done()
					continue
				}

				var localFiles []string
				for _, entry := range entries {
					entryRel := entry.Name()
					if item.relPath != "" {
						entryRel = filepath.Join(item.relPath, entry.Name())
					}
					parts := toParts(entryRel)

					isDir := entry.IsDir()
					if !isDir && s.opts.FollowSymlinks {
						if entry.Type()&os.ModeSymlink != 0 {
							targetStat, err := os.Stat(filepath.Join(rootDir, entryRel))
							if err == nil && targetStat.IsDir() {
								isDir = true
							}
						}
					}

					if matcher.Match(parts, isDir) {
						continue
					}

					if isDir {
						queue.push(dirWorkItem{
							relPath:  entryRel,
							patterns: currentPatterns,
						})
					} else {
						localFiles = append(localFiles, filepath.ToSlash(entryRel))
					}
				}

				if len(localFiles) > 0 {
					resMu.Lock()
					if s.opts.MaxFiles > 0 {
						remaining := s.opts.MaxFiles - len(results)
						if remaining > 0 {
							if len(localFiles) > remaining {
								localFiles = localFiles[:remaining]
							}
							results = append(results, localFiles...)
							atomic.AddInt64(&fileCount, int64(len(localFiles)))
							if len(results) >= s.opts.MaxFiles {
								queue.stop()
							}
						}
					} else {
						results = append(results, localFiles...)
						atomic.AddInt64(&fileCount, int64(len(localFiles)))
					}
					resMu.Unlock()
				}

				queue.done()
			}
		}()
	}

	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return results, nil
}

// IsIgnored tests whether a relative path is excluded by default ignores or local gitignores.
func (s *Scanner) IsIgnored(relPath string, isDir bool) bool {
	rootDir, err := filepath.Abs(s.opts.RootDir)
	if err != nil {
		return false
	}
	parts := toParts(relPath)
	if len(parts) == 0 {
		return false
	}

	// Check default ignores
	for _, ign := range s.opts.DefaultIgnores {
		for _, part := range parts {
			if part == ign {
				return true
			}
		}
	}

	// Load and evaluate gitignore patterns from root down the directory hierarchy
	var patterns []gitignore.Pattern
	for _, ign := range s.opts.DefaultIgnores {
		patterns = append(patterns, gitignore.ParsePattern(ign, nil))
	}

	currDir := rootDir
	if rootPatterns := loadGitignore(currDir, nil); len(rootPatterns) > 0 {
		patterns = append(patterns, rootPatterns...)
	}

	var runningDomain []string
	for i := 0; i < len(parts)-1; i++ {
		currDir = filepath.Join(currDir, parts[i])
		runningDomain = append(runningDomain, parts[i])
		if dirPatterns := loadGitignore(currDir, runningDomain); len(dirPatterns) > 0 {
			patterns = append(patterns, dirPatterns...)
		}
	}

	matcher := gitignore.NewMatcher(patterns)
	return matcher.Match(parts, isDir)
}
