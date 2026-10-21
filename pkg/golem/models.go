package golem

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/provider/openai"
)

const (
	// modelCatalogTimeout bounds the /models round-trip so opening the picker
	// can never hang the UI for longer than a moment.
	modelCatalogTimeout = 5 * time.Second
	// modelCatalogLimit caps how many models are kept from a chatty provider.
	modelCatalogLimit = 300
)

// geminiModelsURL is the public list endpoint for the Gemini API. It is a var
// so tests can point it at a stub server.
var geminiModelsURL = "https://generativelanguage.googleapis.com/v1beta/models"

// ModelCatalog is what a provider says it can serve. When the provider cannot
// be reached the bundled list is returned instead and Note explains why, so the
// picker always has something to show.
type ModelCatalog struct {
	Provider string   // provider the list belongs to, e.g. "opencode"
	Models   []string // model names, in the provider's order
	Current  string   // model currently in use, marked in the picker
	Live     bool     // true when Models came from the provider itself
	Note     string   // human-readable caveat, empty on a clean live fetch
}

// bundledModels is the offline fallback shown when the provider cannot be
// queried. It intentionally only lists a handful of well-known names: it is a
// hint, not a promise that the account has access to them.
func bundledModels(provider string) []string {
	switch provider {
	case config.ProviderGemini:
		return []string{"gemini-flash-latest", "gemini-2.5-flash", "gemini-2.5-pro"}
	case config.ProviderOpenAI:
		return []string{"gpt-4o", "gpt-4o-mini", "o3-mini", "o1"}
	case config.ProviderOpenRouter:
		return []string{
			"anthropic/claude-3.7-sonnet",
			"deepseek/deepseek-r1",
			"meta-llama/llama-3.3-70b-instruct",
		}
	default:
		return []string{
			"kimi-k3", "kimi-k2.7-code", "deepseek-v4-flash", "deepseek-v4-pro",
			"glm-5.3-flash", "grok-4.7", "gpt-6-luna", "qwen3.8-max", "minimax-m3",
			"mimo-v2.6-flash",
		}
	}
}

// ModelCatalog asks the active provider which models it serves. It reads the
// resolved provider, base URL and key off the running Agent, so the answer
// always matches the credentials actually in use.
func (a *Agent) ModelCatalog(ctx context.Context) ModelCatalog {
	cfg := a.Config()
	return FetchModelCatalog(ctx, cfg.Provider, cfg.BaseURL, cfg.APIKey, cfg.ModelName)
}

// FetchModelCatalog queries provider for its model list. A provider that is
// offline, slow or key-less is not an error: the bundled list is returned with
// Live=false and a Note describing the fallback.
func FetchModelCatalog(ctx context.Context, provider, baseURL, apiKey, current string) ModelCatalog {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		p = config.ProviderGemini
	}

	ctx, cancel := context.WithTimeout(ctx, modelCatalogTimeout)
	defer cancel()

	var (
		models []string
		err    error
	)
	if p == config.ProviderGemini {
		models, err = fetchGeminiModels(ctx, apiKey)
	} else {
		models, err = fetchOpenAICompatibleModels(ctx, p, baseURL, apiKey)
	}
	models = dedupeModels(models)

	catalog := ModelCatalog{Provider: p, Current: strings.TrimSpace(current)}
	switch {
	case err != nil:
		catalog.Models = bundledModels(p)
		catalog.Note = fmt.Sprintf("Could not reach %s (%v) — showing the bundled list.", p, err)
	case len(models) == 0:
		catalog.Models = bundledModels(p)
		catalog.Note = fmt.Sprintf("%s returned no models — showing the bundled list.", p)
	default:
		catalog.Models = models
		catalog.Live = true
	}

	// Keep the active model visible even when the provider omits it (a pinned
	// or not-yet-published tag), so the picker can always show where you are.
	if catalog.Current != "" {
		found := false
		for _, name := range catalog.Models {
			if name == catalog.Current {
				found = true
				break
			}
		}
		if !found {
			catalog.Models = append([]string{catalog.Current}, catalog.Models...)
		}
	}
	return catalog
}

// defaultBaseURL mirrors the resolution the provider clients use so a catalogue
// request with an empty base URL still lands somewhere sensible.
func defaultBaseURL(provider string) string {
	switch provider {
	case config.ProviderOpenAI:
		return openai.DefaultBaseURL
	case config.ProviderOpenRouter:
		return "https://openrouter.ai/api/v1"
	default:
		return config.DefaultOpenCodeBaseURL
	}
}

