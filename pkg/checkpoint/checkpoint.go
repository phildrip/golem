package checkpoint

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const maxRetainedCheckpoints = 50

// Snapshot represents a lightweight git plumbing tree snapshot of the workspace.
type Snapshot struct {
	ID         string    `json:"id"`
	TurnID     string    `json:"turn_id,omitempty"`
	Label      string    `json:"label"`
	TreeHash   string    `json:"tree_hash"`
	CreatedAt  time.Time `json:"created_at"`
	PreTracked []string  `json:"pre_tracked,omitempty"`
}

// Manager captures and restores workspace snapshots using a temporary shadow
// GIT_INDEX_FILE so the user's real .git/index, commits, and reflog are never modified.
type Manager struct {
	mu        sync.Mutex
	workDir   string
	snapshots []Snapshot
}

// NewManager creates a workspace checkpoint manager rooted at workDir.
func NewManager(workDir string) *Manager {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	return &Manager{
		workDir: workDir,
	}
}

// Available reports whether workDir is inside a git repository with a usable git binary.
func (m *Manager) Available() bool {
	if m == nil || m.workDir == "" {
		return false
	}
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = m.workDir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// Create captures the current working tree (including untracked non-ignored files)
// into a git tree object using a temporary shadow index file.
func (m *Manager) Create(turnID, label string) (Snapshot, error) {
	if m == nil {
		return Snapshot{}, errors.New("checkpoint manager is nil")
	}
	if !m.Available() {
		return Snapshot{}, errors.New("workspace is not a git repository")
	}

	tmpIdx, err := os.CreateTemp("", "golem-shadow-index-*")
	if err != nil {
		return Snapshot{}, errors.Wrap(err, "creating shadow git index")
	}
	idxPath := tmpIdx.Name()
	tmpIdx.Close()
	// Remove the empty file first: git initializes a fresh index when the path does not exist,
	// whereas a 0-byte existing file is rejected as a corrupt index.
	_ = os.Remove(idxPath)
	defer os.Remove(idxPath)

	// Stage all tracked and untracked (non-ignored) files into the shadow index.
	if _, err := m.runGitWithIndex(idxPath, "add", "-A"); err != nil {
		return Snapshot{}, errors.Wrap(err, "staging workspace into shadow index")
	}

	treeHash, err := m.runGitWithIndex(idxPath, "write-tree")
	if err != nil {
		return Snapshot{}, errors.Wrap(err, "writing shadow tree")
	}
	treeHash = strings.TrimSpace(treeHash)

	trackedOut, _ := m.runGitWithIndex(idxPath, "ls-files")
	var tracked []string
	for _, line := range strings.Split(strings.TrimSpace(trackedOut), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			tracked = append(tracked, line)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	id := fmt.Sprintf("cp-%d", len(m.snapshots)+1)
	if label == "" {
		label = "turn snapshot"
	}
	snap := Snapshot{
		ID:         id,
		TurnID:     turnID,
		Label:      label,
		TreeHash:   treeHash,
		CreatedAt:  time.Now().UTC(),
		PreTracked: tracked,
	}
	m.snapshots = append(m.snapshots, snap)
	if len(m.snapshots) > maxRetainedCheckpoints {
		m.snapshots = m.snapshots[len(m.snapshots)-maxRetainedCheckpoints:]
	}
	return snap, nil
}

// List returns retained snapshots, newest first.
func (m *Manager) List() []Snapshot {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Snapshot, len(m.snapshots))
	for i := range m.snapshots {
		out[i] = m.snapshots[len(m.snapshots)-1-i]
	}
	return out
}

// Latest returns the most recent snapshot, if any.
func (m *Manager) Latest() (Snapshot, bool) {
	if m == nil {
		return Snapshot{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.snapshots) == 0 {
		return Snapshot{}, false
	}
	return m.snapshots[len(m.snapshots)-1], true
}

// resolve finds a snapshot by ID or TurnID, or returns the latest when ref is empty.
func (m *Manager) resolve(ref string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.snapshots) == 0 {
		return Snapshot{}, errors.New("no workspace checkpoints recorded yet")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "latest" || ref == "last" {
		return m.snapshots[len(m.snapshots)-1], nil
	}
	for i := len(m.snapshots) - 1; i >= 0; i-- {
		s := m.snapshots[i]
		if s.ID == ref || s.TurnID == ref || s.TreeHash == ref {
			return s, nil
		}
	}
	return Snapshot{}, errors.Errorf("checkpoint %q not found", ref)
}

// Diff returns the unified git diff between the target snapshot (default: latest)
// and the current working tree, including any newly created untracked files.
func (m *Manager) Diff(ref string) (string, Snapshot, error) {
	if m == nil {
		return "", Snapshot{}, errors.New("checkpoint manager is nil")
	}
	snap, err := m.resolve(ref)
	if err != nil {
		return "", Snapshot{}, err
	}

	// Capture current tree into a temporary shadow index so newly created untracked
	// files are included in the diff against snap.TreeHash.
	tmpIdx, err := os.CreateTemp("", "golem-diff-index-*")
	if err != nil {
		return "", snap, errors.Wrap(err, "creating diff shadow index")
	}
	idxPath := tmpIdx.Name()
	tmpIdx.Close()
	_ = os.Remove(idxPath)
	defer os.Remove(idxPath)

	if _, err := m.runGitWithIndex(idxPath, "add", "-A"); err != nil {
		return "", snap, errors.Wrap(err, "staging current state for diff")
	}
	currTree, err := m.runGitWithIndex(idxPath, "write-tree")
	if err != nil {
		return "", snap, errors.Wrap(err, "writing current tree for diff")
	}
	currTree = strings.TrimSpace(currTree)

	if currTree == snap.TreeHash {
		return "", snap, nil
	}

	diffOut, err := m.runGitWithIndex(idxPath, "diff-tree", "-p", "--binary", snap.TreeHash, currTree)
	if err != nil {
		return "", snap, errors.Wrap(err, "running git diff-tree")
	}
	return strings.TrimSpace(diffOut), snap, nil
}

// Restore atomically reverts the working tree to match the target snapshot
// (default: latest) without modifying the user's real .git/index or commit history.
func (m *Manager) Restore(ref string) (Snapshot, error) {
	if m == nil {
		return Snapshot{}, errors.New("checkpoint manager is nil")
	}
	snap, err := m.resolve(ref)
	if err != nil {
		return Snapshot{}, err
	}

	tmpIdx, err := os.CreateTemp("", "golem-restore-index-*")
	if err != nil {
		return snap, errors.Wrap(err, "creating restore shadow index")
	}
	idxPath := tmpIdx.Name()
	tmpIdx.Close()
	_ = os.Remove(idxPath)
	defer os.Remove(idxPath)

	// Stage current files to find any newly created files that did not exist in snap.PreTracked.
	_, _ = m.runGitWithIndex(idxPath, "add", "-A")
	currFilesOut, _ := m.runGitWithIndex(idxPath, "ls-files")

	preSet := make(map[string]bool, len(snap.PreTracked))
	for _, f := range snap.PreTracked {
		preSet[f] = true
	}

	// Read the snapshot tree into the shadow index and checkout all files to workDir.
	if _, err := m.runGitWithIndex(idxPath, "read-tree", snap.TreeHash); err != nil {
		return snap, errors.Wrap(err, "reading checkpoint tree")
	}
	if _, err := m.runGitWithIndex(idxPath, "checkout-index", "-a", "-f"); err != nil {
		return snap, errors.Wrap(err, "checking out checkpoint files")
	}

	// Remove files that were created after the snapshot was taken.
	for _, line := range strings.Split(strings.TrimSpace(currFilesOut), "\n") {
		f := strings.TrimSpace(line)
		if f == "" || preSet[f] {
			continue
		}
		absTarget := filepath.Join(m.workDir, f)
		if rel, rErr := filepath.Rel(m.workDir, absTarget); rErr == nil && !strings.HasPrefix(rel, "..") {
			_ = os.Remove(absTarget)
		}
	}

	return snap, nil
}

func (m *Manager) runGitWithIndex(indexPath string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = m.workDir
	env := os.Environ()
	filtered := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_INDEX_FILE=") {
			filtered = append(filtered, kv)
		}
	}
	filtered = append(filtered, "GIT_INDEX_FILE="+indexPath)
	cmd.Env = filtered

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// ToolArgs defines arguments for the workspace_checkpoint tool.
type ToolArgs struct {
	Action string `json:"action" jsonschema:"Action to perform: 'create', 'list', 'diff', or 'restore' (alias 'undo')."`
	Ref    string `json:"ref,omitempty" jsonschema:"Optional checkpoint ID (e.g. 'cp-1'), turn_id, or 'latest' for diff/restore."`
	Label  string `json:"label,omitempty" jsonschema:"Optional descriptive label when action is 'create'."`
}

// ToolResult is the output of the workspace_checkpoint tool.
type ToolResult struct {
	CheckpointID string     `json:"checkpoint_id,omitempty"`
	TreeHash     string     `json:"tree_hash,omitempty"`
	Diff         string     `json:"diff,omitempty"`
	Checkpoints  []Snapshot `json:"checkpoints,omitempty"`
	Message      string     `json:"message"`
}

// NewTool creates the ADK workspace_checkpoint tool.
func NewTool(mgr *Manager) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "workspace_checkpoint",
		Description: "Manages shadow git tree workspace checkpoints without touching the user's git index or commit history. Actions: 'create' (snapshot before risky multi-file edits), 'list', 'diff' (show changes since checkpoint), 'restore' (atomically roll back workspace to checkpoint).",
	}, func(ctx agent.Context, args ToolArgs) (ToolResult, error) {
		switch strings.ToLower(strings.TrimSpace(args.Action)) {
		case "create", "save", "snapshot":
			snap, err := mgr.Create("", args.Label)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{
				CheckpointID: snap.ID,
				TreeHash:     snap.TreeHash,
				Message:      fmt.Sprintf("Created checkpoint %s (%s) at tree %s", snap.ID, snap.Label, snap.TreeHash[:min(12, len(snap.TreeHash))]),
			}, nil

		case "list", "ls":
			list := mgr.List()
			return ToolResult{
				Checkpoints: list,
				Message:     fmt.Sprintf("%d checkpoint(s) available", len(list)),
			}, nil

		case "diff":
			diff, snap, err := mgr.Diff(args.Ref)
			if err != nil {
				return ToolResult{}, err
			}
			msg := fmt.Sprintf("No changes since checkpoint %s (%s)", snap.ID, snap.Label)
			if diff != "" {
				msg = fmt.Sprintf("Diff since checkpoint %s (%s):\n%s", snap.ID, snap.Label, diff)
			}
			return ToolResult{
				CheckpointID: snap.ID,
				TreeHash:     snap.TreeHash,
				Diff:         diff,
				Message:      msg,
			}, nil

		case "restore", "undo", "rollback":
			snap, err := mgr.Restore(args.Ref)
			if err != nil {
				return ToolResult{}, err
			}
			return ToolResult{
				CheckpointID: snap.ID,
				TreeHash:     snap.TreeHash,
				Message:      fmt.Sprintf("Restored workspace to checkpoint %s (%s)", snap.ID, snap.Label),
			}, nil

		default:
			return ToolResult{}, fmt.Errorf("unknown action %q (expected 'create', 'list', 'diff', or 'restore')", args.Action)
		}
	})
}
