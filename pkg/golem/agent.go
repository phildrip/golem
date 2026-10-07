package golem

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/mcp"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/provider/openai"
	"github.com/user/golem/pkg/subagent"
	"github.com/user/golem/pkg/swarm"
	"github.com/user/golem/pkg/tools"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
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
	Profile           string
	Provider          string
	BaseURL           string
	ModelName         string
	APIKey            string
	StorageDir        string
	WorkspaceDir      string
	MountedMemories   []config.MountedMemoryConfig
	LiveHeadCount     int
	ViewBudgetBytes   int
	CompactorInterval time.Duration
	CompactorProvider string
	CompactorModel    string
	CompactorBaseURL  string
	CompactorAPIKey   string
	OnRetry           RetryCallback
	MCPConfig         *mcp.Config
	MCPConfigPaths    []string
	FileConfig        config.FileConfig
	SwarmBus          *swarm.Bus
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
	memoryMgr        *fractal.MemoryManager
	compactor        *fractal.Compactor
	mcpManager       *mcp.Manager
	mcpToolsets      []mcp.NamedToolset
	subagentRunner   *subagent.Runner
	runner           *runner.Runner
	adkAgent         agent.Agent
	userID           string
	swarmBus         *swarm.Bus
	activeHandlerMu     sync.RWMutex
	activeHandler       StreamHandler
	compactorTrigger    chan struct{}
	workerCancel        context.CancelFunc
	dynModel            *dynamicModel
	modelFactory        *subagent.ModelFactory
	currentProfileMount string
}

// dynamicModel is a thread-safe proxy implementing model.LLM that allows hot-swapping the active LLM client.
type dynamicModel struct {
	mu      sync.RWMutex
	current model.LLM
}

func newDynamicModel(initial model.LLM) *dynamicModel {
	return &dynamicModel{current: initial}
}

func (d *dynamicModel) Switch(newModel model.LLM) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.current = newModel
}

func (d *dynamicModel) Current() model.LLM {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.current
}

func (d *dynamicModel) Name() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.current != nil {
		return d.current.Name()
	}
	return "dynamic"
}

func (d *dynamicModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	d.mu.RLock()
	m := d.current
	d.mu.RUnlock()
	return m.GenerateContent(ctx, req, stream)
}

