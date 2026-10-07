package server

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/importer"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/swarm"
)

// Session represents an independent orchestrator agent session.
type Session struct {
	ID        string
	Title     string
	Agent     *golem.Agent
	CreatedAt time.Time
	UpdatedAt time.Time
	TurnCount  int
	ctx        context.Context
	cancel     context.CancelFunc
	unsubSwarm func()
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
		_ = sm.hub.Broadcast(WSEventMemoryAdded, sessID, map[string]any{
			"id":        ev.ID,
			"kind":      ev.Kind,
			"text":      ev.Text,
			"timestamp": ev.Timestamp,
		})
	})

	// Wire reactive swarm message pub-sub listener to broadcast to WebSocket clients
	if sb := agentInstance.SwarmBus(); sb != nil {
		sess.unsubSwarm = sb.OnMessage(func(msg swarm.Message) {
			_ = sm.hub.Broadcast(WSEventSwarmMessage, sessID, msg)
		})
	}

	_ = sm.hub.Broadcast(WSEventSessionCreated, id, SessionInfo{
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
		_ = sm.hub.Broadcast(WSEventMemoryAdded, sessID, map[string]any{
			"id":        ev.ID,
			"kind":      ev.Kind,
			"text":      ev.Text,
			"timestamp": ev.Timestamp,
		})
	})

	if sb := agentInstance.SwarmBus(); sb != nil {
		sess.unsubSwarm = sb.OnMessage(func(msg swarm.Message) {
			_ = sm.hub.Broadcast(WSEventSwarmMessage, sessID, msg)
		})
	}

	sm.sessions[id] = sess
	if sm.activeID == "" {
		sm.activeID = id
	}
	sm.mu.Unlock()

	_ = sm.hub.Broadcast(WSEventSessionCreated, id, SessionInfo{
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
	_ = sm.hub.Broadcast(WSEventSessionActive, id, map[string]string{"active_id": id})
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

	sess.cancel()
	if sess.unsubSwarm != nil {
		sess.unsubSwarm()
	}
	_ = sess.Agent.Close()
	_ = sm.hub.Broadcast(WSEventSessionDeleted, id, map[string]string{"id": id})
	return nil
}

// SendPrompt streams a user prompt to the agent session and broadcasts events.
func (sm *SessionManager) SendPrompt(ctx context.Context, sessionID, prompt string) (string, error) {
	sess, ok := sm.GetSession(sessionID)
	if !ok {
		return "", errors.Errorf("session %s not found", sessionID)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	sess.TurnCount++
	sess.UpdatedAt = time.Now().UTC()

	// Broadcast user prompt start
	_ = sm.hub.Broadcast(WSEventUserPrompt, sessionID, map[string]string{"prompt": prompt})

	handler := &sessionStreamAdapter{
		sessionID: sessionID,
		hub:       sm.hub,
	}

	response, err := sess.Agent.ProcessStream(ctx, prompt, handler)
	if err != nil {
		_ = sm.hub.Broadcast(WSEventAgentDone, sessionID, map[string]any{
			"error": err.Error(),
		})
		return "", err
	}

	_ = sm.hub.Broadcast(WSEventAgentDone, sessionID, map[string]any{
		"response": response,
	})
	return response, nil
}

type sessionStreamAdapter struct {
	sessionID string
	hub       *Hub
}

func (a *sessionStreamAdapter) OnChunk(token string) {
	_ = a.hub.Broadcast(WSEventAgentChunk, a.sessionID, map[string]string{
		"token": token,
		"delta": token,
	})
}

func (a *sessionStreamAdapter) OnToolStart(toolName, command string) {
	_ = a.hub.Broadcast(WSEventToolStart, a.sessionID, map[string]any{
		"tool":    toolName,
		"command": command,
		"args":    map[string]any{"command": command},
	})
}

func (a *sessionStreamAdapter) OnToolDone(toolName, output string, exitCode int) {
	_ = a.hub.Broadcast(WSEventToolDone, a.sessionID, map[string]any{
		"tool":      toolName,
		"output":    output,
		"exit_code": exitCode,
	})
}

func (a *sessionStreamAdapter) OnSubagentStart(role, model, prompt string) {
	_ = a.hub.Broadcast(WSEventSubagentStart, a.sessionID, map[string]string{
		"role":   role,
		"model":  model,
		"prompt": prompt,
	})
}

func (a *sessionStreamAdapter) OnSubagentTool(role, model, toolName, command, output string, exitCode int, isRunning bool) {
	_ = a.hub.Broadcast(WSEventSubagentTool, a.sessionID, map[string]any{
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
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	_ = a.hub.Broadcast(WSEventSubagentDone, a.sessionID, map[string]any{
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
		_ = s.Agent.Close()
		delete(sm.sessions, id)
	}
}
