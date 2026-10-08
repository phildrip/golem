package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/memory/fractal"
)

// MessageType distinguishes message kinds in the viewport.
type MessageType int

const (
	TypeUser MessageType = iota
	TypeAgent
	TypeTool
	TypeSubagent
	TypeSystem
)

// ChatEntry represents a single visual item in the conversation history.
type ChatEntry struct {
	Type           MessageType
	Content        string
	ToolName       string
	Command        string
	ExitCode       int
	IsRunning      bool
	SubagentRole   string
	SubagentModel  string
	SubagentPrompt string
}

// Custom Bubbletea Messages
type agentChunkMsg string

type toolStartMsg struct {
	toolName string
	command  string
}

type toolDoneMsg struct {
	toolName string
	output   string
	exitCode int
}

type subagentStartMsg struct {
	role   string
	model  string
	prompt string
}

type subagentDoneMsg struct {
	role     string
	model    string
	result   string
	duration time.Duration
	err      error
}

type streamMsg struct {
	Payload tea.Msg
}

type agentDoneMsg struct {
	reply string
	err   error
}

type memoryStatsMsg struct {
	totalEvents int
	tailLevels  map[int]int
	liveCount   int
}

// Model is the Bubbletea TUI application state machine.
type Model struct {
	agent              *golem.Agent
	store              *fractal.Store
	viewEngine         *fractal.View
	viewport           viewport.Model
	textarea           textarea.Model
	spinner            spinner.Model
	entries            []ChatEntry
	startupHeader      string
	width              int
	height             int
	ready              bool
	isLoading          bool
	streamingReply     string
	renderer           *glamour.TermRenderer
	memoryStats        memoryStatsMsg
	showTreeModal      bool
	treeContent        string
	treeViewport       viewport.Model
	showShortcutsModal bool
	shortcutsViewport  viewport.Model
	showMCPModal       bool
	mcpViewport        viewport.Model
	showProfileModal   bool
	profileContent     string
	profileViewport    viewport.Model
	currentProfile     string
	inputHistory       []string
	historyIndex       int
	currentDraft       string
	statusMessage      string
	statusMessageAt    time.Time
	cancelTurn         context.CancelFunc
	streamChan         <-chan tea.Msg
	isBlurred          bool
	expandTools        bool
}

// NewModel constructs a new Bubbletea TUI model for Golem.
func NewModel(agentInstance *golem.Agent, store *fractal.Store, viewEngine *fractal.View) Model {
	ta := textarea.New()
	ta.Placeholder = "Ask Golem a question or type / for commands..."
	ta.Focus()
	ta.Prompt = "> "
	ta.CharLimit = 4000
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(ColorPrimary)

	renderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(80),
	)

	var allEvents []fractal.Event
	if store != nil {
		allEvents = store.EventsSlice(0, store.CountEvents())
	}

	var initialHistory []string
	for _, ev := range allEvents {
		if ev.Kind == fractal.KindUser && strings.TrimSpace(ev.Text) != "" {
			trimmed := strings.TrimSpace(ev.Text)
			if len(initialHistory) == 0 || initialHistory[len(initialHistory)-1] != trimmed {
				initialHistory = append(initialHistory, trimmed)
			}
		}
	}

	currentProfile := "default"
	if agentInstance != nil && agentInstance.Config().Profile != "" {
		currentProfile = agentInstance.Config().Profile
	}

	// Restore the recent conversation so opening golem shows where you left off.
	cwd, _ := os.Getwd()
	startupHeader, startupEntries := buildStartupEntries(allEvents, cwd, currentProfile, time.Now())

	return Model{
		agent:          agentInstance,
		store:          store,
		viewEngine:     viewEngine,
		textarea:       ta,
		spinner:        sp,
		renderer:       renderer,
		entries:        startupEntries,
		startupHeader:  startupHeader,
		currentProfile: currentProfile,
		inputHistory:   initialHistory,
		historyIndex:   len(initialHistory),
		memoryStats: memoryStatsMsg{
			tailLevels: make(map[int]int),
		},
	}
}

// Init sets up initial commands for the model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
		m.updateMemoryStatsCmd(),
	)
}

