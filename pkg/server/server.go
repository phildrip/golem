package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/user/golem/pkg/golem"
	"github.com/user/golem/pkg/tools"
)

// ServerConfig configures the HTTP and WebSocket multiplexer server.
type ServerConfig struct {
	Port         int
	BaseAgentCfg golem.Config
	StaticFS     fs.FS // embedded SvelteKit production build or nil
}

// Server provides REST APIs, WebSocket streaming, and SPA hosting.
type Server struct {
	cfg        ServerConfig
	hub        *Hub
	sessions   *SessionManager
	httpServer *http.Server
}

// NewServer initializes a new Server.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Port <= 0 {
		cfg.Port = 8080
	}
	hub := NewHub()
	sessions := NewSessionManager(cfg.BaseAgentCfg, hub)

	s := &Server{
		cfg:      cfg,
		hub:      hub,
		sessions: sessions,
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: mux,
	}

	return s
}

// Start boots the WebSocket hub and listens for HTTP requests.
func (s *Server) Start() error {
	go s.hub.Run()
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

				sess, ok := s.sessions.GetSession(sessionID)
				if !ok {
					writeError(w, http.StatusNotFound, "session not found")
					return
				}

				// Run turn tied to the session's cancelable context
				go func(sessCtx context.Context, p string) {
					turnCtx, cancel := context.WithTimeout(sessCtx, 10*time.Minute)
					defer cancel()
					_, _ = s.sessions.SendPrompt(turnCtx, sessionID, p)
				}(sess.ctx, req.Prompt)

				writeJSON(w, http.StatusAccepted, map[string]string{"status": "processing"})
			} else {
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

		res, err := tools.PatchFileWithHashline(s.cfg.BaseAgentCfg.WorkspaceDir, cleanTarget, req.Op, req.Anchor, req.Content, req.DryRun)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
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
