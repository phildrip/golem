package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/golem/pkg/sandbox"
)

// TestShellToolRunsConfinedInIsolationMode is the end-to-end check that a
// shell call in mode isolation goes through the kernel sandbox rather than a
// plain bash -c: it must see the workspace, and it must not be able to write
// outside it even though the policy would never have seen that path.
func TestShellToolRunsConfinedInIsolationMode(t *testing.T) {
	ws := t.TempDir()
	p, err := sandbox.New(sandbox.Config{Mode: sandbox.ModeIsolation}, ws)
	if err != nil {
		t.Skipf("OS isolation unavailable: %v", err)
	}

	res, err := ExecuteShell(context.Background(), p, "pwd && echo hello-confined")
	if err != nil {
		t.Fatalf("ExecuteShell: %v (output %q)", err, res.Output)
	}
	if !strings.Contains(res.Output, "hello-confined") {
		t.Fatalf("output %q missing the command's own output", res.Output)
	}
	if !strings.Contains(res.Output, ws) {
		t.Errorf("output %q does not run in the workspace %s", res.Output, ws)
	}

	// A write inside the workspace works...
	if _, err := ExecuteShell(context.Background(), p, "echo ok > confined.txt"); err != nil {
		t.Fatalf("write inside workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "confined.txt")); err != nil {
		t.Fatalf("workspace write did not land: %v", err)
	}

	// ...and a write into the user's home is refused by the kernel, not by the
	// policy. /tmp is deliberately writable in the default layout, so the home
	// directory is where an escaping model would actually aim.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	outside := filepath.Join(home, fmt.Sprintf(".golem-confined-%d.txt", os.Getpid()))
	t.Cleanup(func() { os.Remove(outside) })
	out, err := ExecuteShell(context.Background(), p, "echo nope > "+outside)
	if err != nil {
		t.Fatalf("ExecuteShell: %v", err)
	}
	if out.ExitCode == 0 {
		t.Fatalf("write outside the workspace succeeded: %q", out.Output)
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatalf("file outside the workspace was created: %q", out.Output)
	}
	if !strings.Contains(out.Output, "Permission denied") {
		t.Errorf("output %q does not report a refusal", out.Output)
	}
}

// TestShellToolWithoutPolicyStaysUnconfined keeps mode off honest: the same
// write outside the workspace succeeds when no policy is attached.
func TestShellToolWithoutPolicyStaysUnconfined(t *testing.T) {
	target := filepath.Join(t.TempDir(), "unconfined.txt")
	res, err := ExecuteShellCommand(context.Background(), "echo ok > "+target)
	if err != nil {
		t.Fatalf("ExecuteShellCommand: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, output %q", res.ExitCode, res.Output)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("unconfined write did not land: %v", err)
	}
}
