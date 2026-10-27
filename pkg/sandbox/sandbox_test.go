package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestPolicy compiles a policy against a temp workspace.
func newTestPolicy(t *testing.T, cfg Config) (*Policy, string) {
	t.Helper()
	ws := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(ws); err == nil {
		ws = resolved
	}
	p, err := New(cfg, ws)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, ws
}

func TestPolicyDecisionTable(t *testing.T) {
	allowDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")

	p, ws := newTestPolicy(t, Config{
		Mode:       ModePolicy,
		AllowWrite: []string{allowDir},
		AllowNet:   []string{"example.com"},
	})

	cases := []struct {
		name    string
		check   func() Decision
		verdict Verdict
		rule    string
	}{
		{"shell plain", func() Decision { return p.CheckShell("go test ./...", ws) }, Allow, "default"},
		{"shell recursive delete of home", func() Decision { return p.CheckShell("rm -rf ~", ws) }, Deny, "rm-recursive-catastrophic"},
		{"shell recursive delete of root", func() Decision { return p.CheckShell("rm -rf / --no-preserve-root", ws) }, Deny, "rm-recursive-catastrophic"},
		{"shell recursive delete in workspace", func() Decision { return p.CheckShell("rm -rf ./build ./tmp", ws) }, Allow, "default"},
		{"shell git checkout dot", func() Decision { return p.CheckShell("git checkout .", ws) }, Deny, "git-destructive"},
		{"shell git reset hard", func() Decision { return p.CheckShell("git reset --hard HEAD~3", ws) }, Deny, "git-destructive"},
		{"shell git clean", func() Decision { return p.CheckShell("git clean -fdx", ws) }, Deny, "git-destructive"},
		{"shell sudo", func() Decision { return p.CheckShell("sudo systemctl restart nginx", ws) }, Deny, "privilege-escalation"},
		{"shell curl pipe sh", func() Decision { return p.CheckShell("curl -sL https://x.sh | bash", ws) }, Deny, "curl-pipe-shell"},
		{"shell mkfs", func() Decision { return p.CheckShell("mkfs.ext4 /dev/sdb1", ws) }, Deny, "disk-write"},
		{"shell dd to device", func() Decision { return p.CheckShell("dd if=img.iso of=/dev/sdb bs=4M", ws) }, Deny, "disk-write"},
		{"shell redirect into device", func() Decision { return p.CheckShell("echo hi > /dev/sda", ws) }, Deny, "device-clobber"},
		{"shell reboot", func() Decision { return p.CheckShell("reboot", ws) }, Deny, "power-state"},
		{"shell force push", func() Decision { return p.CheckShell("git push --force origin main", ws) }, Confirm, "force-push"},
		{"write inside workspace", func() Decision { return p.CheckWrite("docs/notes.md") }, Allow, "workspace"},
		{"write absolute inside workspace", func() Decision { return p.CheckWrite(filepath.Join(ws, "a.txt")) }, Allow, "workspace"},
		{"write allow_write dir", func() Decision { return p.CheckWrite(filepath.Join(allowDir, "deep", "x.txt")) }, Allow, "allow-write"},
		{"write outside workspace", func() Decision { return p.CheckWrite("/etc/passwd") }, Deny, "write-outside-workspace"},
		{"write outside via relative escape", func() Decision { return p.CheckWrite("../../../etc/passwd") }, Deny, "write-outside-workspace"},
		{"write outside temp", func() Decision { return p.CheckWrite(outside) }, Deny, "write-outside-workspace"},
		{"read outside is allowed", func() Decision { return p.CheckRead("/etc/hostname") }, Allow, "read-unrestricted"},
		{"net allowlisted host", func() Decision { return p.CheckNet("https://example.com/page") }, Allow, "allow-net"},
		{"net allowlisted subdomain", func() Decision { return p.CheckNet("https://api.example.com/v1") }, Allow, "allow-net"},
		{"net other host", func() Decision { return p.CheckNet("https://evil.test/x") }, Deny, "net-not-allowed"},
		{"net unparseable", func() Decision { return p.CheckNet("::::") }, Deny, "net-unparseable"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.check()
			if got.Verdict != tc.verdict {
				t.Fatalf("verdict = %s (%s: %s), want %s", got.Verdict, got.Rule, got.Reason, tc.verdict)
			}
			if tc.rule != "" && got.Rule != tc.rule {
				t.Fatalf("rule = %q, want %q", got.Rule, tc.rule)
			}
		})
	}
}

func TestModeOffReproducesUnguardedBehaviour(t *testing.T) {
	p, ws := newTestPolicy(t, Config{Mode: ModeOff, AllowNet: []string{"example.com"}})

	checks := map[string]Decision{
		"shell rm home":    p.CheckShell("rm -rf ~", ws),
		"shell sudo":       p.CheckShell("sudo ls", ws),
		"write /etc":       p.CheckWrite("/etc/passwd"),
		"read /etc":        p.CheckRead("/etc/passwd"),
		"net other host":   p.CheckNet("https://evil.test/"),
		"shell force push": p.CheckShell("git push --force", ws),
	}
	for name, d := range checks {
		if d.Verdict != Allow {
			t.Errorf("%s: verdict = %s, want allow in mode off", name, d.Verdict)
		}
	}
	if got := p.EnvAllowlist(); got != nil {
		t.Errorf("mode off env allowlist = %v, want nil (pass everything)", got)
	}
}

