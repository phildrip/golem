package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// Provider constants
const (
	ProviderGemini     = "gemini"
	ProviderOpenCode   = "opencode"
	ProviderOpenAI     = "openai"
	ProviderOpenRouter = "openrouter"
)

// Default Base URLs
const (
	DefaultOpenCodeBaseURL   = "https://api.opencode.ai/v1"
	DefaultOpenAIBaseURL     = "https://api.openai.com/v1"
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
)

// ProviderConfig stores credentials and endpoint for an individual model provider.
type ProviderConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
}

// FileConfig represents the JSON structure stored in ~/.golem/config.json or ~/.config/golem/config.json.
type FileConfig struct {
	Provider  string                    `json:"provider,omitempty"`
	APIKey    string                    `json:"api_key,omitempty"`
	BaseURL   string                    `json:"base_url,omitempty"`
	Model     string                    `json:"model,omitempty"`
	MemoryDir string                    `json:"memory_dir,omitempty"`
	Providers map[string]ProviderConfig `json:"providers,omitempty"`
}

// UserDir returns Golem's user configuration directory.
// Priority:
// 1. $GOLEM_CONFIG_DIR environment variable
// 2. ~/.golem (if it exists as a directory)
// 3. ~/.config/golem (fallback if ~/.config/golem exists and ~/.golem does not)
// 4. Default: ~/.golem
func UserDir() (string, error) {
	if env := os.Getenv("GOLEM_CONFIG_DIR"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrap(err, "getting user home directory")
	}
	golemHome := filepath.Join(home, ".golem")
	if fi, err := os.Stat(golemHome); err == nil && fi.IsDir() {
		return golemHome, nil
	}
	configHome := filepath.Join(home, ".config", "golem")
	if fi, err := os.Stat(configHome); err == nil && fi.IsDir() {
		return configHome, nil
	}
	return golemHome, nil
}

// WorkspaceDir returns the workspace-local configuration directory (.golem under root).
func WorkspaceDir(workspaceRoot string) string {
	if workspaceRoot == "" {
		return ""
	}
	return filepath.Join(workspaceRoot, ".golem")
}

// DefaultPath returns the default configuration file path.
// Checks UserDir()/config.json, then fallback ~/.config/golem/config.json.
func DefaultPath() (string, error) {
	dir, err := UserDir()
	if err != nil {
		return "", err
	}
	golemCfg := filepath.Join(dir, "config.json")
	if _, err := os.Stat(golemCfg); err == nil {
		return golemCfg, nil
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		fallback := filepath.Join(home, ".config", "golem", "config.json")
		if _, err := os.Stat(fallback); err == nil {
			return fallback, nil
		}
	}
	return golemCfg, nil
}

// Load reads and unmarshals configuration from path. Returns empty config if file doesn't exist.
func Load(path string) (FileConfig, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return FileConfig{}, err
		}
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return FileConfig{}, nil
	}
	if err != nil {
		return FileConfig{}, errors.Wrapf(err, "reading config file at %s", path)
	}

	var cfg FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return FileConfig{}, errors.Wrapf(err, "parsing json from %s", path)
	}
	return cfg, nil
}

// Save writes the configuration to path with secure 0600 file permissions.
func Save(path string, cfg FileConfig) error {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.Wrapf(err, "creating config directory %s", dir)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return errors.Wrap(err, "marshaling config json")
	}

	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		return errors.Wrapf(err, "writing config file %s", path)
	}
	return nil
}

// ResolveAPIKey determines the API key in priority order:
// 1. Explicit CLI flag
// 2. GEMINI_API_KEY or GOOGLE_API_KEY environment variables
// 3. ~/.config/golem/config.json file
// 4. Interactive prompt via scanner (if stdin is interactive)
func ResolveAPIKey(flagKey string, configPath string, scanner *bufio.Scanner) (string, error) {
	if strings.TrimSpace(flagKey) != "" {
		return strings.TrimSpace(flagKey), nil
	}

	if envKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); envKey != "" {
		return envKey, nil
	}
	if envKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")); envKey != "" {
		return envKey, nil
	}

	fileCfg, err := Load(configPath)
	if err == nil && strings.TrimSpace(fileCfg.APIKey) != "" {
		return strings.TrimSpace(fileCfg.APIKey), nil
	}

	// If no scanner provided or cannot read interactively, return empty
	if scanner == nil {
		return "", nil
	}

	fmt.Println("\nNo Gemini API key found in flags, environment, or config file.")
	fmt.Print("Enter your Gemini API key (or press Enter to try Vertex AI / ADC): ")

	if !scanner.Scan() {
		return "", nil
	}

	inputKey := strings.TrimSpace(scanner.Text())
	if inputKey == "" {
		return "", nil
	}

	// Prompt whether to persist key to ~/.config/golem/config.json
	targetPath := configPath
	if targetPath == "" {
		targetPath, _ = DefaultPath()
	}
	fmt.Printf("Save this key to %s for future sessions? [Y/n]: ", targetPath)
	if scanner.Scan() {
		ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if ans == "" || ans == "y" || ans == "yes" {
			fileCfg.APIKey = inputKey
			if err := Save(targetPath, fileCfg); err != nil {
				fmt.Printf("Warning: failed to save config file: %v\n", err)
			} else {
				fmt.Printf("Saved API key to %s\n", targetPath)
			}
		}
	}

	return inputKey, nil
}

