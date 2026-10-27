package tools

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/user/golem/pkg/sandbox"
)

// policyFor builds a policy-mode sandbox bound to a temp workspace.
func policyFor(t *testing.T) (*sandbox.Policy, string) {
	t.Helper()
	ws := t.TempDir()
	p, err := sandbox.New(sandbox.Config{Mode: sandbox.ModePolicy}, ws)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	return p, ws
}

// TestToolGateConsultsThePolicy proves the plumbing every tool shares: the
// write gate denies outside the workspace, allows inside it, and the read gate
// stays permissive.
func TestToolGateConsultsThePolicy(t *testing.T) {
	p, ws := policyFor(t)
	o := newOptions([]Option{WithPolicy(p)})

	if err := o.checkWrite("write_file", "/etc/passwd"); !errors.Is(err, sandbox.ErrBlocked) {
		t.Fatalf("write outside workspace: err = %v, want sandbox.ErrBlocked", err)
	}
	if err := o.checkWrite("write_file", filepath.Join(ws, "ok.txt")); err != nil {
		t.Fatalf("write inside workspace: unexpected err %v", err)
	}
	if err := o.checkWrite("hashline_patch", "../../escape.go"); !errors.Is(err, sandbox.ErrBlocked) {
		t.Fatalf("relative escape: err = %v, want sandbox.ErrBlocked", err)
	}
	if err := o.checkRead("read_file", "/etc/hostname"); err != nil {
		t.Fatalf("read outside workspace should be allowed: %v", err)
	}
}

// TestToolGateWithoutPolicyAllowsEverything pins the mode-off contract: a tool
// built without WithPolicy behaves exactly as it did before the sandbox existed.
func TestToolGateWithoutPolicyAllowsEverything(t *testing.T) {
	o := newOptions(nil)
	if err := o.checkWrite("write_file", "/etc/passwd"); err != nil {
		t.Fatalf("no policy must allow the write: %v", err)
	}
	if err := o.checkRead("read_file", "/etc/passwd"); err != nil {
		t.Fatalf("no policy must allow the read: %v", err)
	}

	off, _ := policyFor(t)
	offPolicy, err := sandbox.New(sandbox.Config{Mode: sandbox.ModeOff}, t.TempDir())
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	_ = off
	o = newOptions([]Option{WithPolicy(offPolicy)})
	if err := o.checkWrite("write_file", "/etc/passwd"); err != nil {
		t.Fatalf("mode off must allow the write: %v", err)
	}
}

// TestEveryConstructorAcceptsAPolicy is a compile-time-plus-runtime guard: each
// tool family must be constructible with the shared policy option.
func TestEveryConstructorAcceptsAPolicy(t *testing.T) {
	p, ws := policyFor(t)
	opt := WithPolicy(p)

	builders := map[string]func() error{
		"shell":               func() error { _, err := NewShellTool(opt); return err },
		"read_file":           func() error { _, err := NewReadFileTool(ws, opt); return err },
		"write_file":          func() error { _, err := NewWriteFileTool(ws, opt); return err },
		"replace_regex":       func() error { _, err := NewReplaceRegexTool(ws, opt); return err },
		"find_files":          func() error { _, err := NewFindFilesTool(ws, opt); return err },
		"grep_files":          func() error { _, err := NewGrepFilesTool(ws, opt); return err },
		"hashline_read":       func() error { _, err := NewHashlineReadTool(ws, opt); return err },
		"hashline_find_block": func() error { _, err := NewHashlineFindBlockTool(ws, opt); return err },
		"hashline_patch":      func() error { _, err := NewHashlinePatchTool(ws, opt); return err },
	}
	for name, build := range builders {
		if err := build(); err != nil {
			t.Errorf("%s: constructor rejected the policy option: %v", name, err)
		}
	}
}
