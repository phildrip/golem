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
			MarginBottom(1)

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
	if commandOrArgs != "" {
		contentLines = append(contentLines, lipgloss.NewStyle().Foreground(ColorMuted).Render("$ "+commandOrArgs))
	}

	if output != "" {
		// Truncate output lines if excessive for display
		lines := strings.Split(output, "\n")
		if len(lines) > 8 {
			output = strings.Join(lines[:8], "\n") + fmt.Sprintf("\n... (%d more lines)", len(lines)-8)
		}
		contentLines = append(contentLines, lipgloss.NewStyle().Foreground(ColorHighlight).Render(output))
	}

	cardContent := strings.Join(contentLines, "\n")
	cardStyle := ToolCardBox
	if width > 4 {
		cardStyle = cardStyle.Width(width - 4)
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
