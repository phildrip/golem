package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/importer"
	"github.com/user/golem/pkg/sandbox"
	"github.com/user/golem/pkg/tools"
)

// DefaultListenHost keeps the server on loopback unless the operator opts in to
// a wider bind. Exposing an unconfined agent shell to the network by default is
// not a safe default.
const DefaultListenHost = "127.0.0.1"

// ServerConfig configures the HTTP and WebSocket multiplexer server.
type ServerConfig struct {
	// Host is the interface to bind. Empty means DefaultListenHost (loopback).
	Host string
	Port int
	// Token is the bearer token required on /api/* and /ws. Empty disables auth,
	// which Start logs as a warning; the CLI always supplies one.
	Token string
	// AllowOrigins lists extra origins permitted to open a WebSocket beyond the
	// request host and loopback (e.g. a tailscale serve hostname).
	AllowOrigins []string
	BaseAgentCfg golem.Config
	StaticFS     fs.FS // embedded SvelteKit production build or nil
}

// Server provides REST APIs, WebSocket streaming, and SPA hosting.
type Server struct {
	cfgMu      sync.RWMutex
	configMu   sync.Mutex
	cfg        ServerConfig
	addr       string
	hub        *Hub
	sessions   *SessionManager
	httpServer *http.Server
}

var profileNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)

func validateProfileName(name string) error {
	if !profileNameRegex.MatchString(name) {
		return errors.New("profile name must be 1-64 characters (letters, numbers, underscores, dashes, periods)")
	}
	if name == "active" || name == "." || name == ".." {
		return fmt.Errorf("profile name %q is reserved", name)
	}
	return nil
}

func (s *Server) BaseAgentCfg() golem.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.BaseAgentCfg
}

func (s *Server) WorkspaceDir() string {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.BaseAgentCfg.WorkspaceDir
}

func (s *Server) SetBaseAgentCfg(cfg golem.Config) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg.BaseAgentCfg = cfg
}

// NewServer initializes a new Server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Port <= 0 {
		cfg.Port = 8080
	}
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		host = DefaultListenHost
	}
	hub := NewHub()
	hub.SetAllowOrigins(cfg.AllowOrigins)
	sessions := NewSessionManager(cfg.BaseAgentCfg, hub)

	s := &Server{
		cfg:      cfg,
		addr:     net.JoinHostPort(host, strconv.Itoa(cfg.Port)),
		hub:      hub,
		sessions: sessions,
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.httpServer = &http.Server{
		Addr:    s.addr,
		Handler: s.authMiddleware(mux),
	}

	return s
}

// Addr reports the resolved listen address (host:port).
func (s *Server) Addr() string {
	return s.addr
}

// Start boots the WebSocket hub and listens for HTTP requests.
func (s *Server) Start() error {
	go s.hub.Run()
	if !s.AuthEnabled() {
		log.Printf("WARNING: web auth is disabled; any client that can reach %s can drive this agent", s.addr)
	}
	return s.httpServer.ListenAndServe()
}

// Shutdown stops the server and closes sessions.
func (s *Server) Shutdown(ctx context.Context) error {
	s.sessions.Close()
	s.hub.Close()
	return s.httpServer.Shutdown(ctx)
}

// Hub returns the underlying WebSocket Hub.
func (s *Server) Hub() *Hub {
	return s.hub
}

