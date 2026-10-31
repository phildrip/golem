package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/runtime"
)

// Session is an alias for runtime.Session so existing server callers remain compatible.
type Session = runtime.Session

// SessionManager is an alias for runtime.SessionManager.
type SessionManager = runtime.SessionManager

// NewSessionManager creates a runtime.SessionManager wired to broadcast events over hub.
func NewSessionManager(baseCfg golem.Config, hub *Hub) *SessionManager {
	return runtime.NewSessionManager(baseCfg, func(eventType string, sessionID string, payload any) {
		if hub == nil {
			return
		}
		_ = hub.Broadcast(WSEventType(eventType), sessionID, payload)
	})
}

type sessionStreamAdapter struct {
	sessionID   string
	hub         *Hub
	subagentMu  sync.Mutex
	subagentIDs map[string]string
}

func (a *sessionStreamAdapter) broadcast(eventType WSEventType, sessionID string, payload any) {
	if a.hub == nil {
		return
	}
	_ = a.hub.Broadcast(eventType, sessionID, payload)
}

func (a *sessionStreamAdapter) OnChunk(token string) {
	a.broadcast(WSEventAgentChunk, a.sessionID, map[string]string{
		"token": token,
		"delta": token,
	})
}

func (a *sessionStreamAdapter) OnApprovalRequest(req approval.Request) {
	a.broadcast(WSEventApprovalRequest, a.sessionID, map[string]any{
		"call_id": req.CallID,
		"tool":    req.Tool,
		"hint":    req.Hint,
		"action":  req.Action,
		"reason":  req.Reason,
	})
}

func (a *sessionStreamAdapter) OnToolStart(toolName, command string) {
	a.broadcast(WSEventToolStart, a.sessionID, map[string]any{
		"tool":    toolName,
		"command": command,
		"args":    map[string]any{"command": command},
	})
}

func (a *sessionStreamAdapter) OnToolDone(toolName, output string, exitCode int) {
	a.broadcast(WSEventToolDone, a.sessionID, map[string]any{
		"tool":      toolName,
		"output":    output,
		"exit_code": exitCode,
	})
}

func (a *sessionStreamAdapter) OnSubagentStart(role, model, prompt string) {
	a.subagentMu.Lock()
	if a.subagentIDs == nil {
		a.subagentIDs = make(map[string]string)
	}
	id := fmt.Sprintf("subagent-%s-%d", role, time.Now().UnixNano())
	a.subagentIDs[role] = id
	a.subagentMu.Unlock()

	a.broadcast(WSEventSubagentStart, a.sessionID, map[string]string{
		"id":     id,
		"name":   role,
		"role":   role,
		"model":  model,
		"prompt": prompt,
	})
}

func (a *sessionStreamAdapter) OnSubagentTool(role, model, toolName, command, output string, exitCode int, isRunning bool) {
	a.subagentMu.Lock()
	id := a.subagentIDs[role]
	a.subagentMu.Unlock()

	a.broadcast(WSEventSubagentTool, a.sessionID, map[string]any{
		"id":         id,
		"role":       role,
		"model":      model,
		"tool":       toolName,
		"command":    command,
		"output":     output,
		"exit_code":  exitCode,
		"is_running": isRunning,
	})
}

func (a *sessionStreamAdapter) OnSubagentDone(role, model, result string, duration time.Duration, err error) {
	a.subagentMu.Lock()
	id := a.subagentIDs[role]
	a.subagentMu.Unlock()

	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	a.broadcast(WSEventSubagentDone, a.sessionID, map[string]any{
		"id":       id,
		"role":     role,
		"model":    model,
		"result":   result,
		"duration": duration.String(),
		"error":    errStr,
	})
}
