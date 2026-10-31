package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/runtime"
)

// chatState holds all conversation-scoped UI state for a single session so
// switching sessions is O(1) and preserves entries, scroll position, draft,
// input history, and in-flight streaming state.
type chatState struct {
	id                string
	title             string
	createdAt         time.Time
	lastActive        time.Time
	agent             *golem.Agent
	store             *fractal.Store
	viewEngine        *fractal.View
	entries           []ChatEntry
	startupHeader     string
	startupScrolled   bool
	yOffset           int
	atBottom          bool
	draft             string
	inputHistory      []string
	historyIndex      int
	currentDraft      string
	isLoading         bool
	streamingReply    string
	pendingApproval   *approval.Request
	showApprovalModal bool
	turnCtx           context.Context
	cancelTurn        context.CancelFunc
	streamChan        <-chan tea.Msg
	turnSeq           int
	attachments       []Attachment
	expandTools       bool
}

// initDefaultSession seeds the first session in m.sessions from the initial Model fields.
func (m *Model) initDefaultSession() {
	if m.sessions == nil {
		m.sessions = make(map[string]*chatState)
	}
	if m.activeSessionID != "" {
		return
	}
	id := "session-1"
	if m.agent != nil && m.agent.SessionID() != "" {
		id = m.agent.SessionID()
	}
	now := time.Now()
	st := &chatState{
		id:              id,
		title:           "Session 1",
		createdAt:       now,
		lastActive:      now,
		agent:           m.agent,
		store:           m.store,
		viewEngine:      m.viewEngine,
		entries:         m.entries,
		startupHeader:   m.startupHeader,
		startupScrolled: m.startupScrolled,
		atBottom:        true,
		inputHistory:    m.inputHistory,
		historyIndex:    m.historyIndex,
		currentDraft:    m.currentDraft,
		turnSeq:         m.turnSeq,
		expandTools:     m.expandTools,
	}
	m.sessions[id] = st
	m.sessionOrder = []string{id}
	m.activeSessionID = id
}

// SetSessionManager attaches a shared runtime.SessionManager to the TUI model
// and adopts the initial agent session if not already registered.
func (m *Model) SetSessionManager(sm *runtime.SessionManager) {
	m.initDefaultSession()
	m.sessionMgr = sm
	if sm == nil {
		return
	}
	if _, ok := sm.GetSession(m.activeSessionID); !ok && m.agent != nil {
		sess := sm.AdoptSession(m.activeSessionID, "Session 1", m.agent, m.agent.Config())
		if st, exists := m.sessions[m.activeSessionID]; exists {
			delete(m.sessions, m.activeSessionID)
			st.id = sess.ID
			st.title = sess.Title
			m.sessions[sess.ID] = st
			m.sessionOrder[0] = sess.ID
			m.activeSessionID = sess.ID
		}
	}
}

// ActiveSessionID returns the ID of the currently displayed session.
func (m Model) ActiveSessionID() string {
	return m.activeSessionID
}

// SessionCount returns the number of sessions managed by the TUI.
func (m Model) SessionCount() int {
	return len(m.sessionOrder)
}

// SessionIDs returns the ordered list of session IDs.
func (m Model) SessionIDs() []string {
	out := make([]string, len(m.sessionOrder))
	copy(out, m.sessionOrder)
	return out
}

