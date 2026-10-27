package golem

import (
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