// fetchOpenAICompatibleModels lists models from any OpenAI-shaped /models
// endpoint (opencode, openai, openrouter, local proxies).
func fetchOpenAICompatibleModels(ctx context.Context, provider, baseURL, apiKey string) ([]string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = defaultBaseURL(provider)
	}
	endpoint := strings.TrimRight(base, "/") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", endpoint, resp.Status)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding model list: %w", err)
	}
	models := make([]string, 0, len(payload.Data))
	for _, entry := range payload.Data {
		if id := strings.TrimSpace(entry.ID); id != "" {
			models = append(models, id)
		}
	}
	return models, nil
}

// fetchGeminiModels lists Gemini models that can actually generate content.
// Vertex users have no plain API key, so the bundled list is used there.
func fetchGeminiModels(ctx context.Context, apiKey string) ([]string, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("no API key for the Gemini API")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiModelsURL+"?pageSize=200", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", key)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", geminiModelsURL, resp.Status)
	}

	var payload struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding model list: %w", err)
	}

	models := make([]string, 0, len(payload.Models))
	for _, entry := range payload.Models {
		if !supportsGenerateContent(entry.SupportedGenerationMethods) {
			continue
		}
		name := strings.TrimPrefix(strings.TrimSpace(entry.Name), "models/")
		if name != "" {
			models = append(models, name)
		}
	}
	sort.SliceStable(models, func(i, j int) bool { return models[i] < models[j] })
	return models, nil
}

func supportsGenerateContent(methods []string) bool {
	for _, method := range methods {
		if method == "generateContent" {
			return true
		}
	}
	return false
}

// dedupeModels drops blanks and repeats while keeping the provider's ordering,
// and caps the list so a provider with hundreds of variants stays navigable.
func dedupeModels(models []string) []string {
	seen := make(map[string]bool, len(models))
	out := make([]string, 0, len(models))
	for _, name := range models {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= modelCatalogLimit {
			break
		}
	}
	return out
}

// modelSuggestionLimit caps how many "did you mean" names are offered.
const modelSuggestionLimit = 4

// MatchModel checks a typed model name against the catalogue. It returns the
// canonical spelling when the name exists (exact match first, then a unique
// case-insensitive prefix so a truncated tag still works). When nothing
// matches, candidates lists the closest known names so the caller can offer
// them instead of silently switching to a model the provider may not serve.
func (c ModelCatalog) MatchModel(query string) (match string, candidates []string, ok bool) {
	typed := strings.TrimSpace(query)
	if typed == "" {
		return "", nil, false
	}

	var prefixed []string
	for _, name := range c.Models {
		if strings.EqualFold(name, typed) {
			return name, nil, true
		}
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(typed)) {
			prefixed = append(prefixed, name)
		}
	}
	switch len(prefixed) {
	case 1:
		return prefixed[0], nil, true
	case 0:
		return "", c.similarModels(typed, modelSuggestionLimit), false
	default:
		// Ambiguous prefix: offer the family rather than guessing.
		if len(prefixed) > modelSuggestionLimit {
			prefixed = prefixed[:modelSuggestionLimit]
		}
		return "", prefixed, false
	}
}

// similarModels ranks known names that plausibly mean what the user typed:
// substring hits first (in catalogue order), then near-misses by edit distance.
func (c ModelCatalog) similarModels(query string, limit int) []string {
	lower := strings.ToLower(query)
	out := make([]string, 0, limit)
	seen := make(map[string]bool, limit)
	add := func(name string) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		out = append(out, name)
		return len(out) >= limit
	}

	for _, name := range c.Models {
		if strings.Contains(strings.ToLower(name), lower) && add(name) {
			return out
		}
	}

	type scored struct {
		name string
		dist int
	}
	var near []scored
	for _, name := range c.Models {
		if seen[name] {
			continue
		}
		if d := editDistance(lower, strings.ToLower(name)); d <= 2 {
			near = append(near, scored{name: name, dist: d})
		}
	}
	sort.SliceStable(near, func(i, j int) bool {
		if near[i].dist != near[j].dist {
			return near[i].dist < near[j].dist
		}
		return near[i].name < near[j].name
	})
	for _, cand := range near {
		if add(cand.name) {
			break
		}
	}
	return out
}

// editDistance is the classic Levenshtein distance, used only to spot typos in
// short model names so the cost never matters.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}