// saveActiveSessionState snapshots the active conversation's fields into m.sessions.
func (m *Model) saveActiveSessionState() {
	m.initDefaultSession()
	st, ok := m.sessions[m.activeSessionID]
	if !ok {
		st = &chatState{
			id:         m.activeSessionID,
			title:      "Session",
			createdAt:  time.Now(),
			lastActive: time.Now(),
		}
		m.sessions[m.activeSessionID] = st
	}
	entriesCopy := make([]ChatEntry, len(m.entries))
	copy(entriesCopy, m.entries)
	histCopy := make([]string, len(m.inputHistory))
	copy(histCopy, m.inputHistory)
	attCopy := make([]Attachment, len(m.attachments))
	copy(attCopy, m.attachments)

	st.agent = m.agent
	st.store = m.store
	st.viewEngine = m.viewEngine
	st.entries = entriesCopy
	st.startupHeader = m.startupHeader
	st.startupScrolled = m.startupScrolled
	st.yOffset = m.viewport.YOffset
	st.atBottom = m.viewport.AtBottom()
	st.draft = m.textarea.Value()
	st.inputHistory = histCopy
	st.historyIndex = m.historyIndex
	st.currentDraft = m.currentDraft
	st.isLoading = m.isLoading
	st.streamingReply = m.streamingReply
	st.pendingApproval = m.pendingApproval
	st.showApprovalModal = m.showApprovalModal
	st.turnCtx = m.turnCtx
	st.cancelTurn = m.cancelTurn
	st.streamChan = m.streamChan
	st.turnSeq = m.turnSeq
	st.attachments = attCopy
	st.expandTools = m.expandTools
}

// loadSessionState restores st into m's active fields and re-renders the viewport
// at the saved scroll offset.
func (m *Model) loadSessionState(id string) {
	st, ok := m.sessions[id]
	if !ok {
		return
	}
	if m.sessionMgr != nil {
		m.sessionMgr.SetActiveSession(id)
		if rtSess, ok := m.sessionMgr.GetSession(id); ok && rtSess.Agent != nil {
			st.agent = rtSess.Agent
			st.store = rtSess.Agent.Store()
			st.viewEngine = rtSess.Agent.View()
		}
	}

	m.activeSessionID = id
	st.lastActive = time.Now()
	m.agent = st.agent
	m.store = st.store
	m.viewEngine = st.viewEngine

	entriesCopy := make([]ChatEntry, len(st.entries))
	copy(entriesCopy, st.entries)
	m.entries = entriesCopy

	m.startupHeader = st.startupHeader
	m.startupScrolled = st.startupScrolled

	histCopy := make([]string, len(st.inputHistory))
	copy(histCopy, st.inputHistory)
	m.inputHistory = histCopy
	m.historyIndex = st.historyIndex
	m.currentDraft = st.currentDraft

	m.textarea.SetValue(st.draft)
	m.textarea.CursorEnd()

	m.isLoading = st.isLoading
	m.streamingReply = st.streamingReply
	m.pendingApproval = st.pendingApproval
	m.showApprovalModal = st.showApprovalModal
	m.turnCtx = st.turnCtx
	m.cancelTurn = st.cancelTurn
	m.streamChan = st.streamChan
	m.turnSeq = st.turnSeq

	attCopy := make([]Attachment, len(st.attachments))
	copy(attCopy, st.attachments)
	m.attachments = attCopy
	m.expandTools = st.expandTools

	m.renderConversationToViewport()
	if m.ready {
		maxOffset := max(0, m.viewport.TotalLineCount()-m.viewport.Height)
		offset := st.yOffset
		if offset > maxOffset {
			offset = maxOffset
		}
		if offset < 0 {
			offset = 0
		}
		m.viewport.SetYOffset(offset)
	}
}

// SwitchSession switches the active TUI session to id, preserving the previous
// session's scroll position, draft, entries, and history.
func (m Model) SwitchSession(id string) (Model, tea.Cmd) {
	m.initDefaultSession()
	if _, ok := m.sessions[id]; !ok {
		m.setStatus(fmt.Sprintf("Session %q not found", id))
		return m, nil
	}
	m.confirmCloseSessionID = ""
	if id == m.activeSessionID {
		return m, nil
	}
	m.saveActiveSessionState()
	m.loadSessionState(id)
	st := m.sessions[id]
	m.setStatus(fmt.Sprintf("Switched to %s (%s)", st.title, st.id))

	var cmds []tea.Cmd
	cmds = append(cmds, m.updateMemoryStatsCmd())
	if m.streamChan != nil && m.isLoading {
		cmds = append(cmds, listenStreamCmd(m.activeSessionID, m.streamChan))
	}
	return m, tea.Batch(cmds...)
}

