package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/user/golem/pkg/config"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newAuthTestServer(t *testing.T, token string) (*httptest.Server, *Server) {
	t.Helper()
	srv := NewServer(ServerConfig{Token: token})
	ts := httptest.NewServer(srv.httpServer.Handler)
	t.Cleanup(ts.Close)
	return ts, srv
}

func get(t *testing.T, ts *httptest.Server, path string, mutate func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestAPIRoutesRequireAToken(t *testing.T) {
	ts, _ := newAuthTestServer(t, testToken)

	if resp := get(t, ts, "/api/sessions", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /api/sessions: got %d, want 401", resp.StatusCode)
	}

	withBearer := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testToken) }
	if resp := get(t, ts, "/api/sessions", withBearer); resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated /api/sessions: got %d, want 200", resp.StatusCode)
	}

	withQuery := func(r *http.Request) { r.URL.RawQuery = "token=" + testToken }
	if resp := get(t, ts, "/api/sessions", withQuery); resp.StatusCode != http.StatusOK {
		t.Fatalf("query-token /api/sessions: got %d, want 200", resp.StatusCode)
	}

	wrongToken := func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }
	if resp := get(t, ts, "/api/sessions", wrongToken); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", resp.StatusCode)
	}

	// A prefix of the real token must not authenticate.
	prefix := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testToken[:8]) }
	if resp := get(t, ts, "/api/sessions", prefix); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token prefix: got %d, want 401", resp.StatusCode)
	}
}

func TestWebSocketRequiresAToken(t *testing.T) {
	ts, _ := newAuthTestServer(t, testToken)

	// A plain GET never upgrades, so a 401 here proves the middleware ran before
	// the upgrader could reject it with 400.
	if resp := get(t, ts, "/ws", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /ws: got %d, want 401", resp.StatusCode)
	}
}

func TestWebSocketUpgradeHonoursTheToken(t *testing.T) {
	ts, _ := newAuthTestServer(t, testToken)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	// Without a credential the upgrade must be refused with 401 rather than a
	// protocol-level 400, which is what proves the middleware runs first.
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil {
		t.Fatal("unauthenticated WebSocket dial succeeded")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("unauthenticated WebSocket status = %d, want 401", status)
	}

	// A cookie is the only credential a browser can attach to a WS handshake, so
	// this is the path the phone actually uses.
	header := http.Header{}
	header.Set("Cookie", authCookieName+"="+testToken)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("cookie-authenticated WebSocket dial: %v", err)
	}
	_ = conn.Close()
}

func TestSPAShellStaysOpenSoTheLoginPromptCanRender(t *testing.T) {
	ts, _ := newAuthTestServer(t, testToken)

	resp := get(t, ts, "/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unauthenticated GET /: got %d, want 200", resp.StatusCode)
	}
}

