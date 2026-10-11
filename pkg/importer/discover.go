package importer

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	_ "modernc.org/sqlite"
)

// DefaultAGYBrainDir returns the default directory where Antigravity stores transcripts.
func DefaultAGYBrainDir() string {
	if custom := os.Getenv("AGY_BRAIN_DIR"); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain")
}

// DiscoverHistories scans local environments (Antigravity, Cursor, and OpenCode) for past sessions.
// Results are sorted with current workspace matches first, ordered by most recent update time.
func DiscoverHistories(currentWorkspace string) ([]DiscoveredSession, error) {
	var sessions []DiscoveredSession
	cleanWorkspace := ""
	if currentWorkspace != "" {
		cleanWorkspace = filepath.Clean(currentWorkspace)
	}

	// 1. Discover Antigravity sessions
	agySessions, err := discoverAGYSessions(cleanWorkspace)
	if err == nil {
		sessions = append(sessions, agySessions...)
	}

	// 2. Discover Cursor sessions
	cursorSessions, err := discoverCursorSessions(cleanWorkspace)
	if err == nil {
		sessions = append(sessions, cursorSessions...)
	}

	// 3. Discover OpenCode sessions
	openCodeSessions, err := discoverOpenCodeSessions(cleanWorkspace)
	if err == nil {
		sessions = append(sessions, openCodeSessions...)
	}

	// 4. Sort: matches workspace first, then UpdatedAt descending
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].MatchesWorkspace != sessions[j].MatchesWorkspace {
			return sessions[i].MatchesWorkspace
		}
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	return sessions, nil
}

func discoverAGYSessions(currentWorkspace string) ([]DiscoveredSession, error) {
	brainDir := DefaultAGYBrainDir()
	if !isDir(brainDir) {
		return nil, nil
	}

	entries, err := os.ReadDir(brainDir)
	if err != nil {
		return nil, errors.Wrap(err, "reading AGY brain dir")
	}

	var sessions []DiscoveredSession

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cid := e.Name()
		convDir := filepath.Join(brainDir, cid)
		transcriptPath := filepath.Join(convDir, ".system_generated", "logs", "transcript.jsonl")
		if !fileExists(transcriptPath) {
			transcriptPath = filepath.Join(convDir, "transcript.jsonl")
			if !fileExists(transcriptPath) {
				continue
			}
		}

		sess, err := inspectAGYTranscript(cid, convDir, transcriptPath, currentWorkspace)
		if err == nil {
			sessions = append(sessions, sess)
		}
	}

	return sessions, nil
}

func inspectAGYTranscript(cid, convDir, transcriptPath, currentWorkspace string) (DiscoveredSession, error) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return DiscoveredSession{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 256*1024)
	scanner.Buffer(buf, 1024*1024)

	var firstStep, lastStep agyStep
	var detectedWorkspace string
	count := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var step agyStep
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			continue
		}

		if count == 0 {
			firstStep = step
			// Look for workspace in prompt metadata
			detectedWorkspace = extractWorkspaceFromText(step.Content)
		}
		lastStep = step
		count++

		// Also check tool calls for Cwd if workspace not found yet
		if detectedWorkspace == "" && len(step.ToolCalls) > 0 {
			for _, tc := range step.ToolCalls {
				var args struct {
					Cwd          string `json:"Cwd"`
					AbsolutePath string `json:"AbsolutePath"`
				}
				if err := json.Unmarshal(tc.Args, &args); err == nil {
					cleanCwd := strings.Trim(strings.TrimSpace(args.Cwd), "\"")
					cleanAbs := strings.Trim(strings.TrimSpace(args.AbsolutePath), "\"")
					if cleanCwd != "" {
						detectedWorkspace = filepath.Clean(cleanCwd)
						break
					} else if cleanAbs != "" {
						detectedWorkspace = filepath.Dir(cleanAbs)
						break
					}
				}
			}
		}
	}

	if count == 0 {
		return DiscoveredSession{}, errors.New("empty transcript")
	}

	title := cleanUserContent(firstStep.Content)
	title = truncateRunes(title, 80)
	if title == "" {
		title = "AGY Session " + safePrefix(cid, 8)
	}

	created := parseTimestamp(firstStep.CreatedAt)
	updated := parseTimestamp(lastStep.CreatedAt)

	matches := false
	if currentWorkspace != "" && detectedWorkspace != "" {
		matches = filepath.Clean(detectedWorkspace) == currentWorkspace ||
			strings.HasPrefix(currentWorkspace, filepath.Clean(detectedWorkspace)) ||
			strings.HasPrefix(filepath.Clean(detectedWorkspace), currentWorkspace)
	}

	return DiscoveredSession{
		ID:               cid,
		Format:           FormatAGY,
		Title:            title,
		Path:             convDir,
		Workspace:        detectedWorkspace,
		CreatedAt:        created,
		UpdatedAt:        updated,
		EventCount:       count,
		MatchesWorkspace: matches,
	}, nil
}

