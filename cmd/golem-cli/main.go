package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/importer"
	"github.com/user/golem/pkg/sandbox"
	"github.com/user/golem/pkg/server"
	"github.com/user/golem/pkg/tui"
)

// CLIOptions holds parsed command-line flags.
type CLIOptions struct {
	Profile           string
	Provider          string
	BaseURL           string
	Model             string
	MemoryDir         string
	APIKey            string
	ConfigPath        string
	NoTUI             bool
	TUI               bool
	NoMouse           bool
	Mouse             bool
	Theme             string
	Serve             bool
	WebHost           string
	WebPort           int
	WebPortSet        bool
	WebToken          string
	NoWebAuth         bool
	Import            string
	CompactorModel    string
	CompactorProvider string
}

// AgentProcessor defines the interface for executing agent turns.
type AgentProcessor interface {
	Process(ctx context.Context, prompt string) (string, error)
}

// parseFlags parses command-line flags into CLIOptions.
func parseFlags(args []string) (CLIOptions, error) {
	fs := flag.NewFlagSet("golem", flag.ContinueOnError)
	profileFlag := fs.String("profile", "", "Named profile to use (e.g. work, personal)")
	providerFlag := fs.String("provider", "", "LLM provider: gemini (default), opencode, openai, openrouter")
	baseURLFlag := fs.String("base-url", "", "Custom API base URL (e.g. for OpenCode Go, OpenAI, or Ollama)")
	modelFlag := fs.String("model", "", "Model name (e.g. gemini-flash-latest, deepseek-chat)")
	storageFlag := fs.String("memory-dir", "", "Directory to store append-only fractal memory")
	apiKeyFlag := fs.String("api-key", "", "API key for the selected provider")
	configPathFlag := fs.String("config", "", "Path to config file")
	noTuiFlag := fs.Bool("no-tui", false, "Disable rich TUI and fall back to standard readline CLI")
	tuiFlag := fs.Bool("tui", false, "Force enable rich TUI even if terminal detection is ambiguous")
	noMouseFlag := fs.Bool("no-mouse", false, "Disable mouse capture in the TUI (restores native terminal/tmux text selection)")
	mouseFlag := fs.Bool("mouse", false, "Force mouse capture inside tmux/screen (in-app wheel scrolling instead of pane copy-mode)")
	themeFlag := fs.String("theme", "", "TUI palette: 'terminal' (default, uses your terminal's own colours) or 'nord' (bundled, adaptive)")
	serveFlag := fs.Bool("serve", false, "Launch realtime Web UI multiplexer server")
	webFlag := fs.Bool("web", false, "Launch realtime Web UI multiplexer server (alias for -serve)")
	portFlag := fs.Int("port", 8080, "Port for realtime Web UI server (default: 8080)")
	hostFlag := fs.String("host", "", "Interface to bind the web server to (default 127.0.0.1; use your tailscale IP or 0.0.0.0 to reach it from a phone)")
	tokenFlag := fs.String("token", "", "Web auth token (default: generated once and stored in config.json)")
	noWebAuthFlag := fs.Bool("no-web-auth", false, "Disable web auth entirely (only safe when bound to loopback)")
	importFlag := fs.String("import", "", "Import past chat history: 'pick' (interactive), 'latest', or path/UUID")
	compactorModelFlag := fs.String("compactor-model", "", "Dedicated model for background fractal memory compaction (e.g. gemini-2.5-flash, gpt-4o-mini)")
	compactorProviderFlag := fs.String("compactor-provider", "", "Dedicated provider for compactor: gemini, opencode, openai, openrouter")

	if err := fs.Parse(args); err != nil {
		return CLIOptions{}, err
	}

	// Track which flags the user actually set, so a config-file value can win
	// over a flag default without winning over an explicit flag.
	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	return CLIOptions{
		Profile:           *profileFlag,
		Provider:          *providerFlag,
		BaseURL:           *baseURLFlag,
		Model:             *modelFlag,
		MemoryDir:         *storageFlag,
		APIKey:            *apiKeyFlag,
		ConfigPath:        *configPathFlag,
		NoTUI:             *noTuiFlag,
		TUI:               *tuiFlag,
		NoMouse:           *noMouseFlag,
		Mouse:             *mouseFlag,
		Theme:             *themeFlag,
		Serve:             *serveFlag || *webFlag,
		WebHost:           *hostFlag,
		WebPort:           *portFlag,
		WebPortSet:        setFlags["port"],
		WebToken:          *tokenFlag,
		NoWebAuth:         *noWebAuthFlag,
		Import:            *importFlag,
		CompactorModel:    *compactorModelFlag,
		CompactorProvider: *compactorProviderFlag,
	}, nil
}

