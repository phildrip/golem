package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme colors.
//
// The palette is adaptive: every colour resolves differently for light and dark
// terminal backgrounds so the UI stays readable either way, and the tones are
// muted rather than neon. Body text uses the semantic ColorHighlight tone
// instead of forcing a hard white, so Golem blends with the user's own theme.
var (
	ColorPrimary   lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#5E81AC", Dark: "#88C0D0"} // frost blue (accent)
	ColorSecondary lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#7C6A9C", Dark: "#B48EAD"} // muted purple
	ColorSuccess   lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#5F7E4E", Dark: "#A3BE8C"} // sage green
	ColorWarning   lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#9C7A2E", Dark: "#EBCB8B"} // sand amber
	ColorDanger    lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#A33A44", Dark: "#BF616A"} // muted red
	ColorMuted     lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#6A737D", Dark: "#7B8794"} // cool gray
	ColorHighlight lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#2E3440", Dark: "#E5E9F0"} // body text
	ColorSurface   lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#E7EAEE", Dark: "#3B4252"} // subtle fill
	ColorBorder    lipgloss.TerminalColor = lipgloss.AdaptiveColor{Light: "#C2C7D0", Dark: "#4C566A"} // border

	// ColorDarkBg is kept as a legacy alias for the subtle surface fill.
	ColorDarkBg lipgloss.TerminalColor = ColorSurface
)

// Component Styles
var (
	// Header & Titles
	HeaderTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorPrimary).
				Padding(0, 1)

	HeaderSubStyle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	// StartupHeaderStyle renders the replay header above the conversation.
	StartupHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(ColorPrimary)

	// ModalTitleStyle is the shared banner used by the inspector popups.
	ModalTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			Background(ColorSurface).
			Padding(0, 1)

	// Status & HUD
	StatusBarModelStyle = lipgloss.NewStyle().
				Foreground(ColorHighlight).
				Background(ColorSurface).
				Padding(0, 1)

	StatusBarMemoryStyle = lipgloss.NewStyle().
				Foreground(ColorMuted).
				Padding(0, 1)

	StatusBarKeyHintStyle = lipgloss.NewStyle().
				Foreground(ColorMuted)

	// User & Agent labels
	UserBadgeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorMuted)

	AgentBadgeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary)

	// Tool execution card styles
	ToolCardBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1).
			MarginTop(0).
			MarginBottom(0)

	ToolRunningBadge = lipgloss.NewStyle().
				Foreground(ColorWarning).
				Bold(true)

	ToolSuccessBadge = lipgloss.NewStyle().
				Foreground(ColorSuccess).
				Bold(true)

	ToolErrorBadge = lipgloss.NewStyle().
			Foreground(ColorDanger).
			Bold(true)

	// Memory tree visualization styles
	TreeNodeLevelStyle = lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true)

	TreeNodeAddrStyle = lipgloss.NewStyle().
				Foreground(ColorSecondary).
				Bold(true)

	TreeLiveHeadStyle = lipgloss.NewStyle().
				Foreground(ColorSuccess).
				Bold(true)
)

// toolBullet is the compact glyph that prefixes collapsed tool summary lines.
const toolBullet = "▸"

// RenderCollapsedToolSummary formats a collapsed group of tool calls into a single compact line.
func RenderCollapsedToolSummary(tools []ChatEntry) string {
	if len(tools) == 0 {
		return ""
	}

	expandHint := lipgloss.NewStyle().Foreground(ColorMuted).Render("• Ctrl+O to expand")

	if len(tools) == 1 {
		t := tools[0]
		cleanName := strings.TrimSpace(t.ToolName)
		if cleanName == "" {
			cleanName = "tool"
		}

		cmdStr := ""
		if t.Command != "" && t.Command != "null" && t.Command != "{}" {
			cmd := t.Command
			if len(cmd) > 50 {
				cmd = cmd[:47] + "..."
			}
			cmdStr = lipgloss.NewStyle().Foreground(ColorMuted).Render(fmt.Sprintf(" ($ %s)", cmd))
		}

		if t.IsRunning {
			return fmt.Sprintf("  %s Running tool: %s%s %s",
				toolBullet,
				lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(cleanName),
				cmdStr,
				ToolRunningBadge.Render("RUNNING"),
			)
		}

		var statusBadge string
		if t.ExitCode == 0 {
			statusBadge = ToolSuccessBadge.Render("EXIT 0")
		} else {
			statusBadge = ToolErrorBadge.Render(fmt.Sprintf("EXIT %d", t.ExitCode))
		}

		return fmt.Sprintf("  %s Tool: %s%s %s %s",
			toolBullet,
			lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(cleanName),
			cmdStr,
			statusBadge,
			expandHint,
		)
	}

	// Multiple tools in contiguous group
	errCount := 0
	isRunning := false
	var runningTool string
	var runningCmd string
	toolNamesMap := make(map[string]bool)
	var toolNames []string

	for _, t := range tools {
		if t.IsRunning {
			isRunning = true
			runningTool = t.ToolName
			runningCmd = t.Command
		}
		if t.ExitCode != 0 {
			errCount++
		}
		name := strings.TrimSpace(t.ToolName)
		if name == "" {
			name = "tool"
		}
		if !toolNamesMap[name] {
			toolNamesMap[name] = true
			toolNames = append(toolNames, name)
		}
	}

	if isRunning {
		cmdStr := ""
		if runningCmd != "" && runningCmd != "null" && runningCmd != "{}" {
			cmd := runningCmd
			if len(cmd) > 40 {
				cmd = cmd[:37] + "..."
			}
			cmdStr = lipgloss.NewStyle().Foreground(ColorMuted).Render(fmt.Sprintf(" ($ %s)", cmd))
		}
		return fmt.Sprintf("  %s Running tool (%d/%d): %s%s %s %s",
			toolBullet,
			len(tools), len(tools),
			lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(runningTool),
			cmdStr,
			ToolRunningBadge.Render("RUNNING"),
			expandHint,
		)
	}

	var statusBadge string
	if errCount > 0 {
		statusBadge = ToolErrorBadge.Render(fmt.Sprintf("%d ERRORS", errCount))
	} else {
		statusBadge = ToolSuccessBadge.Render(fmt.Sprintf("%d DONE", len(tools)))
	}

	namesList := strings.Join(toolNames, ", ")
	return fmt.Sprintf("  %s %d tool calls (%s) %s %s",
		toolBullet,
		len(tools),
		namesList,
		statusBadge,
		expandHint,
	)
}

