package config

import "testing"

// TestResolveSandboxConfigDefaults pins the confinement defaults: a profile
// that says nothing gets the policy layer, no network for confined commands, and
// a two-minute shell budget.
func TestResolveSandboxConfigDefaults(t *testing.T) {
	got := ResolveSandboxConfig(nil)
	if got.Mode != "policy" {
		t.Errorf("mode = %q, want policy", got.Mode)
	}
	if got.Network != "none" {
		t.Errorf("network = %q, want none: a confined command must not reach the network by default", got.Network)
	}
	if got.ShellTimeout != "120s" {
		t.Errorf("shell_timeout = %q, want 120s", got.ShellTimeout)
	}
}

// TestResolveSandboxConfigKeepsExplicitValues checks that the isolation knobs
// survive resolution untouched.
func TestResolveSandboxConfigKeepsExplicitValues(t *testing.T) {
	in := &SandboxConfig{
		Mode:           "isolation",
		Network:        "host",
		AllowWrite:     []string{"~/scratch"},
		ReadOnly:       []string{"/srv/data"},
		RequireConfirm: []string{"shell"},
		EnvAllow:       []string{"PATH"},
		ShellTimeout:   "30s",
	}
	got := ResolveSandboxConfig(in)
	if got.Mode != "isolation" || got.Network != "host" || got.ShellTimeout != "30s" {
		t.Fatalf("resolution changed explicit values: %+v", got)
	}
	if len(got.AllowWrite) != 1 || got.AllowWrite[0] != "~/scratch" {
		t.Errorf("allow_write = %v", got.AllowWrite)
	}
	if len(got.ReadOnly) != 1 || got.ReadOnly[0] != "/srv/data" {
		t.Errorf("read_only = %v", got.ReadOnly)
	}
}
