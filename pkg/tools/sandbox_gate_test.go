package tools

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/user/golem/pkg/sandbox"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
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

	if err := o.checkWrite(nil, "write_file", "/etc/passwd", ""); !errors.Is(err, sandbox.ErrBlocked) {
		t.Fatalf("write outside workspace: err = %v, want sandbox.ErrBlocked", err)
	}
	if err := o.checkWrite(nil, "write_file", filepath.Join(ws, "ok.txt"), ""); err != nil {
		t.Fatalf("write inside workspace: unexpected err %v", err)
	}
	if err := o.checkWrite(nil, "hashline_patch", "../../escape.go", ""); !errors.Is(err, sandbox.ErrBlocked) {
		t.Fatalf("relative escape: err = %v, want sandbox.ErrBlocked", err)
	}
	if err := o.checkRead(nil, "read_file", "/etc/hostname"); err != nil {
		t.Fatalf("read outside workspace should be allowed: %v", err)
	}
}

// TestToolGateWithoutPolicyAllowsEverything pins the mode-off contract: a tool
// built without WithPolicy behaves exactly as it did before the sandbox existed.
func TestToolGateWithoutPolicyAllowsEverything(t *testing.T) {
	o := newOptions(nil)
	if err := o.checkWrite(nil, "write_file", "/etc/passwd", ""); err != nil {
		t.Fatalf("no policy must allow the write: %v", err)
	}
	if err := o.checkRead(nil, "read_file", "/etc/passwd"); err != nil {
		t.Fatalf("no policy must allow the read: %v", err)
	}

	off, _ := policyFor(t)
	offPolicy, err := sandbox.New(sandbox.Config{Mode: sandbox.ModeOff}, t.TempDir())
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	_ = off
	o = newOptions([]Option{WithPolicy(offPolicy)})
	if err := o.checkWrite(nil, "write_file", "/etc/passwd", ""); err != nil {
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

// fakeApprover is the narrow slice of agent.Context the gate needs.
type fakeApprover struct {
	conf *toolconfirmation.ToolConfirmation

	asked bool
	hint  string
}

func (f *fakeApprover) ToolConfirmation() *toolconfirmation.ToolConfirmation { return f.conf }

func (f *fakeApprover) RequestConfirmation(hint string, payload any) error {
	f.asked = true
	f.hint = hint
	return nil
}

func (f *fakeApprover) Actions() *session.EventActions { return &session.EventActions{} }

// TestRequireConfirmPromotesAnAllowedWriteToAPrompt covers the config path:
// a tool listed in require_confirm asks even when the call itself is allowed,
// the prompt quotes the real action, and the answer is honoured.
func TestRequireConfirmPromotesAnAllowedWriteToAPrompt(t *testing.T) {
	ws := t.TempDir()
	p, err := sandbox.New(sandbox.Config{
		Mode:           sandbox.ModePolicy,
		RequireConfirm: []string{"write_file"},
	}, ws)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	target := filepath.Join(ws, "notes.md")
	o := newOptions([]Option{WithPolicy(p)})

	ctx := &fakeApprover{}
	err = o.checkWrite(ctx, "write_file", target, "write_file notes.md")
	if !errors.Is(err, tool.ErrConfirmationRequired) {
		t.Fatalf("err = %v, want tool.ErrConfirmationRequired", err)
	}
	if !ctx.asked || !strings.Contains(ctx.hint, "write_file notes.md") {
		t.Fatalf("asked = %v, hint = %q", ctx.asked, ctx.hint)
	}

	approved := &fakeApprover{conf: &toolconfirmation.ToolConfirmation{Confirmed: true}}
	if err := o.checkWrite(approved, "write_file", target, "write_file notes.md"); err != nil {
		t.Fatalf("approved write must run: %v", err)
	}

	// Nobody is watching a subagent's turn: the same call is refused outright.
	delegate := newOptions([]Option{WithPolicy(p), WithUnattended()})
	if err := delegate.checkWrite(&fakeApprover{}, "write_file", target, "write_file notes.md"); !errors.Is(err, sandbox.ErrBlocked) {
		t.Fatalf("unattended write: err = %v, want sandbox.ErrBlocked", err)
	}
}

// TestRequireConfirmNeverDowngradesADeny keeps a refused call refused, even if
// the tool is also listed in require_confirm.
func TestRequireConfirmNeverDowngradesADeny(t *testing.T) {
	ws := t.TempDir()
	p, err := sandbox.New(sandbox.Config{
		Mode:           sandbox.ModePolicy,
		RequireConfirm: []string{"write_file"},
	}, ws)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	o := newOptions([]Option{WithPolicy(p)})
	ctx := &fakeApprover{}
	err = o.checkWrite(ctx, "write_file", "/etc/passwd", "write_file /etc/passwd")
	var blocked *sandbox.BlockedError
	if !errors.As(err, &blocked) || blocked.Rule == "require-confirm" {
		t.Fatalf("err = %v, want the deny rule, not a prompt", err)
	}
	if ctx.asked {
		t.Fatal("a denied call must not be offered for approval")
	}
}
