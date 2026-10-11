// Package web provides Golem's web search and fetch tools: a swappable
// SearchProvider backend and an SSRF-guarded HTML fetcher.
package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// SearchResult is one ranked web result.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

// SearchOptions tunes a search request.
type SearchOptions struct {
	// Num is the requested result count (Google CSE supports 1-10 per call).
	Num int
}

// SearchProvider is a swappable web-search backend.
type SearchProvider interface {
	Name() string
	Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
}

// GoogleCSE implements SearchProvider against the Google Programmable Search
// (Custom Search JSON) API.
type GoogleCSE struct {
	APIKey  string
	CX      string
	BaseURL string
	Client  *http.Client
}

// NewGoogleCSE constructs a Google Custom Search provider.
func NewGoogleCSE(apiKey, cx, baseURL string) *GoogleCSE {
	if baseURL == "" {
		baseURL = "https://www.googleapis.com/customsearch/v1"
	}
	return &GoogleCSE{
		APIKey:  apiKey,
		CX:      cx,
		BaseURL: baseURL,
		Client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Name implements SearchProvider.
func (g *GoogleCSE) Name() string { return "google_cse" }

// Configured reports whether the API key and CSE id are present.
func (g *GoogleCSE) Configured() bool { return g.APIKey != "" && g.CX != "" }

// Search implements SearchProvider.
func (g *GoogleCSE) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("web search: empty query")
	}
	if !g.Configured() {
		return nil, errors.New("web search is not configured: set web.api_key and web.cx (or GOOGLE_CSE_API_KEY/GOOGLE_CSE_CX)")
	}

	num := opts.Num
	if num <= 0 {
		num = 8
	}
	if num > 10 {
		num = 10
	}

	q := url.Values{}
	q.Set("key", g.APIKey)
	q.Set("cx", g.CX)
	q.Set("q", query)
	q.Set("num", strconv.Itoa(num))
	endpoint := g.BaseURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.Wrap(err, "building search request")
	}
	req.Header.Set("Accept", "application/json")

	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// client.Do errors embed the request URL, which carries the API key;
		// redact it so the secret never reaches logs or fractal memory.
		return nil, errors.New("executing search request: " + redactSecret(err.Error(), g.APIKey))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("web search failed: HTTP %d: %s", resp.StatusCode, redactSecret(strings.TrimSpace(string(body)), g.APIKey))
	}

	var payload struct {
		Items []struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.Wrap(err, "decoding search response")
	}

	results := make([]SearchResult, 0, len(payload.Items))
	for _, item := range payload.Items {
		results = append(results, SearchResult{Title: item.Title, URL: item.Link, Snippet: item.Snippet})
	}
	return results, nil
}

// redactSecret removes a secret from arbitrary text so it cannot leak through
// errors or response bodies into logs or fractal memory.
func redactSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

// FormatResults renders results as a compact numbered list for the model.
func FormatResults(query string, results []SearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No web results for %q.", query)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Web results for %q:\n", query)
	for i, r := range results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, strings.TrimSpace(r.Title), r.URL)
		if s := strings.TrimSpace(r.Snippet); s != "" {
			fmt.Fprintf(&sb, "   %s\n", s)
		}
	}
	return sb.String()
}
