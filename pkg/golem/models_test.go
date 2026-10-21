package golem

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchModelCatalogLiveFromOpenAICompatibleProvider(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "deepseek-v4.1-flash"},
				{"id": "kimi-k3"},
				{"id": "deepseek-v4.1-flash"}, // duplicate must be dropped
				{"id": "  "},
			},
		})
	}))
	defer srv.Close()

	got := FetchModelCatalog(context.Background(), "opencode", srv.URL, "secret-key", "kimi-k3")

	if !got.Live {
		t.Fatalf("expected a live catalogue, got fallback with note %q", got.Note)
	}
	if got.Note != "" {
		t.Errorf("clean live fetch should carry no note, got %q", got.Note)
	}
	if gotPath != "/models" {
		t.Errorf("expected request to /models, got %q", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("expected bearer auth from the resolved key, got %q", gotAuth)
	}
	want := []string{"deepseek-v4.1-flash", "kimi-k3"}
	if strings.Join(got.Models, ",") != strings.Join(want, ",") {
		t.Errorf("models = %v, want %v", got.Models, want)
	}
	if got.Provider != "opencode" || got.Current != "kimi-k3" {
		t.Errorf("catalog metadata wrong: provider=%q current=%q", got.Provider, got.Current)
	}
}

func TestFetchModelCatalogFallsBackWhenProviderFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	got := FetchModelCatalog(context.Background(), "opencode", srv.URL, "k", "my-pinned-model")

	if got.Live {
		t.Error("a 500 response must not be reported as live")
	}
	if !strings.Contains(got.Note, "bundled list") {
		t.Errorf("expected the fallback note, got %q", got.Note)
	}
	if len(got.Models) == 0 {
		t.Fatal("fallback list must not be empty")
	}
	if got.Models[0] != "my-pinned-model" {
		t.Errorf("current model should be prepended to the fallback list, got %v", got.Models[:1])
	}
}

func TestFetchModelCatalogGeminiWithoutKeyUsesBundledList(t *testing.T) {
	got := FetchModelCatalog(context.Background(), "gemini", "", "", "gemini-2.5-pro")

	if got.Live {
		t.Error("no key means no live Gemini listing")
	}
	if !strings.Contains(got.Note, "bundled list") {
		t.Errorf("expected the fallback note, got %q", got.Note)
	}
	if got.Current != "gemini-2.5-pro" {
		t.Errorf("current model should be reported, got %q", got.Current)
	}
	for _, name := range got.Models {
		if name == "gemini-flash-latest" {
			return
		}
	}
	t.Errorf("expected the bundled Gemini list, got %v", got.Models)
}

func TestFetchGeminiModelsFiltersNonGeneratingModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "gem-key" {
			t.Errorf("expected x-goog-api-key, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "models/gemini-2.5-pro", "supportedGenerationMethods": []string{"generateContent"}},
				{"name": "models/text-embedding-004", "supportedGenerationMethods": []string{"embedContent"}},
				{"name": "models/gemini-2.5-flash", "supportedGenerationMethods": []string{"generateContent", "countTokens"}},
			},
		})
	}))
	defer srv.Close()

	old := geminiModelsURL
	geminiModelsURL = srv.URL
	t.Cleanup(func() { geminiModelsURL = old })

	got := FetchModelCatalog(context.Background(), "gemini", "", "gem-key", "gemini-2.5-pro")
	if !got.Live {
		t.Fatalf("expected a live Gemini catalogue, note=%q", got.Note)
	}
	want := []string{"gemini-2.5-flash", "gemini-2.5-pro"}
	if strings.Join(got.Models, ",") != strings.Join(want, ",") {
		t.Errorf("models = %v, want %v", got.Models, want)
	}
}

func TestModelCatalogUsesAgentConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "qwen3.8-max"}},
		})
	}))
	defer srv.Close()

	agent := &Agent{cfg: Config{
		Provider:  "opencode",
		BaseURL:   srv.URL,
		APIKey:    "agent-key",
		ModelName: "qwen3.8-max",
	}}
	got := agent.ModelCatalog(context.Background())
	if !got.Live || len(got.Models) != 1 || got.Models[0] != "qwen3.8-max" {
		t.Fatalf("agent catalogue mismatch: %+v", got)
	}
}

