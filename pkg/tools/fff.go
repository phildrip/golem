package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// FindFilesArgs defines input for fast file finding (fff).
type FindFilesArgs struct {
	Query       string `json:"query" jsonschema:"Fuzzy search term or pattern to match against file paths (e.g. 'agent.go', 'config', '*.json')"`
	Directory   string `json:"directory,omitempty" jsonschema:"Subdirectory to search within (relative to workspace). Defaults to root if omitted."`
	MaxResults  int    `json:"max_results,omitempty" jsonschema:"Maximum number of results to return (default 20, max 100)"`
}

// FindFilesResult defines output from fast file search.
type FindFilesResult struct {
	Files   []string `json:"files" jsonschema:"Matching file paths relative to search directory"`
	Count   int      `json:"count" jsonschema:"Number of matching files found"`
	Backend string   `json:"backend" jsonschema:"Search backend used: 'fff-native', 'git-ls', or 'walk'"`
}

// GrepFilesArgs defines input for fast content searching (ffgrep).
type GrepFilesArgs struct {
	Pattern     string `json:"pattern" jsonschema:"Text or regex pattern to search for inside files"`
	Directory   string `json:"directory,omitempty" jsonschema:"Subdirectory to search within (relative to workspace). Defaults to root if omitted."`
	FilePattern string `json:"file_pattern,omitempty" jsonschema:"Optional glob filter for filenames (e.g. '*.go', '*.md')"`
	MaxResults  int    `json:"max_results,omitempty" jsonschema:"Maximum number of matching lines to return (default 30, max 100)"`
}

// GrepMatch represents a single matching line in a file.
type GrepMatch struct {
	File    string `json:"file" jsonschema:"File path relative to workspace"`
	LineNum int    `json:"line_num" jsonschema:"Line number (1-based)"`
	Content string `json:"content" jsonschema:"Line text content"`
}

// GrepFilesResult defines output from content searching.
type GrepFilesResult struct {
	Matches []GrepMatch `json:"matches" jsonschema:"List of matching lines"`
	Count   int         `json:"count" jsonschema:"Total matches found"`
	Backend string      `json:"backend" jsonschema:"Search backend used: 'fff-native', 'ripgrep', or 'builtin'"`
}

// FindFiles searches for file paths matching query within baseDir.
// It checks for fff binary first, then git ls-files if in a git repo, then standard filepath.WalkDir.
func FindFiles(baseDir, subDir, query string, maxResults int) (FindFilesResult, error) {
	if maxResults <= 0 {
		maxResults = 20
	} else if maxResults > 100 {
		maxResults = 100
	}

	searchRoot, err := resolveSafePath(baseDir, subDir)
	if err != nil {
		return FindFilesResult{}, err
	}

	// 1. Try fff if installed
	if fffPath, err := exec.LookPath("fff"); err == nil && fffPath != "" {
		res, err := runFFFSearch(fffPath, searchRoot, query, maxResults)
		if err == nil && len(res) > 0 {
			return FindFilesResult{
				Files:   res,
				Count:   len(res),
				Backend: "fff-native",
			}, nil
		}
	}

	// 2. Try git ls-files (fast, respects .gitignore)
	if gitPath, err := exec.LookPath("git"); err == nil && gitPath != "" {
		res, err := runGitFind(gitPath, searchRoot, query, maxResults)
		if err == nil && len(res) > 0 {
			return FindFilesResult{
				Files:   res,
				Count:   len(res),
				Backend: "git-ls",
			}, nil
		}
	}

	// 3. Fallback: Fast WalkDir with fuzzy scoring
	res, err := runWalkFind(searchRoot, query, maxResults)
	if err != nil {
		return FindFilesResult{}, errors.Wrap(err, "walking files")
	}

	return FindFilesResult{
		Files:   res,
		Count:   len(res),
		Backend: "walk",
	}, nil
}

func runFFFSearch(binPath, searchRoot, query string, maxResults int) ([]string, error) {
	cmd := exec.Command(binPath, "find", query, "--limit", fmt.Sprintf("%d", maxResults))
	cmd.Dir = searchRoot
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var lines []string
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text != "" {
			lines = append(lines, text)
			if len(lines) >= maxResults {
				break
			}
		}
	}
	return lines, nil
}

func runGitFind(binPath, searchRoot, query string, maxResults int) ([]string, error) {
	cmd := exec.Command(binPath, "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = searchRoot
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	qLower := strings.ToLower(query)
	var matches []string
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		f := scanner.Text()
		if f == "" {
			continue
		}
		if fuzzyMatch(strings.ToLower(f), qLower) {
			matches = append(matches, f)
			if len(matches) >= maxResults {
				break
			}
		}
	}
	return matches, nil
}

func runWalkFind(searchRoot, query string, maxResults int) ([]string, error) {
	qLower := strings.ToLower(query)
	var matches []string

	ignoredDirs := map[string]bool{
		".git":         true,
		".beads":       true,
		"node_modules": true,
		"vendor":       true,
		".gemini":      true,
		"dist":         true,
		"target":       true,
	}

	err := filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(searchRoot, path)
		if err != nil {
			return nil
		}

		if fuzzyMatch(strings.ToLower(rel), qLower) {
			matches = append(matches, rel)
			if len(matches) >= maxResults {
				return filepath.SkipAll
			}
		}
		return nil
	})

	return matches, err
}

