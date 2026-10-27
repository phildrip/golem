package golem

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/sandbox"
)

// sandboxConfig converts a profile's sandbox block into the compiled policy
// config. A missing block resolves to mode policy: the agent contains accidents
// by default and the user has to ask for mode off.
func sandboxConfig(sb *config.SandboxConfig) sandbox.Config {
	resolved := config.ResolveSandboxConfig(sb)
	return sandbox.Config{
		Mode:           sandbox.Mode(resolved.Mode),
		AllowWrite:     resolved.AllowWrite,
		AllowNet:       resolved.AllowNet,
		RequireConfirm: resolved.RequireConfirm,
		EnvAllow:       resolved.EnvAllow,
		ShellTimeout:   resolved.ShellTimeout,
	}
}

// SandboxPolicy returns the policy every tool consults, for the CLI banner and
// the web config endpoint.
func (a *Agent) SandboxPolicy() *sandbox.Policy {
	if a == nil {
		return nil
	}
	return a.sandbox
}

// AuditLogPath is the decision log this profile will write, for the banner. An
// empty result means the log is in-memory only.
func AuditLogPath(sb *config.SandboxConfig, memoryDir string) string {
	return resolveAuditPath(sb, memoryDir)
}

// resolveAuditPath turns the profile's audit setting into a file path, or ""
// for an in-memory-only log. "off" disables the file; empty means the default
// beside the memory store, so every session leaves a trail without asking.
func resolveAuditPath(sb *config.SandboxConfig, memoryDir string) string {
	setting := ""
	if sb != nil {
		setting = strings.TrimSpace(sb.Audit)
	}
	switch setting {
	case "off", "none", "disabled":
		return ""
	case "":
		if memoryDir == "" {
			return ""
		}
		return filepath.Join(memoryDir, "sandbox-audit.jsonl")
	default:
		if strings.HasPrefix(setting, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				return filepath.Join(home, setting[2:])
			}
		}
		return setting
	}
}

// installAuditor makes the process-wide decision log available to every tool.
//
// The log is process-wide on purpose - one policy decision point means one
// audit log - so a second Agent (the web server creates one per session)
// reuses the installed auditor instead of opening the file again.
func installAuditor(sb *config.SandboxConfig, memoryDir string) error {
	path := resolveAuditPath(sb, memoryDir)
	if existing := approval.CurrentAuditor(); existing != nil {
		if path == "" || existing.Path() == path {
			return nil
		}
	}
	auditor, err := approval.NewAuditor(path, 500)
	if err != nil {
		return errors.Wrapf(err, "opening the sandbox audit log at %s", path)
	}
	approval.SetAuditor(auditor)
	return nil
}