// SessionManager returns the session manager.
func (s *Server) SessionManager() *SessionManager {
	return s.sessions
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// WebSocket endpoint
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		s.hub.HandleWS(w, r)
	})

	// Auth endpoints: the SPA uses /api/auth to decide between the chat and the
	// token prompt, and /api/login to exchange a token for a cookie.
	mux.HandleFunc("/api/auth", s.handleAuth)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/logout", s.handleLogout)

	// History discovery & import endpoints
	mux.HandleFunc("/api/importer/discover", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ws := r.URL.Query().Get("workspace")
		if ws == "" {
			ws = s.WorkspaceDir()
		}
		if ws == "" {
			ws, _ = os.Getwd()
		}
		sessions, err := importer.DiscoverHistories(ws)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if sessions == nil {
			sessions = []importer.DiscoveredSession{}
		}
		writeJSON(w, http.StatusOK, sessions)
	})

	mux.HandleFunc("/api/importer/import", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Path  string `json:"path"`
			ID    string `json:"id,omitempty"`
			Title string `json:"title,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Path == "" {
			writeError(w, http.StatusBadRequest, "path is required")
			return
		}
		cleanPath := filepath.Clean(req.Path)
		fi, err := os.Stat(cleanPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid import path: file not found or inaccessible")
			return
		}
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			writeError(w, http.StatusBadRequest, "path must be a regular file or directory")
			return
		}

		// Ensure cleanPath resides within permitted roots: workspace, AGY brain dir, or Cursor workspaceStorage
		allowedRoots := []string{
			filepath.Clean(s.WorkspaceDir()),
			filepath.Clean(importer.DefaultAGYBrainDir()),
			filepath.Clean(importer.DefaultCursorWorkspaceStorageDir()),
		}
		allowed := false
		for _, root := range allowedRoots {
			if root != "" && (cleanPath == root || strings.HasPrefix(cleanPath, root+string(filepath.Separator))) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "import path is outside permitted workspace and history directories")
			return
		}

		sess, err := s.sessions.CreateSessionWithImport(r.Context(), req.Title, cleanPath, req.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Ensure the newly imported session is activated on the backend
		s.sessions.SetActiveSession(sess.ID)

		writeJSON(w, http.StatusCreated, SessionInfo{
			ID:        sess.ID,
			Title:     sess.Title,
			CreatedAt: sess.CreatedAt,
			UpdatedAt: sess.UpdatedAt,
			IsActive:  true,
			TurnCount: sess.Agent.Store().CountEvents(),
		})
	})

	// REST Session endpoints
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			list := s.sessions.ListSessions()
			writeJSON(w, http.StatusOK, list)
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
			var req CreateSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength > 0 {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			sess, err := s.sessions.CreateSession(r.Context(), req.Title)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusCreated, SessionInfo{
				ID:        sess.ID,
				Title:     sess.Title,
				CreatedAt: sess.CreatedAt,
				UpdatedAt: sess.UpdatedAt,
				IsActive:  true,
				TurnCount: 0,
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/sessions/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
		parts := strings.Split(path, "/")
		sessionID := parts[0]

		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				sess, ok := s.sessions.GetSession(sessionID)
				if !ok {
					writeError(w, http.StatusNotFound, "session not found")
					return
				}
				writeJSON(w, http.StatusOK, SessionInfo{
					ID:        sess.ID,
					Title:     sess.Title,
					CreatedAt: sess.CreatedAt,
					UpdatedAt: sess.UpdatedAt,
					IsActive:  s.sessions.activeID == sess.ID,
					TurnCount: sess.TurnCount,
				})
			case http.MethodDelete:
				if err := s.sessions.DeleteSession(sessionID); err != nil {
					writeError(w, http.StatusNotFound, err.Error())
					return
				}
				writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}

		// Sub-routes: /api/sessions/{id}/prompt, /api/sessions/{id}/activate
		switch parts[1] {
		case "activate":
			if r.Method == http.MethodPost {
				if s.sessions.SetActiveSession(sessionID) {
					writeJSON(w, http.StatusOK, map[string]string{"status": "active"})
				} else {
					writeError(w, http.StatusNotFound, "session not found")
				}
			} else {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		case "prompt":
			if r.Method == http.MethodPost {
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
				var req SendMessageRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					writeError(w, http.StatusBadRequest, "invalid request body")
					return
				}

				sess, acquired := s.sessions.TryAcquireTurn(sessionID)
				if !acquired {
					if sess == nil {
						writeError(w, http.StatusNotFound, "session not found")
					} else {
						writeError(w, http.StatusConflict, "session is currently executing a turn")
					}
					return
				}

				// Run turn tied to the session's cancelable context
				go func(sessCtx context.Context, p string) {
					defer s.sessions.ReleaseTurn(sessionID)
					turnCtx, cancel := context.WithTimeout(sessCtx, 10*time.Minute)
					defer cancel()
					_, _ = s.sessions.SendPrompt(turnCtx, sessionID, p)
				}(sess.ctx, req.Prompt)

				writeJSON(w, http.StatusAccepted, map[string]string{"status": "processing"})
			} else {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		case "approval":
			switch r.Method {
			case http.MethodGet:
				// Re-render a prompt nobody answered yet, e.g. after a reconnect.
				sess, ok := s.sessions.GetSession(sessionID)
				if !ok {
					writeError(w, http.StatusNotFound, "session not found")
					return
				}
				req, pending := sess.Agent.PendingApproval()
				if !pending {
					writeJSON(w, http.StatusOK, map[string]any{"pending": false})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"pending": true,
					"call_id": req.CallID,
					"tool":    req.Tool,
					"hint":    req.Hint,
					"action":  req.Action,
					"reason":  req.Reason,
				})
			case http.MethodPost:
				r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
				var req ApprovalRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					writeError(w, http.StatusBadRequest, "invalid request body")
					return
				}
				sess, acquired := s.sessions.TryAcquireTurn(sessionID)
				if !acquired {
					if sess == nil {
						writeError(w, http.StatusNotFound, "session not found")
					} else {
						writeError(w, http.StatusConflict, "session is currently executing a turn")
					}
					return
				}
				go func(sessCtx context.Context, callID string, approved bool) {
					defer s.sessions.ReleaseTurn(sessionID)
					turnCtx, cancel := context.WithTimeout(sessCtx, 10*time.Minute)
					defer cancel()
					_, _ = s.sessions.SendApproval(turnCtx, sessionID, callID, approved)
				}(sess.ctx, req.CallID, req.Approved)
				writeJSON(w, http.StatusAccepted, map[string]string{"status": "processing"})
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		case "memory":
			if r.Method == http.MethodGet {
				sess, ok := s.sessions.GetSession(sessionID)
				if !ok {
					writeError(w, http.StatusNotFound, "session not found")
					return
				}
				events := sess.Agent.Store().GetAllEvents()
				writeJSON(w, http.StatusOK, events)
			} else {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		default:
			http.NotFound(w, r)
		}
	})

	// Hashline patch API
	mux.HandleFunc("/api/patch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req PatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid patch body")
			return
		}

		// Prevent path traversal
		cleanTarget := filepath.Clean(req.FilePath)
		if cleanTarget == ".." || strings.HasPrefix(cleanTarget, ".."+string(filepath.Separator)) || filepath.IsAbs(cleanTarget) {
			writeError(w, http.StatusBadRequest, "invalid relative file path for workspace")
			return
		}

		// The same policy the agent's tools obey. A human is clicking, so a
		// Confirm verdict is already answered by the click itself; a Deny still
		// refuses, because the profile said so.
		if policy := s.patchPolicy(); policy != nil {
			if d := policy.CheckWrite(filepath.Join(s.WorkspaceDir(), cleanTarget)); d.Verdict == sandbox.Deny {
				writeError(w, http.StatusForbidden, "blocked by sandbox: "+d.Reason)
				return
			}
		}

		res, err := tools.PatchFileWithHashline(s.WorkspaceDir(), cleanTarget, req.Op, req.Anchor, req.Content, req.DryRun)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// Sandbox decision log: what the policy allowed, refused and asked about.
	mux.HandleFunc("/api/sandbox/audit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		auditor := approval.CurrentAuditor()
		if auditor == nil {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "records": []approval.Record{}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": true,
			"path":    auditor.Path(),
			"records": auditor.Recent(200),
		})
	})

	// Profile management endpoints
	mux.HandleFunc("/api/profiles", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			s.handleGetProfiles(w, r)
		case http.MethodPost:
			s.handleSaveProfile(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/profiles/", func(w http.ResponseWriter, r *http.Request) {
		subpath := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
		if subpath == "active" {
			if r.Method == http.MethodPost {
				s.handleSetActiveProfile(w, r)
				return
			}
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		profileName := subpath
		if profileName == "" {
			http.NotFound(w, r)
			return
		}

		if r.Method == http.MethodDelete {
			s.handleDeleteProfile(w, r, profileName)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// Provider models discovery endpoint
	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		s.handleGetModels(w, r)
	})

	// Embedded UI SPA or placeholder static file handler
	if s.cfg.StaticFS != nil {
		fileServer := http.FileServer(http.FS(s.cfg.StaticFS))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
				http.NotFound(w, r)
				return
			}

			// Clean request path relative to root
			trimmedPath := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
			if trimmedPath == "" || trimmedPath == "." {
				trimmedPath = "index.html"
			}

			// Check if static asset exists
			f, err := s.cfg.StaticFS.Open(trimmedPath)
			if err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}

			// SPA client-side routing fallback: serve index.html
			indexFile, err := s.cfg.StaticFS.Open("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer indexFile.Close()

			stat, err := indexFile.Stat()
			if err != nil {
				http.NotFound(w, r)
				return
			}
			http.ServeContent(w, r, "index.html", stat.ModTime(), indexFile.(io.ReadSeeker))
		})
	} else {
		// Fallback landing page when UI is not yet embedded
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Golem</title></head><body><h1>Golem Multiplexer Server</h1><p>WebSocket hub and REST APIs active.</p></body></html>`))
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