// NewSession creates a new session and switches to it.
func (m Model) NewSession(title string) (Model, tea.Cmd) {
	m.initDefaultSession()
	m.saveActiveSessionState()
	m.confirmCloseSessionID = ""

	num := len(m.sessionOrder) + 1
	if strings.TrimSpace(title) == "" {
		title = fmt.Sprintf("Session %d", num)
	}

	var (
		id         string
		ag         *golem.Agent
		store      *fractal.Store
		viewEngine *fractal.View
	)
	if m.sessionMgr != nil {
		sess, err := m.sessionMgr.CreateSession(context.Background(), title)
		if err != nil {
			m.setStatus("New session failed: " + err.Error())
			return m, nil
		}
		id = sess.ID
		ag = sess.Agent
		if ag != nil {
			store = ag.Store()
			viewEngine = ag.View()
		}
	} else {
		id = fmt.Sprintf("session-%d", time.Now().UnixNano())
		for _, exists := m.sessions[id]; exists; _, exists = m.sessions[id] {
			id = fmt.Sprintf("session-%d-%d", time.Now().UnixNano(), len(m.sessions)+1)
		}
		ag = m.agent
		store = m.store
		viewEngine = m.viewEngine
	}

	now := time.Now()
	st := &chatState{
		id:              id,
		title:           title,
		createdAt:       now,
		lastActive:      now,
		agent:           ag,
		store:           store,
		viewEngine:      viewEngine,
		startupScrolled: true,
		atBottom:        true,
		expandTools:     m.expandTools,
	}
	m.sessions[id] = st
	m.sessionOrder = append(m.sessionOrder, id)
	m.loadSessionState(id)
	m.setStatus(fmt.Sprintf("Created %s • Ctrl+S: switch • Ctrl+W: close", title))
	return m, m.updateMemoryStatsCmd()
}

// CloseSession closes session id. If a turn is in flight on id and force is false,
// it prompts for confirmation first. When the active session is closed, it falls
// back to a neighbour (or creates a fresh session if it was the last one).
func (m Model) CloseSession(id string, force bool) (Model, tea.Cmd) {
	m.initDefaultSession()
	if id == "" {
		id = m.activeSessionID
	}
	m.saveActiveSessionState()

	st, ok := m.sessions[id]
	if !ok {
		return m, nil
	}

	if st.isLoading && !force && m.confirmCloseSessionID != id {
		m.confirmCloseSessionID = id
		m.setStatus(fmt.Sprintf("Turn in flight in %s — press Ctrl+W (or y) again to close, Esc to cancel", st.title))
		return m, nil
	}
	m.confirmCloseSessionID = ""

	if st.cancelTurn != nil {
		st.cancelTurn()
		st.cancelTurn = nil
	}
	st.isLoading = false
	st.streamChan = nil

	if m.sessionMgr != nil {
		_ = m.sessionMgr.DeleteSession(id)
	}

	idx := 0
	for i, oid := range m.sessionOrder {
		if oid == id {
			idx = i
			m.sessionOrder = append(m.sessionOrder[:i], m.sessionOrder[i+1:]...)
			break
		}
	}
	delete(m.sessions, id)

	closedTitle := st.title
	if len(m.sessionOrder) == 0 {
		m.activeSessionID = ""
		m.entries = nil
		m.textarea.Reset()
		m.inputHistory = nil
		m.historyIndex = 0
		m.currentDraft = ""
		m.isLoading = false
		m.streamingReply = ""
		m.initDefaultSession()
		m.renderConversationToViewport()
		m.setStatus(fmt.Sprintf("Closed %s (started fresh session)", closedTitle))
		return m, m.updateMemoryStatsCmd()
	}

	if m.activeSessionID == id {
		nextIdx := idx
		if nextIdx >= len(m.sessionOrder) {
			nextIdx = len(m.sessionOrder) - 1
		}
		if nextIdx < 0 {
			nextIdx = 0
		}
		nextID := m.sessionOrder[nextIdx]
		m.loadSessionState(nextID)
	}
	if m.showSessionModal {
		if m.sessionSelection >= len(m.sessionOrder) {
			m.sessionSelection = max(0, len(m.sessionOrder)-1)
		}
		m.syncSessionPicker()
	}
	m.setStatus(fmt.Sprintf("Closed %s • Active: %s", closedTitle, m.sessions[m.activeSessionID].title))
	return m, m.updateMemoryStatsCmd()
}