func extractWorkspaceFromText(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "Command Working Directory:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				return filepath.Clean(strings.TrimSpace(parts[1]))
			}
		}
		if trimmed == "Active Workspaces:" && i+1 < len(lines) {
			nextLine := strings.TrimSpace(lines[i+1])
			nextLine = strings.TrimPrefix(nextLine, "- ")
			if nextLine != "" {
				return filepath.Clean(nextLine)
			}
		}
	}
	return ""
}

func discoverCursorSessions(currentWorkspace string) ([]DiscoveredSession, error) {
	wsDir := DefaultCursorWorkspaceStorageDir()
	if !isDir(wsDir) {
		return nil, nil
	}

	entries, err := os.ReadDir(wsDir)
	if err != nil {
		return nil, errors.Wrap(err, "reading Cursor workspace storage dir")
	}

	var sessions []DiscoveredSession

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wDir := filepath.Join(wsDir, e.Name())
		vscdb := filepath.Join(wDir, "state.vscdb")
		if !fileExists(vscdb) {
			continue
		}

		workspacePath := ""
		wsFile := filepath.Join(wDir, "workspace.json")
		if fileExists(wsFile) {
			if data, err := os.ReadFile(wsFile); err == nil {
				var meta struct {
					Folder string `json:"folder"`
				}
				if json.Unmarshal(data, &meta) == nil {
					workspacePath = filepath.Clean(strings.TrimPrefix(meta.Folder, "file://"))
				}
			}
		}

		matches := false
		if currentWorkspace != "" && workspacePath != "" {
			matches = filepath.Clean(workspacePath) == currentWorkspace
		}

		// Read composers from state.vscdb
		db, err := sql.Open("sqlite", vscdb)
		if err != nil {
			continue
		}

		var rawComposerData string
		_ = db.QueryRow("SELECT value FROM ItemTable WHERE key = 'composer.composerData'").Scan(&rawComposerData)
		db.Close()

		if rawComposerData != "" {
			var cd cursorComposerData
			if json.Unmarshal([]byte(rawComposerData), &cd) == nil {
				for _, c := range cd.AllComposers {
					title := strings.TrimSpace(c.Name)
					if title == "" {
						title = strings.TrimSpace(c.Text)
					}
					if title == "" {
						title = "Cursor Composer " + safePrefix(c.ComposerID, 8)
					}
					title = truncateRunes(title, 80)

					cTime := time.UnixMilli(c.CreatedAt).UTC()
					uTime := cTime
					if c.LastUpdatedAt > 0 {
						uTime = time.UnixMilli(c.LastUpdatedAt).UTC()
					}

					sessions = append(sessions, DiscoveredSession{
						ID:               c.ComposerID,
						Format:           FormatCursor,
						Title:            title,
						Path:             vscdb,
						Workspace:        workspacePath,
						CreatedAt:        cTime,
						UpdatedAt:        uTime,
						MatchesWorkspace: matches,
					})
				}
			}
		}
	}

	return sessions, nil
}
