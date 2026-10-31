package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	run("add", "main.go")
	run("commit", "-m", "initial")
	return dir
}

func TestManagerCreateDiffAndRestoreLeavesUserIndexIntact(t *testing.T) {
	dir := initTestGitRepo(t)
	mgr := NewManager(dir)
	if !mgr.Available() {
		t.Fatal("expected checkpoint manager to be available in git repo")
	}

	// Stage a change in the user's real git index to verify our shadow index never touches it!
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("staged content\n"), 0644); err != nil {
		t.Fatalf("write staged.txt: %v", err)
	}
	cmd := exec.Command("git", "add", "staged.txt")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add staged.txt: %v", err)
	}

	// 1. Create checkpoint
	snap, err := mgr.Create("turn-1", "before risky refactor")
	if err != nil {
		t.Fatalf("Create checkpoint: %v", err)
	}
	if snap.ID != "cp-1" || snap.TreeHash == "" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	// 2. Mutate main.go and create a brand new untracked file
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { panic(\"broken\") }\n"), 0644); err != nil {
		t.Fatalf("modify main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "untracked_temp.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("create untracked_temp.go: %v", err)
	}

	// 3. Diff against checkpoint
	diff, _, err := mgr.Diff("cp-1")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "panic(\"broken\")") || !strings.Contains(diff, "untracked_temp.go") {
		t.Fatalf("expected diff to contain both modified main.go and untracked_temp.go, got:\n%s", diff)
	}

	// 4. Restore checkpoint
	if _, err := mgr.Restore("cp-1"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Verify main.go is restored
	restoredMain, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("read restored main.go: %v", err)
	}
	if string(restoredMain) != "package main\n\nfunc main() {}\n" {
		t.Fatalf("main.go was not restored properly: %q", string(restoredMain))
	}

	// Verify untracked_temp.go was removed
	if _, err := os.Stat(filepath.Join(dir, "untracked_temp.go")); !os.IsNotExist(err) {
		t.Fatalf("expected untracked_temp.go to be removed on restore, err=%v", err)
	}

	// Verify user's real .git/index still has staged.txt staged!
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = dir
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if !strings.Contains(string(statusOut), "A  staged.txt") {
		t.Fatalf("user's real git index was disturbed! git status:\n%s", string(statusOut))
	}
}
