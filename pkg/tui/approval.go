package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/golem"
)

// approvalRequestMsg carries a sandbox confirmation into the TUI. The turn is
// paused until the user answers it.
type approvalRequestMsg struct {
	req approval.Request
}

// OnApprovalRequest implements golem.ApprovalStreamHandler: the agent stopped on
// a call the policy wants a human to approve.
func (b *tuiStreamBridge) OnApprovalRequest(req approval.Request) {
	b.send(approvalRequestMsg{req: req})
}

// openApprovalPrompt shows the modal for req and remembers the call id.
func (m *Model) openApprovalPrompt(req approval.Request) {
	m.pendingApproval = &req
	m.showApprovalModal = true
	// The turn is not over, it is waiting: keep the spinner and the loading
	// state so the transcript does not claim the turn finished.
	m.isLoading = true
	m.setStatus("Approval needed • y: run it • n: refuse")
}

// handleApprovalKey answers the pending prompt. y/Enter approves, n/Esc refuses,
// and either way the turn resumes through the agent so the ADK can re-dispatch
// the held call.
func (m Model) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		return m.answerApproval(true)
	case "n", "N", "esc", "q", "ctrl+c":
		return m.answerApproval(false)
	}
	return m, nil
}

// answerApproval closes the prompt, records the decision in the transcript and
// resumes the turn.
func (m Model) answerApproval(approved bool) (tea.Model, tea.Cmd) {
	if m.pendingApproval == nil {
		m.showApprovalModal = false
		return m, nil
	}
	req := *m.pendingApproval
	m.pendingApproval = nil
	m.showApprovalModal = false

	verdict := "refused"
	if approved {
		verdict = "approved"
	}
	m.entries = append(m.entries, ChatEntry{
		Type:     TypeTool,
		ToolName: "approval",
		Command:  fmt.Sprintf("%s • %s", req.Action, verdict),
		Content:  verdict,
		ExitCode: map[bool]int{true: 0, false: 1}[approved],
	})
	m.renderAndFollow()

	ctx := m.turnCtx
	if ctx == nil || m.agent == nil {
		// The turn context is gone (the transport dropped it); answering cannot
		// resume anything, so say so instead of hanging.
		m.isLoading = false
		m.setStatus("Approval not delivered: the turn is no longer running")
		return m, nil
	}
	return m, m.resumeAgentTurnCmd(ctx, req.CallID, approved, m.turnSeq)
}

// resumeAgentTurnCmd continues a paused turn with the user's answer.
func (m *Model) resumeAgentTurnCmd(ctx context.Context, callID string, approved bool, seq int) tea.Cmd {
	msgChan := make(chan tea.Msg, 2048)
	m.streamChan = msgChan
	bridge := &tuiStreamBridge{ctx: ctx, msgChan: msgChan}

	go func() {
		defer close(msgChan)
		defer func() {
			if r := recover(); r != nil {
				select {
				case <-ctx.Done():
				case msgChan <- agentDoneMsg{
					err: fmt.Errorf("agent internal error: %v", r),
					seq: seq,
				}:
				}
			}
		}()
		reply, err := m.agent.RespondToApproval(ctx, callID, approved, bridge)
		select {
		case <-ctx.Done():
		case msgChan <- agentDoneMsg{reply: reply, err: err, seq: seq}:
		}
	}()

	return listenStreamCmd(m.activeSessionID, msgChan)
}

// renderApprovalModalView draws the confirmation prompt. It shows the exact
// command or patch being approved, because that is what the human is deciding.
func (m Model) renderApprovalModalView() string {
	width := m.width - 6
	if width < 24 {
		width = 24
	}
	body := "(the pending call is gone)"
	if m.pendingApproval != nil {
		body = m.pendingApproval.Hint
		if m.pendingApproval.Action != "" && !strings.Contains(body, m.pendingApproval.Action) {
			body += "\n\n" + m.pendingApproval.Action
		}
	}

	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorWarning).
		Padding(1, 2).
		Width(width)

	title := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).
		Render("Approve this tool call?")
	hint := StatusBarKeyHintStyle.Render("\n[y: run it • n/Esc: refuse • the model is told either way]")

	return modalStyle.Render(lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		wrapPlain(body, width-4),
		"",
		hint,
	))
}

// wrapPlain hard-wraps text at width so a long command cannot push the frame
// past the pane, which would make tmux scroll the whole screen.
func wrapPlain(text string, width int) string {
	if width <= 0 {
		return text
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if lipgloss.Width(line) <= width {
			out = append(out, line)
			continue
		}
		words := strings.Fields(line)
		cur := ""
		for _, w := range words {
			switch {
			case cur == "":
				cur = w
			case lipgloss.Width(cur)+1+lipgloss.Width(w) <= width:
				cur += " " + w
			default:
				out = append(out, cur)
				cur = w
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return strings.Join(out, "\n")
}

// awaitingApproval reports whether the turn stopped for a human answer, so the
// transcript can say so instead of looking finished.
func awaitingApproval(err error) bool {
	return errors.Is(err, golem.ErrAwaitingApproval)
}

// renderAuditLog formats the most recent sandbox decisions. The audit log is the
// answer to "what did it actually do", so allowed calls are in it too.
func (m Model) renderAuditLog(limit int) string {
	auditor := approval.CurrentAuditor()
	if auditor == nil {
		return "Sandbox audit log is not enabled."
	}
	records := auditor.Recent(limit)
	if len(records) == 0 {
		return "No sandbox decisions recorded yet."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Sandbox decisions (last %d):\n", len(records))
	for _, r := range records {
		action := r.Action
		if len([]rune(action)) > 60 {
			action = string([]rune(action)[:60]) + "…"
		}
		line := fmt.Sprintf("%s  %-7s %-14s %s", r.Time.Format("15:04:05"), r.Verdict, r.Tool, action)
		if r.Rule != "" {
			line += "  [" + r.Rule + "]"
		}
		if r.Unattended {
			line += "  (unattended)"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if path := auditor.Path(); path != "" {
		fmt.Fprintf(&b, "\nlog: %s", path)
	}
	return strings.TrimRight(b.String(), "\n")
}
