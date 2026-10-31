package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/graph"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/runtime"
)

func newTestSessionManager(t *testing.T) (*runtime.SessionManager, *fractal.Store, *fractal.View) {
	t.Helper()
	tmpDir := t.TempDir()
	baseCfg := golem.Config{
		Provider:          "opencode",
		BaseURL:           "http://127.0.0.1:1",
		ModelName:         "mock-model",
		APIKey:            "test-key",
		StorageDir:        filepath.Join(tmpDir, "sessions"),
		WorkspaceDir:      tmpDir,
		LiveHeadCount:     4,
		ViewBudgetBytes:   1024,
		CompactorInterval: 50 * time.Millisecond,
	}
	sm := runtime.NewSessionManager(baseCfg, nil)
	t.Cleanup(func() { sm.Close() })

	sess, err := sm.CreateSession(context.Background(), "Session Alpha")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	return sm, sess.Agent.Store(), sess.Agent.View()
}

func TestMultiSessionStateIsolationAndBackgroundStream(t *testing.T) {
	sm, store, view := newTestSessionManager(t)
	m := NewModel(nil, store, view)
	m.SetSessionManager(sm)

	// Initialize window size
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)

	alphaID := m.ActiveSessionID()
	if alphaID == "" {
		t.Fatalf("expected non-empty activeSessionID")
	}

	// Set draft and entries in Session Alpha
	m.textarea.SetValue("draft in alpha")
	m.entries = append(m.entries, ChatEntry{Type: TypeUser, Content: "hello alpha"})
	m.inputHistory = []string{"alpha cmd 1"}
	m.historyIndex = 1
	m.isLoading = true
	m.turnSeq = 7

	// Create Session Beta via Ctrl+N
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	m = next.(Model)

	betaID := m.ActiveSessionID()
	if betaID == alphaID {
		t.Fatalf("expected Ctrl+N to switch to a new session, still on %s", betaID)
	}
	if m.textarea.Value() != "" {
		t.Fatalf("expected empty draft in new session Beta, got %q", m.textarea.Value())
	}
	if m.isLoading {
		t.Fatalf("expected Beta not loading while Alpha is loading in background")
	}

	// Header should render session tabs with ● indicator on Alpha
	header := m.renderHeader()
	if !strings.Contains(header, "●") {
		t.Fatalf("expected header tabs to show ● for background-running Alpha, got:\n%s", header)
	}

	// Type a draft in Beta
	m.textarea.SetValue("draft in beta")

	// Stream chunks and tool calls from Alpha while Beta is active!
	next, _ = m.Update(streamMsg{
		SessionID: alphaID,
		Payload:   agentChunkMsg("streamed from alpha "),
	})
	m = next.(Model)
	next, _ = m.Update(streamMsg{
		SessionID: alphaID,
		Payload:   toolStartMsg{toolName: "bash", command: "ls"},
	})
	m = next.(Model)
	next, _ = m.Update(streamMsg{
		SessionID: alphaID,
		Payload:   toolDoneMsg{toolName: "bash", output: "ok", exitCode: 0},
	})
	m = next.(Model)

	// Verify Beta was NOT polluted by Alpha's background stream
	if m.streamingReply != "" {
		t.Fatalf("Beta streamingReply polluted by Alpha: %q", m.streamingReply)
	}
	for _, e := range m.entries {
		if e.ToolName == "bash" {
			t.Fatalf("Beta entries polluted by Alpha tool card")
		}
	}

	// Complete Alpha's turn in the background
	next, _ = m.Update(streamMsg{
		SessionID: alphaID,
		Payload:   agentDoneMsg{reply: "final alpha reply", seq: 7},
	})
	m = next.(Model)

	// Switch back to Alpha via SwitchSession
	next, _ = m.SwitchSession(alphaID)
	m = next.(Model)

	if m.ActiveSessionID() != alphaID {
		t.Fatalf("expected active session %s, got %s", alphaID, m.ActiveSessionID())
	}
	if m.textarea.Value() != "draft in alpha" {
		t.Fatalf("expected Alpha draft 'draft in alpha' restored, got %q", m.textarea.Value())
	}
	if m.isLoading {
		t.Fatalf("expected Alpha isLoading=false after background agentDoneMsg")
	}
	foundReply := false
	foundTool := false
	for _, e := range m.entries {
		if e.Type == TypeAgent && e.Content == "final alpha reply" {
			foundReply = true
		}
		if e.Type == TypeTool && e.ToolName == "bash" && !e.IsRunning && e.Content == "ok" {
			foundTool = true
		}
	}
	if !foundReply || !foundTool {
		t.Fatalf("expected Alpha to contain completed tool and agent reply, entries=%+v", m.entries)
	}
}

