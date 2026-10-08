package subagent

import (
	"time"
)

// Standard subagent archetype roles.
const (
	RoleResearcher = "researcher"
	RoleCoder      = "coder"
	RoleGeneral    = "general"
)

// InvokeArgs defines the input schema for the invoke_subagent tool.
type InvokeArgs struct {
	Role        string `json:"role" jsonschema:"The subagent archetype: 'researcher' (read-only code/doc exploration), 'coder' (code modifications and execution), or 'general' (general task execution)"`
	Prompt      string `json:"prompt" jsonschema:"Clear, detailed objective and instructions for the subagent to accomplish"`
	Model       string `json:"model,omitempty" jsonschema:"Optional model override for the subagent (e.g. 'gemini-flash-latest', 'gemini-2.5-pro', 'flash', 'pro', or 'inherit'). Defaults to 'inherit'"`
	Description string `json:"description,omitempty" jsonschema:"Brief one-line summary of why this task is being delegated"`
}

// InvokeResult defines the output returned after subagent completion.
type InvokeResult struct {
	Role       string        `json:"role"`
	SubagentID string        `json:"subagent_id"`
	ModelUsed  string        `json:"model_used"`
	Output     string        `json:"output"`
	ToolCalls  int           `json:"tool_calls"`
	Duration   time.Duration `json:"duration"`
	Error      string        `json:"error,omitempty"`
}

// EventType distinguishes subagent lifecycle notifications.
type EventType string

const (
	EventSubagentStart EventType = "subagent_start"
	EventSubagentTool  EventType = "subagent_tool"
	EventSubagentDone  EventType = "subagent_done"
)

// Event conveys real-time subagent activity to streaming handlers and TUIs.
type Event struct {
	Type        EventType
	Role        string
	SubagentID  string
	Model       string
	Prompt      string
	Description string
	ToolName    string
	Command     string
	ToolOutput  string
	ExitCode    int
	IsRunning   bool
	Result      string
	Duration    time.Duration
	Err         error
}

// EventHandler receives real-time subagent events.
type EventHandler func(event Event)