func TestAuthEndpointsAreReachableUnauthenticated(t *testing.T) {
	ts, _ := newAuthTestServer(t, testToken)

	resp := get(t, ts, "/api/auth", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/auth: got %d, want 200", resp.StatusCode)
	}
	var body struct {
		AuthRequired  bool `json:"auth_required"`
		Authenticated bool `json:"authenticated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /api/auth: %v", err)
	}
	if !body.AuthRequired || body.Authenticated {
		t.Fatalf("/api/auth = %+v, want required and unauthenticated", body)
	}
}

func TestLoginExchangesTokenForACookie(t *testing.T) {
	ts, _ := newAuthTestServer(t, testToken)

	post := func(payload string) *http.Response {
		resp, err := http.Post(ts.URL+"/api/login", "application/json", bytes.NewBufferString(payload))
		if err != nil {
			t.Fatalf("POST /api/login: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	if resp := post(`{"token":"wrong"}`); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token login: got %d, want 401", resp.StatusCode)
	}

	resp := post(`{"token":"` + testToken + `"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good token login: got %d, want 200", resp.StatusCode)
	}

	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == authCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("login did not set the %s cookie", authCookieName)
	}
	if !cookie.HttpOnly {
		t.Error("auth cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Error("auth cookie must be SameSite=Strict")
	}

	// The cookie alone must satisfy the middleware, which is what makes the WS
	// upgrade work in a browser (no headers available there).
	withCookie := func(r *http.Request) { r.AddCookie(cookie) }
	if got := get(t, ts, "/api/sessions", withCookie); got.StatusCode != http.StatusOK {
		t.Fatalf("cookie-authenticated /api/sessions: got %d, want 200", got.StatusCode)
	}
}

func TestAuthDisabledPassesEverythingThrough(t *testing.T) {
	ts, srv := newAuthTestServer(t, "")
	if srv.AuthEnabled() {
		t.Fatal("AuthEnabled() = true with an empty token")
	}
	if resp := get(t, ts, "/api/sessions", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("auth disabled /api/sessions: got %d, want 200", resp.StatusCode)
	}
}

func TestDefaultBindIsLoopback(t *testing.T) {
	srv := NewServer(ServerConfig{Port: 8080})
	if got := srv.Addr(); got != "127.0.0.1:8080" {
		t.Fatalf("default addr = %q, want 127.0.0.1:8080", got)
	}

	srv = NewServer(ServerConfig{Host: "0.0.0.0", Port: 9000})
	if got := srv.Addr(); got != "0.0.0.0:9000" {
		t.Fatalf("explicit host addr = %q, want 0.0.0.0:9000", got)
	}

	// IPv6 must be bracketed by JoinHostPort.
	srv = NewServer(ServerConfig{Host: "::1", Port: 9001})
	if got := srv.Addr(); got != "[::1]:9001" {
		t.Fatalf("ipv6 addr = %q, want [::1]:9001", got)
	}
}

func TestWebSocketOriginPolicy(t *testing.T) {
	cases := []struct {
		name       string
		origin     string
		host       string
		remoteAddr string
		allow      []string
		want       bool
	}{
		{name: "no origin is a non-browser client", host: "127.0.0.1:8080", remoteAddr: "203.0.113.5:1234", want: true},
		{name: "same origin", origin: "http://127.0.0.1:8080", host: "127.0.0.1:8080", remoteAddr: "203.0.113.5:1234", want: true},
		{name: "cross site is rejected", origin: "https://evil.example", host: "127.0.0.1:8080", remoteAddr: "203.0.113.5:1234", want: false},
		{name: "loopback proxy may rewrite host", origin: "https://golem.tailnet.ts.net", host: "127.0.0.1:8080", remoteAddr: "127.0.0.1:41234", want: true},
		{name: "tailscale origin without proxy is rejected", origin: "https://golem.tailnet.ts.net", host: "100.64.0.1:8080", remoteAddr: "100.64.0.9:5555", want: false},
		{name: "allowlist admits a tailscale origin", origin: "https://golem.tailnet.ts.net", host: "100.64.0.1:8080", remoteAddr: "100.64.0.9:5555", allow: []string{"https://golem.tailnet.ts.net"}, want: true},
		{name: "allowlist by bare host", origin: "https://golem.tailnet.ts.net", host: "100.64.0.1:8080", remoteAddr: "100.64.0.9:5555", allow: []string{"golem.tailnet.ts.net"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewHub()
			hub.SetAllowOrigins(tc.allow)
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+"/ws", nil)
			req.Host = tc.host
			req.RemoteAddr = tc.remoteAddr
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := hub.originAllowed(req); got != tc.want {
				t.Fatalf("originAllowed(%q, host %q, remote %q, allow %v) = %v, want %v",
					tc.origin, tc.host, tc.remoteAddr, tc.allow, got, tc.want)
			}
		})
	}
}

func TestAuthExemptPaths(t *testing.T) {
	exempt := []string{"/", "/index.html", "/_app/immutable/entry.js", "/api/login", "/api/logout", "/api/auth"}
	for _, p := range exempt {
		if !authExempt(p) {
			t.Errorf("authExempt(%q) = false, want true", p)
		}
	}
	gated := []string{"/ws", "/api/sessions", "/api/sessions/x/prompt", "/api/patch", "/api/models", "/api/profiles"}
	for _, p := range gated {
		if authExempt(p) {
			t.Errorf("authExempt(%q) = true, want false", p)
		}
	}
}

func TestEnsureWebTokenGeneratesAndPersists(t *testing.T) {
	path := t.TempDir() + "/config.json"

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	first, err := config.EnsureWebToken(&cfg, path)
	if err != nil {
		t.Fatalf("first EnsureWebToken: %v", err)
	}
	if len(first) < 32 {
		t.Fatalf("token %q is too short to be a 32-byte random secret", first)
	}
	if strings.ContainsAny(first, " \t\n") {
		t.Fatalf("token %q contains whitespace", first)
	}

	// Second call must return the persisted token, not mint a new one, or every
	// restart would log out the phone.
	cfg2, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	second, err := config.EnsureWebToken(&cfg2, path)
	if err != nil {
		t.Fatalf("second EnsureWebToken: %v", err)
	}
	if second != first {
		t.Fatalf("token changed between runs: %q then %q", first, second)
	}
}
