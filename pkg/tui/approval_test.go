package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/golem"
)

func pendingShellRequest() approval.Request {
	return approval.Request{
		CallID: "conf-1",
		Tool:   "shell",
		Hint:   "shell wants to run: git push --force origin main\nwhy: a force push rewrites shared history",
		Action: "git push --force origin main",
		Reason: "a force push rewrites shared history",
	}
}

// TestApprovalPromptShowsTheExactCommand pins the ticket's wording: the prompt
// shows the real command, not a summary of it.
func TestApprovalPromptShowsTheExactCommand(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	next, _ := m.Update(approvalRequestMsg{req: pendingShellRequest()})
	m = next.(Model)

	if !m.showApprovalModal {
		t.Fatal("the confirmation prompt did not open")
	}
	if !m.isLoading {
		t.Fatal("a paused turn must keep the loading state")
	}
	view := m.View()
	if !strings.Contains(view, "git push --force origin main") {
		t.Fatalf("prompt does not show the command:\n%s", view)
	}
	if !strings.Contains(view, "Approve this tool call?") {
		t.Fatalf("prompt has no title:\n%s", view)
	}
}

// TestApprovalApproveClosesAndRecords covers the yes path: the prompt closes,
// the decision lands in the transcript, and the turn is resumed (here the turn
// context is gone, so it reports instead of hanging).
func TestApprovalApproveClosesAndRecords(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)
	next, _ := m.Update(approvalRequestMsg{req: pendingShellRequest()})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(Model)

	if m.showApprovalModal {
		t.Fatal("the prompt stayed open after an answer")
	}
	if m.pendingApproval != nil {
		t.Fatal("the pending call was not cleared")
	}
	found := false
	for _, e := range m.entries {
		if e.ToolName == "approval" && strings.Contains(e.Command, "approved") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no approval entry recorded: %+v", m.entries)
	}
}

// TestApprovalRefuseRecordsTheVerdict covers the no path.
func TestApprovalRefuseRecordsTheVerdict(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)
	next, _ := m.Update(approvalRequestMsg{req: pendingShellRequest()})
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)

	if m.showApprovalModal {
		t.Fatal("the prompt stayed open after a refusal")
	}
	found := false
	for _, e := range m.entries {
		if e.ToolName == "approval" && strings.Contains(e.Command, "refused") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no refusal entry recorded: %+v", m.entries)
	}
}

// TestPausedTurnIsNotReportedAsFinished keeps ErrAwaitingApproval out of the
// transcript: the turn is waiting, not done, and the prompt is still up.
func TestPausedTurnIsNotReportedAsFinished(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)
	m.isLoading = true
	before := len(m.entries)

	next, _ := m.Update(agentDoneMsg{err: golem.ErrAwaitingApproval})
	m = next.(Model)

	if !m.isLoading {
		t.Fatal("the paused turn was marked finished")
	}
	if len(m.entries) != before {
		t.Fatalf("the paused turn added %d transcript entries", len(m.entries)-before)
	}
}

// TestApprovalPromptFitsThePane keeps the frame inside the terminal: an
// over-tall frame makes tmux scroll the whole screen.
func TestApprovalPromptFitsThePane(t *testing.T) {
	m := newSlashTestModel(t, 40, 20)
	req := pendingShellRequest()
	req.Hint = strings.Repeat("very long command line ", 40)
	next, _ := m.Update(approvalRequestMsg{req: req})
	m = next.(Model)

	view := m.View()
	for i, line := range strings.Split(view, "\n") {
		if w := len([]rune(line)); w > 40 {
			t.Fatalf("line %d is %d columns wide, pane is 40: %q", i, w, line)
		}
	}
}

// TestAuditCommandShowsRecentDecisions covers /audit: the transcript gets the
// decisions, allowed calls included, and says so when the log is empty.
func TestAuditCommandShowsRecentDecisions(t *testing.T) {
	auditor, err := approval.NewAuditor("", 10)
	if err != nil {
		t.Fatalf("NewAuditor: %v", err)
	}
	approval.SetAuditor(auditor)
	t.Cleanup(func() { approval.SetAuditor(nil) })

	m := newSlashTestModel(t, 100, 30)
	next, _ := m.handleSlashCommand("/audit")
	m = next.(Model)
	last := m.entries[len(m.entries)-1]
	if !strings.Contains(last.Content, "No sandbox decisions") {
		t.Fatalf("empty log message = %q", last.Content)
	}

	auditor.Record(approval.Record{Tool: "shell", Verdict: "deny", Rule: "privilege-escalation", Action: "sudo ls"})
	auditor.Record(approval.Record{Tool: "write_file", Verdict: "allow", Action: "write_file notes.md"})

	next, _ = m.handleSlashCommand("/audit")
	m = next.(Model)
	last = m.entries[len(m.entries)-1]
	for _, want := range []string{"sudo ls", "deny", "write_file notes.md", "allow", "privilege-escalation"} {
		if !strings.Contains(last.Content, want) {
			t.Fatalf("/audit output missing %q:\n%s", want, last.Content)
		}
	}
}

// TestAuditCommandWithoutAnAuditor keeps /audit honest instead of empty.
func TestAuditCommandWithoutAnAuditor(t *testing.T) {
	approval.SetAuditor(nil)
	m := newSlashTestModel(t, 100, 30)
	next, _ := m.handleSlashCommand("/audit")
	m = next.(Model)
	last := m.entries[len(m.entries)-1]
	if !strings.Contains(last.Content, "not enabled") {
		t.Fatalf("message = %q", last.Content)
	}
}
