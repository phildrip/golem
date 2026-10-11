package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleCSESearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "golang" {
			t.Errorf("q = %q, want golang", got)
		}
		if r.URL.Query().Get("key") == "" || r.URL.Query().Get("cx") == "" {
			t.Errorf("missing key/cx: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"title":"A","link":"https://a.example/1","snippet":"sa"},
			{"title":"B","link":"https://b.example/2","snippet":"sb"}
		]}`))
	}))
	defer server.Close()

	p := NewGoogleCSE("k", "cx", server.URL)
	p.Client = server.Client()

	results, err := p.Search(context.Background(), "golang", SearchOptions{Num: 5})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 || results[0].Title != "A" || results[1].URL != "https://b.example/2" {
		t.Fatalf("unexpected results: %+v", results)
	}

	out := FormatResults("golang", results)
	for _, want := range []string{"1. A", "https://a.example/1", "sa", "2. B"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatted results missing %q:\n%s", want, out)
		}
	}
}

func TestGoogleCSEErrors(t *testing.T) {
	if _, err := NewGoogleCSE("", "", "").Search(context.Background(), "x", SearchOptions{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected not-configured error, got %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()

	p := NewGoogleCSE("k", "cx", server.URL)
	p.Client = server.Client()
	if _, err := p.Search(context.Background(), "x", SearchOptions{}); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected 403 error, got %v", err)
	}
}

func TestIsPublicIP(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":         true,
		"1.1.1.1":         true,
		"127.0.0.1":       false,
		"10.0.0.5":        false,
		"192.168.1.1":     false,
		"169.254.169.254": false,
		"100.64.1.1":      false,
		"0.0.0.0":         false,
		"::1":             false,
		"fc00::1":         false,
		"2606:4700::1111": true,
	}
	for ipStr, want := range cases {
		ip := net.ParseIP(ipStr)
		if got := isPublicIP(ip); got != want {
			t.Errorf("isPublicIP(%s) = %v, want %v", ipStr, got, want)
		}
	}
}

func TestRedactSecret(t *testing.T) {
	got := redactSecret(`Get "https://api.example/v1?key=SECRET&cx=1": dial tcp: timeout`, "SECRET")
	if strings.Contains(got, "SECRET") {
		t.Errorf("secret leaked: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Errorf("secret not replaced: %q", got)
	}
	if redactSecret("no secret here", "") != "no secret here" {
		t.Errorf("empty secret changed text")
	}
}
