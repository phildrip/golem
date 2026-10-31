package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// slashMenuMaxRows caps how many completions the popup shows at once; the
// window of visible rows follows the highlighted entry.
const slashMenuMaxRows = 6

// slashDescColumn is the width reserved for the command column of the popup.
// On narrow panes the description is dropped instead of wrapping.
const slashDescColumn = 26

// slashCommand is one entry in the command palette. The same table drives Tab
// completion, the popup menu and the status-bar hints, so the three cannot
// drift apart.
type slashCommand struct {
	Name    string   // canonical name, e.g. "/profile"
	Args    string   // argument shape, e.g. "[name]"; empty means none
	Desc    string   // one-line description
	Aliases []string // alternative spellings accepted on submit
}

// Label renders the command plus its argument shape, e.g. "/zoom [t] <id> <n>".
func (c slashCommand) Label() string {
	if c.Args == "" {
		return c.Name
	}
	return c.Name + " " + c.Args
}

// matchesToken reports whether the typed token is a prefix of this command,
// including its aliases, so "/mem" still finds /tree.
func (c slashCommand) matchesToken(token string) bool {
	if strings.HasPrefix(c.Name, token) {
		return true
	}
	for _, alias := range c.Aliases {
		if strings.HasPrefix(alias, token) {
			return true
		}
	}
	return false
}

// isCommand reports whether the typed token names this command exactly.
func (c slashCommand) isCommand(token string) bool {
	if c.Name == token {
		return true
	}
	for _, alias := range c.Aliases {
		if alias == token {
			return true
		}
	}
	return false
}

// slashCommands returns the palette in display order.
func slashCommands() []slashCommand {
	return []slashCommand{
		{Name: "/model", Args: "[name]", Desc: "Pick a model, or switch to a named one", Aliases: []string{"/models"}},
		{Name: "/profile", Args: "[name]", Desc: "Inspect profiles, or switch provider+model", Aliases: []string{"/profiles"}},
		{Name: "/remember", Args: "<text>", Desc: "Record a preference or decision into memory"},
		{Name: "/tree", Desc: "Inspect the fractal memory hierarchy", Aliases: []string{"/memory"}},
		{Name: "/zoom", Args: "[target] <id> <n>", Desc: "Inspect a memory node (n=1 for verbatim)"},
		{Name: "/compact", Desc: "Run a memory compaction pass now"},
		{Name: "/diff", Args: "[ref]", Desc: "Show workspace changes since the last checkpoint"},
		{Name: "/undo", Args: "[ref]", Desc: "Restore workspace to a checkpoint (or 'list')", Aliases: []string{"/rewind"}},
		{Name: "/clear", Desc: "Clear the chat viewport (disk memory kept)"},
		{Name: "/tools", Desc: "Toggle expanded tool cards (Ctrl+O)", Aliases: []string{"/expand"}},
		{Name: "/mcp", Desc: "Inspect MCP servers, tools and configs"},
		{Name: "/audit", Desc: "Show recent sandbox decisions (allow/deny/confirm)"},
		{Name: "/shortcuts", Desc: "Show the keyboard cheat sheet", Aliases: []string{"/keys"}},
		{Name: "/help", Desc: "Show the full command reference"},
		{Name: "/exit", Desc: "Exit Golem", Aliases: []string{"/quit"}},
	}
}

// slashQuery extracts the command token being typed. It reports ok only while
// the user is still completing the command name: a leading "/" with no space
// after it. Once arguments are being typed, completion steps out of the way.
func slashQuery(val string) (string, bool) {
	if !strings.HasPrefix(val, "/") {
		return "", false
	}
	if strings.ContainsAny(val, " \t\n") {
		return "", false
	}
	return strings.ToLower(val), true
}

// matchSlashCommands returns the commands matching the token being typed.
func matchSlashCommands(val string) []slashCommand {
	token, ok := slashQuery(val)
	if !ok {
		return nil
	}
	var matches []slashCommand
	for _, c := range slashCommands() {
		if c.matchesToken(token) {
			matches = append(matches, c)
		}
	}
	return matches
}

// slashExactMatch reports whether the typed token already names a command, in
// which case there is nothing left to complete.
func slashExactMatch(val string) bool {
	token, ok := slashQuery(val)
	if !ok {
		return false
	}
	for _, c := range slashCommands() {
		if c.isCommand(token) {
			return true
		}
	}
	return false
}