// routeBackgroundStreamMsg applies a streaming event to a non-active session's
// chatState without mutating the currently displayed session's viewport or entries.
func (m *Model) routeBackgroundStreamMsg(sessionID string, payload tea.Msg) tea.Cmd {
	m.initDefaultSession()
	st, ok := m.sessions[sessionID]
	if !ok {
		return nil
	}
	st.lastActive = time.Now()

	switch msg := payload.(type) {
	case agentChunkMsg:
		st.streamingReply += string(msg)

	case toolStartMsg:
		st.entries = append(st.entries, ChatEntry{
			Type:      TypeTool,
			ToolName:  msg.toolName,
			Command:   msg.command,
			IsRunning: true,
		})

	case toolDoneMsg:
		for i := len(st.entries) - 1; i >= 0; i-- {
			if st.entries[i].Type == TypeTool && st.entries[i].ToolName == msg.toolName && st.entries[i].IsRunning {
				st.entries[i].IsRunning = false
				st.entries[i].Content = msg.output
				st.entries[i].ExitCode = msg.exitCode
				break
			}
		}

	case subagentStartMsg:
		st.entries = append(st.entries, ChatEntry{
			Type:           TypeSubagent,
			SubagentRole:   msg.role,
			SubagentModel:  msg.model,
			SubagentPrompt: msg.prompt,
			IsRunning:      true,
		})

	case subagentDoneMsg:
		for i := len(st.entries) - 1; i >= 0; i-- {
			if st.entries[i].Type == TypeSubagent && st.entries[i].SubagentRole == msg.role && st.entries[i].IsRunning {
				st.entries[i].IsRunning = false
				if msg.err != nil {
					st.entries[i].Content = fmt.Sprintf("Error: %v", msg.err)
					st.entries[i].ExitCode = 1
				} else {
					st.entries[i].Content = msg.result
					st.entries[i].ExitCode = 0
				}
				break
			}
		}

	case approvalRequestMsg:
		req := msg.req
		st.pendingApproval = &req
		st.showApprovalModal = true
		st.isLoading = true

	case agentDoneMsg:
		if msg.seq != 0 && msg.seq != st.turnSeq {
			return nil
		}
		if awaitingApproval(msg.err) {
			return nil
		}
		if !st.isLoading {
			return nil
		}
		st.isLoading = false
		st.cancelTurn = nil
		st.streamChan = nil
		reply := msg.reply
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				reply = "^C (turn interrupted)"
			} else {
				reply = fmt.Sprintf("Error: %v", msg.err)
			}
		}
		st.entries = append(st.entries, ChatEntry{
			Type:    TypeAgent,
			Content: reply,
		})
		st.streamingReply = ""
		return nil
	}

	if st.streamChan != nil && st.isLoading {
		return listenStreamCmd(sessionID, st.streamChan)
	}
	return nil
}