// Update handles incoming messages and terminal events.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
		spCmd tea.Cmd
	)

	// Only forward non-key events (e.g. mouse wheel, window events) to viewports so typing in
	// the textarea never triggers viewport pager scrolling (space, d, u, j, k, etc.).
	if _, isKey := msg.(tea.KeyMsg); !isKey {
		if m.showProfileModal {
			m.profileViewport, vpCmd = m.profileViewport.Update(msg)
		} else if m.showTreeModal {
			m.treeViewport, vpCmd = m.treeViewport.Update(msg)
		} else if m.showShortcutsModal {
			m.shortcutsViewport, vpCmd = m.shortcutsViewport.Update(msg)
		} else if m.showMCPModal {
			m.mcpViewport, vpCmd = m.mcpViewport.Update(msg)
		} else {
			m.viewport, vpCmd = m.viewport.Update(msg)
		}
	}
	m.spinner, spCmd = m.spinner.Update(msg)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerHeight := 4
		footerHeight := 6
		verticalMarginHeight := headerHeight + footerHeight

		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-verticalMarginHeight)
			m.viewport.KeyMap = viewport.KeyMap{}
			m.viewport.YPosition = headerHeight
			m.treeViewport = viewport.New(msg.Width-10, msg.Height-8)
			m.treeViewport.KeyMap = viewport.KeyMap{}
			m.shortcutsViewport = viewport.New(msg.Width-10, msg.Height-8)
			m.shortcutsViewport.KeyMap = viewport.KeyMap{}
			m.mcpViewport = viewport.New(msg.Width-10, msg.Height-8)
			m.mcpViewport.KeyMap = viewport.KeyMap{}
			m.profileViewport = viewport.New(msg.Width-10, msg.Height-8)
			m.profileViewport.KeyMap = viewport.KeyMap{}
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - verticalMarginHeight
			m.viewport.KeyMap = viewport.KeyMap{}
			m.treeViewport.Width = msg.Width - 10
			m.treeViewport.Height = msg.Height - 8
			m.treeViewport.KeyMap = viewport.KeyMap{}
			m.shortcutsViewport.Width = msg.Width - 10
			m.shortcutsViewport.Height = msg.Height - 8
			m.shortcutsViewport.KeyMap = viewport.KeyMap{}
			m.mcpViewport.Width = msg.Width - 10
			m.mcpViewport.Height = msg.Height - 8
			m.mcpViewport.KeyMap = viewport.KeyMap{}
			m.profileViewport.Width = msg.Width - 10
			m.profileViewport.Height = msg.Height - 8
			m.profileViewport.KeyMap = viewport.KeyMap{}
		}

		m.textarea.SetWidth(msg.Width - 4)

		if m.renderer != nil {
			m.renderer, _ = glamour.NewTermRenderer(
				glamour.WithAutoStyle(),
				glamour.WithWordWrap(msg.Width-6),
			)
		}

		m.renderConversationToViewport()

	case tea.KeyMsg:
		if m.showProfileModal {
			switch msg.Type {
			case tea.KeyCtrlC:
				return m, tea.Quit
			case tea.KeyCtrlD:
				m.profileViewport.HalfViewDown()
				return m, nil
			case tea.KeyCtrlU:
				m.profileViewport.HalfViewUp()
				return m, nil
			case tea.KeyEsc:
				m.showProfileModal = false
				if !m.isBlurred {
					m.textarea.Focus()
				}
				return m, nil
			case tea.KeyUp:
				m.profileViewport.LineUp(1)
				return m, nil
			case tea.KeyDown:
				m.profileViewport.LineDown(1)
				return m, nil
			case tea.KeyPgUp:
				m.profileViewport.HalfViewUp()
				return m, nil
			case tea.KeyPgDown:
				m.profileViewport.HalfViewDown()
				return m, nil
			case tea.KeyHome:
				m.profileViewport.GotoTop()
				return m, nil
			case tea.KeyEnd:
				m.profileViewport.GotoBottom()
				return m, nil
			default:
				if msg.String() == "q" {
					m.showProfileModal = false
					if !m.isBlurred {
						m.textarea.Focus()
					}
					return m, nil
				}
				if msg.String() == "j" {
					m.profileViewport.LineDown(1)
					return m, nil
				}
				if msg.String() == "k" {
					m.profileViewport.LineUp(1)
					return m, nil
				}
				return m, nil
			}
		}

		if m.showMCPModal {
			switch msg.Type {
			case tea.KeyCtrlC:
				return m, tea.Quit
			case tea.KeyCtrlD:
				m.mcpViewport.HalfViewDown()
				return m, nil
			case tea.KeyCtrlU:
				m.mcpViewport.HalfViewUp()
				return m, nil
			case tea.KeyEsc:
				m.showMCPModal = false
				if !m.isBlurred {
					m.textarea.Focus()
				}
				return m, nil
			case tea.KeyUp:
				m.mcpViewport.LineUp(1)
				return m, nil
			case tea.KeyDown:
				m.mcpViewport.LineDown(1)
				return m, nil
			case tea.KeyPgUp:
				m.mcpViewport.HalfViewUp()
				return m, nil
			case tea.KeyPgDown:
				m.mcpViewport.HalfViewDown()
				return m, nil
			case tea.KeyHome:
				m.mcpViewport.GotoTop()
				return m, nil
			case tea.KeyEnd:
				m.mcpViewport.GotoBottom()
				return m, nil
			default:
				if msg.String() == "q" {
					m.showMCPModal = false
					if !m.isBlurred {
						m.textarea.Focus()
					}
					return m, nil
				}
				if msg.String() == "j" {
					m.mcpViewport.LineDown(1)
					return m, nil
				}
				if msg.String() == "k" {
					m.mcpViewport.LineUp(1)
					return m, nil
				}
				return m, nil
			}
		}

		if m.showShortcutsModal {
			switch msg.Type {
			case tea.KeyCtrlC:
				return m, tea.Quit
			case tea.KeyCtrlD:
				m.shortcutsViewport.HalfViewDown()
				return m, nil
			case tea.KeyCtrlU:
				m.shortcutsViewport.HalfViewUp()
				return m, nil
			case tea.KeyEsc, tea.KeyCtrlK:
				m.showShortcutsModal = false
				if !m.isBlurred {
					m.textarea.Focus()
				}
				return m, nil
			case tea.KeyUp:
				m.shortcutsViewport.LineUp(1)
				return m, nil
			case tea.KeyDown:
				m.shortcutsViewport.LineDown(1)
				return m, nil
			case tea.KeyPgUp:
				m.shortcutsViewport.HalfViewUp()
				return m, nil
			case tea.KeyPgDown:
				m.shortcutsViewport.HalfViewDown()
				return m, nil
			case tea.KeyHome:
				m.shortcutsViewport.GotoTop()
				return m, nil
			case tea.KeyEnd:
				m.shortcutsViewport.GotoBottom()
				return m, nil
			default:
				if msg.String() == "q" || msg.String() == "?" {
					m.showShortcutsModal = false
					if !m.isBlurred {
						m.textarea.Focus()
					}
					return m, nil
				}
				if msg.String() == "j" {
					m.shortcutsViewport.LineDown(1)
					return m, nil
				}
				if msg.String() == "k" {
					m.shortcutsViewport.LineUp(1)
					return m, nil
				}
				return m, nil
			}
		}

		if m.showTreeModal {
			switch msg.Type {
			case tea.KeyCtrlC:
				return m, tea.Quit
			case tea.KeyCtrlD:
				m.treeViewport.HalfViewDown()
				return m, nil
			case tea.KeyCtrlU:
				m.treeViewport.HalfViewUp()
				return m, nil
			case tea.KeyEsc, tea.KeyCtrlT:
				m.showTreeModal = false
				if !m.isBlurred {
					m.textarea.Focus()
				}
				return m, nil
			case tea.KeyUp:
				m.treeViewport.LineUp(1)
				return m, nil
			case tea.KeyDown:
				m.treeViewport.LineDown(1)
				return m, nil
			case tea.KeyPgUp:
				m.treeViewport.HalfViewUp()
				return m, nil
			case tea.KeyPgDown:
				m.treeViewport.HalfViewDown()
				return m, nil
			case tea.KeyHome:
				m.treeViewport.GotoTop()
				return m, nil
			case tea.KeyEnd:
				m.treeViewport.GotoBottom()
				return m, nil
			default:
				if msg.String() == "q" {
					m.showTreeModal = false
					if !m.isBlurred {
						m.textarea.Focus()
					}
					return m, nil
				}
				if msg.String() == "j" {
					m.treeViewport.LineDown(1)
					return m, nil
				}
				if msg.String() == "k" {
					m.treeViewport.LineUp(1)
					return m, nil
				}
				return m, nil
			}
		}

		// Non-modal scrolling shortcuts for conversation viewport
		if msg.String() == "ctrl+up" || msg.String() == "shift+up" {
			m.viewport.LineUp(2)
			return m, nil
		}
		if msg.String() == "ctrl+down" || msg.String() == "shift+down" {
			m.viewport.LineDown(2)
			return m, nil
		}
		if msg.Type == tea.KeyPgUp {
			m.viewport.HalfViewUp()
			return m, nil
		}
		if msg.Type == tea.KeyPgDown {
			m.viewport.HalfViewDown()
			return m, nil
		}
		if msg.String() == "ctrl+o" {
			wasAtBottom := m.viewport.AtBottom()
			m.expandTools = !m.expandTools
			if m.expandTools {
				m.statusMessage = "Expanded tool calls"
			} else {
				m.statusMessage = "Collapsed tool calls"
			}
			m.statusMessageAt = time.Now()
			m.renderConversationToViewport()
			if wasAtBottom {
				m.viewport.GotoBottom()
			}
			return m, nil
		}

		switch msg.Type {
		case tea.KeyCtrlD:
			// 1. If LLM turn is running: do not exit on Ctrl+D (turn interruption is reserved for Ctrl+C)
			if m.isLoading {
				return m, nil
			}

			// 2. If textarea is blurred (navigation mode), scroll conversation viewport down half a page
			if m.isBlurred {
				m.viewport.HalfViewDown()
				return m, nil
			}

			// 3. If textarea has text: forward to textarea to perform standard forward-delete
			if len(m.textarea.Value()) > 0 {
				var tiCmd tea.Cmd
				m.textarea, tiCmd = m.textarea.Update(msg)
				return m, tiCmd
			}

			// 4. Prompt is empty and idle: exit with EOF
			return m, tea.Quit

		case tea.KeyCtrlU:
			// If textarea is blurred (navigation mode), scroll conversation viewport up half a page
			if m.isBlurred {
				m.viewport.HalfViewUp()
				return m, nil
			}

		case tea.KeyCtrlC:
			// 1. If LLM turn is running: interrupt it
			if m.isLoading {
				if m.cancelTurn != nil {
					m.cancelTurn()
					m.cancelTurn = nil
				}
				m.isLoading = false
				m.streamingReply = ""
				m.entries = append(m.entries, ChatEntry{
					Type:    TypeSystem,
					Content: "^C (turn interrupted)",
				})
				m.renderConversationToViewport()
				m.viewport.GotoBottom()
				return m, nil
			}

			// 2. If input line has text: clear it
			if len(m.textarea.Value()) > 0 {
				m.textarea.Reset()
				m.currentDraft = ""
				m.historyIndex = len(m.inputHistory)
				return m, nil
			}

			// 3. Line is already clear: exit
			return m, tea.Quit

		case tea.KeyCtrlT:
			// Toggle memory tree modal
			m.textarea.Blur()
			m.showTreeModal = true
			m.treeContent = m.generateTreeVisual()
			m.treeViewport.SetContent(m.treeContent)
			m.treeViewport.GotoTop()
			return m, nil

		case tea.KeyCtrlK:
			// Toggle shortcuts cheatsheet modal
			m.textarea.Blur()
			m.showShortcutsModal = true
			m.shortcutsViewport.SetContent(m.generateShortcutsVisual())
			m.shortcutsViewport.GotoTop()
			return m, nil

		case tea.KeyCtrlO:
			// Toggle tool execution cards expanded vs collapsed
			wasAtBottom := m.viewport.AtBottom()
			m.expandTools = !m.expandTools
			if m.expandTools {
				m.statusMessage = "Expanded tool calls"
			} else {
				m.statusMessage = "Collapsed tool calls"
			}
			m.statusMessageAt = time.Now()
			m.renderConversationToViewport()
			if wasAtBottom {
				m.viewport.GotoBottom()
			}
			return m, nil

		case tea.KeyPgUp:
			m.viewport.HalfViewUp()
			return m, nil

		case tea.KeyPgDown:
			m.viewport.HalfViewDown()
			return m, nil

		case tea.KeyUp:
			// Cycle backwards through prompt history when at top line
			if m.textarea.Line() == 0 && len(m.inputHistory) > 0 {
				if m.historyIndex == len(m.inputHistory) {
					m.currentDraft = m.textarea.Value()
				}
				if m.historyIndex > 0 {
					m.historyIndex--
					m.textarea.SetValue(m.inputHistory[m.historyIndex])
					m.textarea.CursorEnd()
					return m, nil
				}
			}

		case tea.KeyDown:
			// Cycle forward through prompt history when at bottom line
			if (m.textarea.Line() >= m.textarea.LineCount()-1 || m.textarea.Line() == 0) && len(m.inputHistory) > 0 {
				if m.historyIndex < len(m.inputHistory)-1 {
					m.historyIndex++
					m.textarea.SetValue(m.inputHistory[m.historyIndex])
					m.textarea.CursorEnd()
					return m, nil
				} else if m.historyIndex == len(m.inputHistory)-1 {
					m.historyIndex = len(m.inputHistory)
					m.textarea.SetValue(m.currentDraft)
					m.textarea.CursorEnd()
					return m, nil
				}
			}

		case tea.KeyEnter:
			if m.isLoading {
				return m, nil
			}

			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				m.textarea.Reset()
				return m, nil
			}

			// Record into prompt history if non-empty and not identical to last entry
			if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != input {
				m.inputHistory = append(m.inputHistory, input)
			}
			m.historyIndex = len(m.inputHistory)
			m.currentDraft = ""

			// Handle slash commands
			if strings.HasPrefix(input, "/") {
				m.textarea.Reset()
				return m.handleSlashCommand(input)
			}

			// Normal turn submission
			m.textarea.Reset()
			m.isLoading = true
			m.streamingReply = ""

			m.entries = append(m.entries, ChatEntry{
				Type:    TypeUser,
				Content: input,
			})
			m.renderConversationToViewport()
			m.viewport.GotoBottom()

			ctx, cancel := context.WithCancel(context.Background())
			m.cancelTurn = cancel

			turnCmd := m.runAgentTurnCmd(ctx, input)

			return m, tea.Batch(
				turnCmd,
				m.spinner.Tick,
			)
		}

		m.textarea, tiCmd = m.textarea.Update(msg)

	case agentChunkMsg:
		m.streamingReply += string(msg)
		m.renderConversationToViewport()
		m.viewport.GotoBottom()

	case toolStartMsg:
		m.entries = append(m.entries, ChatEntry{
			Type:      TypeTool,
			ToolName:  msg.toolName,
			Command:   msg.command,
			IsRunning: true,
		})
		m.renderConversationToViewport()
		m.viewport.GotoBottom()

	case toolDoneMsg:
		// Update latest tool entry
		for i := len(m.entries) - 1; i >= 0; i-- {
			if m.entries[i].Type == TypeTool && m.entries[i].ToolName == msg.toolName && m.entries[i].IsRunning {
				m.entries[i].IsRunning = false
				m.entries[i].Content = msg.output
				m.entries[i].ExitCode = msg.exitCode
				break
			}
		}
		m.renderConversationToViewport()
		m.viewport.GotoBottom()

	case subagentStartMsg:
		m.entries = append(m.entries, ChatEntry{
			Type:           TypeSubagent,
			SubagentRole:   msg.role,
			SubagentModel:  msg.model,
			SubagentPrompt: msg.prompt,
			IsRunning:      true,
		})
		m.renderConversationToViewport()
		m.viewport.GotoBottom()

	case subagentDoneMsg:
		for i := len(m.entries) - 1; i >= 0; i-- {
			if m.entries[i].Type == TypeSubagent && m.entries[i].SubagentRole == msg.role && m.entries[i].IsRunning {
				m.entries[i].IsRunning = false
				if msg.err != nil {
					m.entries[i].Content = fmt.Sprintf("Error: %v", msg.err)
					m.entries[i].ExitCode = 1
				} else {
					m.entries[i].Content = msg.result
					m.entries[i].ExitCode = 0
				}
				break
			}
		}
		m.renderConversationToViewport()
		m.viewport.GotoBottom()

	case agentDoneMsg:
		if !m.isLoading {
			// Already canceled or interrupted by Ctrl+C
			return m, nil
		}
		m.isLoading = false
		m.cancelTurn = nil
		m.streamChan = nil
		reply := msg.reply
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				reply = "^C (turn interrupted)"
			} else {
				reply = fmt.Sprintf("Error: %v", msg.err)
			}
		}

		m.entries = append(m.entries, ChatEntry{
			Type:    TypeAgent,
			Content: reply,
		})
		m.streamingReply = ""
		m.renderConversationToViewport()
		m.viewport.GotoBottom()

		return m, m.updateMemoryStatsCmd()

	case streamMsg:
		// Forward any stream message (agentChunkMsg, toolStartMsg, toolDoneMsg, agentDoneMsg)
		// into the standard Update message handler, and keep listening on streamChan if open.
		innerModel, innerCmd := m.Update(msg.Payload)
		var nextListenCmd tea.Cmd
		if innerM, ok := innerModel.(Model); ok {
			if innerM.streamChan != nil && innerM.isLoading {
				nextListenCmd = listenStreamCmd(innerM.streamChan)
			}
		} else if m.streamChan != nil && m.isLoading {
			nextListenCmd = listenStreamCmd(m.streamChan)
		}
		return innerModel, tea.Batch(innerCmd, nextListenCmd)

	case tea.FocusMsg:
		m.isBlurred = false
		if !m.showTreeModal && !m.showShortcutsModal && !m.showMCPModal && !m.showProfileModal {
			tiCmd = m.textarea.Focus()
		}

	case tea.BlurMsg:
		m.isBlurred = true
		m.textarea.Blur()

	case memoryStatsMsg:
		m.memoryStats = msg

	default:
		m.textarea, tiCmd = m.textarea.Update(msg)
	}

	return m, tea.Batch(tiCmd, vpCmd, spCmd)
}

