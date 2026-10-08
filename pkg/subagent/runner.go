package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/swarm"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// RunnerConfig configures the execution environment for subagents.
type RunnerConfig struct {
	ModelFactory   *ModelFactory
	WorkspaceDir   string
	ReadOnlyTools  []tool.Tool
	ReadWriteTools []tool.Tool
	MCPToolsets    []tool.Toolset
	EventHandler   EventHandler
	MemoryContext  func() string
	SwarmBus       *swarm.Bus
}

// Runner manages spawning and running isolated subagent instances.
type Runner struct {
	cfg RunnerConfig
}

// NewRunner creates a new subagent Runner.
func NewRunner(cfg RunnerConfig) *Runner {
	return &Runner{cfg: cfg}
}

// ModelFactory returns the runner's model factory.
func (r *Runner) ModelFactory() *ModelFactory {
	return r.cfg.ModelFactory
}

// SetEventHandler updates the event callback.
func (r *Runner) SetEventHandler(handler EventHandler) {
	r.cfg.EventHandler = handler
}

// SelectTools picks tools appropriate for the requested role.
func (r *Runner) SelectTools(role string) []tool.Tool {
	cleanRole := strings.ToLower(strings.TrimSpace(role))
	switch cleanRole {
	case RoleResearcher:
		// Researcher only gets read-only inspection tools
		return r.cfg.ReadOnlyTools
	case RoleCoder, RoleGeneral:
		// Coder and General get both read-only and mutating/execution tools
		combined := make([]tool.Tool, 0, len(r.cfg.ReadOnlyTools)+len(r.cfg.ReadWriteTools))
		combined = append(combined, r.cfg.ReadOnlyTools...)
		combined = append(combined, r.cfg.ReadWriteTools...)
		return combined
	default:
		// Default to read-only for unknown roles to prevent unintended side effects
		return r.cfg.ReadOnlyTools
	}
}

const subagentMemoryGuidance = `

Fractal Memory Architecture & Navigation:
- You share Golem's append-only fractal memory store with the parent agent.
- Past conversation events and historical decisions are organized into hierarchical binary tree nodes.
- You have tools to query past memory when needed:
  - zoom(id, n): Inspects a compacted hierarchical node (id+n) or retrieves the exact verbatim message when n=1 (e.g. zoom(0, 1)).
  - date(id): Returns the creation timestamp for a specific event ID.
- Use zoom when you need to inspect past requirements, verify architectural decisions, or check verbatim past user rulings without guessing.`

// SubagentSwarmGuidance returns the swarm section appended to a subagent's
// system prompt when it is attached to a shared swarm bus.
func SubagentSwarmGuidance(agentID string) string {
	return fmt.Sprintf(`

Swarm Communication:
- Your Swarm Agent ID is %q. Use this ID when communicating with peer agents or the orchestrator.
- send_message delivers a point-to-point message to a peer (e.g. recipient "orchestrator" to report back); broadcast_message publishes to a topic channel (e.g. 'discoveries', 'warnings') visible to all listeners.
- check_inbox reads messages sent to you. Call it after a substantial step if the orchestrator may have replied.
- claim_resource / release_resource / list_locks manage advisory locks on files so parallel agents don't clobber the same target; claim before editing a file another agent may touch, release when done.`, agentID)
}

// SystemPromptForRole returns tailored instructions based on the archetype.
func (r *Runner) SystemPromptForRole(role string) string {
	cleanRole := strings.ToLower(strings.TrimSpace(role))
	switch cleanRole {
	case RoleResearcher:
		return `You are an autonomous Research Subagent.
Your goal is to inspect, search, and analyze the workspace codebase and documentation to answer the parent agent's question thoroughly.
RULES:
1. You have READ-ONLY tools. You CANNOT and MUST NOT attempt to create or edit files.
2. Provide exact file paths, line references, and structured findings.
3. Be concise, factual, and direct.` + subagentMemoryGuidance

	case RoleCoder:
		return `You are an autonomous Coding Subagent.
Your goal is to implement, debug, or refactor code in accordance with the parent agent's prompt.
RULES:
1. Make targeted, careful modifications.
2. Run tests or build checks when appropriate to verify your changes.
3. Report exactly what files were modified and the outcome of your verification.` + subagentMemoryGuidance

	default:
		return `You are an autonomous Assistant Subagent delegated to accomplish a targeted task for the parent agent.
Provide clear and accurate results.` + subagentMemoryGuidance
	}
}