// renderSessionTabs renders a single-line tab strip of sessions for the header,
// showing an activity dot (● streaming, ! awaiting approval, ○ suspended) and
// keeping within maxWidth (legible at 80 columns).
func (m Model) renderSessionTabs(maxWidth int) string {
	if len(m.sessionOrder) <= 1 && m.sessionMgr == nil {
		return ""
	}
	if maxWidth <= 0 {
		maxWidth = 80
	}

	activeStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	inactiveStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	dotBusy := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).Render("●")
	dotApproval := lipgloss.NewStyle().Bold(true).Foreground(ColorDanger).Render("!")
	dotSuspended := lipgloss.NewStyle().Foreground(ColorMuted).Render("○")

	var parts []string
	used := 0
	for i, id := range m.sessionOrder {
		st, ok := m.sessions[id]
		if !ok {
			continue
		}
		isActive := id == m.activeSessionID
		isLoading := st.isLoading
		hasApproval := st.pendingApproval != nil
		if isActive {
			isLoading = m.isLoading
			hasApproval = m.pendingApproval != nil
		}
		isSuspended := false
		if m.sessionMgr != nil {
			if rtSess, ok := m.sessionMgr.GetSession(id); ok {
				isSuspended = rtSess.IsSuspended()
			}
		}

		title := st.title
		if len(title) > 12 {
			title = title[:11] + "…"
		}
		badge := ""
		switch {
		case hasApproval:
			badge = " " + dotApproval
		case isLoading:
			badge = " " + dotBusy
		case isSuspended && !isActive:
			badge = " " + dotSuspended
		}

		rawLabel := fmt.Sprintf("%d:%s", i+1, title)
		var tab string
		if isActive {
			tab = activeStyle.Render("["+rawLabel) + badge + activeStyle.Render("]")
		} else {
			tab = inactiveStyle.Render(" "+rawLabel) + badge + inactiveStyle.Render(" ")
		}

		w := lipgloss.Width(tab)
		if used > 0 && used+1+w > maxWidth-6 {
			rem := len(m.sessionOrder) - i
			more := inactiveStyle.Render(fmt.Sprintf("+%d", rem))
			parts = append(parts, more)
			break
		}
		parts = append(parts, tab)
		used += w + 1
	}

	line := strings.Join(parts, " ")
	return lipgloss.NewStyle().MaxWidth(maxWidth).Render(line)
}

// openSessionPicker opens the session picker modal, preselecting the active session.
func (m *Model) openSessionPicker() {
	m.initDefaultSession()
	m.saveActiveSessionState()
	m.sessionSelection = 0
	for i, id := range m.sessionOrder {
		if id == m.activeSessionID {
			m.sessionSelection = i
			break
		}
	}
	m.showSessionModal = true
	m.textarea.Blur()
	m.syncSessionPicker()
}

func (m *Model) closeSessionPicker() {
	m.showSessionModal = false
	if !m.isBlurred {
		m.textarea.Focus()
	}
}

func (m *Model) moveSessionSelection(delta int) {
	n := len(m.sessionOrder)
	if n == 0 {
		return
	}
	m.sessionSelection = (m.sessionSelection + delta%n + n) % n
	m.syncSessionPicker()
}

func (m Model) sessionPickerContent() (string, int) {
	var lines []string
	lines = append(lines, ModalTitleStyle.Render("GOLEM SESSIONS"), "")

	header := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	lines = append(lines, header.Render(fmt.Sprintf("▍ Active Sessions (%d)", len(m.sessionOrder))))
	lines = append(lines, "")

	selectionLine := len(lines) + m.sessionSelection
	for i, id := range m.sessionOrder {
		st := m.sessions[id]
		if st == nil {
			continue
		}
		isActive := id == m.activeSessionID
		isLoading := st.isLoading
		hasApproval := st.pendingApproval != nil
		entriesCount := len(st.entries)
		if isActive {
			isLoading = m.isLoading
			hasApproval = m.pendingApproval != nil
			entriesCount = len(m.entries)
		}
		isSuspended := false
		if m.sessionMgr != nil {
			if rtSess, ok := m.sessionMgr.GetSession(id); ok {
				isSuspended = rtSess.IsSuspended()
			}
		}

		stateStr := "open"
		switch {
		case hasApproval:
			stateStr = "awaiting approval (!)"
		case isLoading:
			stateStr = "streaming (●)"
		case isSuspended:
			stateStr = "suspended (○)"
		case isActive:
			stateStr = "active"
		}

		age := time.Since(st.lastActive).Round(time.Second)
		if age < time.Second {
			age = 0
		}
		rowText := fmt.Sprintf("[%d] %-16s  %-21s  %3d items  last: %s ago",
			i+1, st.title, stateStr, entriesCount, age)
		line := "  " + rowText
		if i == m.sessionSelection {
			line = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("▸ " + rowText)
		}
		if isActive {
			line += lipgloss.NewStyle().Foreground(ColorSuccess).Render("  [current]")
		}
		lines = append(lines, line)
	}

	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(ColorMuted).Render(
		"Enter/1-9: switch • n/Ctrl+N: new session • d/Ctrl+W: close selected • Esc/q: back"))
	return strings.Join(lines, "\n"), selectionLine
}