// New constructs an operational Golem agent.
func New(ctx context.Context, cfg Config) (*Agent, error) {
	if cfg.ModelName == "" {
		cfg.ModelName = DefaultModelName
	}
	if cfg.Profile == "" {
		cfg.Profile = cfg.FileConfig.ActiveProfile
		if cfg.Profile == "" {
			cfg.Profile = "default"
		}
	}
	if cfg.Provider == "" {
		cfg.Provider = config.ResolveProvider("", cfg.FileConfig, cfg.ModelName)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = config.ResolveBaseURL(cfg.Provider, "", cfg.FileConfig)
	}
	if cfg.APIKey == "" {
		cfg.APIKey = config.ResolveProviderKey(cfg.Provider, "", cfg.FileConfig)
	}
	if cfg.StorageDir == "" {
		cfg.StorageDir = DefaultConfig().StorageDir
	}

	// 1. Initialize fractal memory storage
	store, err := fractal.OpenStore(cfg.StorageDir)
	if err != nil {
		return nil, errors.Wrapf(err, "opening fractal store at %s", cfg.StorageDir)
	}

	// 2. Initialize fractal view & multi-store MemoryManager
	viewCfg := fractal.ViewConfig{
		ViewBudgetBytes: cfg.ViewBudgetBytes,
		LiveHeadCount:   cfg.LiveHeadCount,
	}
	view := fractal.NewView(store, viewCfg)
	memoryMgr := fractal.NewMemoryManager(store, view)

	// Mount profile memory if configured and different from primary workspace memory
	if cfg.Profile != "" && cfg.Profile != "default" {
		profDir, _ := config.ProfileMemoryDir(cfg.Profile)
		if profDir != "" && profDir != cfg.StorageDir {
			if profStore, err := fractal.OpenStore(profDir); err == nil {
				profView := fractal.NewView(profStore, viewCfg)
				memoryMgr.Mount(cfg.Profile, profStore, profView, true)
			}
		}
	}

	// Mount explicit mounted memories from Config or FileConfig
	allMounted := append([]config.MountedMemoryConfig(nil), cfg.MountedMemories...)
	allMounted = append(allMounted, cfg.FileConfig.MountedMemories...)
	for _, mm := range allMounted {
		if mm.Path != "" && mm.Path != cfg.StorageDir {
			if mStore, err := fractal.OpenStore(mm.Path); err == nil {
				mView := fractal.NewView(mStore, viewCfg)
				memoryMgr.Mount(mm.Name, mStore, mView, mm.ReadOnly)
			}
		}
	}

	// 3. Initialize Model (Gemini or OpenAI-compatible provider)
	var clientCfg *genai.ClientConfig
	var modelInstance model.LLM

	if cfg.Provider == config.ProviderGemini {
		attempts := int32(5)
		initialDelay := 1.0
		maxDelay := 30.0
		expBase := 2.0
		jitter := 0.5

		clientCfg = &genai.ClientConfig{
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

		gm, err := gemini.NewModel(ctx, cfg.ModelName, clientCfg)
		if err != nil {
			store.Close()
			return nil, errors.Wrapf(err, "initializing gemini model %s", cfg.ModelName)
		}
		modelInstance = gm
	} else {
		modelInstance = openai.NewClient(cfg.ModelName, cfg.BaseURL, cfg.APIKey, nil)
	}

	// 4. Initialize Compactor Model (either dedicated compactor model or shared primary model)
	compactorModelInstance := modelInstance
	compactorModelName := cfg.CompactorModel
	compactorProvider := cfg.CompactorProvider
	if compactorModelName == "" && cfg.FileConfig.Compactor != nil {
		compactorModelName = cfg.FileConfig.Compactor.Model
	}
	if compactorProvider == "" && cfg.FileConfig.Compactor != nil {
		compactorProvider = cfg.FileConfig.Compactor.Provider
	}

	if compactorModelName != "" && compactorModelName != cfg.ModelName {
		if compactorProvider == "" {
			compactorProvider = config.DeduceProviderFromModel(compactorModelName)
			if compactorProvider == "" {
				compactorProvider = config.ResolveProvider("", cfg.FileConfig, compactorModelName)
			}
		}
		compactorBaseURL := cfg.CompactorBaseURL
		if compactorBaseURL == "" && cfg.FileConfig.Compactor != nil {
			compactorBaseURL = cfg.FileConfig.Compactor.BaseURL
		}
		compactorBaseURL = config.ResolveBaseURL(compactorProvider, compactorBaseURL, cfg.FileConfig)

		compactorAPIKey := cfg.CompactorAPIKey
		if compactorAPIKey == "" && cfg.FileConfig.Compactor != nil {
			compactorAPIKey = cfg.FileConfig.Compactor.APIKey
			if compactorAPIKey == "" && cfg.FileConfig.Compactor.APIKeyEnv != "" {
				compactorAPIKey = os.Getenv(cfg.FileConfig.Compactor.APIKeyEnv)
			}
		}
		compactorAPIKey = config.ResolveProviderKey(compactorProvider, compactorAPIKey, cfg.FileConfig)

		if compactorProvider == config.ProviderGemini {
			cmClientCfg := clientCfg
			if compactorAPIKey != "" && (cmClientCfg == nil || cmClientCfg.APIKey != compactorAPIKey) {
				cmClientCfg = &genai.ClientConfig{APIKey: compactorAPIKey}
			}
			cgm, err := gemini.NewModel(ctx, compactorModelName, cmClientCfg)
			if err == nil {
				compactorModelInstance = cgm
			}
		} else {
			compactorModelInstance = openai.NewClient(compactorModelName, compactorBaseURL, compactorAPIKey, nil)
		}
	}

	compactorCfg := fractal.DefaultCompactorConfig()
	if cfg.FileConfig.Compactor != nil && cfg.FileConfig.Compactor.TargetBytes > 0 {
		compactorCfg.TargetBytes = cfg.FileConfig.Compactor.TargetBytes
	}

	summarizer := fractal.NewLLMSummarizer(compactorModelInstance)
	compactor := fractal.NewCompactor(store, view, summarizer, compactorCfg)

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

	zoomTool, err := fractal.NewMultiZoomTool(memoryMgr)
	if err != nil {
		memoryMgr.Close()
		return nil, errors.Wrap(err, "creating zoom tool")
	}

	dateTool, err := fractal.NewMultiDateTool(memoryMgr)
	if err != nil {
		memoryMgr.Close()
		return nil, errors.Wrap(err, "creating date tool")
	}

	rememberTool, err := fractal.NewRememberTool(memoryMgr, cfg.Profile)
	if err != nil {
		memoryMgr.Close()
		return nil, errors.Wrap(err, "creating remember tool")
	}

	findFilesTool, err := tools.NewFindFilesTool(workDir)
	if err != nil {
		memoryMgr.Close()
		return nil, errors.Wrap(err, "creating find_files tool")
	}

	grepFilesTool, err := tools.NewGrepFilesTool(workDir)
	if err != nil {
		memoryMgr.Close()
		return nil, errors.Wrap(err, "creating grep_files tool")
	}

	hashlineReadTool, err := tools.NewHashlineReadTool(workDir)
	if err != nil {
		memoryMgr.Close()
		return nil, errors.Wrap(err, "creating hashline_read tool")
	}

	hashlineFindBlockTool, err := tools.NewHashlineFindBlockTool(workDir)
	if err != nil {
		memoryMgr.Close()
		return nil, errors.Wrap(err, "creating hashline_find_block tool")
	}

	hashlinePatchTool, err := tools.NewHashlinePatchTool(workDir)
	if err != nil {
		memoryMgr.Close()
		return nil, errors.Wrap(err, "creating hashline_patch tool")
	}

	allTools := []tool.Tool{
		shellTool,
		readFileTool,
		writeFileTool,
		replaceRegexTool,
		findFilesTool,
		grepFilesTool,
		hashlineReadTool,
		hashlineFindBlockTool,
		hashlinePatchTool,
		zoomTool,
		dateTool,
		rememberTool,
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

	// 6. Initialize Swarm Bus & Tools
	swarmBus := cfg.SwarmBus
	if swarmBus == nil {
		swarmBus = swarm.NewBus()
	}
	orchestratorSwarmTS := swarm.NewToolset(swarmBus, "orchestrator")
	if swarmTools, err := orchestratorSwarmTS.BuildTools(); err == nil {
		allTools = append(allTools, swarmTools...)
	}

	// 7. Initialize Subagent Runner & Delegation Tool
	modelFactory := subagent.NewModelFactoryWithConfig(subagent.FactoryConfig{
		ClientConfig:    clientCfg,
		DefaultModel:    cfg.ModelName,
		DefaultProvider: cfg.Provider,
		DefaultBaseURL:  cfg.BaseURL,
		DefaultAPIKey:   cfg.APIKey,
		FileConfig:      cfg.FileConfig,
	})
	subagentRunner := subagent.NewRunner(subagent.RunnerConfig{
		ModelFactory:   modelFactory,
		WorkspaceDir:   workDir,
		ReadOnlyTools:  []tool.Tool{readFileTool, findFilesTool, grepFilesTool, hashlineReadTool, hashlineFindBlockTool, zoomTool, dateTool},
		ReadWriteTools: []tool.Tool{writeFileTool, replaceRegexTool, hashlinePatchTool, shellTool, rememberTool},
		MCPToolsets:    adkToolsets,
		SwarmBus:       swarmBus,
		EventHandler: func(ev subagent.Event) {
			if agentPtr != nil {
				agentPtr.dispatchSubagentEvent(ev)
			}
		},
		MemoryContext: func() string {
			if view != nil {
				view.Sync()
				liveHead := view.LiveHeadEvents()
				if len(liveHead) == 0 {
					return ""
				}
				var sb strings.Builder
				sb.WriteString("<recent_live_head>\n")
				for _, ev := range liveHead {
					clean := strings.ReplaceAll(ev.Text, "\n", " ")
					if len(clean) > 120 {
						clean = clean[:120] + "..."
					}
					sb.WriteString(fmt.Sprintf("#%d [%s]: %s\n", ev.ID, ev.Kind, clean))
				}
				sb.WriteString("</recent_live_head>")
				return sb.String()
			}
			return ""
		},
	})
	invokeSubagentTool, err := subagent.NewInvokeTool(subagentRunner)
	if err != nil {
		store.Close()
		mcpManager.Close()
		return nil, errors.Wrap(err, "creating invoke_subagent tool")
	}
	allTools = append(allTools, invokeSubagentTool)

	// 8. Build LLM Agent with dynamic fractal memory injection & logging callbacks
	dynModel := newDynamicModel(modelInstance)
	adkAgent, err := llmagent.New(llmagent.Config{
		Name:        "golem",
		Description: "An autonomous agent equipped with shell and file tools, and infinite fractal memory.",
		Model:       dynModel,
		Tools:       allTools,
		Toolsets:    adkToolsets,
		IncludeContents: llmagent.IncludeContentsNone,
		InstructionProvider: func(rc agent.ReadonlyContext) (string, error) {
			memoryMgr.SyncAll()
			renderedView := memoryMgr.RenderCombined()

			var sb strings.Builder
			sb.WriteString(`You are Golem, an AI agent working with the user across long-running sessions.
You have access to local file operations, a shell execution environment, and an infinite fractal memory.

Memory Navigation & Scopes:
- The memory section below contains multi-resolution summaries of older messages and mounted profile memories.
- Each line is formatted: id+n|summary (where id is the starting message ID and n is the message count).
- When you need exact details, verbatim code, or specific past rulings, use zoom(id, n) (with optional target, e.g. target="work") to open the line.
- zoom(id, 1) returns the exact verbatim message in full.
- Always zoom before guessing or asking the user about past facts.
- Use date(id) to check the timestamp of any message.
- Use remember(text, scope) to persist important user preferences, architectural decisions, or conventions into profile memory.

Fast File Search & Safe Editing:
- Use find_files to locate files by name or fuzzy path across the project.
- Use grep_files to search for code occurrences or regular expressions across files.
- For reading and editing files safely, prefer hashline_read, hashline_find_block, and hashline_patch:
  - hashline_read formats lines with stable xxHash anchors (e.g. 42:a3).
  - hashline_find_block locates the enclosing syntactic block (function, struct, etc.) around an anchor.
  - hashline_patch applies changes ('SWAP', 'DEL', 'INS.PRE', 'INS.POST', 'SWAP.BLK', 'DEL.BLK', 'INS.BLK.PRE', 'INS.BLK.POST') bound to anchors.
  - This guarantees edits never hit wrong lines or stale content.
- You may also use read_file, write_file, and replace_regex when standard editing is preferred.

Subagents & Delegation:
- Use invoke_subagent to delegate focused investigations, file explorations, or complex coding tasks to subagents.
- Subagents run with isolated context windows, preventing context bloat.
- Both you and your subagents share the same fractal memory store. Subagents can use zoom(id, n), date(id), and remember(text) to inspect and update memory.

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

	workerCtx, workerCancel := context.WithCancel(ctx)
	agentPtr = &Agent{
		cfg:              cfg,
		store:            store,
		view:             view,
		memoryMgr:        memoryMgr,
		compactor:        compactor,
		mcpManager:       mcpManager,
		mcpToolsets:      mcpToolsets,
		subagentRunner:   subagentRunner,
		runner:           adkRunner,
		adkAgent:         adkAgent,
		userID:           "user",
		swarmBus:         swarmBus,
		compactorTrigger: make(chan struct{}, 1),
		workerCancel:     workerCancel,
		dynModel:         dynModel,
		modelFactory:     modelFactory,
	}

	// Start background compactor loop if configured or for trigger events
	go agentPtr.startCompactorWorker(workerCtx)

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
	if a.workerCancel != nil {
		a.workerCancel()
	}
	if a.swarmBus != nil {
		_ = a.swarmBus.Close()
	}
	if a.mcpManager != nil {
		a.mcpManager.Close()
	}
	if a.memoryMgr != nil {
		return a.memoryMgr.Close()
	}
	if a.store != nil {
		return a.store.Close()
	}
	return nil
}

// MemoryManager returns the multi-store memory manager coordinating primary and mounted stores.
func (a *Agent) MemoryManager() *fractal.MemoryManager {
	return a.memoryMgr
}

// Config returns the active agent configuration.
func (a *Agent) Config() Config {
	return a.cfg
}

// SwitchProfile hot-switches the active profile on the running Agent, mounting the profile memory store if needed.
func (a *Agent) SwitchProfile(ctx context.Context, profileName string) (config.ProfileConfig, error) {
	name, prof := config.ResolveActiveProfile(profileName, a.cfg.FileConfig)
	a.cfg.Profile = name

	if prof.Provider != "" {
		a.cfg.Provider = prof.Provider
	}
	if prof.Model != "" {
		a.cfg.ModelName = prof.Model
	}
	if prof.BaseURL != "" {
		a.cfg.BaseURL = prof.BaseURL
	}
	if prof.APIKey != "" {
		a.cfg.APIKey = prof.APIKey
	}
	if prof.Compactor != nil {
		a.cfg.FileConfig.Compactor = prof.Compactor
		if prof.Compactor.Model != "" {
			a.cfg.CompactorModel = prof.Compactor.Model
		}
		if prof.Compactor.Provider != "" {
			a.cfg.CompactorProvider = prof.Compactor.Provider
		}
		if prof.Compactor.BaseURL != "" {
			a.cfg.CompactorBaseURL = prof.Compactor.BaseURL
		}
		if prof.Compactor.APIKey != "" {
			a.cfg.CompactorAPIKey = prof.Compactor.APIKey
		}
	}

	// 1. Re-instantiate LLM client and hot-swap active model on dynModel
	var newModel model.LLM
	if a.cfg.Provider == config.ProviderGemini {
		attempts := int32(5)
		initialDelay := 1.0
		maxDelay := 30.0
		expBase := 2.0
		jitter := 0.5

		clientCfg := &genai.ClientConfig{
			APIKey: a.cfg.APIKey,
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

		if a.cfg.APIKey == "" {
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

		gm, err := gemini.NewModel(ctx, a.cfg.ModelName, clientCfg)
		if err != nil {
			return prof, errors.Wrapf(err, "initializing gemini model %s for profile switch", a.cfg.ModelName)
		}
		newModel = gm
	} else {
		newModel = openai.NewClient(a.cfg.ModelName, a.cfg.BaseURL, a.cfg.APIKey, nil)
	}

	if a.dynModel != nil && newModel != nil {
		a.dynModel.Switch(newModel)
	}
	if a.modelFactory != nil {
		a.modelFactory.UpdateDefaults(a.cfg.ModelName, a.cfg.Provider, a.cfg.BaseURL, a.cfg.APIKey, a.cfg.FileConfig)
	}

	// 2. Mount target profile memory if configured and unmount previous dynamic profile memory
	if name != "" && name != "default" && a.memoryMgr != nil {
		profDir := prof.MemoryDir
		if profDir == "" {
			profDir, _ = config.ProfileMemoryDir(name)
		}
		if profDir != "" && profDir != a.cfg.StorageDir {
			if a.currentProfileMount != "" && a.currentProfileMount != name {
				_ = a.memoryMgr.Unmount(a.currentProfileMount)
				a.currentProfileMount = ""
			}
			if _, exists := a.memoryMgr.GetStore(name); !exists {
				if profStore, err := fractal.OpenStore(profDir); err == nil {
					viewCfg := fractal.ViewConfig{
						ViewBudgetBytes: a.cfg.ViewBudgetBytes,
						LiveHeadCount:   a.cfg.LiveHeadCount,
					}
					profView := fractal.NewView(profStore, viewCfg)
					a.memoryMgr.Mount(name, profStore, profView, true)
					a.currentProfileMount = name
				}
			} else {
				a.currentProfileMount = name
			}
		}
	} else if a.memoryMgr != nil && a.currentProfileMount != "" {
		_ = a.memoryMgr.Unmount(a.currentProfileMount)
		a.currentProfileMount = ""
	}

	return prof, nil
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

// SwarmBus returns the swarm communication bus.
func (a *Agent) SwarmBus() *swarm.Bus {
	return a.swarmBus
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