// RenderToolCard formats a tool invocation and its result as a styled card.
func RenderToolCard(toolName string, commandOrArgs string, output string, exitCode int, isRunning bool, width int) string {
	var statusBadge string
	if isRunning {
		statusBadge = ToolRunningBadge.Render("RUNNING")
	} else if exitCode == 0 {
		statusBadge = ToolSuccessBadge.Render("EXIT 0")
	} else {
		statusBadge = ToolErrorBadge.Render(fmt.Sprintf("EXIT %d", exitCode))
	}

	header := fmt.Sprintf("%s Tool: %s %s", toolBullet, lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(toolName), statusBadge)

	contentLines := []string{header}
	if commandOrArgs != "" && commandOrArgs != "null" && commandOrArgs != "{}" {
		contentLines = append(contentLines, lipgloss.NewStyle().Foreground(ColorMuted).Render("$ "+commandOrArgs))
	}

	if output != "" && output != "null" && output != "{}" {
		output = strings.TrimRight(output, "\r\n")
		// Truncate output lines if excessive for display
		lines := strings.Split(output, "\n")
		if len(lines) > 8 {
			output = strings.Join(lines[:8], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-8)
		}
		contentLines = append(contentLines, lipgloss.NewStyle().Foreground(ColorHighlight).Render(output))
	}

	cardContent := strings.Join(contentLines, "\n")
	cardStyle := ToolCardBox
	if width > 6 {
		cardStyle = cardStyle.Width(width - 6)
	}

	return cardStyle.Render(cardContent)
}

// RenderSubagentCard formats a subagent execution and result as a styled card.
func RenderSubagentCard(role, model, prompt, output string, isRunning bool, width int) string {
	roleBadge := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary).
		Render(strings.ToUpper(role))

	modelTag := lipgloss.NewStyle().
		Foreground(ColorMuted).
		Italic(true).
		Render("[" + model + "]")

	var statusBadge string
	if isRunning {
		statusBadge = ToolRunningBadge.Render("RUNNING")
	} else {
		statusBadge = ToolSuccessBadge.Render("DONE")
	}

	header := fmt.Sprintf("◆ Subagent: %s %s %s", roleBadge, modelTag, statusBadge)
	contentLines := []string{header}

	if prompt != "" {
		cleanPrompt := strings.ReplaceAll(prompt, "\n", " ")
		if len(cleanPrompt) > 80 {
			cleanPrompt = cleanPrompt[:80] + "..."
		}
		contentLines = append(contentLines, lipgloss.NewStyle().Foreground(ColorMuted).Render("Objective: "+cleanPrompt))
	}

	if output != "" {
		lines := strings.Split(output, "\n")
		if len(lines) > 8 {
			output = strings.Join(lines[:8], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-8)
		}
		contentLines = append(contentLines, lipgloss.NewStyle().Foreground(ColorHighlight).Render(output))
	}

	cardContent := strings.Join(contentLines, "\n")
	cardStyle := ToolCardBox
	if width > 6 {
		cardStyle = cardStyle.Width(width - 6)
	}

	return cardStyle.Render(cardContent)
}

// RenderMemoryMinimap formats a single-line summary of fractal tree levels and live head.
func RenderMemoryMinimap(totalEvents int, tailNodesByLevel map[int]int, liveHeadCount int, compactorActive bool) string {
	parts := []string{
		fmt.Sprintf("Memory: %d msgs", totalEvents),
	}

	// Show levels from highest down to L0
	maxLevel := 0
	for lvl := range tailNodesByLevel {
		if lvl > maxLevel {
			maxLevel = lvl
		}
	}

	for lvl := maxLevel; lvl >= 0; lvl-- {
		count := tailNodesByLevel[lvl]
		if count > 0 {
			parts = append(parts, fmt.Sprintf("L%d:%d", lvl, count))
		}
	}

	parts = append(parts, TreeLiveHeadStyle.Render(fmt.Sprintf("Live:%d", liveHeadCount)))

	if compactorActive {
		parts = append(parts, ToolRunningBadge.Render("Compacting"))
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorMuted).Render("Idle"))
	}

	return strings.Join(parts, " • ")
}