// View renders the complete TUI screen.
func (m Model) View() string {
	if !m.ready {
		return "Initializing Golem TUI..."
	}

	if m.showProfileModal {
		return m.renderProfileModalView()
	}

	if m.showShortcutsModal {
		return m.renderShortcutsModalView()
	}

	if m.showMCPModal {
		return m.renderMCPModalView()
	}

	if m.showTreeModal {
		return m.renderTreeModalView()
	}

	// 1. Header
	title := HeaderTitleStyle.Render("GOLEM")
	profStr := m.currentProfile
	if profStr == "" {
		profStr = "default"
	}
	provStr := "gemini"
	modelStr := "gemini-flash-latest"
	if m.agent != nil {
		if m.agent.Config().Provider != "" {
			provStr = m.agent.Config().Provider
		}
		if m.agent.Config().ModelName != "" {
			modelStr = m.agent.Config().ModelName
		}
	}
	sub := HeaderSubStyle.Render(fmt.Sprintf(" [%s | %s:%s] • Fractal Memory", profStr, provStr, modelStr))
	minimap := RenderMemoryMinimap(m.memoryStats.totalEvents, m.memoryStats.tailLevels, m.memoryStats.liveCount, false)

	header := lipgloss.JoinVertical(
		lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Center, title, sub),
		StatusBarMemoryStyle.Render(minimap),
	)

	// 2. Viewport
	body := m.viewport.View()

	// 3. Footer & Input Area
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Render(m.textarea.View())

	var statusLine string
	if m.isLoading {
		statusLine = fmt.Sprintf("%s Golem is thinking... %s", m.spinner.View(), lipgloss.NewStyle().Foreground(ColorMuted).Render("[Ctrl+C: Interrupt • Ctrl+O: Tools]"))
	} else if m.statusMessage != "" && time.Since(m.statusMessageAt) < 5*time.Second {
		statusLine = lipgloss.NewStyle().Foreground(ColorSuccess).Render(m.statusMessage)
	} else {
		val := m.textarea.Value()
		if strings.HasPrefix(val, "/") {
			statusLine = m.renderSlashCommandHelp(val)
		} else {
			statusLine = StatusBarKeyHintStyle.Render("[Enter: Send • ↑/↓: History • Ctrl+O: Tools • Ctrl+K: Shortcuts • Ctrl+C: Clear • Ctrl+D: Exit]")
		}
	}

	footer := lipgloss.JoinVertical(
		lipgloss.Left,
		inputBox,
		statusLine,
	)

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		body,
		footer,
	)
}