// slashMenuVisible reports whether the completion popup should be drawn: the
// prompt is mid-command, at least one command matches, and the user has not
// dismissed the menu with Esc.
func (m Model) slashMenuVisible() bool {
	if m.slashDismissed {
		return false
	}
	val := m.textarea.Value()
	if _, ok := slashQuery(val); !ok {
		return false
	}
	if slashExactMatch(val) {
		return false
	}
	return len(matchSlashCommands(val)) > 0
}

// slashSelectedIndex clamps the highlight into the current match list so it
// survives the list shrinking as the user types.
func (m Model) slashSelectedIndex(matches []slashCommand) int {
	if len(matches) == 0 {
		return 0
	}
	idx := m.slashSel
	if idx < 0 || idx >= len(matches) {
		idx = 0
	}
	return idx
}

// resetSlashState re-arms the popup whenever the typed token changes: a new
// prefix starts at the top row and un-dismisses a previously escaped menu.
func (m *Model) resetSlashState() {
	token, ok := slashQuery(m.textarea.Value())
	if !ok {
		token = ""
	}
	if token != m.slashQuerySeen {
		m.slashQuerySeen = token
		m.slashSel = 0
		m.slashDismissed = false
	}
}

// acceptSlashCompletion replaces the prompt with the highlighted command.
// Commands that take arguments keep a trailing space so the user can type them;
// bare commands land complete so Enter submits straight away.
func (m Model) acceptSlashCompletion() Model {
	matches := matchSlashCommands(m.textarea.Value())
	if len(matches) == 0 {
		return m
	}
	c := matches[m.slashSelectedIndex(matches)]
	val := c.Name
	if c.Args != "" {
		val += " "
	}
	m.textarea.SetValue(val)
	m.textarea.CursorEnd()
	m.slashSel = 0
	m.slashDismissed = false
	m.slashQuerySeen = ""
	return m
}

// renderSlashMenuRows renders the completion popup rows above the prompt, or nil
// when the popup is closed.
func (m Model) renderSlashMenuRows() []string {
	if !m.slashMenuVisible() {
		return nil
	}
	matches := matchSlashCommands(m.textarea.Value())
	if len(matches) == 0 {
		return nil
	}

	maxRows := slashMenuMaxRows
	// Never take more than half the conversation area: the newest output stays
	// visible while the popup is open.
	if m.viewport.Height > 2 && m.viewport.Height/2 < maxRows {
		maxRows = m.viewport.Height / 2
	}
	if maxRows < 1 {
		return nil
	}

	sel := m.slashSelectedIndex(matches)
	start := 0
	if sel >= maxRows {
		start = sel - maxRows + 1
	}
	end := start + maxRows
	if end > len(matches) {
		end = len(matches)
	}

	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, m.renderSlashMenuRow(matches[i], i == sel))
	}
	return rows
}

// renderSlashMenuRow renders one popup line: a selection marker, the command
// with its argument shape, and its description when there is room.
func (m Model) renderSlashMenuRow(c slashCommand, selected bool) string {
	marker := "  "
	labelStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	if selected {
		marker = lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true).Render("› ")
		labelStyle = labelStyle.Bold(true)
	}

	label := c.Label()
	row := marker + labelStyle.Render(label)

	// Descriptions only when the pane is wide enough for both columns.
	if m.width >= slashDescColumn+24 && len(label) < slashDescColumn {
		row += strings.Repeat(" ", slashDescColumn-len(label))
		row += lipgloss.NewStyle().Foreground(ColorMuted).Render(c.Desc)
	}

	if m.width > 2 {
		row = lipgloss.NewStyle().MaxWidth(m.width - 2).Render(row)
	}
	return row
}

// slashMenuHint is the compact key legend shown in the status line while the
// popup is open.
func (m Model) slashMenuHint() string {
	hints := hintLine(m.width-4, "Tab: complete", "↑/↓: choose", "Enter: run", "Esc: dismiss")
	return StatusBarKeyHintStyle.Render("[" + hints + "]")
}

// truncateLines keeps at most n leading lines of s. The slash popup is drawn
// over the bottom of the conversation, so the frame height never changes and a
// tmux pane cannot scroll out from under the user.
func truncateLines(s string, n int) string {
	if n < 0 {
		n = 0
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}
