package subagent

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/provider/openai"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/genai"
)

// FactoryConfig configures multi-provider model instantiation for subagents.
type FactoryConfig struct {
	ClientConfig    *genai.ClientConfig
	DefaultModel    string
	DefaultProvider string
	DefaultBaseURL  string
	DefaultAPIKey   string
	FileConfig      config.FileConfig
	HTTPClient      *http.Client
}

// ModelFactory manages and caches ADK model instances across providers (Gemini, OpenCode, OpenAI, OpenRouter),
// allowing subagents to run on specialized or cost-effective models.
type ModelFactory struct {
	mu              sync.RWMutex
	clientCfg       *genai.ClientConfig
	defaultModel    string
	defaultProvider string
	defaultBaseURL  string
	defaultAPIKey   string
	fileCfg         config.FileConfig
	httpClient      *http.Client
	models          map[string]model.LLM
}

// NewModelFactory constructs a ModelFactory with default settings (backward-compatible).
func NewModelFactory(clientCfg *genai.ClientConfig, defaultModel string) *ModelFactory {
	return NewModelFactoryWithConfig(FactoryConfig{
		ClientConfig: clientCfg,
		DefaultModel: defaultModel,
	})
}

// NewModelFactoryWithConfig constructs a ModelFactory supporting multi-provider configuration.
func NewModelFactoryWithConfig(cfg FactoryConfig) *ModelFactory {
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = "gemini-flash-latest"
	}
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = config.ProviderGemini
	}
	return &ModelFactory{
		clientCfg:       cfg.ClientConfig,
		defaultModel:    cfg.DefaultModel,
		defaultProvider: cfg.DefaultProvider,
		defaultBaseURL:  cfg.DefaultBaseURL,
		defaultAPIKey:   cfg.DefaultAPIKey,
		fileCfg:         cfg.FileConfig,
		httpClient:      cfg.HTTPClient,
		models:          make(map[string]model.LLM),
	}
}

// UpdateDefaults updates default model credentials when profiles switch.
func (f *ModelFactory) UpdateDefaults(defaultModel, defaultProvider, defaultBaseURL, defaultAPIKey string, fileCfg config.FileConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if defaultModel != "" {
		f.defaultModel = defaultModel
	}
	if defaultProvider != "" {
		f.defaultProvider = defaultProvider
	}
	if defaultBaseURL != "" {
		f.defaultBaseURL = defaultBaseURL
	}
	if defaultAPIKey != "" {
		f.defaultAPIKey = defaultAPIKey
	}
	f.fileCfg = fileCfg
}

// ResolveModelName normalizes requested model names and resolves aliases.
func (f *ModelFactory) ResolveModelName(requested string) string {
	clean := strings.ToLower(strings.TrimSpace(requested))
	switch clean {
	case "", "inherit", "default":
		return f.defaultModel
	case "flash":
		return "gemini-flash-latest"
	case "pro":
		return "gemini-2.5-pro"
	default:
		return strings.TrimSpace(requested)
	}
}

// GetModel retrieves an existing cached model or initializes a new model instance.
func (f *ModelFactory) GetModel(ctx context.Context, requested string) (model.LLM, string, error) {
	resolved := f.ResolveModelName(requested)

	// Determine provider and underlying model name
	provider := f.defaultProvider
	actualModel := resolved

	if strings.HasPrefix(resolved, "opencode-go/") {
		provider = config.ProviderOpenCode
		actualModel = strings.TrimPrefix(resolved, "opencode-go/")
	} else if strings.HasPrefix(resolved, "opencode/") {
		provider = config.ProviderOpenCode
		actualModel = strings.TrimPrefix(resolved, "opencode/")
	} else if strings.HasPrefix(resolved, "openai/") {
		provider = config.ProviderOpenAI
		actualModel = strings.TrimPrefix(resolved, "openai/")
	} else if strings.HasPrefix(resolved, "openrouter/") {
		provider = config.ProviderOpenRouter
		actualModel = strings.TrimPrefix(resolved, "openrouter/")
	} else if strings.HasPrefix(resolved, "gemini/") {
		provider = config.ProviderGemini
		actualModel = strings.TrimPrefix(resolved, "gemini/")
	} else if strings.HasPrefix(resolved, "gemini-") {
		provider = config.ProviderGemini
	}

	if provider == "" {
		provider = config.ProviderGemini
	}

	cacheKey := fmt.Sprintf("%s:%s", provider, actualModel)

	f.mu.RLock()
	if m, ok := f.models[cacheKey]; ok {
		f.mu.RUnlock()
		return m, resolved, nil
	}
	f.mu.RUnlock()

	f.mu.Lock()
	defer f.mu.Unlock()

	// Double-check after acquiring write lock
	if m, ok := f.models[cacheKey]; ok {
		return m, resolved, nil
	}

	var m model.LLM
	switch provider {
	case config.ProviderGemini:
		clientCfg := f.clientCfg
		if clientCfg == nil {
			apiKey := config.ResolveProviderKey(config.ProviderGemini, f.defaultAPIKey, f.fileCfg)
			clientCfg = &genai.ClientConfig{
				APIKey: apiKey,
			}
		}
		geminiModel, err := gemini.NewModel(ctx, actualModel, clientCfg)
		if err != nil {
			return nil, "", errors.Wrapf(err, "initializing gemini model %q", actualModel)
		}
		m = geminiModel

	default: // OpenCode, OpenAI, OpenRouter, and any OpenAI-compatible provider
		baseURL := config.ResolveBaseURL(provider, f.defaultBaseURL, f.fileCfg)
		apiKey := config.ResolveProviderKey(provider, f.defaultAPIKey, f.fileCfg)
		m = openai.NewClient(actualModel, baseURL, apiKey, f.httpClient)
	}

	f.models[cacheKey] = m
	return m, resolved, nil
}