// fuzzyMatch checks if sub is a subsequence of s, or if s contains sub.
func fuzzyMatch(s, sub string) bool {
	if sub == "" {
		return true
	}
	if strings.Contains(s, sub) {
		return true
	}

	// Subsequence check
	subIdx := 0
	subRunes := []rune(sub)
	for _, r := range s {
		if r == subRunes[subIdx] {
			subIdx++
			if subIdx == len(subRunes) {
				return true
			}
		}
	}
	return false
}

// GrepFiles searches content of files matching pattern within baseDir.
// It checks for rg (ripgrep) first, then fallback WalkDir.
func GrepFiles(baseDir, subDir, pattern, filePattern string, maxResults int) (GrepFilesResult, error) {
	if maxResults <= 0 {
		maxResults = 30
	} else if maxResults > 100 {
		maxResults = 100
	}

	searchRoot, err := resolveSafePath(baseDir, subDir)
	if err != nil {
		return GrepFilesResult{}, err
	}

	// 1. Try ripgrep if installed
	if rgPath, err := exec.LookPath("rg"); err == nil && rgPath != "" {
		res, err := runRipgrep(rgPath, searchRoot, pattern, filePattern, maxResults)
		if err == nil {
			return GrepFilesResult{
				Matches: res,
				Count:   len(res),
				Backend: "ripgrep",
			}, nil
		}
	}

	// 2. Fallback to builtin scanner
	res, err := runBuiltinGrep(searchRoot, pattern, filePattern, maxResults)
	if err != nil {
		return GrepFilesResult{}, errors.Wrap(err, "running builtin grep")
	}

	return GrepFilesResult{
		Matches: res,
		Count:   len(res),
		Backend: "builtin",
	}, nil
}

func runRipgrep(rgPath, searchRoot, pattern, filePattern string, maxResults int) ([]GrepMatch, error) {
	args := []string{"--line-number", "--no-heading", "--color=never", "--max-count", fmt.Sprintf("%d", maxResults)}
	if filePattern != "" {
		args = append(args, "-g", filePattern)
	}
	args = append(args, pattern, ".")

	cmd := exec.Command(rgPath, args...)
	cmd.Dir = searchRoot
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run() // Exit code 1 means no matches, not an error

	var matches []GrepMatch
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 {
			var lineNum int
			fmt.Sscanf(parts[1], "%d", &lineNum)
			matches = append(matches, GrepMatch{
				File:    parts[0],
				LineNum: lineNum,
				Content: strings.TrimSpace(parts[2]),
			})
			if len(matches) >= maxResults {
				break
			}
		}
	}
	return matches, nil
}

func runBuiltinGrep(searchRoot, pattern, filePattern string, maxResults int) ([]GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, errors.Wrapf(err, "compiling grep pattern %q", pattern)
	}

	var globRe *regexp.Regexp
	if filePattern != "" {
		rx := "^" + strings.ReplaceAll(regexp.QuoteMeta(filePattern), `\*`, ".*") + "$"
		globRe, _ = regexp.Compile(rx)
	}

	ignoredDirs := map[string]bool{
		".git":         true,
		".beads":       true,
		"node_modules": true,
		"vendor":       true,
		".gemini":      true,
	}

	var matches []GrepMatch
	err = filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ignoredDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		if globRe != nil && !globRe.MatchString(d.Name()) {
			return nil
		}

		rel, err := filepath.Rel(searchRoot, path)
		if err != nil {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		lineScanner := bufio.NewScanner(f)
		lineNum := 1
		for lineScanner.Scan() {
			t := lineScanner.Text()
			if re.MatchString(t) {
				matches = append(matches, GrepMatch{
					File:    rel,
					LineNum: lineNum,
					Content: strings.TrimSpace(t),
				})
				if len(matches) >= maxResults {
					return filepath.SkipAll
				}
			}
			lineNum++
		}
		return nil
	})

	return matches, err
}

// NewFindFilesTool returns an ADK tool exposing fff fast file finding.
func NewFindFilesTool(baseDir string) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "find_files",
		Description: "Fast file finder (fff) for rapidly locating files by name or path across the workspace. Uses indexed fff/git if available, falling back to directory traversal.",
	}, func(ctx agent.Context, args FindFilesArgs) (FindFilesResult, error) {
		return FindFiles(baseDir, args.Directory, args.Query, args.MaxResults)
	})
}

// NewGrepFilesTool returns an ADK tool exposing ffgrep content searching.
func NewGrepFilesTool(baseDir string) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "grep_files",
		Description: "Fast content search (ffgrep) across workspace files. Returns matching file paths, line numbers, and content excerpts.",
	}, func(ctx agent.Context, args GrepFilesArgs) (GrepFilesResult, error) {
		return GrepFiles(baseDir, args.Directory, args.Pattern, args.FilePattern, args.MaxResults)
	})
}
