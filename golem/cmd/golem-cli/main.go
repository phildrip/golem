package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/user/golem/pkg/agent"
	"github.com/user/golem/pkg/llm"
)

// SimpleMemory is a basic implementation of the agent.Memory interface.
type SimpleMemory struct {
	history []string
}

// NewSimpleMemory creates a new SimpleMemory instance.
func NewSimpleMemory() *SimpleMemory {
	return &SimpleMemory{
		history: []string{},
	}
}

// Add stores a new entry in the memory.
func (m *SimpleMemory) Add(ctx context.Context, entry string) error {
	m.history = append(m.history, entry)
	return nil
}

// Get retrieves the current state of the memory.
func (m *SimpleMemory) Get(ctx context.Context) (string, error) {
	return strings.Join(m.history, "\n"), nil
}

// Clear clears the memory.
func (m *SimpleMemory) Clear(ctx context.Context) error {
	m.history = []string{}
	return nil
}

func main() {
	fmt.Println("Hello, Golem! Initializing Leader and Worker Agents...")

	projectID := os.Getenv("GCP_PROJECT_ID")
	location := os.Getenv("GCP_LOCATION")
	leaderModelID := os.Getenv("GCP_LEADER_MODEL_ID") // New env var for Leader LLM
	workerModelID := os.Getenv("GCP_WORKER_MODEL_ID") // New env var for Worker LLM

	if projectID == "" || location == "" || leaderModelID == "" || workerModelID == "" {
		log.Fatalf("Please set GCP_PROJECT_ID, GCP_LOCATION, GCP_LEADER_MODEL_ID, and GCP_WORKER_MODEL_ID environment variables.")
	}

	// Initialize Leader LLM
	leaderLLM, err := llm.NewVertexClient(projectID, location, leaderModelID)
	if err != nil {
		log.Fatalf("Failed to create Leader LLM client: %v", err)
	}
	defer leaderLLM.Close()

	// Initialize Worker LLM (even if not directly used by WorkerAgent in simple case)
	workerLLM, err := llm.NewVertexClient(projectID, location, workerModelID)
	if err != nil {
		log.Fatalf("Failed to create Worker LLM client: %v", err)
	}
	defer workerLLM.Close()

	// Create a new WorkerAgent and add tools
	worker := agent.NewWorkerAgent(workerLLM) // Pass workerLLM to WorkerAgent if it ever needs it
	worker.AddTool(&agent.ShellTool{})
	worker.AddTool(&agent.FileReadTool{})
	worker.AddTool(&agent.FileWriteTool{})
	worker.AddTool(&agent.FileReplaceRegexTool{})

	// Create Memory for the Leader Agent
	memory := NewSimpleMemory() // Use the local NewSimpleMemory

	// Create the Leader Agent
	leader := agent.NewLeaderAgent(leaderLLM, worker, memory)

	fmt.Println("\nGolem Agent Ready! Type your request or 'quit' to exit.")

	scanner := bufio.NewScanner(os.Stdin)
	ctx := context.Background()

	for {
		fmt.Print("You: ")
		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "quit" {
			break
		}

		// Process the request with the LeaderAgent
		response, err := leader.Process(ctx, input)
		if err != nil {
			fmt.Printf("Error processing request: %v\n", err)
		} else {
			fmt.Printf("Golem: %s\n\n", response)
		}
	}
}