const maskedSecret = "••••••••"

var allowedProviders = map[string]bool{
	"gemini":     true,
	"opencode":   true,
	"openai":     true,
	"openrouter": true,
}

func sanitizeProfiles(profs map[string]config.ProfileConfig) map[string]config.ProfileConfig {
	out := make(map[string]config.ProfileConfig, len(profs))
	for k, v := range profs {
		copyProf := v
		if copyProf.APIKey != "" {
			copyProf.APIKey = maskedSecret
		}
		if copyProf.Compactor != nil {
			compCopy := *copyProf.Compactor
			if compCopy.APIKey != "" {
				compCopy.APIKey = maskedSecret
			}
			copyProf.Compactor = &compCopy
		}
		out[k] = copyProf
	}
	return out
}

func (s *Server) resolveProfileConfigPaths() (userPath string, wsPath string) {
	userPath, _ = config.DefaultPath()
	wsDir := s.WorkspaceDir()
	if wsDir == "" {
		wsDir, _ = os.Getwd()
	}
	if wsDir != "" {
		wsPath = filepath.Join(config.WorkspaceDir(wsDir), "config.json")
	}
	return userPath, wsPath
}

func (s *Server) getProfilesResponse() (ProfilesResponse, error) {
	userPath, wsPath := s.resolveProfileConfigPaths()
	wsDir := s.WorkspaceDir()
	if wsDir == "" {
		wsDir, _ = os.Getwd()
	}

	merged, err := config.LoadAll(wsDir, "")
	if err != nil {
		return ProfilesResponse{}, err
	}

	activeName := merged.ActiveProfile
	if activeName == "" {
		activeName = "default"
	}

	profs := merged.Profiles
	if profs == nil {
		profs = make(map[string]config.ProfileConfig)
	}

	return ProfilesResponse{
		ActiveProfile:       activeName,
		Profiles:            sanitizeProfiles(profs),
		UserConfigPath:      userPath,
		WorkspaceConfigPath: wsPath,
	}, nil
}

