package golem

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/mcp"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/subagent"
	"github.com/user/golem/pkg/tools"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// DefaultModelName is the primary model used for Golem reasoning.
const DefaultModelName = "gemini-flash-latest"

// RetryCallback is invoked when transient API rate limits or 503s cause a retry.
type RetryCallback func(attempt, maxRetries int, backoff time.Duration)

// StreamHandler receives streaming events during an agent turn.
type StreamHandler interface {
	OnChunk(token string)
	OnToolStart(toolName, command string)
	OnToolDone(toolName, output string, exitCode int)
}

// SubagentStreamHandler optionally extends StreamHandler to receive specialized subagent events.
type SubagentStreamHandler interface {
	StreamHandler
	OnSubagentStart(role, model, prompt string)
	OnSubagentTool(role, model, toolName, command, output string, exitCode int, isRunning bool)
	OnSubagentDone(role, model, result string, duration time.Duration, err error)
}

// Config defines the configuration parameters for Golem.
type Config struct {
	ModelName         string
	APIKey            string
	StorageDir        string
	WorkspaceDir      string
	LiveHeadCount     int
	ViewBudgetBytes   int
	CompactorInterval time.Duration
	OnRetry           RetryCallback
	MCPConfig         *mcp.Config
	MCPConfigPaths    []string
}

// DefaultConfig provides recommended settings.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	storage := "./.golem/memory"
	if home != "" {
		storage = home + "/.golem/memory"
	}

	return Config{
		ModelName:         DefaultModelName,
		StorageDir:        storage,
		LiveHeadCount:     fractal.DefaultLiveHeadCount,
		ViewBudgetBytes:   fractal.DefaultViewBudgetBytes,
		CompactorInterval: 30 * time.Second,
	}
}

// Agent wraps the ADK Agent, Runner, and the fractal memory subsystem.
type Agent struct {
	cfg              Config
	store            *fractal.Store
	view             *fractal.View
	compactor        *fractal.Compactor
	mcpManager       *mcp.Manager
	mcpToolsets      []mcp.NamedToolset
	subagentRunner   *subagent.Runner
	runner           *runner.Runner
	adkAgent         agent.Agent
	userID           string
	activeHandlerMu  sync.RWMutex
	activeHandler    StreamHandler
	compactorTrigger chan struct{}
}