// renderConversationToViewport re-renders formatted conversation history into the viewport.
func (m *Model) renderConversationToViewport() {
	var sb strings.Builder

	if m.startupHeader != "" {
		sb.WriteString(HeaderTitleStyle.Render(m.startupHeader) + "\n\n")
	}

	for i := 0; i < len(m.entries); i++ {
		entry := m.entries[i]
		switch entry.Type {
		case TypeUser:
			sb.WriteString(UserBadgeStyle.Render("YOU") + "\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(ColorHighlight).Render(entry.Content) + "\n\n")

		case TypeAgent:
			sb.WriteString(AgentBadgeStyle.Render("GOLEM") + "\n")
			if m.renderer != nil {
				rendered, err := m.renderer.Render(entry.Content)
				if err == nil {
					sb.WriteString(rendered)
				} else {
					sb.WriteString(entry.Content + "\n")
				}
			} else {
				sb.WriteString(entry.Content + "\n")
			}
			sb.WriteString("\n")

		case TypeTool:
			if m.expandTools {
				card := RenderToolCard(entry.ToolName, entry.Command, entry.Content, entry.ExitCode, entry.IsRunning, m.viewport.Width)
				sb.WriteString(card + "\n")
			} else {
				// Group contiguous tool calls into a single compact line
				toolGroup := []ChatEntry{entry}
				for i+1 < len(m.entries) && m.entries[i+1].Type == TypeTool {
					i++
					toolGroup = append(toolGroup, m.entries[i])
				}
				summary := RenderCollapsedToolSummary(toolGroup)
				if summary != "" {
					sb.WriteString(summary + "\n\n")
				}
			}

		case TypeSubagent:
			card := RenderSubagentCard(entry.SubagentRole, entry.SubagentModel, entry.SubagentPrompt, entry.Content, entry.IsRunning, m.viewport.Width)
			sb.WriteString(card + "\n")

		case TypeSystem:
			sb.WriteString(lipgloss.NewStyle().Foreground(ColorWarning).Italic(true).Render(entry.Content) + "\n\n")
		}
	}

	// If streaming reply is currently active
	if len(m.streamingReply) > 0 {
		sb.WriteString(AgentBadgeStyle.Render("GOLEM") + " " + m.spinner.View() + "\n")
		sb.WriteString(m.streamingReply + "\n")
	}

	m.viewport.SetContent(sb.String())
}

// tuiStreamBridge adapts golem.StreamHandler into a channel of Bubbletea messages.
type tuiStreamBridge struct {
	ctx     context.Context
	msgChan chan tea.Msg
}

func (b *tuiStreamBridge) send(msg tea.Msg) {
	if b.msgChan == nil {
		return
	}
	if b.ctx != nil {
		select {
		case <-b.ctx.Done():
			return
		case b.msgChan <- msg:
		}
	} else {
		select {
		case b.msgChan <- msg:
		default:
		}
	}
}

func (b *tuiStreamBridge) OnChunk(token string) {
	b.send(agentChunkMsg(token))
}

func (b *tuiStreamBridge) OnToolStart(toolName, command string) {
	b.send(toolStartMsg{
		toolName: toolName,
		command:  command,
	})
}

func (b *tuiStreamBridge) OnToolDone(toolName, output string, exitCode int) {
	b.send(toolDoneMsg{
		toolName: toolName,
		output:   output,
		exitCode: exitCode,
	})
}

func (b *tuiStreamBridge) OnSubagentStart(role, model, prompt string) {
	b.send(subagentStartMsg{
		role:   role,
		model:  model,
		prompt: prompt,
	})
}

func (b *tuiStreamBridge) OnSubagentTool(role, model, toolName, command, output string, exitCode int, isRunning bool) {
	if isRunning {
		b.send(toolStartMsg{
			toolName: fmt.Sprintf("[%s] %s", role, toolName),
			command:  command,
		})
	} else {
		b.send(toolDoneMsg{
			toolName: fmt.Sprintf("[%s] %s", role, toolName),
			output:   output,
			exitCode: exitCode,
		})
	}
}

func (b *tuiStreamBridge) OnSubagentDone(role, model, result string, duration time.Duration, err error) {
	b.send(subagentDoneMsg{
		role:     role,
		model:    model,
		result:   result,
		duration: duration,
		err:      err,
	})
}

// listenStreamCmd continuously reads from the stream message channel and returns streamMsg.
func listenStreamCmd(msgChan <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-msgChan
		if !ok {
			return nil
		}
		return streamMsg{Payload: msg}
	}
}