func (m *Model) syncSessionPicker() {
	content, selectionLine := m.sessionPickerContent()
	m.sessionViewport.SetContent(content)

	height := m.sessionViewport.Height
	if height <= 0 {
		return
	}
	offset := m.sessionViewport.YOffset
	if selectionLine < offset {
		offset = selectionLine
	}
	if selectionLine >= offset+height {
		offset = selectionLine - height + 1
	}
	maxOffset := max(0, m.sessionViewport.TotalLineCount()-height)
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}
	m.sessionViewport.SetYOffset(offset)
}

func (m Model) handleSessionModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc, tea.KeyCtrlS:
		m.closeSessionPicker()
		return m, nil
	case tea.KeyCtrlN:
		m.closeSessionPicker()
		return m.NewSession("")
	case tea.KeyCtrlW:
		if m.sessionSelection >= 0 && m.sessionSelection < len(m.sessionOrder) {
			targetID := m.sessionOrder[m.sessionSelection]
			return m.CloseSession(targetID, false)
		}
		return m, nil
	case tea.KeyUp:
		m.moveSessionSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveSessionSelection(1)
		return m, nil
	case tea.KeyPgUp:
		m.sessionViewport.HalfViewUp()
		return m, nil
	case tea.KeyPgDown:
		m.sessionViewport.HalfViewDown()
		return m, nil
	case tea.KeyHome:
		m.moveSessionSelection(-m.sessionSelection)
		return m, nil
	case tea.KeyEnd:
		m.moveSessionSelection(len(m.sessionOrder) - 1 - m.sessionSelection)
		return m, nil
	case tea.KeyEnter:
		if m.sessionSelection < 0 || m.sessionSelection >= len(m.sessionOrder) {
			return m, nil
		}
		targetID := m.sessionOrder[m.sessionSelection]
		m.closeSessionPicker()
		return m.SwitchSession(targetID)
	}

	s := msg.String()
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		idx := int(s[0] - '1')
		if idx < len(m.sessionOrder) {
			targetID := m.sessionOrder[idx]
			m.closeSessionPicker()
			return m.SwitchSession(targetID)
		}
		return m, nil
	}

	switch s {
	case "q":
		m.closeSessionPicker()
	case "j":
		m.moveSessionSelection(1)
	case "k":
		m.moveSessionSelection(-1)
	case "n":
		m.closeSessionPicker()
		return m.NewSession("")
	case "d", "x":
		if m.sessionSelection >= 0 && m.sessionSelection < len(m.sessionOrder) {
			targetID := m.sessionOrder[m.sessionSelection]
			return m.CloseSession(targetID, false)
		}
	}
	return m, nil
}

func (m Model) renderSessionModalView() string {
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(m.width - 6).
		Height(m.height - 4)

	footerHint := StatusBarKeyHintStyle.Render("\n[↑/↓: Select • Enter/1-9: Switch • n: New • d: Close • Esc/q: Back]")
	return modalStyle.Render(m.sessionViewport.View() + footerHint)
}