// New constructs an operational Golem agent.
func New(ctx context.Context, cfg Config) (*Agent, error) {
	if cfg.ModelName == "" {
		cfg.ModelName = DefaultModelName
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("GEMINI_API_KEY")
		if cfg.APIKey == "" {
			cfg.APIKey = os.Getenv("GOOGLE_API_KEY")
		}
	}
	if cfg.StorageDir == "" {
		cfg.StorageDir = DefaultConfig().StorageDir
	}

	// 1. Initialize fractal memory storage
	store, err := fractal.OpenStore(cfg.StorageDir)
	if err != nil {
		return nil, errors.Wrapf(err, "opening fractal store at %s", cfg.StorageDir)
	}

	// 2. Initialize fractal view
	viewCfg := fractal.ViewConfig{
		ViewBudgetBytes: cfg.ViewBudgetBytes,
		LiveHeadCount:   cfg.LiveHeadCount,
	}
	view := fractal.NewView(store, viewCfg)

	attempts := int32(5)
	initialDelay := 1.0
	maxDelay := 30.0
	expBase := 2.0
	jitter := 0.5

	clientCfg := &genai.ClientConfig{
		APIKey: cfg.APIKey,
		HTTPOptions: genai.HTTPOptions{
			RetryOptions: &genai.HTTPRetryOptions{
				Attempts:        &attempts,
				InitialDelay:    &initialDelay,
				MaxDelay:        &maxDelay,
				ExpBase:         &expBase,
				Jitter:          &jitter,
				HTTPStatusCodes: []int32{408, 429, 500, 502, 503, 504},
			},
		},
	}

	// If no API key is provided, check for Vertex AI environment or Antigravity configuration
	if cfg.APIKey == "" {
		if os.Getenv("GOOGLE_GENAI_USE_VERTEXAI") == "" && os.Getenv("GOOGLE_CLOUD_PROJECT") == "" {
			home, _ := os.UserHomeDir()
			if home != "" {
				settingsPath := filepath.Join(home, ".gemini/antigravity-cli/settings.json")
				if data, err := os.ReadFile(settingsPath); err == nil {
					var s struct {
						GCP struct {
							Project  string `json:"project"`
							Location string `json:"location"`
						} `json:"gcp"`
					}
					if err := json.Unmarshal(data, &s); err == nil && s.GCP.Project != "" {
						clientCfg.Backend = genai.BackendVertexAI
						clientCfg.Project = s.GCP.Project
						clientCfg.Location = s.GCP.Location
						if clientCfg.Location == "" {
							clientCfg.Location = "global"
						}
					}
				}
			}
		}
	}
	genaiClient, err := genai.NewClient(ctx, clientCfg)
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "initializing genai client")
	}

	summarizer := fractal.NewGeminiSummarizer(genaiClient, cfg.ModelName)
	compactor := fractal.NewCompactor(store, view, summarizer, fractal.DefaultCompactorConfig())

	// Start background compactor if interval is configured
	if cfg.CompactorInterval > 0 {
		compactor.StartBackground(ctx, cfg.CompactorInterval)
	}

	// 4. Initialize Tools (sandboxed to current working directory)
	workDir, _ := os.Getwd()
	shellTool, err := tools.NewShellTool()
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "creating shell tool")
	}

	readFileTool, err := tools.NewReadFileTool(workDir)
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "creating read_file tool")
	}

	writeFileTool, err := tools.NewWriteFileTool(workDir)
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "creating write_file tool")
	}

	replaceRegexTool, err := tools.NewReplaceRegexTool(workDir)
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "creating replace_regex tool")
	}

	zoomTool, err := fractal.NewZoomTool(store)
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "creating zoom tool")
	}

	dateTool, err := fractal.NewDateTool(store)
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "creating date tool")
	}

	allTools := []tool.Tool{
		shellTool,
		readFileTool,
		writeFileTool,
		replaceRegexTool,
		zoomTool,
		dateTool,
	}

	// 5. Discover & Build MCP Toolsets
	mcpManager := mcp.NewManager()
	var mcpToolsets []mcp.NamedToolset
	var adkToolsets []tool.Toolset

	mcpCfg := cfg.MCPConfig
	if mcpCfg == nil {
		wsDir := cfg.WorkspaceDir
		if wsDir == "" {
			wsDir = workDir
		}
		discovered, loadedPaths, _ := mcp.DiscoverAndMerge(wsDir)
		mcpCfg = &discovered
		cfg.MCPConfigPaths = loadedPaths
	}
	if mcpCfg != nil {
		mcpManager.LoadConfig(ctx, *mcpCfg)
		builtToolsets, _ := mcp.BuildToolsets(*mcpCfg)
		mcpToolsets = builtToolsets
		for _, ts := range builtToolsets {
			adkToolsets = append(adkToolsets, ts.Toolset)
		}
	}

	var agentPtr *Agent

	// 6. Initialize Subagent Runner & Delegation Tool
	modelFactory := subagent.NewModelFactory(clientCfg, cfg.ModelName)
	subagentRunner := subagent.NewRunner(subagent.RunnerConfig{
		ModelFactory:   modelFactory,
		WorkspaceDir:   workDir,
		ReadOnlyTools:  []tool.Tool{readFileTool, zoomTool, dateTool},
		ReadWriteTools: []tool.Tool{writeFileTool, replaceRegexTool, shellTool},
		MCPToolsets:    adkToolsets,
		EventHandler: func(ev subagent.Event) {
			if agentPtr != nil {
				agentPtr.dispatchSubagentEvent(ev)
			}
		},
	})
	invokeSubagentTool, err := subagent.NewInvokeTool(subagentRunner)
	if err != nil {
		store.Close()
		mcpManager.Close()
		return nil, errors.Wrap(err, "creating invoke_subagent tool")
	}
	allTools = append(allTools, invokeSubagentTool)

	// 7. Initialize Gemini ADK Model
	modelInstance, err := gemini.NewModel(ctx, cfg.ModelName, clientCfg)
	if err != nil {
		store.Close()
		mcpManager.Close()
		return nil, errors.Wrapf(err, "initializing gemini model %s", cfg.ModelName)
	}

	// 8. Build LLM Agent with dynamic fractal memory injection & logging callbacks
	adkAgent, err := llmagent.New(llmagent.Config{
		Name:        "golem",
		Description: "An autonomous agent equipped with shell and file tools, and infinite fractal memory.",
		Model:       modelInstance,
		Tools:       allTools,
		Toolsets:    adkToolsets,
		IncludeContents: llmagent.IncludeContentsNone,
		InstructionProvider: func(rc agent.ReadonlyContext) (string, error) {
			view.Sync()
			renderedView := view.Render()

			var sb strings.Builder
			sb.WriteString(`You are Golem, an AI agent working with the user across long-running sessions.
You have access to local file operations, a shell execution environment, and an infinite fractal memory.

Memory Navigation:
- The <chat> section below contains multi-resolution summaries of older messages, oldest first.
- Each line is formatted: id+n|summary (where id is the starting message ID and n is the message count).
- When you need exact details, verbatim code, or specific past rulings, use zoom(id, n) to open the line.
- zoom(id, 1) returns the exact verbatim message in full.
- Always zoom before guessing or asking the user about past facts.
- Use date(id) to check the timestamp of any message.

`)
			if renderedView != "" {
				sb.WriteString(renderedView)
				sb.WriteString("\n\n")
			}
			sb.WriteString("Now assist the user with their request accurately and effectively.")
			return sb.String(), nil
		},
		BeforeToolCallbacks: []llmagent.BeforeToolCallback{
			func(ctx agent.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
				argsBytes, _ := json.Marshal(args)
				cmdStr := string(argsBytes)
				if cmdArg, ok := args["command"].(string); ok {
					cmdStr = cmdArg
				} else if pathArg, ok := args["path"].(string); ok {
					cmdStr = pathArg
				}
				_, _ = store.AppendEvent(fractal.KindTool, fmt.Sprintf("%s: %s", t.Name(), string(argsBytes)))

				if agentPtr != nil {
					agentPtr.activeHandlerMu.RLock()
					h := agentPtr.activeHandler
					agentPtr.activeHandlerMu.RUnlock()
					if h != nil {
						h.OnToolStart(t.Name(), cmdStr)
					}
				}
				return args, nil
			},
		},
		AfterToolCallbacks: []llmagent.AfterToolCallback{
			func(ctx agent.Context, t tool.Tool, args, result map[string]any, toolErr error) (map[string]any, error) {
				var out string
				var exitCode int
				if toolErr != nil {
					out = fmt.Sprintf("error: %v", toolErr)
					exitCode = 1
				} else {
					resBytes, _ := json.Marshal(result)
					out = string(resBytes)
					if ec, ok := result["exit_code"].(float64); ok {
						exitCode = int(ec)
					} else if ec, ok := result["exit_code"].(int); ok {
						exitCode = ec
					}
					// Cap output to 30,000 characters if huge
					if len(out) > 30000 {
						out = out[:30000] + "... [truncated]"
					}
				}
				_, _ = store.AppendEvent(fractal.KindEcho, fmt.Sprintf("%s: %s", t.Name(), out))

				if agentPtr != nil {
					agentPtr.activeHandlerMu.RLock()
					h := agentPtr.activeHandler
					agentPtr.activeHandlerMu.RUnlock()
					if h != nil {
						h.OnToolDone(t.Name(), out, exitCode)
					}
				}
				return result, toolErr
			},
		},
	})
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "creating adk llm agent")
	}

	// 7. Initialize Runner
	adkRunner, err := runner.NewInMemory("golem-app", adkAgent)
	if err != nil {
		store.Close()
		return nil, errors.Wrap(err, "creating in-memory adk runner")
	}

	agentPtr = &Agent{
		cfg:              cfg,
		store:            store,
		view:             view,
		compactor:        compactor,
		mcpManager:       mcpManager,
		mcpToolsets:      mcpToolsets,
		subagentRunner:   subagentRunner,
		runner:           adkRunner,
		adkAgent:         adkAgent,
		userID:           "user",
		compactorTrigger: make(chan struct{}, 1),
	}

	// Start background compactor loop if configured or for trigger events
	go agentPtr.startCompactorWorker(ctx)

	return agentPtr, nil
}