func (s *Server) handleGetModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if provider == "" {
		provider = "opencode"
	}

	switch provider {
	case "opencode":
		baseURL := strings.TrimSpace(r.URL.Query().Get("base_url"))
		if baseURL == "" {
			baseURL = config.DefaultOpenCodeBaseURL
		}
		baseURL = strings.TrimRight(baseURL, "/")

		client := &http.Client{Timeout: 5 * time.Second}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, baseURL+"/models", nil)
		if err == nil {
			if resp, err := client.Do(req); err == nil && resp.StatusCode == http.StatusOK {
				var data struct {
					Data []struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&data); err == nil && len(data.Data) > 0 {
					_ = resp.Body.Close()
					models := make([]string, 0, len(data.Data))
					for _, m := range data.Data {
						if m.ID != "" {
							models = append(models, m.ID)
						}
					}
					writeJSON(w, http.StatusOK, ModelsResponse{Provider: "opencode", Models: models})
					return
				}
				_ = resp.Body.Close()
			}
		}

		// Fallback presets if offline or unreachable
		writeJSON(w, http.StatusOK, ModelsResponse{
			Provider: "opencode",
			Models: []string{
				"kimi-k3", "kimi-k2.7-code", "deepseek-v4-flash", "deepseek-v4-pro",
				"glm-5.3-flash", "grok-4.7", "gpt-6-luna", "qwen3.8-max", "minimax-m3", "mimo-v2.6-flash",
			},
		})

	case "gemini":
		writeJSON(w, http.StatusOK, ModelsResponse{
			Provider: "gemini",
			Models:   []string{"gemini-2.5-flash", "gemini-2.5-pro", "gemini-flash-latest"},
		})

	case "openai":
		writeJSON(w, http.StatusOK, ModelsResponse{
			Provider: "openai",
			Models:   []string{"gpt-4o", "gpt-4o-mini", "o3-mini", "o1"},
		})

	case "openrouter":
		writeJSON(w, http.StatusOK, ModelsResponse{
			Provider: "openrouter",
			Models:   []string{"anthropic/claude-3.7-sonnet", "deepseek/deepseek-r1", "meta-llama/llama-3.3-70b-instruct"},
		})

	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown provider %q", provider))
	}
}