// runAgentTurnCmd launches an agent turn in a background goroutine with streaming.
func (m *Model) runAgentTurnCmd(ctx context.Context, input string) tea.Cmd {
	msgChan := make(chan tea.Msg, 2048)
	m.streamChan = msgChan
	bridge := &tuiStreamBridge{
		ctx:     ctx,
		msgChan: msgChan,
	}

	// Launch agent stream processing in background goroutine
	go func() {
		defer close(msgChan)
		defer func() {
			if r := recover(); r != nil {
				select {
				case <-ctx.Done():
				case msgChan <- agentDoneMsg{
					err: fmt.Errorf("agent internal error: %v", r),
				}:
				}
			}
		}()

		reply, err := m.agent.ProcessStream(ctx, input, bridge)
		select {
		case <-ctx.Done():
		case msgChan <- agentDoneMsg{
			reply: reply,
			err:   err,
		}:
		}
	}()

	return listenStreamCmd(msgChan)
}

// updateMemoryStatsCmd calculates current memory hierarchy stats for the minimap.
func (m *Model) updateMemoryStatsCmd() tea.Cmd {
	return func() tea.Msg {
		if m.store == nil || m.viewEngine == nil {
			return memoryStatsMsg{}
		}

		total := m.store.CountEvents()
		liveHead := m.viewEngine.LiveHeadEvents()
		liveCount := len(liveHead)

		levels := make(map[int]int)
		tailEnd := total - liveCount
		if tailEnd < 0 {
			tailEnd = 0
		}

		// Count nodes per level in tail
		for lvl := 0; lvl <= 10; lvl++ {
			step := 1 << lvl
			if step > tailEnd {
				break
			}
			count := 0
			maxIdx := tailEnd / step
			for idx := 0; idx < maxIdx; idx++ {
				if _, ok := m.store.GetNode(lvl, idx); ok {
					count++
				}
			}
			if count > 0 {
				levels[lvl] = count
			}
		}

		return memoryStatsMsg{
			totalEvents: total,
			tailLevels:  levels,
			liveCount:   liveCount,
		}
	}
}

// generateTreeVisual creates an ASCII tree diagram of stored fractal memory nodes.
func (m *Model) generateTreeVisual() string {
	if m.store == nil {
		return "No memory store initialized."
	}

	var sb strings.Builder
	sb.WriteString("=== FRACTAL MEMORY TREE (OptChat Hierarchy) ===\n\n")

	total := m.store.CountEvents()
	sb.WriteString(fmt.Sprintf("Total Events: %d | Live Head Window: %d\n\n", total, len(m.viewEngine.LiveHeadEvents())))

	// Scan through levels
	for lvl := 4; lvl >= 0; lvl-- {
		step := 1 << lvl
		if step > total && lvl > 0 {
			continue
		}
		sb.WriteString(TreeNodeLevelStyle.Render(fmt.Sprintf("Level %d (covers %d msgs/node):", lvl, step)) + "\n")
		count := 0
		maxIdx := total / step
		if maxIdx == 0 && total > 0 {
			maxIdx = 1
		}
		for idx := 0; idx < maxIdx && idx < 8; idx++ {
			if node, ok := m.store.GetNode(lvl, idx); ok {
				count++
				addr := node.Address()
				cleanText := strings.ReplaceAll(node.Text, "\n", " ")
				if len(cleanText) > 70 {
					cleanText = cleanText[:70] + "..."
				}
				sb.WriteString(fmt.Sprintf("  ├─ %s | %s\n", TreeNodeAddrStyle.Render(addr), cleanText))
			}
		}
		if count == 0 {
			sb.WriteString("  └─ (no nodes)\n")
		}
		sb.WriteString("\n")
	}

	// Live Head preview
	sb.WriteString(lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true).Render("Live Head (Verbatim Uncompressed Window):") + "\n")
	liveEvents := m.viewEngine.LiveHeadEvents()
	if len(liveEvents) == 0 {
		sb.WriteString("  └─ (empty live head)\n")
	} else {
		for i, ev := range liveEvents {
			prefix := "├─"
			if i == len(liveEvents)-1 {
				prefix = "└─"
			}
			badge := string(ev.Kind)
			clean := strings.ReplaceAll(ev.Text, "\n", " ")
			if len(clean) > 65 {
				clean = clean[:65] + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s #%-3d [%-4s] %s\n", prefix, ev.ID, badge, clean))
		}
	}
	sb.WriteString("\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(ColorMuted).Render("Tip: Use /zoom <id> <n> to inspect any node in full detail.") + "\n")

	return sb.String()
}

// renderSlashCommandHelp generates dynamic status-bar assistance as the user types slash commands.
func (m Model) renderSlashCommandHelp(input string) string {
	type cmdHelp struct {
		cmd  string
		desc string
	}
	commands := []cmdHelp{
		{"/profile", "Inspect configured profiles or switch active profile"},
		{"/remember", "Record preference or decision into profile memory"},
		{"/tree", "Inspect fractal memory hierarchy"},
		{"/zoom", "Inspect memory node (/zoom [target] <id> <n>)"},
		{"/compact", "Trigger memory compaction fold"},
		{"/clear", "Clear chat viewport (disk memory preserved)"},
		{"/tools", "Toggle expanded/collapsed tool call cards (Ctrl+O)"},
		{"/mcp", "Inspect active MCP servers, tools & configs"},
		{"/shortcuts", "Show keyboard shortcuts & controls cheat sheet"},
		{"/help", "Show full command reference"},
		{"/exit", "Exit Golem"},
	}

	cleanInput := strings.ToLower(strings.TrimSpace(input))
	var matches []string
	for _, c := range commands {
		cmdName := strings.Split(c.cmd, " ")[0]
		if strings.HasPrefix(cmdName, cleanInput) || strings.HasPrefix(c.cmd, cleanInput) {
			matches = append(matches, fmt.Sprintf("%s %s",
				lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true).Render(c.cmd),
				lipgloss.NewStyle().Foreground(ColorMuted).Render("("+c.desc+")"),
			))
		}
	}

	if len(matches) == 0 {
		return lipgloss.NewStyle().Foreground(ColorWarning).Render(fmt.Sprintf("Unknown command: %s (type /help for options)", input))
	}
	return lipgloss.NewStyle().Foreground(ColorSecondary).Render("Palette: ") + strings.Join(matches, "  •  ")
}

