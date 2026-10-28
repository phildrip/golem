package server

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/importer"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/swarm"
)

// Session represents an independent orchestrator agent session.
type Session struct {
	ID         string
	Title      string
	Agent      *golem.Agent
	CreatedAt  time.Time
	UpdatedAt  time.Time
	TurnCount  int
	isBusy     bool
	turnWg     sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
	unsubSwarm func()
	unsubLock  func()
	mu         sync.Mutex
}

// SessionManager manages active agent sessions and dispatches turns.
type SessionManager struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	activeID    string
	baseCfg     golem.Config
	hub         *Hub
	subagentHub func(sessionID string) golem.SubagentStreamHandler
}

// broadcast fans an event out to WebSocket subscribers. Delivery is best
// effort by design: a session nobody is watching has nothing to deliver to,
// and a failed frame is dropped by the hub's writer anyway.
func (sm *SessionManager) broadcast(eventType WSEventType, sessionID string, payload any) {
	if sm.hub == nil {
		return
	}
	_ = sm.hub.Broadcast(eventType, sessionID, payload)
}

// NewSessionManager creates a new SessionManager.
func NewSessionManager(baseCfg golem.Config, hub *Hub) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
		baseCfg:  baseCfg,
		hub:      hub,
	}
}

// CreateSession initializes a new agent session.
func (sm *SessionManager) CreateSession(ctx context.Context, title string) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	id := fmt.Sprintf("session-%d", time.Now().UnixNano())
	if title == "" {
		title = fmt.Sprintf("Session %d", len(sm.sessions)+1)
	}

	// Session storage directory: isolated per session
	cfg := sm.baseCfg
	cfg.SessionID = id
	if cfg.StorageDir == "" {
		cfg.StorageDir = golem.DefaultConfig().StorageDir
	}
	cfg.StorageDir = filepath.Join(cfg.StorageDir, id)
	sessCtx, cancel := context.WithCancel(context.Background())
	agentInstance, err := golem.New(sessCtx, cfg)
	if err != nil {
		cancel()
		return nil, errors.Wrap(err, "initializing agent for session")
	}

	sess := &Session{
		ID:        id,
		Title:     title,
		Agent:     agentInstance,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		ctx:       sessCtx,
		cancel:    cancel,
	}

	sm.sessions[id] = sess
	if sm.activeID == "" {
		sm.activeID = id
	}

	// Wire reactive memory pub-sub listener to broadcast to WebSocket clients
	sessID := id
	agentInstance.Store().OnEvent(func(ev fractal.Event) {
		sm.broadcast(WSEventMemoryAdded, sessID, map[string]any{
			"id":        ev.ID,
			"kind":      ev.Kind,
			"text":      ev.Text,
			"timestamp": ev.Timestamp,
		})
	})

	// Wire reactive swarm message pub-sub listener to broadcast to WebSocket clients
	if sb := agentInstance.SwarmBus(); sb != nil {
		sess.unsubSwarm = sb.OnMessage(func(msg swarm.Message) {
			sm.broadcast(WSEventSwarmMessage, sessID, msg)
		})
		sess.unsubLock = sb.OnLockChange(func(ev swarm.LockEvent) {
			sm.broadcast(WSEventSwarmLock, sessID, ev)
		})
	}

	sm.broadcast(WSEventSessionCreated, id, SessionInfo{
		ID:        id,
		Title:     title,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		IsActive:  sm.activeID == id,
		TurnCount: 0,
	})

	return sess, nil
}

