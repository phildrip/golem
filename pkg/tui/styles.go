package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme colors
var (
	ColorPrimary   = lipgloss.Color("#00D4FF") // Neon Cyan
	ColorSecondary = lipgloss.Color("#7D56F4") // Electric Purple
	ColorSuccess   = lipgloss.Color("#00E676") // Emerald Green
	ColorWarning   = lipgloss.Color("#FFD600") // Amber Yellow
	ColorDanger    = lipgloss.Color("#FF5252") // Coral Red
	ColorMuted     = lipgloss.Color("#6B7280") // Cool Gray
	ColorHighlight = lipgloss.Color("#F3F4F6") // Bright White
	ColorDarkBg    = lipgloss.Color("#111827") // Deep Slate
	ColorBorder    = lipgloss.Color("#374151") // Border Slate
)

// Component Styles
var (
	// Header & Titles
	HeaderTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(ColorSecondary).
				Padding(0, 1)

	HeaderSubStyle = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)

	// Status & HUD
	StatusBarModelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(lipgloss.Color("#1F2937")).
				Padding(0, 1)

	StatusBarMemoryStyle = lipgloss.NewStyle().
				Foreground(ColorSuccess).
				Background(lipgloss.Color("#1F2937")).
				Padding(0, 1)

	StatusBarKeyHintStyle = lipgloss.NewStyle().
				Foreground(ColorMuted)

	// User & Agent labels
	UserBadgeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(ColorPrimary).
			Padding(0, 1)

	AgentBadgeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(ColorSecondary).
			Padding(0, 1)

	// Tool execution card styles
	ToolCardBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1).
			MarginTop(0).
			MarginBottom(0)

	ToolRunningBadge = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#000000")).
				Background(ColorWarning).
				Bold(true).
				Padding(0, 1)

	ToolSuccessBadge = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#000000")).
				Background(ColorSuccess).
				Bold(true).
				Padding(0, 1)

	ToolErrorBadge = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(ColorDanger).
			Bold(true).
			Padding(0, 1)

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
			return fmt.Sprintf("  ⚡ Running tool: %s%s %s",
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

		return fmt.Sprintf("  ⚡ Tool: %s%s %s %s",
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
		return fmt.Sprintf("  ⚡ Running tool (%d/%d): %s%s %s %s",
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
	return fmt.Sprintf("  ⚡ %d tool calls (%s) %s %s",
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

	header := fmt.Sprintf("⚡ Tool: %s %s", lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(toolName), statusBadge)

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
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(ColorSecondary).
		Padding(0, 1).
		Render(strings.ToUpper(role))

	modelTag := lipgloss.NewStyle().
		Foreground(ColorPrimary).
		Italic(true).
		Render("[" + model + "]")

	var statusBadge string
	if isRunning {
		statusBadge = ToolRunningBadge.Render("RUNNING")
	} else {
		statusBadge = ToolSuccessBadge.Render("DONE")
	}

	header := fmt.Sprintf("🤖 Subagent: %s %s %s", roleBadge, modelTag, statusBadge)
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
		parts = append(parts, ToolRunningBadge.Render("⟡ Compacting"))
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorMuted).Render("⟡ Idle"))
	}

	return strings.Join(parts, " • ")
}