// startCompactorWorker runs a background goroutine servicing periodic intervals and post-turn triggers.
func (a *Agent) startCompactorWorker(ctx context.Context) {
	var tickerChan <-chan time.Time
	if a.cfg.CompactorInterval > 0 {
		ticker := time.NewTicker(a.cfg.CompactorInterval)
		defer ticker.Stop()
		tickerChan = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-tickerChan:
			_, _ = a.compactor.Pump(ctx)
		case <-a.compactorTrigger:
			_, _ = a.compactor.Pump(ctx)
		}
	}
}

// triggerCompactor signals the background compactor worker without blocking.
func (a *Agent) triggerCompactor() {
	select {
	case a.compactorTrigger <- struct{}{}:
	default:
	}
}

// Process handles a single user turn synchronously, logs events to fractal memory, runs the model,
// and returns the agent's textual response.
func (a *Agent) Process(ctx context.Context, input string) (string, error) {
	return a.ProcessStream(ctx, input, nil)
}

// ProcessStream handles a user turn with streaming chunks and live tool notifications.
func (a *Agent) ProcessStream(ctx context.Context, input string, handler StreamHandler) (string, error) {
	a.activeHandlerMu.Lock()
	a.activeHandler = handler
	a.activeHandlerMu.Unlock()

	defer func() {
		a.activeHandlerMu.Lock()
		a.activeHandler = nil
		a.activeHandlerMu.Unlock()
	}()

	// 1. Log user message to fractal store
	if _, err := a.store.AppendEvent(fractal.KindUser, input); err != nil {
		return "", errors.Wrap(err, "logging user event")
	}

	// Sync view
	a.view.Sync()

	// 2. Prepare GenAI content input
	content := genai.Text(input)[0]

	// 3. Execute ADK runner with retry for transient server spikes (503 / 429)
	var finalResponse string
	maxRetries := 3
	backoff := 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		var responseBuilder strings.Builder
		var runErr error

		// Generate a fresh session ID per turn to ensure stateless runner execution.
		// Persistent memory is solely owned and injected via Fractal Memory view.
		turnSessionID := fmt.Sprintf("turn-%d", time.Now().UnixNano())

		eventsSeq := a.runner.Run(ctx, a.userID, turnSessionID, content, agent.RunConfig{
			StreamingMode: agent.StreamingModeSSE,
		})
		for ev, err := range eventsSeq {
			if err != nil {
				runErr = err
				break
			}

			if ev != nil && ev.Content != nil {
				for _, part := range ev.Content.Parts {
					if part.Text != "" {
						responseBuilder.WriteString(part.Text)
						if handler != nil {
							handler.OnChunk(part.Text)
						}
					}
				}
			}
		}

		if runErr == nil {
			finalResponse = responseBuilder.String()
			break
		}

		if isTransientError(runErr) && attempt < maxRetries {
			if a.cfg.OnRetry != nil {
				a.cfg.OnRetry(attempt, maxRetries, backoff)
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
				continue
			}
		}

		return "", errors.Wrap(runErr, "running adk agent")
	}

	// 4. Log agent response to fractal store
	if finalResponse != "" {
		if _, err := a.store.AppendEvent(fractal.KindTalk, finalResponse); err != nil {
			return finalResponse, errors.Wrap(err, "logging agent response event")
		}
	}

	// 5. Sync view and asynchronously trigger compactor pump
	a.view.Sync()
	a.triggerCompactor()

	return finalResponse, nil
}

