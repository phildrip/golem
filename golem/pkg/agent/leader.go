package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/user/golem/pkg/llm"
)

// LeaderAgent orchestrates tasks and uses an LLM to decide on actions.
type LeaderAgent struct {
	LLM    *llm.VertexClient // LLM for reasoning and planning
	Worker *WorkerAgent      // Worker agent to execute tools
	Memory Memory            // Conversation memory
}

// NewLeaderAgent creates a new LeaderAgent.
func NewLeaderAgent(leaderLLM *llm.VertexClient, worker *WorkerAgent, mem Memory) *LeaderAgent {
	return &LeaderAgent{
		LLM:    leaderLLM,
		Worker: worker,
		Memory: mem,
	}
}

// ToolCall represents a structured call to a tool from the LLM.
type ToolCall struct {
	ToolName string          `json:"tool_name"`
	Args     json.RawMessage `json:"args"` // Raw JSON of arguments for the tool
}

// Process takes a natural language input, uses the LLM to decide on actions,
// and orchestrates the WorkerAgent.
func (la *LeaderAgent) Process(ctx context.Context, input string) (string, error) {
	// 1. Construct the prompt for the Leader LLM
	toolDescriptors := la.Worker.GetToolDescriptors()
	var toolDescriptionsBuilder strings.Builder
	for _, td := range toolDescriptors {
		toolDescriptionsBuilder.WriteString(fmt.Sprintf("Tool Name: %s\n", td.Name))
		toolDescriptionsBuilder.WriteString(fmt.Sprintf("Description: %s\n", td.Description))
		toolDescriptionsBuilder.WriteString(fmt.Sprintf("Parameters JSON Schema: %s\n\n", td.Parameters))
	}

	systemInstruction := fmt.Sprintf(`You are an AI assistant capable of interacting with the user and executing code using provided tools.
When you need to use a tool, respond with a JSON object in the following format:
{"tool_name": "name_of_the_tool", "args": {"param1": "value1", "param2": "value2"}}

After you execute a tool, I will provide you with the tool's output, and then you should give a final, concise answer to the user based on their original request and the tool's output.

Here are the tools you have access to:
%s

If you don't need to use a tool, provide a natural language response directly to the user.
Always try to use the available tools to fulfill the user's request if appropriate.`, toolDescriptionsBuilder.String())

	// Get history from memory
	history, err := la.Memory.Get(ctx)
	if err != nil {
		log.Printf("Error getting history: %v", err)
		history = ""
	}

	// First LLM call: Decide on tool or direct response
	initialPrompt := fmt.Sprintf("%s\n\nConversation History:\n%s\n\nUser: %s\n\nAI (tool call JSON or natural language response):", systemInstruction, history, input)

	llmResponse, err := la.LLM.Predict(ctx, initialPrompt)
	if err != nil {
		return "", fmt.Errorf("leader LLM initial prediction failed: %w", err)
	}

	// Try to parse as a ToolCall
	var toolCall ToolCall
	err = json.Unmarshal([]byte(llmResponse), &toolCall)

	if err == nil && toolCall.ToolName != "" {
		// It's a tool call, execute it using the WorkerAgent
		log.Printf("Leader LLM decided to use tool: %s with args: %s", toolCall.ToolName, string(toolCall.Args))

		// Convert structured args to the simple string format expected by WorkerAgent.Process
		workerCommand := ""
		switch toolCall.ToolName {
		case "shell":
			var args struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(toolCall.Args, &args); err != nil {
				return "", fmt.Errorf("failed to parse shell tool args: %w", err)
			}
			workerCommand = fmt.Sprintf("shell:%s", args.Command)
		case "read_file":
			var args struct {
				FilePath string `json:"file_path"`
			}
			if err := json.Unmarshal(toolCall.Args, &args); err != nil {
				return "", fmt.Errorf("failed to parse read_file args: %w", err)
			}
			workerCommand = fmt.Sprintf("read_file:%s", args.FilePath)
		case "write_file":
			var args struct {
				FilePath string `json:"file_path"`
				Content  string `json:"content"`
			}
			if err := json.Unmarshal(toolCall.Args, &args); err != nil {
				return "", fmt.Errorf("failed to parse write_file args: %w", err)
			}
			workerCommand = fmt.Sprintf("write_file:%s::%s", args.FilePath, args.Content)
		case "replace_regex":
			var args struct {
				FilePath        string `json:"file_path"`
				RegexPattern    string `json:"regex_pattern"`
				ReplacementString string `json:"replacement_string"`
			}
			if err := json.Unmarshal(toolCall.Args, &args); err != nil {
				return "", fmt.Errorf("failed to parse replace_regex args: %w", err)
			}
			workerCommand = fmt.Sprintf("replace_regex:%s::%s::%s", args.FilePath, args.RegexPattern, args.ReplacementString)
		default:
			return "", fmt.Errorf("unsupported tool called by LLM: %s", toolCall.ToolName)
		}

		// Execute the tool via WorkerAgent
		toolOutput, execErr := la.Worker.Process(ctx, workerCommand)
		if execErr != nil {
			// If tool execution fails, provide error to the LLM
			toolOutput = fmt.Sprintf("Tool %s execution failed: %v", toolCall.ToolName, execErr)
		}

		// Second LLM call: Get final natural language response based on tool output
		feedbackPrompt := fmt.Sprintf(`%s

Original User Request: %s
Tool %s output: %s

Based on the original user's request and the tool output, provide a concise final answer:`, systemInstruction, input, toolCall.ToolName, toolOutput)

		finalResponse, err := la.LLM.Predict(ctx, feedbackPrompt)
		if err != nil {
			return "", fmt.Errorf("leader LLM failed to interpret tool output: %w", err)
		}

		// Add the final response to memory
		la.Memory.Add(ctx, fmt.Sprintf("AI: %s\n", finalResponse))
		return finalResponse, nil

	} else {
		// It's a natural language response directly from the LLM
		la.Memory.Add(ctx, fmt.Sprintf("AI: %s\n", llmResponse))
		return llmResponse, nil
	}
}