func TestSessionPickerModalAndCloseConfirmation(t *testing.T) {
	sm, store, view := newTestSessionManager(t)
	m := NewModel(nil, store, view)
	m.SetSessionManager(sm)

	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	firstID := m.ActiveSessionID()

	// Create second session via /sessions new
	m.textarea.SetValue("/sessions new Second Session")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	secondID := m.ActiveSessionID()
	if secondID == firstID {
		t.Fatalf("expected new session created via /sessions new")
	}

	// Open session picker via Ctrl+S
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(Model)
	if !m.showSessionModal {
		t.Fatalf("expected showSessionModal=true after Ctrl+S")
	}
	modalView := m.View()
	if !strings.Contains(modalView, "GOLEM SESSIONS") {
		t.Fatalf("expected session modal view, got:\n%s", modalView)
	}

	// Press '1' to jump directly to session 1
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = next.(Model)
	if m.showSessionModal {
		t.Fatalf("expected session modal to close after number key selection")
	}
	if m.ActiveSessionID() != firstID {
		t.Fatalf("expected active session %s after pressing '1', got %s", firstID, m.ActiveSessionID())
	}

	// Mark session 1 as loading and press Ctrl+W -> should require confirmation
	m.isLoading = true
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlW})
	m = next.(Model)
	if m.confirmCloseSessionID != firstID {
		t.Fatalf("expected confirmCloseSessionID=%s when closing busy session, got %q", firstID, m.confirmCloseSessionID)
	}
	if len(m.sessionOrder) != 2 {
		t.Fatalf("expected session not closed before confirmation")
	}

	// Confirm close with 'y'
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = next.(Model)
	if len(m.sessionOrder) != 1 {
		t.Fatalf("expected 1 remaining session after confirmation, got %d", len(m.sessionOrder))
	}
	if m.ActiveSessionID() != secondID {
		t.Fatalf("expected active session to fall back to %s, got %s", secondID, m.ActiveSessionID())
	}
}

func TestTUI_RestoresPersistedSessionEntriesOnSwitch(t *testing.T) {
	tmpDir := t.TempDir()
	baseCfg := golem.Config{
		Provider:          "opencode",
		BaseURL:           "http://127.0.0.1:1",
		ModelName:         "mock-model",
		APIKey:            "test-key",
		StorageDir:        tmpDir,
		WorkspaceDir:      tmpDir,
		LiveHeadCount:     4,
		ViewBudgetBytes:   1024,
		CompactorInterval: 50 * time.Millisecond,
	}

	// 1. Create a session in sm1, add conversation events, then close sm1
	sm1 := runtime.NewSessionManager(baseCfg, nil)
	sess1, err := sm1.CreateSession(context.Background(), "Historic Work")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	historicID := sess1.ID
	sess1.Agent.Store().AppendEvent(fractal.KindUser, "user historic prompt")
	sess1.Agent.Store().AppendEvent(fractal.KindTalk, "agent historic answer")
	sm1.Close()

	// 2. Open Golem main agent on base storage dir
	mainAgent, err := golem.New(context.Background(), baseCfg)
	if err != nil {
		t.Fatalf("golem.New: %v", err)
	}
	defer mainAgent.Close()

	// 3. Create SessionManager which restores sessions.json
	sm2 := runtime.NewSessionManager(baseCfg, nil)
	defer sm2.Close()
	sm2.AdoptSession("default", "Main Session", mainAgent, baseCfg)

	// 4. Construct TUI Model and attach SessionManager
	m := NewModel(mainAgent, mainAgent.Store(), mainAgent.View())
	m.SetSessionManager(sm2)

	// Window resize
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)

	// Should have Main Session and Historic Work
	if m.SessionCount() != 2 {
		t.Fatalf("expected 2 sessions in TUI, got %d", m.SessionCount())
	}

	// Switch to Historic Work
	next, _ = m.SwitchSession(historicID)
	m = next.(Model)

	if m.ActiveSessionID() != historicID {
		t.Fatalf("expected active session %s, got %s", historicID, m.ActiveSessionID())
	}

	// Entries should have been restored from the store
	foundUser := false
	foundAgent := false
	for _, entry := range m.entries {
		if entry.Type == TypeUser && strings.Contains(entry.Content, "user historic prompt") {
			foundUser = true
		}
		if entry.Type == TypeAgent && strings.Contains(entry.Content, "agent historic answer") {
			foundAgent = true
		}
	}
	if !foundUser || !foundAgent {
		t.Fatalf("expected restored entries in Historic Work session, got: %+v", m.entries)
	}
}

