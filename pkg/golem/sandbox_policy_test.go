package golem

import (
	"os"
	"regexp"
	"testing"

	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/sandbox"
)

func TestSandboxConfigDefaultsToPolicy(t *testing.T) {
	cfg := sandboxConfig(nil)
	if cfg.Mode != sandbox.ModePolicy {
		t.Fatalf("mode = %q, want policy by default", cfg.Mode)
	}
	if cfg.ShellTimeout != "120s" {
		t.Fatalf("shell timeout = %q, want 120s", cfg.ShellTimeout)
	}
}

func TestSandboxConfigHonoursTheProfileBlock(t *testing.T) {
	cfg := sandboxConfig(&config.SandboxConfig{
		Mode:           "off",
		AllowWrite:     []string{"/tmp/scratch"},
		AllowNet:       []string{"example.com"},
		RequireConfirm: []string{"shell"},
		EnvAllow:       []string{"PATH"},
		ShellTimeout:   "30s",
	})
	if cfg.Mode != sandbox.ModeOff {
		t.Fatalf("mode = %q, want off", cfg.Mode)
	}
	if len(cfg.AllowWrite) != 1 || cfg.AllowWrite[0] != "/tmp/scratch" {
		t.Fatalf("allow_write = %v", cfg.AllowWrite)
	}
	if len(cfg.AllowNet) != 1 || len(cfg.RequireConfirm) != 1 || len(cfg.EnvAllow) != 1 {
		t.Fatalf("lists not carried over: %#v", cfg)
	}
	if cfg.ShellTimeout != "30s" {
		t.Fatalf("shell timeout = %q", cfg.ShellTimeout)
	}
	// A typo must fail loudly rather than silently disabling enforcement.
	bad := sandboxConfig(&config.SandboxConfig{Mode: "polciy"})
	if _, err := sandbox.New(bad, t.TempDir()); err == nil {
		t.Fatal("expected an error for an unknown sandbox mode")
	}
}

// TestEveryToolConstructorGetsThePolicy is a source-level tripwire: a new tool
// added to agent.go without the policy option would be a silent bypass, which
// is exactly what the sandbox epic forbids.
func TestEveryToolConstructorGetsThePolicy(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("reading agent.go: %v", err)
	}
	// Matches tools.NewXxxTool(...) and web.NewWebFetchTool(...) call sites.
	call := regexp.MustCompile(`(?m)^\s*\w+, err := (tools|web)\.(New\w*Tool)\((.*)$`)
	matches := call.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no tool constructor calls found in agent.go; the tripwire regex is stale")
	}
	for _, m := range matches {
		callArgs := m[3]
		if regexp.MustCompile(`policyOpt|WithPolicy`).MatchString(callArgs) {
			continue
		}
		t.Errorf("%s.%s is built without the sandbox policy: %s", m[1], m[2], callArgs)
	}
}