// determineUseTUI decides whether to activate the full-screen Bubbletea TUI.
func determineUseTUI(isTTY, forceTUI, disableTUI bool) bool {
	return (forceTUI || isTTY) && !disableTUI
}

// shouldCaptureMouse decides whether the TUI grabs the mouse at startup.
//
// Capture is the default everywhere, including inside tmux/screen: golem is a
// full-screen (alternate-screen) app, so the pane has no scrollback for the
// multiplexer copy-mode to reveal - releasing the mouse there only makes the
// wheel do nothing. With capture on, the wheel scrolls golem's own conversation
// viewport (rate-limited). Ctrl+G hands the mouse back to tmux/terminal for
// copy-mode and native selection, and -no-mouse starts with it released.
func shouldCaptureMouse(opts CLIOptions) bool {
	return !opts.NoMouse
}

// buildConfig merges default settings, stored config file, and flag overrides.
func buildConfig(opts CLIOptions, fileCfg config.FileConfig, apiKey string) golem.Config {
	cfg := golem.DefaultConfig()
	if fileCfg.Model != "" {
		cfg.ModelName = fileCfg.Model
	}
	if fileCfg.MemoryDir != "" {
		cfg.StorageDir = fileCfg.MemoryDir
	}
	if opts.Model != "" {
		cfg.ModelName = opts.Model
	}
	if opts.MemoryDir != "" {
		cfg.StorageDir = opts.MemoryDir
	}

	provider := config.ResolveProvider(opts.Provider, fileCfg, cfg.ModelName)
	baseURL := config.ResolveBaseURL(provider, opts.BaseURL, fileCfg)

	cfg.Provider = provider
	cfg.BaseURL = baseURL
	cfg.APIKey = apiKey
	cfg.FileConfig = fileCfg
	cfg.CompactorModel = opts.CompactorModel
	cfg.CompactorProvider = opts.CompactorProvider
	return cfg
}

// cliStreamBridge formats streaming tokens, tool executions, and subagents in CLI mode.
type cliStreamBridge struct {
	out io.Writer
}

func (h *cliStreamBridge) OnChunk(token string) {
	fmt.Fprint(h.out, token)
}

func (h *cliStreamBridge) OnToolStart(toolName, command string) {
	fmt.Fprintf(h.out, "\n[Tool: %s %s]\n", toolName, command)
}

func (h *cliStreamBridge) OnToolDone(toolName, output string, exitCode int) {
	if exitCode != 0 {
		fmt.Fprintf(h.out, "[Tool: %s exit code %d]\n", toolName, exitCode)
	}
}

func (h *cliStreamBridge) OnSubagentStart(role, model, prompt string) {
	fmt.Fprintf(h.out, "\n[Subagent %s (%s) started: %s]\n", role, model, prompt)
}

func (h *cliStreamBridge) OnSubagentTool(role, model, toolName, command, output string, exitCode int, isRunning bool) {
	if isRunning {
		fmt.Fprintf(h.out, "  [Subagent %s -> %s %s]\n", role, toolName, command)
	}
}

func (h *cliStreamBridge) OnSubagentDone(role, model, result string, duration time.Duration, err error) {
	if err != nil {
		fmt.Fprintf(h.out, "[Subagent %s (%s) failed in %v: %v]\n", role, model, duration.Round(time.Millisecond), err)
	} else {
		fmt.Fprintf(h.out, "[Subagent %s (%s) completed in %v]\n", role, model, duration.Round(time.Millisecond))
	}
}

type streamableAgent interface {
	ProcessStream(ctx context.Context, prompt string, handler golem.StreamHandler) (string, error)
}

// approvableAgent is the readline transport's view of the sandbox confirmation
// flow: a turn that stopped for a human can be answered here, because this
// transport has the human on the same goroutine and nothing else to do.
type approvableAgent interface {
	streamableAgent
	PendingApproval() (approval.Request, bool)
	RespondToApproval(ctx context.Context, callID string, approved bool, handler golem.StreamHandler) (string, error)
}