// renderTreeModalView renders the overlay modal when Ctrl+T or /tree is active.
func (m Model) renderTreeModalView() string {
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(m.width - 6).
		Height(m.height - 4)

	footerHint := StatusBarKeyHintStyle.Render("\n[↑/↓: Scroll • PgUp/PgDn: Page • Ctrl+T/Esc/q: Back to Chat]")
	return modalStyle.Render(m.treeViewport.View() + footerHint)
}

// renderShortcutsModalView renders the overlay popup when Ctrl+K or /shortcuts is active.
func (m Model) renderShortcutsModalView() string {
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorSecondary).
		Padding(1, 2).
		Width(m.width - 6).
		Height(m.height - 4)

	footerHint := StatusBarKeyHintStyle.Render("\n[↑/↓: Scroll • PgUp/PgDn: Page • Ctrl+K/Esc/q/?: Back to Chat]")
	return modalStyle.Render(m.shortcutsViewport.View() + footerHint)
}

// renderMCPModalView renders the overlay popup when /mcp is active.
func (m Model) renderMCPModalView() string {
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(m.width - 6).
		Height(m.height - 4)

	footerHint := StatusBarKeyHintStyle.Render("\n[↑/↓: Scroll • PgUp/PgDn: Page • Esc/q: Back to Chat]")
	return modalStyle.Render(m.mcpViewport.View() + footerHint)
}

// generateShortcutsVisual builds a formatted keyboard shortcuts reference cheat sheet.
func (m Model) generateShortcutsVisual() string {
	var sb strings.Builder

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight).
		Background(ColorSecondary).
		Padding(0, 1)

	headerSection := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary).
		MarginTop(1)

	keyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorWarning)

	descStyle := lipgloss.NewStyle().
		Foreground(ColorHighlight)

	mutedStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)

	sb.WriteString(titleStyle.Render("GOLEM KEYBOARD SHORTCUTS & CONTROLS") + "\n\n")

	type shortcutItem struct {
		keys string
		desc string
		note string
	}

	type section struct {
		title string
		items []shortcutItem
	}

	sections := []section{
		{
			title: "Prompt Input & Turn Execution",
			items: []shortcutItem{
				{"Enter", "Send prompt", "Submits active input line to the agent"},
				{"↑ (Up)", "Previous prompt", "Cycle backwards through prompt history"},
				{"↓ (Down)", "Next prompt", "Cycle forward through prompt history / draft"},
				{"Ctrl+C", "Clear line / Interrupt", "Interrupts running turn; if idle, clears line; if line empty, exits"},
				{"Ctrl+D", "Exit / Forward Delete", "Exits when prompt is empty & idle; forward-deletes char when typing"},
				{"Ctrl+O", "Toggle tool cards", "Expand / collapse tool call cards in chat (collapsed by default)"},
			},
		},
		{
			title: "Viewport & Chat Scrolling",
			items: []shortcutItem{
				{"Ctrl+↑ / Shift+↑", "Scroll up", "Scrolls conversation viewport up by 2 lines"},
				{"Ctrl+↓ / Shift+↓", "Scroll down", "Scrolls conversation viewport down by 2 lines"},
				{"PgUp / Ctrl+U", "Half page up", "Scrolls conversation viewport up by half a page (when blurred)"},
				{"PgDn / Ctrl+D", "Half page down", "Scrolls conversation viewport down by half a page (when blurred)"},
			},
		},
		{
			title: "Inspectors & Popups",
			items: []shortcutItem{
				{"Ctrl+K", "Shortcuts popup", "Open / close this shortcuts and controls guide"},
				{"Ctrl+T", "Fractal memory tree", "Open / close the OptChat hierarchical tree modal"},
				{"/profile", "Profile manager", "Inspect active & configured provider profiles"},
				{"/mcp", "MCP server status", "Open active MCP servers and configuration inspector"},
			},
		},
		{
			title: "Modal Navigation (Tree, MCP, Profile & Shortcuts)",
			items: []shortcutItem{
				{"↑ / k", "Scroll up 1 line", "Navigate modal contents"},
				{"↓ / j", "Scroll down 1 line", "Navigate modal contents"},
				{"PgUp / PgDn / Ctrl+D / Ctrl+U", "Half page scroll", "Page through modal contents (Vim / pager)"},
				{"Home / End", "Jump top / bottom", "Go directly to top or bottom of modal"},
				{"Esc / q", "Close modal", "Return to main chat interface"},
			},
		},
		{
			title: "Slash Commands (type in prompt)",
			items: []shortcutItem{
				{"/profile [name]", "Switch/view profile", "Hot-switch active provider profile or view modal"},
				{"/remember <text>", "Record memory", "Persist key ruling/decision into profile memory"},
				{"/tree, /memory", "View memory tree", "Inspect binary hierarchy and live head"},
				{"/zoom [t] id n", "Inspect node", "Full text inspect (target optional, n=1 for verbatim)"},
				{"/compact", "Compact memory", "Manually trigger a background compaction pass"},
				{"/clear", "Clear chat view", "Clear viewport without deleting stored history"},
				{"/mcp", "MCP status popup", "Inspect connected MCP servers, tools, and configs"},
				{"/shortcuts", "Shortcuts popup", "Open this keyboard shortcuts modal"},
				{"/help", "Command reference", "Print slash command descriptions in chat"},
				{"/exit, /quit", "Exit application", "Cleanly shutdown Golem"},
			},
		},
	}

	for _, sec := range sections {
		sb.WriteString(headerSection.Render("■ "+sec.title) + "\n")
		for _, item := range sec.items {
			line := fmt.Sprintf("  %-18s  %-24s", keyStyle.Render(item.keys), descStyle.Render(item.desc))
			if item.note != "" {
				line += "  " + mutedStyle.Render("• "+item.note)
			}
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// generateMCPVisual creates a formatted status summary of connected and configured MCP servers.
func (m *Model) generateMCPVisual() string {
	var sb strings.Builder

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight).
		Background(ColorPrimary).
		Padding(0, 1)

	headerSection := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary).
		MarginTop(1)

	sb.WriteString(titleStyle.Render("MODEL CONTEXT PROTOCOL (MCP) SERVERS") + "\n\n")

	// 1. Discovered configuration files
	sb.WriteString(headerSection.Render("■ Discovered Configurations") + "\n")
	var configPaths []string
	if m.agent != nil {
		configPaths = m.agent.MCPConfigPaths()
	}
	if len(configPaths) == 0 {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(ColorMuted).Render("(no configuration files detected)") + "\n")
	} else {
		for _, p := range configPaths {
			sb.WriteString(fmt.Sprintf("  %s %s\n",
				lipgloss.NewStyle().Foreground(ColorSuccess).Render("✓"),
				lipgloss.NewStyle().Foreground(ColorHighlight).Render(p),
			))
		}
	}
	sb.WriteString("\n")

	// 2. Registered servers & tools
	sb.WriteString(headerSection.Render("■ Registered Servers & Tools") + "\n")
	if m.agent == nil || m.agent.MCPManager() == nil {
		sb.WriteString("  " + lipgloss.NewStyle().Foreground(ColorMuted).Render("(MCP manager not initialized)") + "\n")
	} else {
		mgr := m.agent.MCPManager()
		names := mgr.ServerNames()
		sort.Strings(names)

		if len(names) == 0 {
			sb.WriteString("  " + lipgloss.NewStyle().Foreground(ColorMuted).Render("No MCP servers configured.") + "\n")
		} else {
			for _, name := range names {
				running, toolCount, isRemote, err := mgr.ServerStatus(name)
				cfg, _ := mgr.ServerConfig(name)

				var statusBadge string
				if err != nil {
					statusBadge = lipgloss.NewStyle().Foreground(ColorDanger).Bold(true).Render("✕ Error: " + err.Error())
				} else if isRemote {
					statusBadge = lipgloss.NewStyle().Foreground(ColorSecondary).Render("○ Remote (HTTP/SSE)")
				} else if running {
					statusBadge = lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true).Render(fmt.Sprintf("● Running (%d tools)", toolCount))
				} else {
					statusBadge = lipgloss.NewStyle().Foreground(ColorWarning).Render("◌ Starting / Idle")
				}

				srvNameStyled := lipgloss.NewStyle().Bold(true).Foreground(ColorHighlight).Render(name)
				sb.WriteString(fmt.Sprintf("  • %-20s %s\n", srvNameStyled, statusBadge))

				if cfg.Command != "" {
					cmdStr := cfg.Command
					if len(cfg.Args) > 0 {
						cmdStr += " " + strings.Join(cfg.Args, " ")
					}
					sb.WriteString("      " + lipgloss.NewStyle().Foreground(ColorMuted).Render("Command: "+cmdStr) + "\n")
				} else if cfg.URL != "" || cfg.ServerURL != "" {
					u := cfg.URL
					if u == "" {
						u = cfg.ServerURL
					}
					sb.WriteString("      " + lipgloss.NewStyle().Foreground(ColorMuted).Render("Endpoint: "+u) + "\n")
				}
				sb.WriteString("\n")
			}
		}
	}

	sb.WriteString(lipgloss.NewStyle().Foreground(ColorMuted).Render("Tip: MCP servers are loaded exclusively from .golem/mcp.json (workspace) and ~/.golem/mcp.json (user).") + "\n")
	return sb.String()
}

