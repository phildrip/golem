package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/golem/pkg/graph"
)

// liveGraphNode tracks real-time status of a node in an active graph run.
type liveGraphNode struct {
	Graph       string
	Node        string
	Role        string
	Model       string
	Status      string // "running", "ok", "failed"
	Activations int
	StartedAt   time.Time
	Duration    time.Duration
	Err         string
}

// parseGraphSubagentRole extracts (graphName, role, nodeID, ok) from the label
// produced by Agent.dispatchGraphEvent ("<graph>/<role>·<node>" or "<graph>/<node>").
func parseGraphSubagentRole(label string) (graphName, role, nodeID string, ok bool) {
	slash := strings.IndexByte(label, '/')
	if slash <= 0 || slash >= len(label)-1 {
		return "", "", "", false
	}
	graphName = label[:slash]
	rest := label[slash+1:]
	if idx := strings.Index(rest, "\u00b7"); idx > 0 {
		role = rest[:idx]
		nodeID = rest[idx+len("\u00b7"):]
	} else {
		nodeID = rest
	}
	if nodeID == "" {
		return "", "", "", false
	}
	return graphName, role, nodeID, true
}

func (m *Model) trackGraphStart(roleLabel, modelName string) {
	gName, role, nodeID, ok := parseGraphSubagentRole(roleLabel)
	if !ok {
		return
	}
	if m.liveGraphNodes == nil {
		m.liveGraphNodes = make(map[string]*liveGraphNode)
	}
	key := gName + "/" + nodeID
	prev := m.liveGraphNodes[key]
	act := 1
	if prev != nil {
		act = prev.Activations + 1
	}
	m.liveGraphNodes[key] = &liveGraphNode{
		Graph:       gName,
		Node:        nodeID,
		Role:        role,
		Model:       modelName,
		Status:      "running",
		Activations: act,
		StartedAt:   time.Now(),
	}
	m.activeGraphName = gName
	if m.showGraphModal {
		m.syncGraphModal()
	}
}

func (m *Model) trackGraphDone(roleLabel string, duration time.Duration, err error) {
	gName, role, nodeID, ok := parseGraphSubagentRole(roleLabel)
	if !ok {
		return
	}
	if m.liveGraphNodes == nil {
		m.liveGraphNodes = make(map[string]*liveGraphNode)
	}
	key := gName + "/" + nodeID
	node, exists := m.liveGraphNodes[key]
	if !exists {
		node = &liveGraphNode{
			Graph:       gName,
			Node:        nodeID,
			Role:        role,
			Activations: 1,
		}
		m.liveGraphNodes[key] = node
	}
	if duration > 0 {
		node.Duration += duration
	} else if !node.StartedAt.IsZero() {
		node.Duration += time.Since(node.StartedAt)
	}
	if err != nil {
		node.Status = "failed"
		node.Err = err.Error()
	} else {
		node.Status = "ok"
		node.Err = ""
	}
	if m.showGraphModal {
		m.syncGraphModal()
	}
}

type graphPickerItem struct {
	Name   string
	RunID  string
	Source string
	Spec   *graph.Spec
	Record *graph.RunRecord
}

func (m Model) loadGraphPickerItems() []graphPickerItem {
	var items []graphPickerItem
	if m.agent == nil || m.agent.Graphs() == nil {
		return items
	}
	gm := m.agent.Graphs()
	if runs, err := gm.ListRuns(); err == nil {
		for _, r := range runs {
			if r == nil {
				continue
			}
			spec := r.Spec
			if spec == nil {
				spec, _ = gm.Load(r.Graph)
			}
			items = append(items, graphPickerItem{
				Name:   r.Graph,
				RunID:  r.RunID,
				Source: "run:" + string(r.Status),
				Spec:   spec,
				Record: r,
			})
		}
	}
	if entries, err := gm.List(); err == nil {
		for _, e := range entries {
			spec, err := gm.Load(e.Name)
			if err != nil {
				continue
			}
			src := "saved"
			if e.Builtin() {
				src = "built-in"
			}
			items = append(items, graphPickerItem{
				Name:   e.Name,
				Source: src,
				Spec:   spec,
			})
		}
	}
	return items
}

func (m *Model) openGraphModal(targetName string) {
	m.graphItems = m.loadGraphPickerItems()
	if targetName != "" {
		for i, item := range m.graphItems {
			if strings.EqualFold(item.Name, targetName) || strings.EqualFold(item.RunID, targetName) {
				m.graphSelection = i
				break
			}
		}
	}
	if m.graphSelection >= len(m.graphItems) {
		m.graphSelection = 0
	}
	m.showGraphModal = true
	m.textarea.Blur()
	m.syncGraphModal()
}

func (m *Model) closeGraphModal() {
	m.showGraphModal = false
	if !m.isBlurred {
		m.textarea.Focus()
	}
}

func (m *Model) moveGraphSelection(delta int) {
	n := len(m.graphItems)
	if n == 0 {
		return
	}
	m.graphSelection = (m.graphSelection + delta%n + n) % n
	m.syncGraphModal()
}

