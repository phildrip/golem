package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/importer"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/swarm"
)

const (
	// DefaultMaxOpenSessions caps concurrently open bbolt stores per SessionManager
	// using LRU suspension.
	DefaultMaxOpenSessions = 3
	// DefaultIdleTimeout is the idle window after which inactive open sessions
	// can be suspended.
	DefaultIdleTimeout = 5 * time.Minute
)

// EventType names transport-agnostic session and stream events.
const (
	EventSessionCreated  = "session_created"
	EventSessionDeleted  = "session_deleted"
	EventSessionActive   = "session_active"
	EventUserPrompt      = "user_prompt"
	EventAgentChunk      = "agent_chunk"
	EventAgentDone       = "agent_done"
	EventToolStart       = "tool_start"
	EventToolDone        = "tool_done"
	EventSubagentStart   = "subagent_start"
	EventSubagentTool    = "subagent_tool"
	EventSubagentDone    = "subagent_done"
	EventSwarmMessage    = "swarm_message"
	EventSwarmLock       = "swarm_lock"
	EventMemoryAdded     = "memory_added"
	EventApprovalRequest = "approval_request"
	EventTokenUsage      = "token_usage"
)

// BroadcastFunc is an optional callback that receives session lifecycle and
// streaming events (e.g. wired to a WebSocket hub by pkg/server).
type BroadcastFunc func(eventType string, sessionID string, payload any)

// SessionInfo provides metadata for a managed agent session.
type SessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	IsActive  bool      `json:"is_active"`
	TurnCount int       `json:"turn_count"`
	Suspended bool      `json:"suspended,omitempty"`
	IsBusy    bool      `json:"is_busy,omitempty"`
}

var sessionSeq atomic.Int64

// Session represents an independent orchestrator agent session that can be
// suspended (releasing its bbolt lock and stopping its compactor worker) and
// resumed on demand.
type Session struct {
	ID           string
	Title        string
	Agent        *golem.Agent
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastActiveAt time.Time
	TurnCount    int

	cfg          golem.Config
	suspended    bool
	cachedEvents []fractal.Event
	isBusy       bool
	turnWg       sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	unsubSwarm   func()
	unsubLock    func()
	onWire       func(*Session)
	manager      *SessionManager
	mu           sync.Mutex
}

// Context returns the session's cancelable lifecycle context.
func (s *Session) Context() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctx
}

// StorageDir returns the directory backing this session's fractal store.
func (s *Session) StorageDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.StorageDir
}

// IsSuspended reports whether the session's bbolt store and compactor are currently suspended.
func (s *Session) IsSuspended() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.suspended
}

// IsBusy reports whether a turn is currently executing on this session.
func (s *Session) IsBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isBusy
}

// SwitchProfile safely switches the active profile on the session's Agent under lock.
func (s *Session) SwitchProfile(ctx context.Context, name string) (config.ProfileConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Agent == nil {
		return config.ProfileConfig{}, errors.New("session is suspended")
	}
	prof, err := s.Agent.SwitchProfile(ctx, name)
	if err == nil {
		s.cfg = s.Agent.Config()
	}
	return prof, err
}

// Events returns the session's fractal events, reading from the in-memory
// snapshot when the session is suspended so callers do not need to reopen bbolt.
func (s *Session) Events() []fractal.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.suspended && s.Agent != nil && s.Agent.Store() != nil {
		events := s.Agent.Store().GetAllEvents()
		s.cachedEvents = events
		return events
	}
	if len(s.cachedEvents) == 0 && s.cfg.StorageDir != "" {
		if st, err := fractal.OpenStore(s.cfg.StorageDir); err == nil {
			s.cachedEvents = st.GetAllEvents()
			_ = st.Close()
		}
	}
	out := make([]fractal.Event, len(s.cachedEvents))
	copy(out, s.cachedEvents)
	return out
}

