package server

import (
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/sandbox"
)

// patchPolicy compiles the profile's sandbox policy for the human-facing REST
// routes, so the API obeys the same write rules as the agent's tools.
//
// A nil policy (mode off, or a profile that will not compile) means the routes
// fall back to their own confinement: the workspace check in the handler.
func (s *Server) patchPolicy() *sandbox.Policy {
	s.cfgMu.RLock()
	sb := s.cfg.BaseAgentCfg.FileConfig.Sandbox
	ws := s.cfg.BaseAgentCfg.WorkspaceDir
	s.cfgMu.RUnlock()
	if ws == "" {
		ws = s.WorkspaceDir()
	}
	resolved := config.ResolveSandboxConfig(sb)
	if resolved.Mode == string(sandbox.ModeOff) {
		return nil
	}
	p, err := sandbox.New(sandbox.Config{
		Mode:           sandbox.Mode(resolved.Mode),
		AllowWrite:     resolved.AllowWrite,
		AllowNet:       resolved.AllowNet,
		RequireConfirm: resolved.RequireConfirm,
		EnvAllow:       resolved.EnvAllow,
		ShellTimeout:   resolved.ShellTimeout,
	}, ws)
	if err != nil {
		return nil
	}
	return p
}
