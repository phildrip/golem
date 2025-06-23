package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/user/golem/pkg/llm"
	// "github.com/user/golem/pkg/tools" // tools package is now implicitly used by the Tool implementations
)

// WorkerAgent is a simple agent that executes specific commands using its tools.
type WorkerAgent struct {
	LLM   *llm.VertexClient
	Tools map[string]ToolWithDescriptor // A map of tool names to ToolWithDescriptor implementations
}

// NewWorkerAgent creates a new WorkerAgent.
func NewWorkerAgent(llmClient *llm.VertexClient) *WorkerAgent {
	return &WorkerAgent{
		LLM:   llmClient,
		Tools: make(map[string]ToolWithDescriptor),
	}
}

// AddTool adds a tool to the WorkerAgent.
func (wa *WorkerAgent) AddTool(tool ToolWithDescriptor) {
	wa.Tools[tool.Name()] = tool
}

// GetToolDescriptors returns a slice of all registered tool descriptors.
func (wa *WorkerAgent) GetToolDescriptors() []ToolDescriptor {
	descriptors := make([]ToolDescriptor, 0, len(wa.Tools))
	for _, tool := range wa.Tools {
		descriptors = append(descriptors, tool.Descriptor())
	}
	return descriptors
}

// Process takes an input string which is expected to be in a "toolName:command" format,
// and executes the corresponding tool.
func (wa *WorkerAgent) Process(ctx context.Context, input string) (string, error) {
	parts := strings.SplitN(input, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid worker command format. Expected 'toolName:command'.")
	}

	toolName := parts[0]
	command := parts[1]

	tool, ok := wa.Tools[toolName]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}

	// In a more advanced scenario, the LLM could be used here to decide which tool to use
	// or how to format the command for the tool based on a natural language input.
	// For this basic worker, we assume the input directly specifies the tool and command.

	result, err := tool.Run(ctx, command)
	if err != nil {
		return "", fmt.Errorf("tool %s failed: %w", toolName, err)
	}

	return result, nil
}
