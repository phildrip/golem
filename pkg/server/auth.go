package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// authCookieName is the cookie a successful /api/login sets. A cookie rather
	// than a query parameter because browsers cannot set headers on a WebSocket
	// upgrade, and putting the token in the URL leaks it into history and logs.
	authCookieName = "golem_token"
	authCookieTTL  = 30 * 24 * time.Hour
)

// authExempt reports whether a path may be served without a credential.
//
// The SPA shell and its static assets stay open so the token prompt can render;
// everything under /api/ and the WebSocket endpoint require a token. The auth
// endpoints themselves must be reachable unauthenticated or the client could
// never present one.
func authExempt(path string) bool {
	switch path {
	case "/api/login", "/api/logout", "/api/auth":
		return true
	}
	return !strings.HasPrefix(path, "/api/") && path != "/ws"
}

// webToken returns the configured bearer token, or "" when auth is disabled.
func (s *Server) webToken() string {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Token
}

// AuthEnabled reports whether the server will reject unauthenticated requests.
func (s *Server) AuthEnabled() bool {
	return s.webToken() != ""
}

// presentedToken returns the first credential found on the request: an
// Authorization bearer header, a ?token= query parameter, or the auth cookie.
func presentedToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if c, err := r.Cookie(authCookieName); err == nil {
		return c.Value
	}
	return ""
}

// tokenMatches compares in constant time so a wrong token leaks no prefix
// information through timing.
func tokenMatches(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// authMiddleware gates /api/* and /ws behind the bearer token. An empty token
// disables auth entirely (used by tests and by -no-web-auth); Start logs a
// warning in that case.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.webToken()
		if authExempt(r.URL.Path) || token == "" {
			next.ServeHTTP(w, r)
			return
		}
		if tokenMatches(presentedToken(r), token) {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/ws" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="golem"`)
		writeError(w, http.StatusUnauthorized, "unauthorized: missing or invalid token")
	})
}

type loginRequest struct {
	Token string `json:"token"`
}

// handleLogin exchanges a token for a session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	token := s.webToken()
	if token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auth": false})
		return
	}

	var req loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if !tokenMatches(strings.TrimSpace(req.Token), token) {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(authCookieTTL.Seconds()),
		// TLS usually terminates at `tailscale serve`, so r.TLS is nil while the
		// browser is on https. Secure=false cookies are still accepted there,
		// and forcing Secure would break plain-http tailnet access.
		Secure: r.TLS != nil,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleLogout clears the auth cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAuth tells the SPA whether auth is required and whether this request
// already carries a valid credential, so it can decide between the chat and the
// token prompt.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	token := s.webToken()
	required := token != ""
	authed := !required || tokenMatches(presentedToken(r), token)
	writeJSON(w, http.StatusOK, map[string]any{"auth_required": required, "authenticated": authed})
}

// originAllowed validates the Origin header on a WebSocket upgrade.
//
// Browsers always send Origin on a WS handshake, so requiring it to match the
// request Host blocks cross-site WebSocket hijacking and DNS-rebinding. Non
// browser clients (CLI, test harness) send no Origin and are allowed. A request
// arriving from loopback is allowed regardless because it is either a local
// browser or a local reverse proxy such as `tailscale serve`, which may rewrite
// Host. Extra origins can be allowed explicitly via config.
func (h *Hub) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return true
		}
	}

	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	h.mu.RLock()
	allowOrigins := h.allowOrigins
	h.mu.RUnlock()
	for _, allowed := range allowOrigins {
		if allowed == "" {
			continue
		}
		if strings.EqualFold(allowed, origin) || strings.EqualFold(allowed, u.Host) {
			return true
		}
	}
	return false
}
