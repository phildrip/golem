package golem

import (
	"github.com/user/golem/pkg/tools"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
)

// repairToolArguments is the first BeforeToolCallback of both the orchestrator
// and its subagents: models sometimes send a number as a string
// ({"id":"5441"}), which the ADK's schema validation rejects before the tool
// runs. The map handed to a callback is the same map handed to the tool, so
// repairing it in place is enough to let the call through. See
// tools.RepairArgs for what is and is not rewritten.
func repairToolArguments(_ agent.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
	tools.RepairArgs(t, args)
	return nil, nil
}
