// Command themepreview renders Golem's real TUI widgets in every shipped theme
// so a palette can be judged by eye in an actual terminal.
//
// This is a developer tool, not part of the shipped binary.
//
//	go run ./tools/themepreview             # every theme
//	go run ./tools/themepreview terminal    # one theme
//	go run ./tools/themepreview nord
//
// The "terminal" theme emits only ANSI colour indexes (e.g. ESC[36m), so what you
// see is your own terminal's palette. The "nord" theme emits 24-bit RGB.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/user/golem/pkg/tui"
)

// markdownSample exercises the markdown paths most likely to clash: a heading,
// a link, inline code (a file path, the original "everything looks like an
// error" case) and a fenced code block.
const markdownSample = "### Plan\n\n" +
	"Config lives in `~/.golem/config.json`; see the [README](../README.md).\n\n" +
	"- inline `code` is teal, not red\n" +
	"- **strong** and *emphasis* stay neutral\n\n" +
	"```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n"

func themes() []string {
	return []string{tui.ThemeTerminal, tui.ThemeNord}
}

// render draws a mock conversation out of the real component styles, so the
// preview cannot drift away from what the TUI actually paints.
func render() string {
	var b strings.Builder

	header := lipgloss.JoinHorizontal(
		lipgloss.Center,
		tui.HeaderTitleStyle.Render("GOLEM"),
		tui.HeaderSubStyle.Render(" [default | gemini:gemini-flash-latest] • Fractal Memory"),
	)
	b.WriteString(header + "\n")
	b.WriteString(tui.StatusBarMemoryStyle.Render(
		tui.RenderMemoryMinimap(2048, map[int]int{0: 24, 1: 12, 2: 6, 3: 3}, 9, false, ""),
	) + "\n\n")

	b.WriteString(tui.UserBadgeStyle.Render("YOU") + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(tui.ColorHighlight).Render("does the palette follow my terminal?") + "\n\n")

	b.WriteString(tui.AgentBadgeStyle.Render("GOLEM") + "\n")
	body := lipgloss.NewStyle().Foreground(tui.ColorHighlight)
	agentLine := body.Render("It does now: ") +
		lipgloss.NewStyle().Foreground(tui.ColorPrimary).Render("accents") + body.Render(", ") +
		lipgloss.NewStyle().Foreground(tui.ColorSuccess).Render("success") + body.Render(", ") +
		lipgloss.NewStyle().Foreground(tui.ColorWarning).Render("warning") + body.Render(", ") +
		lipgloss.NewStyle().Foreground(tui.ColorDanger).Render("errors") + body.Render(", ") +
		lipgloss.NewStyle().Foreground(tui.ColorSecondary).Render("subagents") + body.Render(" and ") +
		lipgloss.NewStyle().Foreground(tui.ColorMuted).Render("muted metadata") + body.Render(".")
	b.WriteString(agentLine + "\n\n")

	b.WriteString(tui.RenderCollapsedToolSummary([]tui.ChatEntry{
		{Type: tui.TypeTool, ToolName: "shell", Command: "go test ./...", ExitCode: 0},
	}) + "\n")
	b.WriteString(tui.RenderCollapsedToolSummary([]tui.ChatEntry{
		{Type: tui.TypeTool, ToolName: "shell", Command: "go build ./...", IsRunning: true},
	}) + "\n\n")

	b.WriteString(tui.RenderToolCard("shell", "false", "command failed", 1, false, 64) + "\n")
	b.WriteString(tui.RenderSubagentCard("researcher", "gemini-flash-latest", "check the shutdown path", "read 3 files, found 1 bug", false, false, 64) + "\n\n")

	// Markdown goes through the real renderer, which is where a stray red inline
	// code chip (glamour's default) would show up.
	b.WriteString(tui.RenderMarkdown(72, markdownSample))

	b.WriteString(lipgloss.NewStyle().Foreground(tui.ColorWarning).Italic(true).
		Render("... 32 earlier message(s) are summarized in fractal memory") + "\n\n")

	input := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(tui.ColorBorder).
		Render("> look at the palette")
	b.WriteString(input + "\n")
	b.WriteString(tui.StatusBarKeyHintStyle.Render(
		"[Enter: Send • ↑/↓: History • Ctrl+O: Tools • Ctrl+K: Shortcuts • Ctrl+G: Mouse • Ctrl+C: Clear • Ctrl+D: Exit]",
	) + "\n")

	return b.String()
}

// ensureColorProfile keeps the preview honest when its output is piped: otherwise
// lipgloss detects a non-TTY and strips every escape, hiding the thing being
// previewed. ANSI colours survive a TrueColor profile unchanged (ANSI, not
// converted), so the terminal theme still shows *index* colours here.
func ensureColorProfile() {
	if fi, err := os.Stdout.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return // real terminal: let lipgloss detect the profile itself
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
}

func main() {
	ensureColorProfile()
	want := strings.ToLower(strings.TrimSpace(strings.Join(os.Args[1:], " ")))
	shown := 0
	for _, name := range themes() {
		if want != "" && want != name {
			continue
		}
		if err := tui.ApplyTheme(name); err != nil {
			fmt.Fprintf(os.Stderr, "themepreview: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("=== theme: %s (%s) ===\n", tui.CurrentTheme(), describe(tui.CurrentTheme()))
		fmt.Print(render())
		fmt.Println()
		shown++
	}
	if shown == 0 {
		fmt.Fprintf(os.Stderr, "themepreview: unknown theme %q (have %s)\n", want, strings.Join(themes(), ", "))
		os.Exit(1)
	}
}

// describe explains where the colours come from, which is the whole point of the
// two themes.
func describe(name string) string {
	if name == tui.ThemeNord {
		return "fixed 24-bit Nord tones"
	}
	return "your terminal's own palette"
}