// streamTurnWithApprovals runs one turn and, when the sandbox stops it for a
// human, asks on the terminal and resumes the same turn with the answer.
func streamTurnWithApprovals(ctx context.Context, agent AgentProcessor, sa streamableAgent, input string, bridge *cliStreamBridge, scanner *bufio.Scanner, out io.Writer) error {
	_, err := sa.ProcessStream(ctx, input, bridge)
	for errors.Is(err, golem.ErrAwaitingApproval) {
		approver, ok := agent.(approvableAgent)
		if !ok {
			return fmt.Errorf("this turn needs an approval, but the agent cannot take one")
		}
		req, ok := approver.PendingApproval()
		if !ok {
			return fmt.Errorf("the turn stopped for approval but no call is pending")
		}
		fmt.Fprintf(out, "\n\n[Approval needed] %s\n", req.Hint)
		fmt.Fprint(out, "Run it? [y/N]: ")
		approved := false
		if scanner.Scan() {
			answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
			approved = answer == "y" || answer == "yes"
		}
		fmt.Fprint(out, "\nGolem: ")
		_, err = approver.RespondToApproval(ctx, req.CallID, approved, bridge)
	}
	return err
}

// runReadlineLoop executes non-TUI line-based conversation with Golem.
func runReadlineLoop(ctx context.Context, agent AgentProcessor, scanner *bufio.Scanner, out io.Writer) error {
	fmt.Fprintln(out, "Ready! Type your message, or 'exit' / 'quit' to end.")

	for {
		fmt.Fprint(out, "You: ")
		if !scanner.Scan() {
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "quit" || input == "exit" {
			break
		}

		if sa, ok := agent.(streamableAgent); ok {
			fmt.Fprint(out, "\nGolem: ")
			bridge := &cliStreamBridge{out: out}
			if err := streamTurnWithApprovals(ctx, agent, sa, input, bridge, scanner, out); err != nil {
				fmt.Fprintf(out, "\nError: %v\n\n", err)
			} else {
				fmt.Fprintln(out)
			}
		} else {
			response, err := agent.Process(ctx, input)
			if err != nil {
				fmt.Fprintf(out, "Error: %v\n\n", err)
			} else {
				fmt.Fprintf(out, "\nGolem: %s\n\n", response)
			}
		}
	}
	return scanner.Err()
}

// webServerConfig is the resolved listen/token configuration for -serve.
type webServerConfig struct {
	host         string
	port         int
	token        string
	tokenPath    string
	allowOrigins []string
	loopback     bool
}

// displayURL renders the address a human should open. A wildcard bind is shown
// as localhost because that is where it is reachable from the same machine.
func (w webServerConfig) displayURL() string {
	host := w.host
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(w.port)))
}