func matchCatalog() ModelCatalog {
	return ModelCatalog{
		Provider: "opencode",
		Models: []string{
			"deepseek-v4.1-flash", "deepseek-v4.1-pro", "kimi-k3", "glm-5.3-flash",
		},
		Current: "kimi-k3",
		Live:    true,
	}
}

func TestMatchModelExactAndPrefix(t *testing.T) {
	catalog := matchCatalog()

	tests := []struct {
		name      string
		query     string
		wantMatch string
		wantOK    bool
	}{
		{name: "exact", query: "kimi-k3", wantMatch: "kimi-k3", wantOK: true},
		{name: "exact ignores case", query: "KIMI-K3", wantMatch: "kimi-k3", wantOK: true},
		{name: "surrounding spaces", query: "  kimi-k3  ", wantMatch: "kimi-k3", wantOK: true},
		{name: "unique prefix", query: "glm-5", wantMatch: "glm-5.3-flash", wantOK: true},
		{name: "ambiguous prefix", query: "deepseek-v4.1", wantOK: false},
		{name: "empty", query: "   ", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			match, candidates, ok := catalog.MatchModel(tc.query)
			if ok != tc.wantOK {
				t.Fatalf("MatchModel(%q) ok = %v, want %v (match %q, candidates %v)",
					tc.query, ok, tc.wantOK, match, candidates)
			}
			if ok && match != tc.wantMatch {
				t.Fatalf("MatchModel(%q) = %q, want %q", tc.query, match, tc.wantMatch)
			}
		})
	}
}

func TestMatchModelAmbiguousPrefixOffersFamily(t *testing.T) {
	_, candidates, ok := matchCatalog().MatchModel("deepseek")
	if ok {
		t.Fatal("expected an ambiguous prefix not to resolve")
	}
	want := []string{"deepseek-v4.1-flash", "deepseek-v4.1-pro"}
	if len(candidates) != len(want) {
		t.Fatalf("candidates = %v, want %v", candidates, want)
	}
	for i := range want {
		if candidates[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", candidates, want)
		}
	}
}

func TestMatchModelSuggestsNearMisses(t *testing.T) {
	catalog := matchCatalog()

	// A substring hit finds the family even when it is not a unique prefix.
	match, suggestions, ok := catalog.MatchModel("k3")
	if ok || match != "" {
		t.Fatalf("substring query resolved to %q", match)
	}
	if len(suggestions) != 1 || suggestions[0] != "kimi-k3" {
		t.Fatalf("substring suggestions = %v, want [kimi-k3]", suggestions)
	}

	// A real typo is caught by edit distance and rejected, not silently used.
	match, suggestions, ok = catalog.MatchModel("kim-k3")
	if ok || match != "" {
		t.Fatalf("typo resolved to %q, want a suggestion instead", match)
	}
	if len(suggestions) == 0 || suggestions[0] != "kimi-k3" {
		t.Fatalf("typo suggestions = %v, want kimi-k3 first", suggestions)
	}

	// Gibberish yields no match and at most the suggestion cap.
	match, suggestions, ok = catalog.MatchModel("zzzzzzzz")
	if ok || match != "" {
		t.Fatalf("gibberish resolved to %q", match)
	}
	if len(suggestions) > modelSuggestionLimit {
		t.Fatalf("got %d suggestions, want <= %d", len(suggestions), modelSuggestionLimit)
	}

	// Suggestions never exceed the cap for a broad query.
	if _, many, ok := catalog.MatchModel("flash"); ok || len(many) > modelSuggestionLimit {
		t.Fatalf("suggestion cap broken: %v ok=%v", many, ok)
	}
}

func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"kimi-k3", "kim-k3", 1},
		{"kitten", "sitting", 3},
	}
	for _, tc := range tests {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
