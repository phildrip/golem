package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/tui"
)

// CLIOptions holds parsed command-line flags.
type CLIOptions struct {
	Model      string
	MemoryDir  string
	APIKey     string
	ConfigPath string
	NoTUI      bool
	TUI        bool
}

// AgentProcessor defines the interface for executing agent turns.
type AgentProcessor interface {
	Process(ctx context.Context, prompt string) (string, error)
}

// parseFlags parses command-line flags into CLIOptions.
func parseFlags(args []string) (CLIOptions, error) {
	fs := flag.NewFlagSet("golem", flag.ContinueOnError)
	modelFlag := fs.String("model", "", "Gemini model name")
	storageFlag := fs.String("memory-dir", "", "Directory to store append-only fractal memory")
	apiKeyFlag := fs.String("api-key", "", "Gemini API key")
	configPathFlag := fs.String("config", "", "Path to config file")
	noTuiFlag := fs.Bool("no-tui", false, "Disable rich TUI and fall back to standard readline CLI")
	tuiFlag := fs.Bool("tui", false, "Force enable rich TUI even if terminal detection is ambiguous")

	if err := fs.Parse(args); err != nil {
		return CLIOptions{}, err
	}

	return CLIOptions{
		Model:      *modelFlag,
		MemoryDir:  *storageFlag,
		APIKey:     *apiKeyFlag,
		ConfigPath: *configPathFlag,
		NoTUI:      *noTuiFlag,
		TUI:        *tuiFlag,
	}, nil
}

// determineUseTUI decides whether to activate the full-screen Bubbletea TUI.
func determineUseTUI(isTTY, forceTUI, disableTUI bool) bool {
	return (forceTUI || isTTY) && !disableTUI
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
	cfg.APIKey = apiKey
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
			_, err := sa.ProcessStream(ctx, input, bridge)
			if err != nil {
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

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		log.Fatalf("Error parsing flags: %v", err)
	}

	// Load stored config file (~/.config/golem/config.json) if available
	fileCfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		log.Printf("Note: could not load config file: %v", err)
	}

	scanner := bufio.NewScanner(os.Stdin)

	// Resolve API key (flag -> env -> config file -> interactive prompt)
	apiKey, err := config.ResolveAPIKey(opts.APIKey, opts.ConfigPath, scanner)
	if err != nil {
		log.Fatalf("Error resolving API key: %v", err)
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

	fmt.Println("Golem AI Agent (ADK v2 + Gemini Flash + Fractal Memory)")
	fmt.Printf("Model: %s | Memory: %s\n", cfg.ModelName, cfg.StorageDir)
	if cfg.APIKey != "" {
		fmt.Println("Auth: Google AI Studio API Key")
	} else {
		fmt.Println("Auth: Vertex AI / Application Default Credentials")
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
		p := tea.NewProgram(
			tui.NewModel(agentInstance, agentInstance.Store(), agentInstance.View()),
			tea.WithAltScreen(),
			tea.WithReportFocus(),
		)
		if _, err := p.Run(); err != nil {
			log.Fatalf("Error running Golem TUI: %v", err)
		}
		return
	}

	if err := runReadlineLoop(ctx, agentInstance, scanner, os.Stdout); err != nil {
		log.Printf("Readline error: %v", err)
	}
}