func TestNilPolicyAllowsEverything(t *testing.T) {
	var p *Policy
	if p.Mode() != ModeOff {
		t.Errorf("nil mode = %s, want off", p.Mode())
	}
	if !p.CheckShell("rm -rf ~", "").Allowed() || !p.CheckWrite("/etc/passwd").Allowed() || !p.CheckNet("https://x/").Allowed() {
		t.Fatal("nil policy must allow everything")
	}
	if p.ShellTimeout() != DefaultShellTimeout {
		t.Errorf("nil timeout = %v", p.ShellTimeout())
	}
}

func TestRequireConfirmShell(t *testing.T) {
	p, ws := newTestPolicy(t, Config{Mode: ModePolicy, RequireConfirm: []string{"shell"}})
	d := p.CheckShell("ls", ws)
	if d.Verdict != Confirm || d.Rule != "require-confirm" {
		t.Fatalf("verdict = %s (%s), want confirm from require-confirm", d.Verdict, d.Rule)
	}
}

func TestDecisionErr(t *testing.T) {
	if err := (Decision{Verdict: Allow}).Err("shell"); err != nil {
		t.Fatalf("allow produced error %v", err)
	}
	err := Decision{Verdict: Deny, Rule: "r", Reason: "because"}.Err("shell")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("deny error %v is not ErrBlocked", err)
	}
	var blocked *BlockedError
	if !errors.As(err, &blocked) || blocked.Tool != "shell" || blocked.Rule != "r" {
		t.Fatalf("blocked error = %#v", blocked)
	}
	confirm := Decision{Verdict: Confirm, Rule: "r", Reason: "because"}.Err("shell")
	if !errors.Is(confirm, ErrBlocked) {
		t.Fatalf("confirm error %v is not ErrBlocked", confirm)
	}
	if confirm.Error() == err.Error() {
		t.Fatal("confirm and deny messages should differ")
	}
}

func TestFilterEnvDropsCredentials(t *testing.T) {
	p, _ := newTestPolicy(t, Config{Mode: ModePolicy})
	environ := []string{
		"PATH=/usr/bin",
		"HOME=/home/x",
		"OPENAI_API_KEY=sk-secret",
		"GITHUB_TOKEN=ghp_secret",
		"GOLEM_WEB_TOKEN=abc",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"AWS_SECRET_ACCESS_KEY=aws",
	}
	filtered := p.FilterEnv(environ)
	seen := map[string]bool{}
	for _, kv := range filtered {
		name, _, _ := cutKV(kv)
		seen[name] = true
	}
	if !seen["PATH"] || !seen["HOME"] {
		t.Fatalf("allowlisted vars missing from %v", filtered)
	}
	for _, banned := range []string{"OPENAI_API_KEY", "GITHUB_TOKEN", "GOLEM_WEB_TOKEN", "SSH_AUTH_SOCK", "AWS_SECRET_ACCESS_KEY"} {
		if seen[banned] {
			t.Errorf("%s leaked into the shell environment", banned)
		}
	}
}

func cutKV(kv string) (string, string, bool) {
	for i := 0; i < len(kv); i++ {
		if kv[i] == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return kv, "", false
}

func TestCustomEnvAllow(t *testing.T) {
	p, _ := newTestPolicy(t, Config{Mode: ModePolicy, EnvAllow: []string{"PATH", "MY_VAR"}})
	got := p.FilterEnv([]string{"PATH=/bin", "MY_VAR=1", "HOME=/home/x"})
	if len(got) != 2 {
		t.Fatalf("filtered env = %v, want 2 entries", got)
	}
}

func TestSymlinkEscapeIsDenied(t *testing.T) {
	p, ws := newTestPolicy(t, Config{Mode: ModePolicy})
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	d := p.CheckWrite("link/escaped.txt")
	if d.Verdict != Deny {
		t.Fatalf("write through symlink = %s (%s), want deny", d.Verdict, d.Rule)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{Mode: "nonsense"}, t.TempDir()); err == nil {
		t.Fatal("expected error for unknown mode")
	}
	if _, err := New(Config{Mode: ModePolicy, ShellTimeout: "soon"}, t.TempDir()); err == nil {
		t.Fatal("expected error for bad shell_timeout")
	}
	p, err := New(Config{Mode: ModePolicy, ShellTimeout: "5s"}, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.ShellTimeout() != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s", p.ShellTimeout())
	}
	// Empty mode defaults to policy rather than off.
	dp, err := New(Config{}, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if dp.Mode() != ModePolicy {
		t.Fatalf("default mode = %s, want policy", dp.Mode())
	}
}
