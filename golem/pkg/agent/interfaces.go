package agent

import (
	"context"
)

// Agent defines the interface for an AI agent.
type Agent interface {
	// Process takes a context and an input string, and returns a response.
	Process(ctx context.Context, input string) (string, error)
}

// Tool defines the interface for a callable tool that an agent can use.
type Tool interface {
	// Name returns the unique name of the tool.
	Name() string
	// Description returns a description of what the tool does, useful for LLM reasoning.
	Description() string
	// Run executes the tool with the given input and returns the output.
	Run(ctx context.Context, input string) (string, error)
}

// ToolWithDescriptor extends the Tool interface to include a method for returning a tool's descriptor.
type ToolWithDescriptor interface {
	Tool
	Descriptor() ToolDescriptor
}

// Memory defines the interface for managing conversation history or context.
type Memory interface {
	// Add stores a new entry in the memory (e.g., a message pair).
	Add(ctx context.Context, entry string) error
	// Get retrieves the current state of the memory.
	Get(ctx context.Context) (string, error)
	// Clear clears the memory.
	Clear(ctx context.Context) error
}
