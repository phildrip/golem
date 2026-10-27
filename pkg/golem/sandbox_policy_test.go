package golem

import (
	"os"
	"regexp"
	"strings"
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
// added to the tool set without the policy option would be a silent bypass,
// which is exactly what the sandbox epic forbids.
func TestEveryToolConstructorGetsThePolicy(t *testing.T) {
	src, err := os.ReadFile("toolset.go")
	if err != nil {
		t.Fatalf("reading toolset.go: %v", err)
	}
	// Matches tools.NewXxxTool(...) and web.NewXxxTool(...) call sites.
	call := regexp.MustCompile(`(?m)(tools|web)\.(New\w*Tool)\((.*)$`)
	matches := call.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatal("no tool constructor calls found in toolset.go; the tripwire regex is stale")
	}
	for _, m := range matches {
		callArgs := m[3]
		// Every call spreads the option slice, and that slice carries the
		// policy: checked below.
		if !strings.Contains(callArgs, "opts...") && !strings.Contains(callArgs, "webOpts...") {
			t.Errorf("%s.%s is built without the sandbox policy options: %s", m[1], m[2], callArgs)
		}
	}
	body := string(src)
	if !strings.Contains(body, "tools.WithPolicy(policy)") {
		t.Error("the tools option slice does not carry the sandbox policy")
	}
	if !strings.Contains(body, "web.WithPolicy(policy)") {
		t.Error("the web option slice does not carry the sandbox policy")
	}
}

// TestSubagentsGetUnattendedTools pins the delegation rule: the subagent tool
// set is built unattended, so a delegate can never approve on the user's behalf.
func TestSubagentsGetUnattendedTools(t *testing.T) {
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatalf("reading agent.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "newToolSet(workDir, sandboxPolicy, webCfg, approval.Interactive)") {
		t.Error("the orchestrator tool set is not built interactive")
	}
	if !strings.Contains(body, "newToolSet(workDir, sandboxPolicy, webCfg, approval.Unattended)") {
		t.Error("the subagent tool set is not built unattended")
	}
	if !strings.Contains(body, "ReadWriteTools: []tool.Tool{delegateTools.writeFile") {
		t.Error("the subagent read-write tools are not the delegate set")
	}
}