// BuildSystemPrompt returns the complete system prompt for a role, including
// base role archetype instructions, fractal memory guidance, and dynamic live-head context.
func (r *Runner) BuildSystemPrompt(role string) string {
	systemPrompt := r.SystemPromptForRole(role)
	if r.cfg.MemoryContext != nil {
		if memCtx := r.cfg.MemoryContext(); memCtx != "" {
			systemPrompt += "\n\nRecent Memory Context:\n" + memCtx
		}
	}
	return systemPrompt
}

// Invoke launches the subagent in an isolated session, streams events, and returns the result.
func (r *Runner) Invoke(ctx context.Context, args InvokeArgs) (InvokeResult, error) {
	start := time.Now()
	role := strings.ToLower(strings.TrimSpace(args.Role))
	if role == "" {
		role = RoleGeneral
	}

	subagentID := fmt.Sprintf("subagent-%s-%d", role, time.Now().UnixNano())

	modelInstance, resolvedModel, err := r.cfg.ModelFactory.GetModel(ctx, args.Model)
	if err != nil {
		return InvokeResult{
			Role:       role,
			SubagentID: subagentID,
			ModelUsed:  args.Model,
			Error:      err.Error(),
			Duration:   time.Since(start),
		}, errors.Wrap(err, "resolving model for subagent")
	}

	// Notify subagent start
	if r.cfg.EventHandler != nil {
		r.cfg.EventHandler(Event{
			Type:        EventSubagentStart,
			Role:        role,
			SubagentID:  subagentID,
			Model:       resolvedModel,
			Prompt:      args.Prompt,
			Description: args.Description,
		})
	}

	tools := r.SelectTools(role)

	if r.cfg.SwarmBus != nil {
		swarmTS := swarm.NewToolset(r.cfg.SwarmBus, subagentID)
		if swarmTools, err := swarmTS.BuildTools(); err == nil {
			tools = append(tools, swarmTools...)
		}
	}
	systemPrompt := r.BuildSystemPrompt(role)
	if r.cfg.SwarmBus != nil {
		systemPrompt += SubagentSwarmGuidance(subagentID)
	}

	var toolCallsCount int32

	subAgent, err := llmagent.New(llmagent.Config{
		Name:        subagentID,
		Description: fmt.Sprintf("Autonomous %s subagent using %s", role, resolvedModel),
		Model:       modelInstance,
		Tools:       tools,
		Toolsets:    r.cfg.MCPToolsets,
		InstructionProvider: func(rc agent.ReadonlyContext) (string, error) {
			return systemPrompt, nil
		},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{
			func(ctx agent.Context, t tool.Tool, toolArgs map[string]any) (map[string]any, error) {
				atomic.AddInt32(&toolCallsCount, 1)
				if r.cfg.EventHandler != nil {
					argsBytes, _ := json.Marshal(toolArgs)
					cmd := string(argsBytes)
					if c, ok := toolArgs["command"].(string); ok {
						cmd = c
					} else if p, ok := toolArgs["path"].(string); ok {
						cmd = p
					} else if fp, ok := toolArgs["file_path"].(string); ok {
						cmd = fp
					} else if pat, ok := toolArgs["pattern"].(string); ok {
						cmd = pat
					} else if q, ok := toolArgs["query"].(string); ok {
						cmd = q
					}
					r.cfg.EventHandler(Event{
						Type:      EventSubagentTool,
						Role:      role,
						Model:     resolvedModel,
						ToolName:  t.Name(),
						Command:   cmd,
						IsRunning: true,
					})
				}
				return nil, nil
			},
		},
		AfterToolCallbacks: []llmagent.AfterToolCallback{
			func(ctx agent.Context, t tool.Tool, toolArgs, result map[string]any, toolErr error) (map[string]any, error) {
				if r.cfg.EventHandler != nil {
					var out string
					var exitCode int
					if toolErr != nil {
						out = toolErr.Error()
						exitCode = 1
					} else {
						if o, ok := result["output"].(string); ok {
							out = o
						} else if c, ok := result["content"].(string); ok {
							out = c
						} else if res, ok := result["result"].(string); ok {
							out = res
						} else {
							resBytes, _ := json.Marshal(result)
							out = string(resBytes)
						}
						if ec, ok := result["exit_code"].(float64); ok {
							exitCode = int(ec)
						} else if ec, ok := result["exit_code"].(int); ok {
							exitCode = ec
						}
						if len(out) > 20000 {
							out = out[:20000] + "... [truncated]"
						}
					}
					r.cfg.EventHandler(Event{
						Type:       EventSubagentTool,
						Role:       role,
						Model:      resolvedModel,
						ToolName:   t.Name(),
						ToolOutput: out,
						ExitCode:   exitCode,
						IsRunning:  false,
					})
				}
				return nil, nil
			},
		},
	})
	if err != nil {
		duration := time.Since(start)
		if r.cfg.EventHandler != nil {
			r.cfg.EventHandler(Event{
				Type:       EventSubagentDone,
				Role:       role,
				SubagentID: subagentID,
				Model:      resolvedModel,
				Duration:   duration,
				Err:        err,
			})
		}
		return InvokeResult{
			Role:       role,
			SubagentID: subagentID,
			ModelUsed:  resolvedModel,
			Duration:   duration,
			Error:      err.Error(),
		}, errors.Wrap(err, "creating subagent agent")

	}

	subRunner, err := runner.NewInMemory("subagent-run", subAgent)
	if err != nil {
		duration := time.Since(start)
		if r.cfg.EventHandler != nil {
			r.cfg.EventHandler(Event{
				Type:       EventSubagentDone,
				Role:       role,
				SubagentID: subagentID,
				Model:      resolvedModel,
				Duration:   duration,
				Err:        err,
			})
		}
		return InvokeResult{
			Role:       role,
			SubagentID: subagentID,
			ModelUsed:  resolvedModel,
			Duration:   duration,
			Error:      err.Error(),
		}, errors.Wrap(err, "creating subagent runner")

	}

	content := genai.Text(args.Prompt)[0]
	sessionID := fmt.Sprintf("sub-%s-%d", role, time.Now().UnixNano())

	var finalResponse string
	maxRetries := 3
	backoff := 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		var responseBuilder strings.Builder
		var runErr error

		eventsSeq := subRunner.Run(ctx, "leader", sessionID, content, agent.RunConfig{
			StreamingMode: agent.StreamingModeSSE,
		})
		for ev, seqErr := range eventsSeq {
			if seqErr != nil {
				runErr = seqErr
				break
			}
			if ev != nil && ev.Content != nil {
				for _, part := range ev.Content.Parts {
					if part.Text != "" {
						responseBuilder.WriteString(part.Text)
					}
				}
			}
		}

		if runErr == nil {
			finalResponse = responseBuilder.String()
			break
		}

		if isTransient(runErr) && attempt < maxRetries {
			select {
			case <-ctx.Done():
				return InvokeResult{
					Role:       role,
					SubagentID: subagentID,
					ModelUsed:  resolvedModel,
					Duration:   time.Since(start),
					Error:      ctx.Err().Error(),
				}, ctx.Err()

			case <-time.After(backoff):
				backoff *= 2
				continue
			}
		}

		duration := time.Since(start)
		if r.cfg.EventHandler != nil {
			r.cfg.EventHandler(Event{
				Type:       EventSubagentDone,
				Role:       role,
				SubagentID: subagentID,
				Model:      resolvedModel,
				Duration:   duration,
				Err:        runErr,
			})
		}
		return InvokeResult{
			Role:       role,
			SubagentID: subagentID,
			ModelUsed:  resolvedModel,
			ToolCalls:  int(atomic.LoadInt32(&toolCallsCount)),
			Duration:   duration,
			Error:      runErr.Error(),
		}, errors.Wrap(runErr, "executing subagent turn")

	}

	duration := time.Since(start)
	result := InvokeResult{
		Role:       role,
		SubagentID: subagentID,
		ModelUsed:  resolvedModel,
		Output:     finalResponse,
		ToolCalls:  int(atomic.LoadInt32(&toolCallsCount)),
		Duration:   duration,
	}

	if r.cfg.EventHandler != nil {
		r.cfg.EventHandler(Event{
			Type:       EventSubagentDone,
			Role:       role,
			SubagentID: subagentID,
			Model:      resolvedModel,
			Result:     finalResponse,
			Duration:   duration,
		})

	}

	return result, nil
}

func isTransient(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "503") ||
		strings.Contains(msg, "unavailable") ||
		strings.Contains(msg, "high demand") ||
		strings.Contains(msg, "429") ||
		strings.Contains(msg, "resource_exhausted") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "timeout")
}