// isLoopbackHost reports whether host binds only the loopback interface.
func isLoopbackHost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// resolveWebServerConfig merges flags, the config file [server] block and
// generated defaults, and persists a token on first use.
func resolveWebServerConfig(opts CLIOptions, cfg config.ServerConfig) (webServerConfig, error) {
	out := webServerConfig{
		host:         opts.WebHost,
		port:         opts.WebPort,
		allowOrigins: cfg.AllowOrigins,
	}
	if out.host == "" {
		out.host = cfg.Host
	}
	if out.host == "" {
		out.host = server.DefaultListenHost
	}
	if !opts.WebPortSet && cfg.Port > 0 {
		out.port = cfg.Port
	}
	if out.port <= 0 {
		out.port = 8080
	}
	out.loopback = isLoopbackHost(out.host)

	token := opts.WebToken
	if token == "" {
		token = cfg.Token
	}
	if token == "" && !opts.NoWebAuth {
		path := opts.ConfigPath
		if path == "" {
			var err error
			if path, err = config.DefaultPath(); err != nil {
				return out, err
			}
		}
		// Re-read so a token generated here is written to the on-disk file rather
		// than to the profile-mangled copy already merged in memory.
		fresh, err := config.Load(path)
		if err != nil {
			return out, err
		}
		if token, err = config.EnsureWebToken(&fresh, path); err != nil {
			return out, err
		}
		out.tokenPath = path
	}
	if opts.NoWebAuth {
		token = ""
	}
	out.token = token
	return out, nil
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		log.Fatalf("Error parsing flags: %v", err)
	}

	// Load stored config file (~/.golem/config.json) merged with workspace config (.golem/config.json)
	workDir, _ := os.Getwd()
	fileCfg, err := config.LoadAll(workDir, opts.ConfigPath)
	if err != nil {
		log.Printf("Note: could not load config file: %v", err)
	}

	// The [server] block is top-level, so capture it before ApplyProfile replaces
	// the top-level provider fields with the active profile's.
	serverCfg := config.ServerConfig{}
	if fileCfg.Server != nil {
		serverCfg = *fileCfg.Server
	}

	// Resolve active profile
	profileName, prof := config.ResolveActiveProfile(opts.Profile, fileCfg)
	fileCfg = fileCfg.ApplyProfile(profileName, prof)

	scanner := bufio.NewScanner(os.Stdin)

	// Resolve provider and API key
	provider := config.ResolveProvider(opts.Provider, fileCfg, opts.Model)
	apiKey := config.ResolveProviderKey(provider, opts.APIKey, fileCfg)
	if apiKey == "" && provider == config.ProviderGemini {
		apiKey, err = config.ResolveAPIKey(opts.APIKey, opts.ConfigPath, scanner)
		if err != nil {
			log.Fatalf("Error resolving API key: %v", err)
		}
	}

	cfg := buildConfig(opts, fileCfg, apiKey)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGINT/SIGTERM for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nExiting Golem...")
		cancel()
	}()

	providerLabel := cfg.Provider
	if providerLabel == "" {
		providerLabel = "gemini"
	}
	modelLabel := cfg.ModelName
	if modelLabel == "" {
		modelLabel = "gemini-flash-latest"
	}

	fmt.Printf("Golem AI Agent (ADK v2 + %s:%s + Fractal Memory)\n", providerLabel, modelLabel)
	fmt.Printf("Model: %s | Memory: %s | Workspace: %s\n", cfg.ModelName, cfg.StorageDir, workDir)
	fmt.Println(formatAuthStatus(cfg.Provider, cfg.APIKey))
	fmt.Println(formatSandboxStatus(cfg.FileConfig.Sandbox))
	if auditPath := golem.AuditLogPath(cfg.FileConfig.Sandbox, cfg.StorageDir); auditPath != "" {
		fmt.Printf("Audit: %s\n", auditPath)
	} else {
		fmt.Println("Audit: in-memory only (sandbox.audit: \"off\")")
	}

	if opts.Serve {
		webCfg, err := resolveWebServerConfig(opts, serverCfg)
		if err != nil {
			log.Fatalf("Web server config: %v", err)
		}

		fmt.Printf("\n🚀 Golem Realtime Multiplexer on %s\n", webCfg.displayURL())
		if webCfg.token != "" {
			fmt.Printf("   Web auth token: %s\n", webCfg.token)
			if webCfg.tokenPath != "" {
				fmt.Printf("   (stored in %s; open the URL on your phone and paste it once)\n", webCfg.tokenPath)
			}
		} else {
			fmt.Printf("   ⚠️  Web auth DISABLED (-no-web-auth): any client that can reach this port can drive this agent\n")
		}
		if !webCfg.loopback {
			fmt.Printf("   ⚠️  Bound to %s, not loopback: reachable from other hosts\n", webCfg.host)
		}
		fmt.Printf("Web UI Cockpit active with parallel session tabs, swarm hierarchy, and fractal memory explorer.\n\n")

		srv := server.NewServer(server.ServerConfig{
			Host:         webCfg.host,
			Port:         webCfg.port,
			Token:        webCfg.token,
			AllowOrigins: webCfg.allowOrigins,
			BaseAgentCfg: cfg,
			StaticFS:     server.GetEmbeddedUIFS(),
		})

		go func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = srv.Shutdown(shutdownCtx)
		}()

		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
		return
	}

	isTTY := isatty.IsTerminal(os.Stdout.Fd()) && isatty.IsTerminal(os.Stdin.Fd())
	useTUI := determineUseTUI(isTTY, opts.TUI, opts.NoTUI)

	if !useTUI {
		cfg.OnRetry = func(attempt, maxRetries int, backoff time.Duration) {
			fmt.Printf("\n[Model high demand (503/429). Retrying attempt %d/%d in %v...]\n", attempt, maxRetries, backoff)
		}
	}

	agentInstance, err := golem.New(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize Golem agent: %v", err)
	}
	defer agentInstance.Close()

	if opts.Import != "" {
		cwd, _ := os.Getwd()
		_, err := importer.HandleImportOption(opts.Import, agentInstance.Store(), cwd, os.Stdin, os.Stdout)
		if err != nil {
			log.Fatalf("Import error: %v", err)
		}
	}

	if !useTUI {
		mcpToolsets := agentInstance.MCPToolsets()
		if len(mcpToolsets) > 0 {
			var names []string
			for _, ts := range mcpToolsets {
				names = append(names, ts.Name)
			}
			fmt.Printf("MCP: %d server(s) active (%s)\n", len(mcpToolsets), strings.Join(names, ", "))
		}
	}

	if useTUI {
		// Palette selection: -theme wins, then $GOLEM_THEME, then the default
		// "terminal" palette, which inherits the user's own terminal scheme.
		theme := strings.TrimSpace(opts.Theme)
		if theme == "" {
			theme = strings.TrimSpace(os.Getenv("GOLEM_THEME"))
		}
		if err := tui.ApplyTheme(theme); err != nil {
			log.Printf("TUI theme: %v — falling back to %q", err, tui.CurrentTheme())
		}

		programOpts := []tea.ProgramOption{
			tea.WithAltScreen(),
			tea.WithReportFocus(),
		}
		// In-app mouse wheel scrolling is the default; Ctrl+G releases the mouse
		// for tmux copy-mode / native selection, -no-mouse starts released.
		captureMouse := shouldCaptureMouse(opts)
		if captureMouse {
			programOpts = append(programOpts, tea.WithMouseCellMotion())
		}
		model := tui.NewModel(agentInstance, agentInstance.Store(), agentInstance.View())
		model.SetMouseEnabled(captureMouse)
		p := tea.NewProgram(model, programOpts...)
		if _, err := p.Run(); err != nil {
			log.Fatalf("Error running Golem TUI: %v", err)
		}
		return
	}

	if err := runReadlineLoop(ctx, agentInstance, scanner, os.Stdout); err != nil {
		log.Printf("Readline error: %v", err)
	}
}