// ResolveProvider determines the active provider ("gemini", "opencode", "openai", "openrouter").
// Priority:
// 1. Explicit CLI flag / parameter
// 2. GOLEM_PROVIDER environment variable
// 3. FileConfig.Provider
// 4. Inferred from Model name prefix (e.g. "opencode/...", "openai/...", "openrouter/...")
// 5. Default: "gemini"
func ResolveProvider(flagProvider string, fileCfg FileConfig, modelName string) string {
	if clean := strings.ToLower(strings.TrimSpace(flagProvider)); clean != "" {
		return clean
	}
	if env := strings.ToLower(strings.TrimSpace(os.Getenv("GOLEM_PROVIDER"))); env != "" {
		return env
	}
	if clean := strings.ToLower(strings.TrimSpace(fileCfg.Provider)); clean != "" {
		return clean
	}
	if strings.Contains(modelName, "/") {
		parts := strings.SplitN(modelName, "/", 2)
		prefix := strings.ToLower(strings.TrimSpace(parts[0]))
		switch prefix {
		case ProviderOpenCode, ProviderOpenAI, ProviderOpenRouter, ProviderGemini:
			return prefix
		}
	}
	return ProviderGemini
}

// ResolveProviderKey determines the API key for a specific provider.
func ResolveProviderKey(provider string, flagKey string, fileCfg FileConfig) string {
	if strings.TrimSpace(flagKey) != "" {
		return strings.TrimSpace(flagKey)
	}

	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	switch cleanProvider {
	case ProviderOpenCode:
		if k := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY")); k != "" {
			return k
		}
		if k := strings.TrimSpace(os.Getenv("OPENCODE_KEY")); k != "" {
			return k
		}
		if k := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); k != "" {
			return k
		}
		if pCfg, ok := fileCfg.Providers[ProviderOpenCode]; ok && strings.TrimSpace(pCfg.APIKey) != "" {
			return strings.TrimSpace(pCfg.APIKey)
		}

	case ProviderOpenRouter:
		if k := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); k != "" {
			return k
		}
		if k := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); k != "" {
			return k
		}
		if pCfg, ok := fileCfg.Providers[ProviderOpenRouter]; ok && strings.TrimSpace(pCfg.APIKey) != "" {
			return strings.TrimSpace(pCfg.APIKey)
		}

	case ProviderOpenAI:
		if k := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); k != "" {
			return k
		}
		if pCfg, ok := fileCfg.Providers[ProviderOpenAI]; ok && strings.TrimSpace(pCfg.APIKey) != "" {
			return strings.TrimSpace(pCfg.APIKey)
		}

	default: // Gemini
		if k := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); k != "" {
			return k
		}
		if k := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")); k != "" {
			return k
		}
		if pCfg, ok := fileCfg.Providers[ProviderGemini]; ok && strings.TrimSpace(pCfg.APIKey) != "" {
			return strings.TrimSpace(pCfg.APIKey)
		}
	}

	return strings.TrimSpace(fileCfg.APIKey)
}

// ResolveBaseURL determines the API endpoint base URL for the given provider.
func ResolveBaseURL(provider string, flagURL string, fileCfg FileConfig) string {
	if strings.TrimSpace(flagURL) != "" {
		return strings.TrimSpace(flagURL)
	}

	cleanProvider := strings.ToLower(strings.TrimSpace(provider))
	switch cleanProvider {
	case ProviderOpenCode:
		if u := strings.TrimSpace(os.Getenv("OPENCODE_BASE_URL")); u != "" {
			return u
		}
		if u := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); u != "" {
			return u
		}
		if pCfg, ok := fileCfg.Providers[ProviderOpenCode]; ok && strings.TrimSpace(pCfg.BaseURL) != "" {
			return strings.TrimSpace(pCfg.BaseURL)
		}
		if strings.TrimSpace(fileCfg.BaseURL) != "" {
			return strings.TrimSpace(fileCfg.BaseURL)
		}
		return DefaultOpenCodeBaseURL

	case ProviderOpenRouter:
		if u := strings.TrimSpace(os.Getenv("OPENROUTER_BASE_URL")); u != "" {
			return u
		}
		if pCfg, ok := fileCfg.Providers[ProviderOpenRouter]; ok && strings.TrimSpace(pCfg.BaseURL) != "" {
			return strings.TrimSpace(pCfg.BaseURL)
		}
		if strings.TrimSpace(fileCfg.BaseURL) != "" {
			return strings.TrimSpace(fileCfg.BaseURL)
		}
		return DefaultOpenRouterBaseURL

	case ProviderOpenAI:
		if u := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); u != "" {
			return u
		}
		if pCfg, ok := fileCfg.Providers[ProviderOpenAI]; ok && strings.TrimSpace(pCfg.BaseURL) != "" {
			return strings.TrimSpace(pCfg.BaseURL)
		}
		if strings.TrimSpace(fileCfg.BaseURL) != "" {
			return strings.TrimSpace(fileCfg.BaseURL)
		}
		return DefaultOpenAIBaseURL
	}

	return strings.TrimSpace(fileCfg.BaseURL)
}