// renderProfileModalView renders the overlay popup when /profile is active.
func (m Model) renderProfileModalView() string {
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(m.width - 6).
		Height(m.height - 4)

	footerHint := StatusBarKeyHintStyle.Render("\n[↑/↓: Scroll • PgUp/PgDn: Page • Esc/q: Back to Chat]")
	return modalStyle.Render(m.profileViewport.View() + footerHint)
}

// generateProfileVisual formats a summary of available provider profiles.
func (m *Model) generateProfileVisual() string {
	var sb strings.Builder

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight).
		Background(ColorPrimary).
		Padding(0, 1)

	headerSection := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary).
		MarginTop(1)

	sb.WriteString(titleStyle.Render("GOLEM PROVIDER PROFILES") + "\n\n")
	sb.WriteString(headerSection.Render("■ Configured Profiles") + "\n")

	active := m.currentProfile
	if active == "" {
		active = "default"
	}

	var profiles map[string]config.ProfileConfig
	if m.agent != nil {
		profiles = m.agent.Config().FileConfig.Profiles
	}

	if len(profiles) == 0 {
		sb.WriteString(fmt.Sprintf("  • %s %s\n",
			lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess).Render("default"),
			lipgloss.NewStyle().Foreground(ColorMuted).Render("[ACTIVE]"),
		))
		if m.agent != nil {
			sb.WriteString(fmt.Sprintf("    Provider: %s | Model: %s\n",
				m.agent.Config().Provider, m.agent.Config().ModelName))
			sb.WriteString(fmt.Sprintf("    Memory:   %s\n", m.agent.Config().StorageDir))
		}
	} else {
		names := make([]string, 0, len(profiles))
		for n := range profiles {
			names = append(names, n)
		}
		sort.Strings(names)

		for _, name := range names {
			p := profiles[name]
			isActive := (name == active)
			badge := ""
			nameRender := lipgloss.NewStyle().Bold(true).Foreground(ColorHighlight).Render(name)
			if isActive {
				badge = " " + lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess).Render("[ACTIVE]")
				nameRender = lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess).Render("★ " + name)
			} else {
				nameRender = "• " + nameRender
			}

			sb.WriteString(fmt.Sprintf("  %s%s\n", nameRender, badge))

			provider := p.Provider
			if provider == "" {
				provider = "gemini"
			}
			model := p.Model
			if model == "" {
				model = "gemini-flash-latest"
			}
			sb.WriteString(fmt.Sprintf("    Provider: %s | Model: %s\n",
				lipgloss.NewStyle().Foreground(ColorPrimary).Render(provider),
				lipgloss.NewStyle().Foreground(ColorHighlight).Render(model),
			))
			if p.BaseURL != "" {
				sb.WriteString(fmt.Sprintf("    Endpoint: %s\n", p.BaseURL))
			}
			memDir := p.MemoryDir
			if memDir == "" {
				memDir, _ = config.ProfileMemoryDir(name)
			}
			sb.WriteString(fmt.Sprintf("    Memory:   %s\n", memDir))

			if len(p.MountedMemories) > 0 {
				sb.WriteString("    Mounted Histories:\n")
				for _, mm := range p.MountedMemories {
					sb.WriteString(fmt.Sprintf("      - %s (%s, read_only=%t)\n", mm.Name, mm.Path, mm.ReadOnly))
				}
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorMuted).Render("Commands: /profile <name> to switch profiles • /remember <text> to persist knowledge • q/Esc to close"))
	return sb.String()
}