// formatSandboxStatus summarises the tool policy layer in the startup banner, so
// a misconfigured profile is visible before the first tool call rather than
// after it.
func formatSandboxStatus(sb *config.SandboxConfig) string {
	resolved := config.ResolveSandboxConfig(sb)
	mode := sandbox.Mode(resolved.Mode)
	if mode == sandbox.ModeOff {
		return "Sandbox: OFF - tools run unconfined"
	}
	label := "Sandbox: policy - dangerous shell shapes denied, writes confined to the workspace"
	if mode == sandbox.ModeIsolation {
		net := "network denied"
		if resolved.Network == sandbox.NetworkHost {
			net = "network allowed"
		}
		label = fmt.Sprintf("Sandbox: isolation - shell confined by the kernel (%s, %s)", sandbox.IsolationFeatures(), net)
	}
	var extra []string
	if len(resolved.AllowWrite) > 0 {
		extra = append(extra, fmt.Sprintf("+%d allow_write", len(resolved.AllowWrite)))
	}
	if len(resolved.AllowNet) > 0 {
		extra = append(extra, fmt.Sprintf("allow_net %d host(s)", len(resolved.AllowNet)))
	}
	if len(resolved.RequireConfirm) > 0 {
		extra = append(extra, "confirm: "+strings.Join(resolved.RequireConfirm, ","))
	}
	if len(extra) > 0 {
		label += " | " + strings.Join(extra, " | ")
	}
	return label
}

// formatAuthStatus produces a user-facing authentication summary string based on the active provider.
func formatAuthStatus(provider, apiKey string) string {
	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	switch cleanProvider {
	case config.ProviderOpenCode:
		if apiKey != "" {
			return "Auth: OpenCode API Key"
		}
		return "Auth: OpenCode (no API key configured)"
	case config.ProviderOpenAI:
		if apiKey != "" {
			return "Auth: OpenAI API Key"
		}
		return "Auth: OpenAI (no API key configured)"
	case config.ProviderOpenRouter:
		if apiKey != "" {
			return "Auth: OpenRouter API Key"
		}
		return "Auth: OpenRouter (no API key configured)"
	case config.ProviderGemini:
		if apiKey != "" {
			return "Auth: Google AI Studio API Key"
		}
		return "Auth: Vertex AI / Application Default Credentials"
	default:
		if apiKey != "" {
			if len(cleanProvider) > 0 {
				return fmt.Sprintf("Auth: %s API Key", strings.ToUpper(cleanProvider[:1])+cleanProvider[1:])
			}
			return "Auth: API Key"
		}
		return fmt.Sprintf("Auth: %s (no API key configured)", cleanProvider)
	}
}