func (s *Server) handleGetProfiles(w http.ResponseWriter, r *http.Request) {
	resp, err := s.getProfilesResponse()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSaveProfile(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req SaveProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if err := validateProfileName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Provider != "" && !allowedProviders[strings.ToLower(req.Provider)] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported provider %q (allowed: gemini, opencode, openai, openrouter)", req.Provider))
		return
	}
	if req.Compactor != nil && req.Compactor.Provider != "" && !allowedProviders[strings.ToLower(req.Compactor.Provider)] {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported compactor provider %q", req.Compactor.Provider))
		return
	}

	userPath, wsPath := s.resolveProfileConfigPaths()
	targetPath := wsPath
	if req.Scope == "user" || (targetPath == "" && userPath != "") {
		targetPath = userPath
	} else if targetPath == "" {
		targetPath = userPath
	}

	targetCfg, err := config.Load(targetPath)
	if err != nil {
		targetCfg = config.FileConfig{}
	}
	if targetCfg.Profiles == nil {
		targetCfg.Profiles = make(map[string]config.ProfileConfig)
	}

	wsDir := s.WorkspaceDir()
	if wsDir == "" {
		wsDir, _ = os.Getwd()
	}
	merged, _ := config.LoadAll(wsDir, "")

	// Preserve existing API key if masked
	apiKey := req.APIKey
	if apiKey == maskedSecret {
		if existing, exists := targetCfg.Profiles[req.Name]; exists && existing.APIKey != "" {
			apiKey = existing.APIKey
		} else if existing, exists := merged.Profiles[req.Name]; exists && existing.APIKey != "" {
			apiKey = existing.APIKey
		} else {
			apiKey = ""
		}
	}

	compactorCfg := req.Compactor
	if compactorCfg != nil && compactorCfg.APIKey == maskedSecret {
		compCopy := *compactorCfg
		if existing, exists := targetCfg.Profiles[req.Name]; exists && existing.Compactor != nil && existing.Compactor.APIKey != "" {
			compCopy.APIKey = existing.Compactor.APIKey
		} else if existing, exists := merged.Profiles[req.Name]; exists && existing.Compactor != nil && existing.Compactor.APIKey != "" {
			compCopy.APIKey = existing.Compactor.APIKey
		} else {
			compCopy.APIKey = ""
		}
		compactorCfg = &compCopy
	}

	prof := config.ProfileConfig{
		Provider:  req.Provider,
		Model:     req.Model,
		BaseURL:   req.BaseURL,
		APIKey:    apiKey,
		APIKeyEnv: req.APIKeyEnv,
		MemoryDir: req.MemoryDir,
		Compactor: compactorCfg,
	}

	targetCfg.Profiles[req.Name] = prof
	if req.SetActive {
		targetCfg.ActiveProfile = req.Name
	}

	if err := config.Save(targetPath, targetCfg); err != nil {
		writeError(w, http.StatusInternalServerError, errors.Wrap(err, "saving profile configuration").Error())
		return
	}

	merged, _ = config.LoadAll(wsDir, "")
	base := s.BaseAgentCfg()
	base.FileConfig = merged
	s.SetBaseAgentCfg(base)

	if req.SetActive {
		s.applyActiveProfile(r.Context(), req.Name, merged)
	}

	resp, err := s.getProfilesResponse()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = s.hub.Broadcast(WSEventProfileChanged, "", resp)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSetActiveProfile(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req SetActiveProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if err := validateProfileName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	wsDir := s.WorkspaceDir()
	if wsDir == "" {
		wsDir, _ = os.Getwd()
	}
	mergedCheck, _ := config.LoadAll(wsDir, "")
	if req.Name != "default" {
		if _, exists := mergedCheck.Profiles[req.Name]; !exists {
			writeError(w, http.StatusNotFound, fmt.Sprintf("profile %q does not exist", req.Name))
			return
		}
	}

	userPath, wsPath := s.resolveProfileConfigPaths()
	targetPath := wsPath
	if req.Scope == "user" || (targetPath == "" && userPath != "") {
		targetPath = userPath
	} else if targetPath == "" {
		targetPath = userPath
	}

	targetCfg, err := config.Load(targetPath)
	if err != nil {
		targetCfg = config.FileConfig{}
	}
	targetCfg.ActiveProfile = req.Name

	if err := config.Save(targetPath, targetCfg); err != nil {
		writeError(w, http.StatusInternalServerError, errors.Wrap(err, "saving active profile").Error())
		return
	}

	merged, _ := config.LoadAll(wsDir, "")
	base := s.BaseAgentCfg()
	base.FileConfig = merged
	s.SetBaseAgentCfg(base)

	s.applyActiveProfile(r.Context(), req.Name, merged)

	resp, err := s.getProfilesResponse()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = s.hub.Broadcast(WSEventProfileChanged, "", resp)
	writeJSON(w, http.StatusOK, resp)
}

func deleteProfileFromPath(filePath, profileName string) bool {
	cfg, err := config.Load(filePath)
	if err != nil || cfg.Profiles == nil {
		return false
	}
	if _, exists := cfg.Profiles[profileName]; !exists {
		return false
	}
	delete(cfg.Profiles, profileName)
	if cfg.ActiveProfile == profileName {
		cfg.ActiveProfile = ""
	}
	_ = config.Save(filePath, cfg)
	return true
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request, profileName string) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	if err := validateProfileName(profileName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	wsDir := s.WorkspaceDir()
	if wsDir == "" {
		wsDir, _ = os.Getwd()
	}
	merged, _ := config.LoadAll(wsDir, "")
	if merged.ActiveProfile == profileName || (merged.ActiveProfile == "" && profileName == "default") {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot delete currently active profile %q; switch to another profile first", profileName))
		return
	}

	scope := r.URL.Query().Get("scope")
	userPath, wsPath := s.resolveProfileConfigPaths()

	deleted := false
	if scope == "workspace" {
		if wsPath != "" {
			deleted = deleteProfileFromPath(wsPath, profileName)
		}
	} else if scope == "user" {
		if userPath != "" {
			deleted = deleteProfileFromPath(userPath, profileName)
		}
	} else {
		if wsPath != "" && deleteProfileFromPath(wsPath, profileName) {
			deleted = true
		} else if userPath != "" && deleteProfileFromPath(userPath, profileName) {
			deleted = true
		}
	}

	if !deleted {
		writeError(w, http.StatusNotFound, fmt.Sprintf("profile %q not found", profileName))
		return
	}

	merged, _ = config.LoadAll(wsDir, "")
	base := s.BaseAgentCfg()
	base.FileConfig = merged
	s.SetBaseAgentCfg(base)

	resp, err := s.getProfilesResponse()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	_ = s.hub.Broadcast(WSEventProfileChanged, "", resp)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) applyActiveProfile(ctx context.Context, profileName string, fileCfg config.FileConfig) {
	name, prof := config.ResolveActiveProfile(profileName, fileCfg)
	appliedFileCfg := fileCfg.ApplyProfile(name, prof)

	base := s.BaseAgentCfg()
	base.Profile = name
	base.FileConfig = appliedFileCfg
	if prof.Provider != "" {
		base.Provider = prof.Provider
	}
	if prof.Model != "" {
		base.ModelName = prof.Model
	}
	if prof.BaseURL != "" {
		base.BaseURL = prof.BaseURL
	}
	if prof.APIKey != "" {
		base.APIKey = prof.APIKey
	}
	if prof.Compactor != nil {
		if prof.Compactor.Model != "" {
			base.CompactorModel = prof.Compactor.Model
		}
		if prof.Compactor.Provider != "" {
			base.CompactorProvider = prof.Compactor.Provider
		}
		if prof.Compactor.BaseURL != "" {
			base.CompactorBaseURL = prof.Compactor.BaseURL
		}
		if prof.Compactor.APIKey != "" {
			base.CompactorAPIKey = prof.Compactor.APIKey
		}
	}
	s.SetBaseAgentCfg(base)
	s.sessions.UpdateBaseConfig(base)

	if activeSess, ok := s.sessions.ActiveSession(); ok && activeSess.Agent != nil {
		activeSess.mu.Lock()
		defer activeSess.mu.Unlock()
		_, _ = activeSess.Agent.SwitchProfile(ctx, name)
	}
}