func TestRenderGraphDAGAndModal(t *testing.T) {
	spec := &graph.Spec{
		Name: "verify-fix",
		Budgets: &graph.Budgets{
			MaxNodes:       8,
			MaxWallSeconds: 60,
			MaxTokens:      50000,
		},
		Nodes: []graph.Node{
			{ID: "plan", Kind: graph.KindAgent, Role: "planner", Model: "gemini-2.5-pro"},
			{ID: "implement", Kind: graph.KindAgent, Role: "coder"},
			{ID: "verify", Kind: graph.KindAgent, Role: "verifier"},
			{ID: "fix", Kind: graph.KindAgent, Role: "fixer"},
			{ID: "summarize", Kind: graph.KindAgent, Role: "writer"},
		},
		Edges: [][]string{
			{graph.Start, "plan"},
			{"plan", "implement"},
			{"implement", "verify"},
			{"verify", "summarize", "pass"},
			{"verify", "fix", "fail"},
			{"fix", "verify"},
		},
	}
	rec := &graph.RunRecord{
		RunID:         "run-001",
		Graph:         "verify-fix",
		Status:        graph.RunCompleted,
		Tokens:        1400,
		CachedTokens:  1050,
		CacheHitRatio: 0.75,
		Duration:      3080 * time.Millisecond,
		Nodes: []graph.NodeResult{
			{ID: "plan", Role: "planner", Activations: 1, Duration: 420 * time.Millisecond, Tokens: 200},
			{ID: "implement", Role: "coder", Activations: 1, Duration: 950 * time.Millisecond, Tokens: 400},
			{ID: "verify", Role: "verifier", Activations: 2, Duration: 610 * time.Millisecond, Tokens: 300},
			{ID: "fix", Role: "fixer", Activations: 1, Duration: 800 * time.Millisecond, Tokens: 350},
			{ID: "summarize", Role: "writer", Activations: 1, Duration: 300 * time.Millisecond, Tokens: 150},
		},
	}

	dag := RenderGraphDAG(spec, rec, nil, 100)
	for _, want := range []string{
		"DAG: verify-fix",
		"plan",
		"implement",
		"verify",
		"summarize [route=pass]",
		"fix [route=fail]",
		"Cache: 1050 (75.0%)",
		"verifier(x2)",
	} {
		if !strings.Contains(dag, want) {
			t.Errorf("expected RenderGraphDAG output to contain %q, got:\n%s", want, dag)
		}
	}

	// Test /graph slash command opens modal and renders live subagent nodes
	sm, store, view := newTestSessionManager(t)
	m := NewModel(nil, store, view)
	m.SetSessionManager(sm)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)

	// Simulate live graph subagent telemetry ("<graph>/<role>·<node>")
	next, _ = m.Update(subagentStartMsg{role: "verify-fix/coder·implement", model: "flash", prompt: "write fix"})
	m = next.(Model)
	next, _ = m.Update(subagentDoneMsg{role: "verify-fix/coder·implement", model: "flash", result: "done", duration: 150 * time.Millisecond})
	m = next.(Model)

	m.textarea.SetValue("/graph")
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if !m.showGraphModal {
		t.Fatalf("expected /graph to open graph modal")
	}
	viewOut := m.View()
	if !strings.Contains(viewOut, "GOLEM WORKFLOW GRAPHS") || !strings.Contains(viewOut, "coder") {
		t.Fatalf("expected graph modal view to show live subagent node 'coder', got:\n%s", viewOut)
	}
}
