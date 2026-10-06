package grep

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// GrepMatch represents a single matching line in a file with optional context.
type GrepMatch struct {
	File          string   `json:"file"`
	LineNum       int      `json:"line_num"`
	Content       string   `json:"content"`
	ContextBefore []string `json:"context_before,omitempty"`
	ContextAfter  []string `json:"context_after,omitempty"`
}

// GrepOptions configures the grep search.
type GrepOptions struct {
	Pattern       string
	FilePattern   string
	IsRegex       bool
	CaseSensitive bool
	ContextBefore int
	ContextAfter  int
	MaxMatches    int
	MaxLineLength int
	Concurrency   int
}

// isBinary checks if data contains a null byte in the first 512 bytes (standard git heuristic).
func isBinary(data []byte) bool {
	limit := 512
	if len(data) < limit {
		limit = len(data)
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// matchFilePattern reports whether relPath satisfies the optional glob/extension file pattern.
func matchFilePattern(pattern, relPath string) bool {
	if pattern == "" {
		return true
	}
	base := filepath.Base(relPath)
	// Check wildcard glob
	if matched, err := filepath.Match(pattern, base); err == nil && matched {
		return true
	}
	// Check extension without asterisk (e.g. "go" or ".go")
	cleanExt := strings.TrimPrefix(pattern, "*")
	if strings.HasSuffix(strings.ToLower(base), strings.ToLower(cleanExt)) {
		return true
	}
	return false
}

// SearchData scans an in-memory byte slice for pattern matches.
func SearchData(data []byte, relPath string, opts GrepOptions, targetBytes []byte, re *regexp.Regexp) []GrepMatch {
	if isBinary(data) {
		return nil
	}

	lines := bytes.Split(data, []byte("\n"))
	var matches []GrepMatch

	for i, line := range lines {
		// Trim trailing carriage return
		line = bytes.TrimRight(line, "\r")

		if opts.MaxLineLength > 0 && len(line) > opts.MaxLineLength {
			continue
		}

		matched := false
		if re != nil {
			matched = re.Match(line)
		} else {
			if opts.CaseSensitive {
				matched = bytes.Contains(line, targetBytes)
			} else {
				matched = bytes.Contains(bytes.ToLower(line), targetBytes)
			}
		}

		if !matched {
			continue
		}

		// Collect context before
		var ctxBefore []string
		if opts.ContextBefore > 0 {
			start := i - opts.ContextBefore
			if start < 0 {
				start = 0
			}
			for k := start; k < i; k++ {
				ctxBefore = append(ctxBefore, string(bytes.TrimRight(lines[k], "\r")))
			}
		}

		// Collect context after
		var ctxAfter []string
		if opts.ContextAfter > 0 {
			end := i + 1 + opts.ContextAfter
			if end > len(lines) {
				end = len(lines)
			}
			for k := i + 1; k < end; k++ {
				ctxAfter = append(ctxAfter, string(bytes.TrimRight(lines[k], "\r")))
			}
		}

		matches = append(matches, GrepMatch{
			File:          relPath,
			LineNum:       i + 1,
			Content:       string(line),
			ContextBefore: ctxBefore,
			ContextAfter:  ctxAfter,
		})

		if opts.MaxMatches > 0 && len(matches) >= opts.MaxMatches {
			break
		}
	}

	return matches
}

// SearchFiles concurrently greps matching files within rootDir.
func SearchFiles(ctx context.Context, rootDir string, filePaths []string, opts GrepOptions) ([]GrepMatch, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = runtime.NumCPU() * 2
	}
	if opts.MaxLineLength <= 0 {
		opts.MaxLineLength = 1000
	}

	var (
		targetBytes []byte
		re          *regexp.Regexp
	)

	if opts.IsRegex || strings.ContainsAny(opts.Pattern, `*+?^$()[]{}|\`) {
		pattern := opts.Pattern
		if !opts.CaseSensitive && !strings.HasPrefix(pattern, "(?i)") {
			pattern = "(?i)" + pattern
		}
		if compiled, cErr := regexp.Compile(pattern); cErr == nil {
			re = compiled
		} else if opts.IsRegex {
			return nil, cErr
		}
	}

	if re == nil {
		if opts.CaseSensitive {
			targetBytes = []byte(opts.Pattern)
		} else {
			targetBytes = bytes.ToLower([]byte(opts.Pattern))
		}
	}

	// Filter files by file pattern upfront
	var candidates []string
	for _, f := range filePaths {
		if matchFilePattern(opts.FilePattern, f) {
			candidates = append(candidates, f)
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	var (
		resMu      sync.Mutex
		results    []GrepMatch
		matchCount int64
		fileIdx    int64
		wg         sync.WaitGroup
	)

	numWorkers := opts.Concurrency
	if numWorkers > len(candidates) {
		numWorkers = len(candidates)
	}

	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()

			for {
				select {
				case <-ctx.Done():
					return
				default:
				}

				if opts.MaxMatches > 0 && atomic.LoadInt64(&matchCount) >= int64(opts.MaxMatches) {
					return
				}

				idx := atomic.AddInt64(&fileIdx, 1) - 1
				if idx >= int64(len(candidates)) {
					return
				}

				relPath := candidates[idx]
				absPath := filepath.Join(rootDir, relPath)

				data, err := os.ReadFile(absPath)
				if err != nil {
					continue
				}

				fileMatches := SearchData(data, relPath, opts, targetBytes, re)
				if len(fileMatches) > 0 {
					resMu.Lock()
					if opts.MaxMatches > 0 {
						remaining := opts.MaxMatches - len(results)
						if remaining > 0 {
							if len(fileMatches) > remaining {
								fileMatches = fileMatches[:remaining]
							}
							results = append(results, fileMatches...)
							atomic.AddInt64(&matchCount, int64(len(fileMatches)))
						}
					} else {
						results = append(results, fileMatches...)
						atomic.AddInt64(&matchCount, int64(len(fileMatches)))
					}
					resMu.Unlock()
				}
			}
		}()
	}

	wg.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	return results, nil
}
