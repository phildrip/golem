package mcp

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/errors"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
)

// NamedToolset pairs a human-readable server name with its ADK toolset.
type NamedToolset struct {
	Name    string
	Toolset tool.Toolset
	Config  ServerConfig
	cmd     *exec.Cmd
}

// Close terminates any underlying stdio child process started by this toolset.
func (n NamedToolset) Close() {
	if n.cmd != nil && n.cmd.Process != nil {
		_ = n.cmd.Process.Kill()
	}
}

// BuildToolsets constructs ADK toolsets for all valid server configurations.
func BuildToolsets(cfg Config) ([]NamedToolset, []error) {
	var toolsets []NamedToolset
	var errs []error

	for name, srv := range cfg.Servers {
		if srv.Disabled {
			continue
		}

		var tsConfig mcptoolset.Config
		var cmd *exec.Cmd

		if srv.IsRemote() {
			endpoint := srv.EndpointURL()
			if endpoint == "" {
				errs = append(errs, errors.Errorf("remote mcp server %q has empty URL", name))
				continue
			}
			tsConfig = mcptoolset.Config{
				Endpoint: endpoint,
			}
		} else {
			if srv.Command == "" {
				errs = append(errs, errors.Errorf("stdio mcp server %q has empty command", name))
				continue
			}

			cmd = exec.Command(srv.Command, srv.Args...)
			cmd.Env = os.Environ()
			for k, v := range srv.Env {
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
			}

			tsConfig = mcptoolset.Config{
				Transport: &mcp.CommandTransport{
					Command: cmd,
				},
			}
		}

		ts, err := mcptoolset.New(tsConfig)
		if err != nil {
			errs = append(errs, errors.Wrapf(err, "creating toolset for mcp server %s", name))
			continue
		}

		toolsets = append(toolsets, NamedToolset{
			Name:    name,
			Toolset: ts,
			Config:  srv,
			cmd:     cmd,
		})
	}

	return toolsets, errs
}