// CreateSessionWithImport initializes a new session and imports historical events into its store.
func (sm *SessionManager) CreateSessionWithImport(ctx context.Context, title string, importPath string, importID string) (*Session, error) {
	sm.mu.Lock()
	id := fmt.Sprintf("session-%d", time.Now().UnixNano())
	if title == "" {
		title = fmt.Sprintf("Session %d", len(sm.sessions)+1)
	}

	cfg := sm.baseCfg
	cfg.SessionID = id
	if cfg.StorageDir == "" {
		cfg.StorageDir = golem.DefaultConfig().StorageDir
	}
	cfg.StorageDir = filepath.Join(cfg.StorageDir, id)

	sessCtx, cancel := context.WithCancel(context.Background())
	agentInstance, err := golem.New(sessCtx, cfg)
	if err != nil {
		sm.mu.Unlock()
		cancel()
		return nil, errors.Wrap(err, "initializing agent for session")
	}

	sess := &Session{
		ID:        id,
		Title:     title,
		Agent:     agentInstance,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		ctx:       sessCtx,
		cancel:    cancel,
	}

	// Import history BEFORE attaching real-time WebSocket broadcast listener
	// to avoid flooding the WebSocket hub and disconnecting clients
	if importPath != "" {
		count, impErr := importer.ImportFileOrDir(importPath, sess.Agent.Store(), importID)
		if impErr != nil {
			sm.mu.Unlock()
			cancel()
			_ = agentInstance.Close()
			return nil, errors.Wrap(impErr, "importing historical events")
		}
		if count > 0 {
			sess.UpdatedAt = time.Now().UTC()
		}
	}

	// Now attach the reactive OnEvent listener for subsequent turns
	sessID := id
	agentInstance.Store().OnEvent(func(ev fractal.Event) {
		sm.broadcast(WSEventMemoryAdded, sessID, map[string]any{
			"id":        ev.ID,
			"kind":      ev.Kind,
			"text":      ev.Text,
			"timestamp": ev.Timestamp,
		})
	})

	if sb := agentInstance.SwarmBus(); sb != nil {
		sess.unsubSwarm = sb.OnMessage(func(msg swarm.Message) {
			sm.broadcast(WSEventSwarmMessage, sessID, msg)
		})
		sess.unsubLock = sb.OnLockChange(func(ev swarm.LockEvent) {
			sm.broadcast(WSEventSwarmLock, sessID, ev)
		})
	}

	sm.sessions[id] = sess
	if sm.activeID == "" {
		sm.activeID = id
	}
	sm.mu.Unlock()

	sm.broadcast(WSEventSessionCreated, id, SessionInfo{
		ID:        id,
		Title:     title,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		IsActive:  sm.activeID == id,
		TurnCount: sess.Agent.Store().CountEvents(),
	})

	return sess, nil
}

// ListSessions returns metadata for all active sessions.
func (sm *SessionManager) ListSessions() []SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	list := make([]SessionInfo, 0, len(sm.sessions))
	for id, s := range sm.sessions {
		list = append(list, SessionInfo{
			ID:        id,
			Title:     s.Title,
			CreatedAt: s.CreatedAt,
			UpdatedAt: s.UpdatedAt,
			IsActive:  id == sm.activeID,
			TurnCount: s.TurnCount,
		})
	}
	return list
}

// GetSession returns a session by ID.
func (sm *SessionManager) GetSession(id string) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	sess, ok := sm.sessions[id]
	return sess, ok
}

// SetActiveSession changes the active session ID.
func (sm *SessionManager) SetActiveSession(id string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.sessions[id]; !ok {
		return false
	}
	sm.activeID = id
	sm.broadcast(WSEventSessionActive, id, map[string]string{"active_id": id})
	return true
}

// ActiveSession returns the current active session, if any.
func (sm *SessionManager) ActiveSession() (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.activeID == "" {
		return nil, false
	}
	sess, ok := sm.sessions[sm.activeID]
	return sess, ok
}

// UpdateBaseConfig updates the base configuration for future sessions.
func (sm *SessionManager) UpdateBaseConfig(cfg golem.Config) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.baseCfg = cfg
}