// handleSlashCommand parses and executes slash palette commands.
func (m Model) handleSlashCommand(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/profile", "/profiles":
		if len(parts) == 1 {
			m.textarea.Blur()
			m.showProfileModal = true
			m.profileContent = m.generateProfileVisual()
			m.profileViewport.SetContent(m.profileContent)
			m.profileViewport.GotoTop()
			return m, nil
		}

		targetProfile := parts[1]
		if targetProfile == "switch" && len(parts) > 2 {
			targetProfile = parts[2]
		}

		m.currentProfile = targetProfile
		info := fmt.Sprintf("Switched active profile to: %s", targetProfile)
		if m.agent != nil {
			prof, _ := m.agent.SwitchProfile(context.Background(), targetProfile)
			if prof.Provider != "" || prof.Model != "" {
				info = fmt.Sprintf("Switched active profile to %s (provider: %s, model: %s)", targetProfile, prof.Provider, prof.Model)
			}
		}

		m.entries = append(m.entries, ChatEntry{
			Type:    TypeSystem,
			Content: info,
		})
		m.renderConversationToViewport()
		m.statusMessage = fmt.Sprintf("Profile: %s", targetProfile)
		m.statusMessageAt = time.Now()
		return m, nil

	case "/remember":
		if len(parts) < 2 {
			m.entries = append(m.entries, ChatEntry{
				Type:    TypeSystem,
				Content: "Usage: /remember <text> (records preference or decision into profile memory)",
			})
			m.renderConversationToViewport()
			return m, nil
		}

		remText := strings.TrimSpace(input[len(parts[0]):])
		var remMsg string
		if m.agent != nil && m.agent.MemoryManager() != nil {
			res, err := fractal.Remember(m.agent.MemoryManager(), m.currentProfile, fractal.RememberArgs{
				Text:  remText,
				Scope: m.currentProfile,
			})
			if err != nil {
				remMsg = fmt.Sprintf("Error remembering: %v", err)
			} else {
				remMsg = res.Message
			}
		} else {
			remMsg = fmt.Sprintf("Remembered: %s", remText)
		}

		m.entries = append(m.entries, ChatEntry{
			Type:    TypeSystem,
			Content: remMsg,
		})
		m.renderConversationToViewport()
		m.statusMessage = "Memory recorded"
		m.statusMessageAt = time.Now()
		return m, nil

	case "/mcp":
		m.textarea.Blur()
		m.showMCPModal = true
		m.mcpViewport.SetContent(m.generateMCPVisual())
		m.mcpViewport.GotoTop()
		return m, nil

	case "/tree", "/memory":
		m.textarea.Blur()
		m.showTreeModal = true
		m.treeContent = m.generateTreeVisual()
		m.treeViewport.SetContent(m.treeContent)
		m.treeViewport.GotoTop()
		return m, nil

	case "/compact":
		m.statusMessage = "Compacting memory tree..."
		m.statusMessageAt = time.Now()
		return m, func() tea.Msg {
			ctx := context.Background()
			n, err := m.agent.Compactor().Pump(ctx)
			if err != nil {
				return agentDoneMsg{reply: fmt.Sprintf("Compaction error: %v", err)}
			}
			return agentDoneMsg{reply: fmt.Sprintf("Compaction pass complete: %d nodes built/merged.", n)}
		}

	case "/clear":
		m.entries = []ChatEntry{}
		m.renderConversationToViewport()
		m.statusMessage = "Screen cleared (memory history preserved)."
		m.statusMessageAt = time.Now()
		return m, nil

	case "/zoom":
		if len(parts) < 3 {
			m.entries = append(m.entries, ChatEntry{
				Type:    TypeSystem,
				Content: "Usage: /zoom <id> <n> or /zoom <target> <id> <n> (e.g. /zoom 0 1 for verbatim message 0)",
			})
			m.renderConversationToViewport()
			return m, nil
		}

		var target string
		var id, n int
		if len(parts) >= 4 {
			target = parts[1]
			_, _ = fmt.Sscanf(parts[2], "%d", &id)
			_, _ = fmt.Sscanf(parts[3], "%d", &n)
		} else {
			_, _ = fmt.Sscanf(parts[1], "%d", &id)
			_, _ = fmt.Sscanf(parts[2], "%d", &n)
		}

		var res fractal.ZoomResult
		var err error
		if m.agent != nil && m.agent.MemoryManager() != nil {
			res, err = fractal.Zoom(m.agent.MemoryManager(), fractal.ZoomArgs{
				Target: target,
				ID:     id,
				N:      n,
			})
		} else if m.store != nil {
			res, err = fractal.Zoom(&fractal.SingleStoreRegistry{Store: m.store}, fractal.ZoomArgs{
				Target: target,
				ID:     id,
				N:      n,
			})
		}

		var text string
		if n < 1 || (n&(n-1)) != 0 || id < 0 || id%n != 0 {
			text = fmt.Sprintf("No node at %d+%d", id, n)
		} else if err != nil {
			text = fmt.Sprintf("Zoom error: %v", err)
		} else if res.Result != "" {
			if n == 1 && strings.HasPrefix(res.Result, fmt.Sprintf("%d+1|", id)) {
				raw := strings.TrimPrefix(res.Result, fmt.Sprintf("%d+1|", id))
				text = fmt.Sprintf("Verbatim %d: %s", id, raw)
			} else {
				text = res.Result
			}
		} else {
			text = fmt.Sprintf("No node at %d+%d", id, n)
		}

		m.entries = append(m.entries, ChatEntry{
			Type:    TypeSystem,
			Content: text,
		})
		m.renderConversationToViewport()
		return m, nil

	case "/tools", "/expand":
		wasAtBottom := m.viewport.AtBottom()
		m.expandTools = !m.expandTools
		if m.expandTools {
			m.statusMessage = "Expanded tool calls"
		} else {
			m.statusMessage = "Collapsed tool calls"
		}
		m.statusMessageAt = time.Now()
		m.renderConversationToViewport()
		if wasAtBottom {
			m.viewport.GotoBottom()
		}
		return m, nil

	case "/shortcuts", "/keys":
		m.textarea.Blur()
		m.showShortcutsModal = true
		m.shortcutsViewport.SetContent(m.generateShortcutsVisual())
		m.shortcutsViewport.GotoTop()
		return m, nil

	case "/help":
		helpText := `Available Slash Commands:
/profile [name] - View configured profiles or switch active profile
/remember <text>- Record key preference or decision into profile memory
/tree, /memory  - Visualize the fractal memory binary tree hierarchy
/zoom [t] id n  - Inspect memory node or raw message (n=1 for verbatim)
/compact        - Trigger an immediate background compaction pass
/clear          - Clear viewport chat history (disk storage untouched)
/tools          - Toggle expanded/collapsed tool call cards (or Ctrl+O)
/mcp            - Inspect active MCP servers, tools count, and configs
/shortcuts      - Display interactive keyboard shortcuts cheat sheet
/help           - Display this help message
/exit, /quit    - Exit Golem`
		m.entries = append(m.entries, ChatEntry{
			Type:    TypeSystem,
			Content: helpText,
		})
		m.renderConversationToViewport()
		return m, nil

	case "/exit", "/quit":
		return m, tea.Quit

	default:
		m.entries = append(m.entries, ChatEntry{
			Type:    TypeSystem,
			Content: fmt.Sprintf("Unknown command: %s. Type /help for available commands.", cmd),
		})
		m.renderConversationToViewport()
		return m, nil
	}
}
