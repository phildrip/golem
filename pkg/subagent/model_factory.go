package subagent

import (
	"context"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/genai"
)

// ModelFactory manages and caches ADK Gemini model instances, allowing subagents
// to run on specialized or cost-effective models (e.g. Flash vs Pro).
type ModelFactory struct {
	mu           sync.RWMutex
	clientCfg    *genai.ClientConfig
	defaultModel string
	models       map[string]model.LLM
}

// NewModelFactory constructs a thread-safe ModelFactory.
func NewModelFactory(clientCfg *genai.ClientConfig, defaultModel string) *ModelFactory {
	if defaultModel == "" {
		defaultModel = "gemini-flash-latest"
	}
	return &ModelFactory{
		clientCfg:    clientCfg,
		defaultModel: defaultModel,
		models:       make(map[string]model.LLM),
	}
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

	f.mu.RLock()
	if m, ok := f.models[resolved]; ok {
		f.mu.RUnlock()
		return m, resolved, nil
	}
	f.mu.RUnlock()

	f.mu.Lock()
	defer f.mu.Unlock()

	// Double-check after write lock
	if m, ok := f.models[resolved]; ok {
		return m, resolved, nil
	}

	m, err := gemini.NewModel(ctx, resolved, f.clientCfg)
	if err != nil {
		return nil, "", errors.Wrapf(err, "initializing gemini model %q", resolved)
	}

	f.models[resolved] = m
	return m, resolved, nil
}