// Suspend stops the session's compactor goroutine and closes its bbolt store,
// keeping session metadata and cached transcript events in memory for instant display.
func (s *Session) Suspend(_ context.Context) error {
	s.mu.Lock()
	if s.isBusy {
		s.mu.Unlock()
		return errors.Errorf("cannot suspend session %s while a turn is in flight", s.ID)
	}
	if s.suspended || s.Agent == nil {
		s.suspended = true
		s.mu.Unlock()
		return nil
	}
	if s.Agent.Store() != nil {
		s.cachedEvents = s.Agent.Store().GetAllEvents()
	}
	unsubSwarm := s.unsubSwarm
	unsubLock := s.unsubLock
	s.unsubSwarm = nil
	s.unsubLock = nil
	cancel := s.cancel
	ag := s.Agent
	s.Agent = nil
	s.suspended = true
	s.mu.Unlock()

	if unsubSwarm != nil {
		unsubSwarm()
	}
	if unsubLock != nil {
		unsubLock()
	}
	if cancel != nil {
		cancel()
	}
	if ag != nil {
		return ag.Close()
	}
	return nil
}

// Resume reopens a suspended session's bbolt store and restarts its compactor worker.
func (s *Session) Resume(ctx context.Context) error {
	s.mu.Lock()
	if !s.suspended && s.Agent != nil {
		s.LastActiveAt = time.Now().UTC()
		s.mu.Unlock()
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessCtx, cancel := context.WithCancel(context.Background())
	agentInstance, err := golem.New(sessCtx, s.cfg)
	if err != nil {
		s.mu.Unlock()
		cancel()
		return errors.Wrapf(err, "resuming session %s", s.ID)
	}
	s.Agent = agentInstance
	s.ctx = sessCtx
	s.cancel = cancel
	s.suspended = false
	now := time.Now().UTC()
	s.LastActiveAt = now
	onWire := s.onWire
	mgr := s.manager
	s.mu.Unlock()

	if onWire != nil {
		onWire(s)
	}
	if mgr != nil {
		mgr.enforceOpenCap(ctx, s.ID)
	}
	return nil
}

// PersistedSessionMeta records a session's durable metadata for sessions.json.
type PersistedSessionMeta struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	RelDir       string    `json:"rel_dir,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LastActiveAt time.Time `json:"last_active_at"`
	TurnCount    int       `json:"turn_count"`
}

// SessionRegistry is the JSON layout for <StorageDir>/sessions.json.
type SessionRegistry struct {
	ActiveID string                 `json:"active_id,omitempty"`
	Order    []string               `json:"order"`
	Sessions []PersistedSessionMeta `json:"sessions"`
}

// SessionManager manages active and suspended agent sessions and dispatches turns.
type SessionManager struct {
	mu              sync.RWMutex
	sessions        map[string]*Session
	order           []string
	activeID        string
	baseCfg         golem.Config
	broadcastFn     BroadcastFunc
	maxOpenSessions int
	idleTimeout     time.Duration
}

// NewSessionManager creates a new SessionManager and restores persisted sessions from disk.
func NewSessionManager(baseCfg golem.Config, broadcastFn BroadcastFunc) *SessionManager {
	sm := &SessionManager{
		sessions:        make(map[string]*Session),
		baseCfg:         baseCfg,
		broadcastFn:     broadcastFn,
		maxOpenSessions: DefaultMaxOpenSessions,
		idleTimeout:     DefaultIdleTimeout,
	}
	sm.mu.Lock()
	sm.loadRegistryLocked()
	sm.mu.Unlock()
	return sm
}

func (sm *SessionManager) registryPath() string {
	dir := sm.baseCfg.StorageDir
	if dir == "" {
		dir = golem.DefaultConfig().StorageDir
	}
	return filepath.Join(dir, "sessions.json")
}

func (sm *SessionManager) loadRegistryLocked() {
	baseDir := sm.baseCfg.StorageDir
	if baseDir == "" {
		baseDir = golem.DefaultConfig().StorageDir
	}

	target := sm.registryPath()
	data, err := os.ReadFile(target)
	var reg SessionRegistry
	hasRegistryFile := false
	if err == nil {
		if json.Unmarshal(data, &reg) == nil {
			hasRegistryFile = true
		}
	}

	if hasRegistryFile {
		for _, meta := range reg.Sessions {
			if meta.ID == "" {
				continue
			}
			sessDir := baseDir
			if meta.RelDir != "" {
				if filepath.IsAbs(meta.RelDir) {
					sessDir = meta.RelDir
				} else {
					sessDir = filepath.Join(baseDir, meta.RelDir)
				}
			}

			dbPath := filepath.Join(sessDir, "fractal.db")
			if _, err := os.Stat(dbPath); err != nil && sessDir != baseDir {
				continue
			}

			cfg := sm.baseCfg
			cfg.SessionID = meta.ID
			cfg.StorageDir = sessDir

			sess := &Session{
				ID:           meta.ID,
				Title:        meta.Title,
				CreatedAt:    meta.CreatedAt,
				UpdatedAt:    meta.UpdatedAt,
				LastActiveAt: meta.LastActiveAt,
				TurnCount:    meta.TurnCount,
				cfg:          cfg,
				suspended:    true,
				onWire:       sm.wireSessionCallbacks,
				manager:      sm,
			}
			sm.sessions[meta.ID] = sess
		}
		for _, id := range reg.Order {
			if _, ok := sm.sessions[id]; ok {
				sm.order = append(sm.order, id)
			}
		}
		if _, ok := sm.sessions[reg.ActiveID]; ok {
			sm.activeID = reg.ActiveID
		}
	}

	// Auto-discover past unindexed session-* directories on disk
	entries, err := os.ReadDir(baseDir)
	if err == nil {
		discoveredAny := false
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "session-") {
				continue
			}
			sessID := entry.Name()
			if _, exists := sm.sessions[sessID]; exists {
				continue
			}

			dbPath := filepath.Join(baseDir, sessID, "fractal.db")
			info, err := os.Stat(dbPath)
			if err != nil || info.IsDir() {
				continue
			}

			modTime := info.ModTime().UTC()
			sessDir := filepath.Join(baseDir, sessID)

			title := fmt.Sprintf("Session %d", len(sm.sessions)+1)
			turnCount := 0
			if s, err := fractal.OpenStore(sessDir); err == nil {
				evs := s.GetAllEvents()
				for _, ev := range evs {
					if ev.Kind == fractal.KindUser && strings.TrimSpace(ev.Text) != "" {
						turnCount++
						if title == fmt.Sprintf("Session %d", len(sm.sessions)+1) {
							promptLine := strings.SplitN(strings.TrimSpace(ev.Text), "\n", 2)[0]
							runes := []rune(promptLine)
							if len(runes) > 24 {
								promptLine = string(runes[:24]) + "..."
							}
							title = promptLine
						}
					}
				}
				_ = s.Close()
			}

			cfg := sm.baseCfg
			cfg.SessionID = sessID
			cfg.StorageDir = sessDir

			sess := &Session{
				ID:           sessID,
				Title:        title,
				CreatedAt:    modTime,
				UpdatedAt:    modTime,
				LastActiveAt: modTime,
				TurnCount:    turnCount,
				cfg:          cfg,
				suspended:    true,
				onWire:       sm.wireSessionCallbacks,
				manager:      sm,
			}
			sm.sessions[sessID] = sess
			sm.order = append(sm.order, sessID)
			discoveredAny = true
		}

		if discoveredAny {
			_ = sm.saveRegistryLocked()
		}
	}
}

func (sm *SessionManager) saveRegistryLocked() error {
	baseDir := sm.baseCfg.StorageDir
	if baseDir == "" {
		baseDir = golem.DefaultConfig().StorageDir
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return err
	}

	reg := SessionRegistry{
		ActiveID: sm.activeID,
		Order:    make([]string, len(sm.order)),
		Sessions: make([]PersistedSessionMeta, 0, len(sm.sessions)),
	}
	copy(reg.Order, sm.order)

	for _, id := range sm.order {
		sess, ok := sm.sessions[id]
		if !ok {
			continue
		}
		sess.mu.Lock()
		title := sess.Title
		cAt := sess.CreatedAt
		uAt := sess.UpdatedAt
		lAt := sess.LastActiveAt
		turnCount := sess.TurnCount
		storageDir := sess.cfg.StorageDir
		sess.mu.Unlock()

		relDir := ""
		if storageDir != "" && storageDir != baseDir {
			if rel, err := filepath.Rel(baseDir, storageDir); err == nil && !strings.HasPrefix(rel, "..") {
				relDir = rel
			} else {
				relDir = storageDir
			}
		}

		reg.Sessions = append(reg.Sessions, PersistedSessionMeta{
			ID:           id,
			Title:        title,
			RelDir:       relDir,
			CreatedAt:    cAt,
			UpdatedAt:    uAt,
			LastActiveAt: lAt,
			TurnCount:    turnCount,
		})
	}

	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}

	target := sm.registryPath()
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// SetMaxOpenSessions configures the maximum number of concurrently open session
// stores before LRU suspension kicks in (0 disables the cap).
func (sm *SessionManager) SetMaxOpenSessions(cap int) {
	sm.mu.Lock()
	sm.maxOpenSessions = cap
	active := sm.activeID
	sm.mu.Unlock()
	if cap > 0 {
		sm.enforceOpenCap(context.Background(), active)
	}
}

// MaxOpenSessions returns the configured open-session cap.
func (sm *SessionManager) MaxOpenSessions() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.maxOpenSessions
}

// SetIdleTimeout configures the idle duration after which SuspendIdleSessions
// suspends inactive open sessions.
func (sm *SessionManager) SetIdleTimeout(d time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.idleTimeout = d
}

// IdleTimeout returns the configured idle suspension window.
func (sm *SessionManager) IdleTimeout() time.Duration {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.idleTimeout
}

// OpenSessionCount returns the number of currently open (non-suspended) sessions.
func (sm *SessionManager) OpenSessionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	count := 0
	for _, s := range sm.sessions {
		if !s.IsSuspended() {
			count++
		}
	}
	return count
}

func (sm *SessionManager) broadcast(eventType string, sessionID string, payload any) {
	if sm.broadcastFn == nil {
		return
	}
	sm.broadcastFn(eventType, sessionID, payload)
}

func (sm *SessionManager) wireSessionCallbacks(sess *Session) {
	sess.mu.Lock()
	ag := sess.Agent
	sessID := sess.ID
	sess.mu.Unlock()
	if ag == nil {
		return
	}
	if ag.Store() != nil {
		ag.Store().OnEvent(func(ev fractal.Event) {
			sm.broadcast(EventMemoryAdded, sessID, map[string]any{
				"id":        ev.ID,
				"kind":      ev.Kind,
				"text":      ev.Text,
				"timestamp": ev.Timestamp,
			})
		})
	}
	if sb := ag.SwarmBus(); sb != nil {
		unsubSwarm := sb.OnMessage(func(msg swarm.Message) {
			sm.broadcast(EventSwarmMessage, sessID, msg)
		})
		unsubLock := sb.OnLockChange(func(ev swarm.LockEvent) {
			sm.broadcast(EventSwarmLock, sessID, ev)
		})
		sess.mu.Lock()
		sess.unsubSwarm = unsubSwarm
		sess.unsubLock = unsubLock
		sess.mu.Unlock()
	}
}

// AdoptSession registers an already-initialized Agent into the SessionManager
// without reopening its store (used by the TUI on startup).
func (sm *SessionManager) AdoptSession(id, title string, agentInstance *golem.Agent, cfg golem.Config) *Session {
	sm.mu.Lock()
	if id == "" {
		id = fmt.Sprintf("session-%d-%d", time.Now().UnixNano(), sessionSeq.Add(1))
	}
	if title == "" {
		title = fmt.Sprintf("Session %d", len(sm.sessions)+1)
	}
	now := time.Now().UTC()
	sessCtx, cancel := context.WithCancel(context.Background())
	var cached []fractal.Event
	turnCount := 0
	if agentInstance != nil && agentInstance.Store() != nil {
		cached = agentInstance.Store().GetAllEvents()
		for _, ev := range cached {
			if ev.Kind == fractal.KindUser {
				turnCount++
			}
		}
	}

	var sess *Session
	existing, exists := sm.sessions[id]
	if exists {
		sess = existing
		sess.mu.Lock()
		sess.Agent = agentInstance
		sess.suspended = false
		sess.ctx = sessCtx
		sess.cancel = cancel
		if title != "" {
			sess.Title = title
		}
		if cached != nil {
			sess.cachedEvents = cached
			sess.TurnCount = turnCount
		}
		sess.cfg = cfg
		sess.LastActiveAt = now
		sess.UpdatedAt = now
		sess.mu.Unlock()
	} else {
		sess = &Session{
			ID:           id,
			Title:        title,
			Agent:        agentInstance,
			CreatedAt:    now,
			UpdatedAt:    now,
			LastActiveAt: now,
			TurnCount:    turnCount,
			cfg:          cfg,
			cachedEvents: cached,
			ctx:          sessCtx,
			cancel:       cancel,
			onWire:       sm.wireSessionCallbacks,
			manager:      sm,
		}
		sm.sessions[id] = sess
		sm.order = append(sm.order, id)
	}

	if sm.activeID == "" || id == "default" {
		sm.activeID = id
	}
	_ = sm.saveRegistryLocked()
	sm.mu.Unlock()

	sm.wireSessionCallbacks(sess)
	sm.enforceOpenCap(context.Background(), id)
	return sess
}

// CreateSession initializes a new agent session.
func (sm *SessionManager) CreateSession(ctx context.Context, title string) (*Session, error) {
	sm.mu.Lock()
	id := fmt.Sprintf("session-%d-%d", time.Now().UnixNano(), sessionSeq.Add(1))
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

	now := time.Now().UTC()
	sess := &Session{
		ID:           id,
		Title:        title,
		Agent:        agentInstance,
		CreatedAt:    now,
		UpdatedAt:    now,
		LastActiveAt: now,
		cfg:          cfg,
		ctx:          sessCtx,
		cancel:       cancel,
		onWire:       sm.wireSessionCallbacks,
		manager:      sm,
	}

	sm.sessions[id] = sess
	sm.order = append(sm.order, id)
	if sm.activeID == "" {
		sm.activeID = id
	}
	isActive := sm.activeID == id
	_ = sm.saveRegistryLocked()
	sm.mu.Unlock()

	sm.wireSessionCallbacks(sess)
	sm.enforceOpenCap(ctx, id)

	sm.broadcast(EventSessionCreated, id, SessionInfo{
		ID:        id,
		Title:     title,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		IsActive:  isActive,
		TurnCount: 0,
	})

	return sess, nil
}

// CreateSessionWithImport initializes a new session and imports historical events into its store.
func (sm *SessionManager) CreateSessionWithImport(ctx context.Context, title string, importPath string, importID string) (*Session, error) {
	sm.mu.Lock()
	id := fmt.Sprintf("session-%d-%d", time.Now().UnixNano(), sessionSeq.Add(1))
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

	now := time.Now().UTC()
	sess := &Session{
		ID:           id,
		Title:        title,
		Agent:        agentInstance,
		CreatedAt:    now,
		UpdatedAt:    now,
		LastActiveAt: now,
		cfg:          cfg,
		ctx:          sessCtx,
		cancel:       cancel,
		onWire:       sm.wireSessionCallbacks,
		manager:      sm,
	}

	// Import history BEFORE attaching real-time broadcast listeners
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
	turnCount := sess.Agent.Store().CountEvents()
	sess.cachedEvents = sess.Agent.Store().GetAllEvents()

	sm.sessions[id] = sess
	sm.order = append(sm.order, id)
	if sm.activeID == "" {
		sm.activeID = id
	}
	isActive := sm.activeID == id
	_ = sm.saveRegistryLocked()
	sm.mu.Unlock()

	sm.wireSessionCallbacks(sess)
	sm.enforceOpenCap(ctx, id)

	sm.broadcast(EventSessionCreated, id, SessionInfo{
		ID:        id,
		Title:     title,
		CreatedAt: sess.CreatedAt,
		UpdatedAt: sess.UpdatedAt,
		IsActive:  isActive,
		TurnCount: turnCount,
	})

	return sess, nil
}

// enforceOpenCap suspends the least-recently-active open sessions when the
// number of open stores exceeds maxOpenSessions.
func (sm *SessionManager) enforceOpenCap(ctx context.Context, keepID string) {
	sm.mu.RLock()
	cap := sm.maxOpenSessions
	activeID := sm.activeID
	if cap <= 0 {
		sm.mu.RUnlock()
		return
	}
	type candidate struct {
		sess       *Session
		lastActive time.Time
		isKeep     bool
	}
	var open []candidate
	for _, s := range sm.sessions {
		s.mu.Lock()
		suspended := s.suspended
		busy := s.isBusy
		last := s.LastActiveAt
		id := s.ID
		s.mu.Unlock()
		if suspended || busy {
			continue
		}
		open = append(open, candidate{
			sess:       s,
			lastActive: last,
			isKeep:     id == keepID || id == activeID,
		})
	}
	sm.mu.RUnlock()

	if len(open) <= cap {
		return
	}
	// Sort oldest first; prefer evicting non-active/non-keep sessions first.
	sort.Slice(open, func(i, j int) bool {
		if open[i].isKeep != open[j].isKeep {
			return !open[i].isKeep
		}
		return open[i].lastActive.Before(open[j].lastActive)
	})
	toEvict := len(open) - cap
	for i := 0; i < toEvict; i++ {
		_ = open[i].sess.Suspend(ctx)
	}
}

// SuspendIdleSessions suspends any non-busy open session whose LastActiveAt is
// older than the configured idleTimeout relative to now, returning the count suspended.
func (sm *SessionManager) SuspendIdleSessions(ctx context.Context, now time.Time) int {
	sm.mu.RLock()
	timeout := sm.idleTimeout
	if timeout <= 0 {
		sm.mu.RUnlock()
		return 0
	}
	var candidates []*Session
	for _, s := range sm.sessions {
		s.mu.Lock()
		shouldSuspend := !s.suspended && !s.isBusy && now.Sub(s.LastActiveAt) >= timeout
		s.mu.Unlock()
		if shouldSuspend {
			candidates = append(candidates, s)
		}
	}
	sm.mu.RUnlock()

	suspended := 0
	for _, s := range candidates {
		if err := s.Suspend(ctx); err == nil {
			suspended++
		}
	}
	return suspended
}

// ListSessions returns metadata for all sessions in creation order.
func (sm *SessionManager) ListSessions() []SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	list := make([]SessionInfo, 0, len(sm.sessions))
	for _, id := range sm.order {
		s, ok := sm.sessions[id]
		if !ok {
			continue
		}
		s.mu.Lock()
		info := SessionInfo{
			ID:        id,
			Title:     s.Title,
			CreatedAt: s.CreatedAt,
			UpdatedAt: s.UpdatedAt,
			IsActive:  id == sm.activeID,
			TurnCount: s.TurnCount,
			Suspended: s.suspended,
			IsBusy:    s.isBusy,
		}
		s.mu.Unlock()
		list = append(list, info)
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

// SetActiveSession changes the active session ID and resumes the session if it was suspended.
func (sm *SessionManager) SetActiveSession(id string) bool {
	sm.mu.Lock()
	sess, ok := sm.sessions[id]
	if !ok {
		sm.mu.Unlock()
		return false
	}
	sm.activeID = id
	_ = sm.saveRegistryLocked()
	sm.mu.Unlock()

	_ = sess.Resume(context.Background())
	sm.broadcast(EventSessionActive, id, map[string]string{"active_id": id})
	return true
}

// RenameSession updates the human-readable title of a session and persists it.
func (sm *SessionManager) RenameSession(id, title string) bool {
	sm.mu.Lock()
	sess, ok := sm.sessions[id]
	if !ok {
		sm.mu.Unlock()
		return false
	}
	title = strings.TrimSpace(title)
	if title == "" {
		sm.mu.Unlock()
		return false
	}
	sess.mu.Lock()
	sess.Title = title
	sess.UpdatedAt = time.Now().UTC()
	sess.mu.Unlock()
	_ = sm.saveRegistryLocked()
	sm.mu.Unlock()
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

// ActiveSessionID returns the current active session ID under read lock.
func (sm *SessionManager) ActiveSessionID() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.activeID
}

// UpdateBaseConfig updates the base configuration for future sessions.
func (sm *SessionManager) UpdateBaseConfig(cfg golem.Config) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.baseCfg = cfg
}

// TryAcquireTurn attempts to acquire turn execution on the session, auto-resuming
// a suspended session first.
func (sm *SessionManager) TryAcquireTurn(sessionID string) (*Session, bool) {
	sess, ok := sm.GetSession(sessionID)
	if !ok {
		return nil, false
	}
	if sess.IsSuspended() {
		if err := sess.Resume(context.Background()); err != nil {
			return sess, false
		}
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.isBusy {
		return sess, false
	}
	sess.isBusy = true
	sess.LastActiveAt = time.Now().UTC()
	sess.turnWg.Add(1)
	return sess, true
}

// ReleaseTurn marks the session turn execution as completed.
func (s *Session) ReleaseTurn() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.isBusy = false
	s.LastActiveAt = time.Now().UTC()
	s.mu.Unlock()
	s.turnWg.Done()
}

// ReleaseTurn marks the session turn execution as completed by session ID.
func (sm *SessionManager) ReleaseTurn(sessionID string) {
	if sess, ok := sm.GetSession(sessionID); ok {
		sess.ReleaseTurn()
	}
}

// DeleteSession closes and removes a session, selecting a neighbour if active.
func (sm *SessionManager) DeleteSession(id string) error {
	sm.mu.Lock()
	sess, ok := sm.sessions[id]
	if !ok {
		sm.mu.Unlock()
		return errors.Errorf("session %s not found", id)
	}
	delete(sm.sessions, id)
	idx := -1
	for i, oid := range sm.order {
		if oid == id {
			idx = i
			sm.order = append(sm.order[:i], sm.order[i+1:]...)
			break
		}
	}
	var newActive *Session
	if sm.activeID == id {
		sm.activeID = ""
		if len(sm.order) > 0 {
			pick := idx
			if pick >= len(sm.order) {
				pick = len(sm.order) - 1
			}
			if pick < 0 {
				pick = 0
			}
			sm.activeID = sm.order[pick]
			newActive = sm.sessions[sm.activeID]
		}
	}
	_ = sm.saveRegistryLocked()
	sm.mu.Unlock()

	if newActive != nil && newActive.IsSuspended() {
		_ = newActive.Resume(context.Background())
	}

	sess.mu.Lock()
	cancel := sess.cancel
	unsubSwarm := sess.unsubSwarm
	unsubLock := sess.unsubLock
	ag := sess.Agent
	storageDir := sess.cfg.StorageDir
	sess.unsubSwarm = nil
	sess.unsubLock = nil
	sess.Agent = nil
	sess.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	sess.turnWg.Wait()

	if unsubSwarm != nil {
		unsubSwarm()
	}
	if unsubLock != nil {
		unsubLock()
	}
	if ag != nil {
		_ = ag.Close()
	}

	// Remove dedicated storage directory from disk if it was a subfolder
	baseDir := sm.baseCfg.StorageDir
	if baseDir == "" {
		baseDir = golem.DefaultConfig().StorageDir
	}
	if storageDir != "" && storageDir != baseDir && strings.HasPrefix(filepath.Clean(storageDir), filepath.Clean(baseDir)) {
		_ = os.RemoveAll(storageDir)
	}

	sm.broadcast(EventSessionDeleted, id, map[string]string{"id": id})
	return nil
}

// SendPrompt streams a user prompt to the agent session and broadcasts events.
func (sm *SessionManager) SendPrompt(ctx context.Context, sessionID, prompt string) (string, error) {
	sess, ok := sm.GetSession(sessionID)
	if !ok {
		return "", errors.Errorf("session %s not found", sessionID)
	}
	if err := sess.Resume(ctx); err != nil {
		return "", err
	}

	now := time.Now().UTC()
	sess.mu.Lock()
	sess.TurnCount++
	sess.UpdatedAt = now
	sess.LastActiveAt = now
	ag := sess.Agent
	sess.mu.Unlock()

	sm.mu.Lock()
	_ = sm.saveRegistryLocked()
	sm.mu.Unlock()

	sm.broadcast(EventUserPrompt, sessionID, map[string]string{"prompt": prompt})

	handler := &sessionStreamAdapter{
		sessionID:   sessionID,
		broadcastFn: sm.broadcastFn,
	}

	response, err := ag.ProcessStream(ctx, prompt, handler)
	if errors.Is(err, golem.ErrAwaitingApproval) {
		sm.broadcast(EventAgentDone, sessionID, map[string]any{
			"awaiting_approval": true,
		})
		return "", err
	}
	if err != nil {
		sm.broadcast(EventAgentDone, sessionID, map[string]any{
			"error": err.Error(),
		})
		return "", err
	}

	sm.broadcast(EventAgentDone, sessionID, map[string]any{
		"response": response,
	})
	return response, nil
}

// SendApproval answers a pending sandbox confirmation and resumes the turn it paused.
func (sm *SessionManager) SendApproval(ctx context.Context, sessionID, callID string, approved bool) (string, error) {
	sess, ok := sm.GetSession(sessionID)
	if !ok {
		return "", errors.Errorf("session %s not found", sessionID)
	}
	if err := sess.Resume(ctx); err != nil {
		return "", err
	}

	sess.mu.Lock()
	sess.LastActiveAt = time.Now().UTC()
	ag := sess.Agent
	sess.mu.Unlock()

	handler := &sessionStreamAdapter{
		sessionID:   sessionID,
		broadcastFn: sm.broadcastFn,
	}

	response, err := ag.RespondToApproval(ctx, callID, approved, handler)
	if errors.Is(err, golem.ErrAwaitingApproval) {
		sm.broadcast(EventAgentDone, sessionID, map[string]any{
			"awaiting_approval": true,
		})
		return "", err
	}
	if err != nil {
		sm.broadcast(EventAgentDone, sessionID, map[string]any{
			"error": err.Error(),
		})
		return "", err
	}

	sm.broadcast(EventAgentDone, sessionID, map[string]any{
		"response": response,
	})
	return response, nil
}

type sessionStreamAdapter struct {
	sessionID   string
	broadcastFn BroadcastFunc
	subagentMu  sync.Mutex
	subagentIDs map[string]string
}

func (a *sessionStreamAdapter) broadcast(eventType string, sessionID string, payload any) {
	if a.broadcastFn == nil {
		return
	}
	a.broadcastFn(eventType, sessionID, payload)
}

func (a *sessionStreamAdapter) OnChunk(token string) {
	a.broadcast(EventAgentChunk, a.sessionID, map[string]string{
		"token": token,
		"delta": token,
	})
}

func (a *sessionStreamAdapter) OnApprovalRequest(req approval.Request) {
	a.broadcast(EventApprovalRequest, a.sessionID, map[string]any{
		"call_id": req.CallID,
		"tool":    req.Tool,
		"hint":    req.Hint,
		"action":  req.Action,
		"reason":  req.Reason,
	})
}

func (a *sessionStreamAdapter) OnToolStart(toolName, command string) {
	a.broadcast(EventToolStart, a.sessionID, map[string]any{
		"tool":    toolName,
		"command": command,
		"args":    map[string]any{"command": command},
	})
}

func (a *sessionStreamAdapter) OnToolDone(toolName, output string, exitCode int) {
	a.broadcast(EventToolDone, a.sessionID, map[string]any{
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

	a.broadcast(EventSubagentStart, a.sessionID, map[string]string{
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

	a.broadcast(EventSubagentTool, a.sessionID, map[string]any{
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
	a.broadcast(EventSubagentDone, a.sessionID, map[string]any{
		"id":       id,
		"role":     role,
		"model":    model,
		"result":   result,
		"duration": duration.String(),
		"error":    errStr,
	})
}

func (a *sessionStreamAdapter) OnTokenUsage(usage golem.TokenUsage) {
	a.broadcast(EventTokenUsage, a.sessionID, usage)
}

// Close gracefully closes all active sessions.
func (sm *SessionManager) Close() {
	sm.mu.Lock()
	sessions := make([]*Session, 0, len(sm.sessions))
	for id, s := range sm.sessions {
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		sessions = append(sessions, s)
		delete(sm.sessions, id)
	}
	sm.order = nil
	sm.activeID = ""
	sm.mu.Unlock()

	for _, s := range sessions {
		s.turnWg.Wait()
		s.mu.Lock()
		unsubSwarm := s.unsubSwarm
		unsubLock := s.unsubLock
		ag := s.Agent
		s.unsubSwarm = nil
		s.unsubLock = nil
		s.Agent = nil
		s.mu.Unlock()

		if unsubSwarm != nil {
			unsubSwarm()
		}
		if unsubLock != nil {
			unsubLock()
		}
		if ag != nil {
			_ = ag.Close()
		}
	}
}
