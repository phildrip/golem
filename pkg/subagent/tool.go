package subagent

import (
	"fmt"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ToolName is the identifier for the delegation tool.
const ToolName = "invoke_subagent"

// ToolDescription explains how and when the leader should use the subagent tool.
const ToolDescription = `Delegates a focused task to a specialized subagent with an isolated context window.
Archetypes:
- 'researcher': Read-only exploration and inspection of code, logs, and docs. Use to investigate without modifying files.
- 'coder': Implementation, debugging, or test execution with full file edit and shell tools.
- 'general': General autonomous task execution.
You can optionally specify 'model' (e.g. 'gemini-flash-latest', 'gemini-2.5-pro', 'flash', 'pro', or 'inherit') to select a specific model for the subagent.
The subagent's swarm id is returned in the result header (id: ...); you can use it with send_message to reach a peer on the swarm bus.`

// NewInvokeTool constructs an ADK tool for subagent delegation.
func NewInvokeTool(runner *Runner) (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name:        ToolName,
		Description: ToolDescription,
	}, func(ctx agent.Context, args InvokeArgs) (string, error) {
		res, err := runner.Invoke(ctx, args)
		if err != nil {
			return fmt.Sprintf("[Subagent %s (%s, id: %s) failed: %v]", res.Role, res.ModelUsed, res.SubagentID, err), nil
		}

		header := fmt.Sprintf("[Subagent %s (model: %s, id: %s) completed with %d tool call(s) in %s]\n\n",
			res.Role, res.ModelUsed, res.SubagentID, res.ToolCalls, res.Duration.Round(100*1000000))
		return header + res.Output, nil

	})
	if err != nil {
		return nil, errors.Wrap(err, "creating invoke_subagent tool")
	}
	return t, nil
}