// RenderGraphDAG renders a topological ASCII/Lipgloss DAG view of spec with
// live or recorded node statuses, elapsed times, and budget/error callouts.
func RenderGraphDAG(spec *graph.Spec, rec *graph.RunRecord, live map[string]*liveGraphNode, width int) string {
	if spec == nil {
		return "(no graph topology available)"
	}
	if width <= 0 {
		width = 80
	}

	var sb strings.Builder
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	mutedStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	okStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorSuccess)
	runStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)
	failStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorDanger)
	waitStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)

	header := fmt.Sprintf("DAG: %s (%d nodes", spec.Name, len(spec.Nodes))
	if spec.Budgets != nil {
		header += fmt.Sprintf(", max_nodes=%d, max_wall=%ds, max_tokens=%d",
			spec.Budgets.MaxNodes, spec.Budgets.MaxWallSeconds, spec.Budgets.MaxTokens)
	}
	header += ")"
	sb.WriteString(titleStyle.Render(header) + "\n")

	if rec != nil {
		runBadge := string(rec.Status)
		switch rec.Status {
		case graph.RunCompleted:
			runBadge = okStyle.Render("✓ COMPLETED")
		case graph.RunRunning:
			runBadge = runStyle.Render("● RUNNING")
		case graph.RunWaiting:
			runBadge = waitStyle.Render("! WAITING FOR APPROVAL")
		case graph.RunFailed:
			runBadge = failStyle.Render("✕ FAILED")
		}
		cacheInfo := ""
		if rec.CachedTokens > 0 {
			cacheInfo = fmt.Sprintf("  Cache: %d (%.1f%%)", rec.CachedTokens, rec.CacheHitRatio*100)
		}
		sb.WriteString(fmt.Sprintf("Run: %s  Status: %s  Tokens: %d%s  Duration: %s\n",
			rec.RunID, runBadge, rec.Tokens, cacheInfo, rec.Duration.Round(time.Millisecond)))
		if rec.Err != "" {
			errBanner := rec.Err
			if strings.Contains(errBanner, "budget exhausted") || strings.Contains(errBanner, "wall clock") {
				errBanner = "[BUDGET EXHAUSTED] " + errBanner
			}
			sb.WriteString(failStyle.Render("  ! "+clipRunesPlain(errBanner, max(30, width-8))) + "\n")
		}
	}
	sb.WriteString("\n")

	recNodes := map[string]graph.NodeResult{}
	waitingNodes := map[string]graph.WaitingInterrupt{}
	if rec != nil {
		for _, nr := range rec.Nodes {
			recNodes[nr.ID] = nr
		}
		for _, w := range rec.Waiting {
			waitingNodes[w.Node] = w
		}
	}

	outEdges := map[string][]string{}
	inEdges := map[string][]string{}
	for _, e := range spec.Edges {
		if len(e) < 2 {
			continue
		}
		lbl := e[1]
		if len(e) == 3 && e[2] != "" {
			lbl = fmt.Sprintf("%s [route=%s]", e[1], e[2])
		}
		outEdges[e[0]] = append(outEdges[e[0]], lbl)
		inEdges[e[1]] = append(inEdges[e[1]], e[0])
	}

	if startTargets := outEdges[graph.Start]; len(startTargets) > 0 {
		sb.WriteString(mutedStyle.Render("  START ──► "+strings.Join(startTargets, ", ")) + "\n")
	}

	for i, n := range spec.Nodes {
		status := "pending"
		var dur time.Duration
		var tok int64
		var act int
		var errText string

		if nr, ok := recNodes[n.ID]; ok {
			dur = nr.Duration
			tok = nr.Tokens
			act = nr.Activations
			if nr.Err == "did not run" {
				status = "pending"
			} else if nr.Err != "" {
				status = "failed"
				errText = nr.Err
			} else {
				status = "ok"
			}
		}
		if w, ok := waitingNodes[n.ID]; ok {
			status = "waiting"
			errText = fmt.Sprintf("waiting on %s (%s)", w.ToolName, w.Hint)
		}
		if live != nil {
			if ln, ok := live[spec.Name+"/"+n.ID]; ok {
				status = ln.Status
				if ln.Activations > act {
					act = ln.Activations
				}
				if ln.Status == "running" && !ln.StartedAt.IsZero() {
					dur = ln.Duration + time.Since(ln.StartedAt)
				} else if ln.Duration > 0 {
					dur = ln.Duration
				}
				if ln.Err != "" {
					errText = ln.Err
				}
			}
		}

		var badge string
		switch status {
		case "running":
			badge = runStyle.Render("● RUNNING")
		case "ok":
			badge = okStyle.Render("✓ OK     ")
		case "failed":
			badge = failStyle.Render("✕ FAILED ")
		case "waiting":
			badge = waitStyle.Render("! WAITING")
		default:
			badge = mutedStyle.Render("○ PENDING")
		}

		role := n.Kind
		if n.Role != "" {
			role = n.Role
		} else if n.Tool != "" {
			role = "tool:" + n.Tool
		}
		if act > 1 {
			role = fmt.Sprintf("%s(x%d)", role, act)
		}

		meta := ""
		if dur > 0 {
			meta += fmt.Sprintf(" %6s", dur.Round(time.Millisecond))
		}
		if tok > 0 {
			meta += fmt.Sprintf(" %5dtok", tok)
		}

		connector := "├─"
		if i == len(spec.Nodes)-1 {
			connector = "└─"
		}
		line := fmt.Sprintf("  %s %s  %-16s [%-14s]%s",
			connector, badge, n.ID, role, mutedStyle.Render(meta))
		sb.WriteString(line + "\n")

		if targets := outEdges[n.ID]; len(targets) > 0 {
			branchPrefix := "│ "
			if i == len(spec.Nodes)-1 {
				branchPrefix = "  "
			}
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  %s   └──► %s", branchPrefix, strings.Join(targets, ", "))) + "\n")
		}
		if errText != "" {
			branchPrefix := "│ "
			if i == len(spec.Nodes)-1 {
				branchPrefix = "  "
			}
			sb.WriteString(failStyle.Render(fmt.Sprintf("  %s   ! %s", branchPrefix, clipRunesPlain(errText, max(30, width-14)))) + "\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

func clipRunesPlain(s string, maxLen int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= maxLen {
		return string(r)
	}
	return string(r[:maxLen]) + "…"
}

func (m Model) graphModalContent() (string, int) {
	var lines []string
	lines = append(lines, ModalTitleStyle.Render("GOLEM WORKFLOW GRAPHS (DAG INSPECTOR)"), "")

	if len(m.graphItems) == 0 && len(m.liveGraphNodes) == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorMuted).Render("  No graphs or runs found."))
		return strings.Join(lines, "\n"), 0
	}

	if len(m.graphItems) == 0 && len(m.liveGraphNodes) > 0 {
		header := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
		lines = append(lines, header.Render(fmt.Sprintf("▍ Live Subagent Nodes (%d)", len(m.liveGraphNodes))))
		for _, ln := range m.liveGraphNodes {
			lines = append(lines, fmt.Sprintf("  • %s/%s [%s] status=%s (x%d)", ln.Graph, ln.Node, ln.Role, ln.Status, ln.Activations))
		}
		return strings.Join(lines, "\n"), 0
	}

	header := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	lines = append(lines, header.Render(fmt.Sprintf("▍ Graphs & Recent Runs (%d)", len(m.graphItems))))

	selectionLine := len(lines) + m.graphSelection
	for i, item := range m.graphItems {
		label := item.Name
		if item.RunID != "" {
			label = fmt.Sprintf("%s (%s)", item.Name, item.RunID)
		}
		nodeCount := 0
		if item.Spec != nil {
			nodeCount = len(item.Spec.Nodes)
		}
		row := fmt.Sprintf("%-34s  [%s, %d nodes]", label, item.Source, nodeCount)
		if i == m.graphSelection {
			lines = append(lines, lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("▸ "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}

	lines = append(lines, "")
	if m.graphSelection >= 0 && m.graphSelection < len(m.graphItems) {
		sel := m.graphItems[m.graphSelection]
		lines = append(lines, RenderGraphDAG(sel.Spec, sel.Record, m.liveGraphNodes, m.width-12))
	}

	return strings.Join(lines, "\n"), selectionLine
}

func (m *Model) syncGraphModal() {
	content, selectionLine := m.graphModalContent()
	m.graphViewport.SetContent(content)

	height := m.graphViewport.Height
	if height <= 0 {
		return
	}
	offset := m.graphViewport.YOffset
	if selectionLine < offset {
		offset = selectionLine
	}
	if selectionLine >= offset+height {
		offset = selectionLine - height + 1
	}
	maxOffset := max(0, m.graphViewport.TotalLineCount()-height)
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}
	m.graphViewport.SetYOffset(offset)
}

func (m Model) handleGraphModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.closeGraphModal()
		return m, nil
	case tea.KeyUp:
		m.moveGraphSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveGraphSelection(1)
		return m, nil
	case tea.KeyPgUp, tea.KeyCtrlU:
		m.graphViewport.HalfViewUp()
		return m, nil
	case tea.KeyPgDown, tea.KeyCtrlD:
		m.graphViewport.HalfViewDown()
		return m, nil
	case tea.KeyHome:
		m.moveGraphSelection(-m.graphSelection)
		return m, nil
	case tea.KeyEnd:
		m.moveGraphSelection(len(m.graphItems) - 1 - m.graphSelection)
		return m, nil
	}

	switch msg.String() {
	case "q":
		m.closeGraphModal()
	case "j":
		m.moveGraphSelection(1)
	case "k":
		m.moveGraphSelection(-1)
	}
	return m, nil
}

func (m Model) renderGraphModalView() string {
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(m.width - 6).
		Height(m.height - 4)

	footerHint := StatusBarKeyHintStyle.Render("\n[↑/↓/j/k: Select Graph/Run • PgUp/PgDn: Scroll DAG • Esc/q: Back to Chat]")
	return modalStyle.Render(m.graphViewport.View() + footerHint)
}
