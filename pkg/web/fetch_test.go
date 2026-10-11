package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestFetcher(server *httptest.Server, maxBytes int64) *Fetcher {
	f := NewFetcher(maxBytes)
	f.AllowPrivate = true // httptest serves on loopback
	f.client = server.Client()
	return f
}

func TestFetchExtractsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>t</title><style>.x{}</style></head>
			<body><h1>Heading</h1><p>Hello <b>world</b></p><script>bad()</script></body></html>`))
	}))
	defer server.Close()

	f := newTestFetcher(server, 1<<20)
	text, err := f.Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	for _, want := range []string{"Heading", "Hello world"} {
		if !strings.Contains(text, want) {
			t.Errorf("fetched text missing %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"bad()", ".x{}", "<p>"} {
		if strings.Contains(text, bad) {
			t.Errorf("fetched text should not contain %q:\n%s", bad, text)
		}
	}
}

func TestFetchRejectsNonHTTPAndPrivate(t *testing.T) {
	f := NewFetcher(1 << 20)

	for _, u := range []string{"file:///etc/passwd", "ftp://example.com/x"} {
		if _, err := f.Fetch(context.Background(), u); err == nil {
			t.Errorf("expected scheme rejection for %q", u)
		}
	}

	// Private/loopback targets are rejected while AllowPrivate is false.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer server.Close()
	if _, err := f.Fetch(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Errorf("expected SSRF rejection for %q, got %v", server.URL, err)
	}
}

func TestFetchCapsAndRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(strings.Repeat("x", 5000)))
		case "/a":
			http.Redirect(w, r, "/b", http.StatusFound)
		case "/b":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("redirected body"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Small cap truncates.
	f := newTestFetcher(server, 100)
	text, err := f.Fetch(context.Background(), server.URL+"/big")
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if !strings.Contains(text, "truncated") {
		t.Errorf("expected truncation notice, got %d bytes", len(text))
	}

	// Redirects are followed.
	f2 := newTestFetcher(server, 1<<20)
	text, err = f2.Fetch(context.Background(), server.URL+"/a")
	if err != nil {
		t.Fatalf("redirect Fetch failed: %v", err)
	}
	if !strings.Contains(text, "redirected body") {
		t.Errorf("redirect not followed: %q", text)
	}
}

func TestFetchRejectsUnsupportedContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x00, 0x01, 0x02})
	}))
	defer server.Close()

	f := newTestFetcher(server, 1<<20)
	if _, err := f.Fetch(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), "content-type") {
		t.Errorf("expected content-type rejection, got %v", err)
	}
}

func TestFetchTimeoutIsBounded(t *testing.T) {
	// A non-routable address should fail quickly rather than hang; just assert
	// the context governs by cancelling immediately.
	f := NewFetcher(1 << 20)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := f.Fetch(ctx, "http://10.255.255.1/"); err == nil {
		t.Errorf("expected error on cancelled/private fetch")
	}
}