// TryAcquireTurn attempts to acquire turn execution on the session, returning true if acquired.
func (sm *SessionManager) TryAcquireTurn(sessionID string) (*Session, bool) {
	sess, ok := sm.GetSession(sessionID)
	if !ok {
		return nil, false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.isBusy {
		return sess, false
	}
	sess.isBusy = true
	sess.turnWg.Add(1)
	return sess, true
}

// ReleaseTurn marks the session turn execution as completed.
func (sm *SessionManager) ReleaseTurn(sessionID string) {
	if sess, ok := sm.GetSession(sessionID); ok {
		sess.mu.Lock()
		sess.isBusy = false
		sess.mu.Unlock()
		sess.turnWg.Done()
	}
}

// DeleteSession closes and removes a session.
func (sm *SessionManager) DeleteSession(id string) error {
	sm.mu.Lock()
	sess, ok := sm.sessions[id]
	if !ok {
		sm.mu.Unlock()
		return errors.Errorf("session %s not found", id)
	}
	delete(sm.sessions, id)
	if sm.activeID == id {
		sm.activeID = ""
		for otherID := range sm.sessions {
			sm.activeID = otherID
			break
		}
	}
	sm.mu.Unlock()

	// Cancel context first so ongoing turn aborts quickly
	sess.cancel()

	// Wait for any in-flight turn execution before tearing down resources
	sess.turnWg.Wait()

	if sess.unsubSwarm != nil {
		sess.unsubSwarm()
	}
	if sess.unsubLock != nil {
		sess.unsubLock()
	}
	_ = sess.Agent.Close()
	sm.broadcast(WSEventSessionDeleted, id, map[string]string{"id": id})
	return nil
}

// SendPrompt streams a user prompt to the agent session and broadcasts events.
func (sm *SessionManager) SendPrompt(ctx context.Context, sessionID, prompt string) (string, error) {
	sess, ok := sm.GetSession(sessionID)
	if !ok {
		return "", errors.Errorf("session %s not found", sessionID)
	}

	sess.mu.Lock()
	sess.TurnCount++
	sess.UpdatedAt = time.Now().UTC()
	sess.mu.Unlock()

	// Broadcast user prompt start
	sm.broadcast(WSEventUserPrompt, sessionID, map[string]string{"prompt": prompt})

	handler := &sessionStreamAdapter{
		sessionID: sessionID,
		hub:       sm.hub,
	}

	response, err := sess.Agent.ProcessStream(ctx, prompt, handler)
	if errors.Is(err, golem.ErrAwaitingApproval) {
		// The sandbox stopped the turn for a human. The prompt itself has
		// already been broadcast; the client answers it with SendApproval, so
		// this is not an error the UI should render.
		sm.broadcast(WSEventAgentDone, sessionID, map[string]any{
			"awaiting_approval": true,
		})
		return "", err
	}
	if err != nil {
		sm.broadcast(WSEventAgentDone, sessionID, map[string]any{
			"error": err.Error(),
		})
		return "", err
	}

	sm.broadcast(WSEventAgentDone, sessionID, map[string]any{
		"response": response,
	})
	return response, nil
}

// SendApproval answers a pending sandbox confirmation and resumes the turn it
// paused. A second confirmation in the same turn returns ErrAwaitingApproval
// again and is broadcast like the first.
func (sm *SessionManager) SendApproval(ctx context.Context, sessionID, callID string, approved bool) (string, error) {
	sess, ok := sm.GetSession(sessionID)
	if !ok {
		return "", errors.Errorf("session %s not found", sessionID)
	}

	handler := &sessionStreamAdapter{
		sessionID: sessionID,
		hub:       sm.hub,
	}

	response, err := sess.Agent.RespondToApproval(ctx, callID, approved, handler)
	if errors.Is(err, golem.ErrAwaitingApproval) {
		sm.broadcast(WSEventAgentDone, sessionID, map[string]any{
			"awaiting_approval": true,
		})
		return "", err
	}
	if err != nil {
		sm.broadcast(WSEventAgentDone, sessionID, map[string]any{
			"error": err.Error(),
		})
		return "", err
	}

	sm.broadcast(WSEventAgentDone, sessionID, map[string]any{
		"response": response,
	})
	return response, nil
}

type sessionStreamAdapter struct {
	sessionID   string
	hub         *Hub
	subagentMu  sync.Mutex
	subagentIDs map[string]string
}

// broadcast mirrors SessionManager.broadcast for the per-session event adapter.
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

// Close gracefully closes all active sessions.
func (sm *SessionManager) Close() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for id, s := range sm.sessions {
		s.cancel()
		if s.unsubSwarm != nil {
			s.unsubSwarm()
		}
		if s.unsubLock != nil {
			s.unsubLock()
		}
		_ = s.Agent.Close()
		delete(sm.sessions, id)
	}
}