// Close flushes and shuts down the store and underlying resources.
func (a *Agent) Close() error {
	if a.mcpManager != nil {
		a.mcpManager.Close()
	}
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}

// Store returns the underlying fractal event and node store.
func (a *Agent) Store() *fractal.Store {
	return a.store
}

// View returns the hybrid Live Head + Fractal Tail memory view.
func (a *Agent) View() *fractal.View {
	return a.view
}

// Compactor returns the background fractal memory compactor.
func (a *Agent) Compactor() *fractal.Compactor {
	return a.compactor
}

// MCPManager returns the active MCP client manager.
func (a *Agent) MCPManager() *mcp.Manager {
	return a.mcpManager
}

// MCPToolsets returns the list of loaded MCP toolsets.
func (a *Agent) MCPToolsets() []mcp.NamedToolset {
	return a.mcpToolsets
}

// MCPConfigPaths returns the discovered configuration file paths.
func (a *Agent) MCPConfigPaths() []string {
	return a.cfg.MCPConfigPaths
}

// SubagentRunner returns the subagent delegation runner.
func (a *Agent) SubagentRunner() *subagent.Runner {
	return a.subagentRunner
}

func (a *Agent) dispatchSubagentEvent(ev subagent.Event) {
	a.activeHandlerMu.RLock()
	h := a.activeHandler
	a.activeHandlerMu.RUnlock()
	if h == nil {
		return
	}

	if sh, ok := h.(SubagentStreamHandler); ok {
		switch ev.Type {
		case subagent.EventSubagentStart:
			sh.OnSubagentStart(ev.Role, ev.Model, ev.Prompt)
		case subagent.EventSubagentTool:
			sh.OnSubagentTool(ev.Role, ev.Model, ev.ToolName, ev.Command, ev.ToolOutput, ev.ExitCode, ev.IsRunning)
		case subagent.EventSubagentDone:
			sh.OnSubagentDone(ev.Role, ev.Model, ev.Result, ev.Duration, ev.Err)
		}
		return
	}

	// Fallback to standard StreamHandler tool calls
	switch ev.Type {
	case subagent.EventSubagentStart:
		h.OnToolStart("subagent:"+ev.Role, fmt.Sprintf("[%s] %s", ev.Model, ev.Prompt))
	case subagent.EventSubagentDone:
		if ev.Err != nil {
			h.OnToolDone("subagent:"+ev.Role, fmt.Sprintf("error: %v", ev.Err), 1)
		} else {
			h.OnToolDone("subagent:"+ev.Role, ev.Result, 0)
		}
	}
}

// isTransientError detects high demand, rate limits, or transient gateway errors.
func isTransientError(err error) bool {
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
