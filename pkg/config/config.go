package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/user/golem/pkg/sandbox"
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
	DefaultOpenCodeBaseURL   = "https://opencode.ai/zen/go/v1"
	DefaultOpenAIBaseURL     = "https://api.openai.com/v1"
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"
)

// MountedMemoryConfig specifies a secondary or peer fractal memory store mounted into the session.
type MountedMemoryConfig struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// CompactorConfig defines custom provider and model settings for the background fractal memory compactor.
type CompactorConfig struct {
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
	BaseURL     string `json:"base_url,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
	APIKeyEnv   string `json:"api_key_env,omitempty"`
	TargetBytes int    `json:"target_bytes,omitempty"`
}

// WebConfig configures the web_search / web_fetch tools. Only web_search needs
// credentials; web_fetch is unrestricted apart from the SSRF guard.
type WebConfig struct {
	// Provider selects the search backend (default "google_cse").
	Provider string `json:"provider,omitempty"`
	// APIKey and APIKeyEnv supply the search API key (env wins when APIKey empty).
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// CX is the Google Programmable Search Engine id.
	CX       string `json:"cx,omitempty"`
	CXEnv    string `json:"cx_env,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	MaxFetch int    `json:"max_fetch_bytes,omitempty"`
}

// SandboxConfig configures the policy layer every tool consults before it
// touches the host. Mode off reproduces pre-sandbox behaviour; policy denies
// dangerous shapes and writes outside the workspace; bwrap is reserved for
// OS-level isolation.
type SandboxConfig struct {
	// Mode is off, policy or bwrap (default policy).
	Mode string `json:"mode,omitempty"`
	// AllowWrite lists extra writable paths; the workspace is always writable.
	AllowWrite []string `json:"allow_write,omitempty"`
	// AllowNet lists hosts web_fetch may reach; empty means no allowlist.
	AllowNet []string `json:"allow_net,omitempty"`
	// RequireConfirm lists tools that always need human approval, e.g. ["shell"].
	RequireConfirm []string `json:"require_confirm,omitempty"`
	// EnvAllow overrides the environment allowlist for shell commands.
	EnvAllow []string `json:"env_allow,omitempty"`
	// ShellTimeout is a Go duration string for shell commands (default 120s).
	ShellTimeout string `json:"shell_timeout,omitempty"`
}

// ServerConfig configures the -serve HTTP/WebSocket listener.
//
// Host defaults to loopback. Token is the bearer credential required on /api/*
// and /ws; it is generated on first use and persisted here so a phone can be
// authorised once and stay authorised.
type ServerConfig struct {
	Host         string   `json:"host,omitempty"`
	Port         int      `json:"port,omitempty"`
	Token        string   `json:"token,omitempty"`
	AllowOrigins []string `json:"allow_origins,omitempty"`
}

// ProfileConfig encapsulates provider settings, model preferences, and memory directories
// for a named profile (e.g. "work", "personal").
type ProfileConfig struct {
	Provider        string                `json:"provider,omitempty"`
	Model           string                `json:"model,omitempty"`
	BaseURL         string                `json:"base_url,omitempty"`
	APIKey          string                `json:"api_key,omitempty"`
	APIKeyEnv       string                `json:"api_key_env,omitempty"`
	MemoryDir       string                `json:"memory_dir,omitempty"`
	MountedMemories []MountedMemoryConfig `json:"mounted_memories,omitempty"`
	Compactor       *CompactorConfig      `json:"compactor,omitempty"`
	Web             *WebConfig            `json:"web,omitempty"`
	Sandbox         *SandboxConfig        `json:"sandbox,omitempty"`
}

// ProviderConfig stores credentials and endpoint for an individual model provider.
type ProviderConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
}

// FileConfig represents the JSON structure stored in ~/.golem/config.json or .golem/config.json.
type FileConfig struct {
	ActiveProfile   string                    `json:"active_profile,omitempty"`
	Profiles        map[string]ProfileConfig  `json:"profiles,omitempty"`
	Provider        string                    `json:"provider,omitempty"`
	APIKey          string                    `json:"api_key,omitempty"`
	BaseURL         string                    `json:"base_url,omitempty"`
	Model           string                    `json:"model,omitempty"`
	MemoryDir       string                    `json:"memory_dir,omitempty"`
	MountedMemories []MountedMemoryConfig     `json:"mounted_memories,omitempty"`
	Providers       map[string]ProviderConfig `json:"providers,omitempty"`
	Compactor       *CompactorConfig          `json:"compactor,omitempty"`
	Web             *WebConfig                `json:"web,omitempty"`
	Sandbox         *SandboxConfig            `json:"sandbox,omitempty"`
	Server          *ServerConfig             `json:"server,omitempty"`
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
	tmpPath := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return errors.Wrapf(err, "writing temp config file %s", tmpPath)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return errors.Wrapf(err, "renaming temp config to %s", path)
	}
	return nil
}

// EnsureWebToken returns the configured web auth token, generating and
// persisting one on first use. An empty token means "no auth", so this is what
// keeps a real deployment from silently running unauthenticated.
func EnsureWebToken(cfg *FileConfig, configPath string) (string, error) {
	if cfg.Server == nil {
		cfg.Server = &ServerConfig{}
	}
	if cfg.Server.Token != "" {
		return cfg.Server.Token, nil
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(err, "generating web auth token")
	}
	cfg.Server.Token = hex.EncodeToString(buf)

	if configPath == "" {
		var err error
		configPath, err = DefaultPath()
		if err != nil {
			return "", err
		}
	}
	if err := Save(configPath, *cfg); err != nil {
		return "", errors.Wrapf(err, "persisting web auth token to %s", configPath)
	}
	return cfg.Server.Token, nil
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

// DeduceProviderFromModel inspects model prefixes and common names to guess the provider.
func DeduceProviderFromModel(modelName string) string {
	lower := strings.ToLower(strings.TrimSpace(modelName))
	if strings.Contains(lower, "/") {
		parts := strings.SplitN(lower, "/", 2)
		prefix := strings.TrimSpace(parts[0])
		switch prefix {
		case "opencode-go", ProviderOpenCode:
			return ProviderOpenCode
		case ProviderOpenAI, ProviderOpenRouter, ProviderGemini:
			return prefix
		}
	}
	if strings.HasPrefix(lower, "gemini") {
		return ProviderGemini
	}
	if strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") {
		return ProviderOpenAI
	}
	if strings.HasPrefix(lower, "deepseek") || strings.HasPrefix(lower, "claude") {
		return ProviderOpenCode
	}
	return ""
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
	if deduced := DeduceProviderFromModel(modelName); deduced != "" {
		return deduced
	}
	if clean := strings.ToLower(strings.TrimSpace(fileCfg.Provider)); clean != "" {
		return clean
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

	// Only fall back to generic fileCfg.APIKey if the requested provider matches
	// the fileCfg.Provider (or if fileCfg.Provider is empty and provider is gemini default),
	// to avoid poisoning one provider with another provider's credential.
	cfgProvider := strings.ToLower(strings.TrimSpace(fileCfg.Provider))
	if cleanProvider == cfgProvider || (cfgProvider == "" && cleanProvider == ProviderGemini) {
		return strings.TrimSpace(fileCfg.APIKey)
	}

	return ""
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

// ProfileMemoryDir returns the canonical storage directory for a profile's fractal memory.
// Typically ~/.golem/profiles/<name>/memory
func ProfileMemoryDir(profileName string) (string, error) {
	if profileName == "" {
		profileName = "default"
	}
	cleanName := filepath.Clean(profileName)
	if cleanName == "." || cleanName == ".." || strings.Contains(cleanName, "/") || strings.Contains(cleanName, "\\") {
		return "", errors.Errorf("invalid profile name %q for memory directory", profileName)
	}
	userDir, err := UserDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userDir, "profiles", cleanName, "memory"), nil
}

// MergeConfigs merges workspace config over user config.
func MergeConfigs(userCfg, wsCfg FileConfig) FileConfig {
	merged := userCfg

	if wsCfg.ActiveProfile != "" {
		merged.ActiveProfile = wsCfg.ActiveProfile
	}
	if wsCfg.Provider != "" {
		merged.Provider = wsCfg.Provider
	}
	if wsCfg.APIKey != "" {
		merged.APIKey = wsCfg.APIKey
	}
	if wsCfg.BaseURL != "" {
		merged.BaseURL = wsCfg.BaseURL
	}
	if wsCfg.Model != "" {
		merged.Model = wsCfg.Model
	}
	if wsCfg.MemoryDir != "" {
		merged.MemoryDir = wsCfg.MemoryDir
	}

	if len(wsCfg.MountedMemories) > 0 {
		merged.MountedMemories = append([]MountedMemoryConfig(nil), wsCfg.MountedMemories...)
	}

	if len(wsCfg.Providers) > 0 {
		if merged.Providers == nil {
			merged.Providers = make(map[string]ProviderConfig)
		}
		for k, v := range wsCfg.Providers {
			merged.Providers[k] = v
		}
	}

	if len(wsCfg.Profiles) > 0 {
		if merged.Profiles == nil {
			merged.Profiles = make(map[string]ProfileConfig)
		}
		for k, v := range wsCfg.Profiles {
			merged.Profiles[k] = v
		}
	}

	if wsCfg.Server != nil {
		if merged.Server == nil {
			merged.Server = &ServerConfig{}
		} else {
			clone := *merged.Server
			merged.Server = &clone
		}
		if wsCfg.Server.Host != "" {
			merged.Server.Host = wsCfg.Server.Host
		}
		if wsCfg.Server.Port > 0 {
			merged.Server.Port = wsCfg.Server.Port
		}
		if wsCfg.Server.Token != "" {
			merged.Server.Token = wsCfg.Server.Token
		}
		if len(wsCfg.Server.AllowOrigins) > 0 {
			merged.Server.AllowOrigins = append([]string(nil), wsCfg.Server.AllowOrigins...)
		}
	}

	if wsCfg.Compactor != nil {
		if merged.Compactor == nil {
			merged.Compactor = &CompactorConfig{}
		} else {
			clone := *merged.Compactor
			merged.Compactor = &clone
		}
		if wsCfg.Compactor.Provider != "" {
			merged.Compactor.Provider = wsCfg.Compactor.Provider
		}
		if wsCfg.Compactor.Model != "" {
			merged.Compactor.Model = wsCfg.Compactor.Model
		}
		if wsCfg.Compactor.BaseURL != "" {
			merged.Compactor.BaseURL = wsCfg.Compactor.BaseURL
		}
		if wsCfg.Compactor.APIKey != "" {
			merged.Compactor.APIKey = wsCfg.Compactor.APIKey
		}
		if wsCfg.Compactor.APIKeyEnv != "" {
			merged.Compactor.APIKeyEnv = wsCfg.Compactor.APIKeyEnv
		}
		if wsCfg.Compactor.TargetBytes > 0 {
			merged.Compactor.TargetBytes = wsCfg.Compactor.TargetBytes
		}
	}

	if wsCfg.Sandbox != nil {
		if merged.Sandbox == nil {
			merged.Sandbox = &SandboxConfig{}
		} else {
			clone := *merged.Sandbox
			merged.Sandbox = &clone
		}
		if wsCfg.Sandbox.Mode != "" {
			merged.Sandbox.Mode = wsCfg.Sandbox.Mode
		}
		if len(wsCfg.Sandbox.AllowWrite) > 0 {
			merged.Sandbox.AllowWrite = append([]string(nil), wsCfg.Sandbox.AllowWrite...)
		}
		if len(wsCfg.Sandbox.AllowNet) > 0 {
			merged.Sandbox.AllowNet = append([]string(nil), wsCfg.Sandbox.AllowNet...)
		}
		if len(wsCfg.Sandbox.RequireConfirm) > 0 {
			merged.Sandbox.RequireConfirm = append([]string(nil), wsCfg.Sandbox.RequireConfirm...)
		}
		if len(wsCfg.Sandbox.EnvAllow) > 0 {
			merged.Sandbox.EnvAllow = append([]string(nil), wsCfg.Sandbox.EnvAllow...)
		}
		if wsCfg.Sandbox.ShellTimeout != "" {
			merged.Sandbox.ShellTimeout = wsCfg.Sandbox.ShellTimeout
		}
	}

	return merged
}

// LoadWorkspace loads the workspace configuration (.golem/config.json) if it exists.
func LoadWorkspace(workspaceDir string) (FileConfig, error) {
	if workspaceDir == "" {
		return FileConfig{}, nil
	}
	wsConfigPath := filepath.Join(WorkspaceDir(workspaceDir), "config.json")
	return Load(wsConfigPath)
}

// LoadAll loads user config and merges any workspace config found in workspaceDir.
func LoadAll(workspaceDir string, customConfigPath string) (FileConfig, error) {
	userCfg, err := Load(customConfigPath)
	if err != nil {
		return FileConfig{}, err
	}
	if workspaceDir == "" {
		return userCfg, nil
	}
	wsCfg, err := LoadWorkspace(workspaceDir)
	if err != nil {
		return userCfg, err
	}
	return MergeConfigs(userCfg, wsCfg), nil
}

// ResolveActiveProfile resolves the active profile name and its merged profile configuration.
// Resolution precedence for profile name:
// 1. Explicit flagProfile (e.g. from -profile CLI flag)
// 2. $GOLEM_PROFILE environment variable
// 3. FileConfig.ActiveProfile
// 4. Default: "default"
func ResolveActiveProfile(flagProfile string, fileCfg FileConfig) (string, ProfileConfig) {
	name := strings.TrimSpace(flagProfile)
	if name == "" {
		name = strings.TrimSpace(os.Getenv("GOLEM_PROFILE"))
	}
	if name == "" {
		name = strings.TrimSpace(fileCfg.ActiveProfile)
	}
	if name == "" {
		name = "default"
	}

	var prof ProfileConfig
	if fileCfg.Profiles != nil {
		if p, ok := fileCfg.Profiles[name]; ok {
			prof = p
		}
	}

	// Fallback to top-level FileConfig defaults if profile leaves them empty
	if prof.Provider == "" && fileCfg.Provider != "" {
		prof.Provider = fileCfg.Provider
	}
	if prof.Model == "" && fileCfg.Model != "" {
		prof.Model = fileCfg.Model
	}
	if prof.BaseURL == "" && fileCfg.BaseURL != "" {
		prof.BaseURL = fileCfg.BaseURL
	}
	if prof.APIKey == "" && prof.APIKeyEnv != "" {
		prof.APIKey = os.Getenv(prof.APIKeyEnv)
	}
	if prof.APIKey == "" && fileCfg.APIKey != "" {
		prof.APIKey = fileCfg.APIKey
	}
	if prof.MemoryDir == "" {
		if name != "default" {
			prof.MemoryDir, _ = ProfileMemoryDir(name)
		} else if fileCfg.MemoryDir != "" {
			prof.MemoryDir = fileCfg.MemoryDir
		}
	}

	// Include top-level mounted memories if profile doesn't define any
	if len(prof.MountedMemories) == 0 && len(fileCfg.MountedMemories) > 0 {
		prof.MountedMemories = fileCfg.MountedMemories
	}

	// Inherit compactor config from top-level if profile doesn't specify one
	if prof.Compactor == nil && fileCfg.Compactor != nil {
		prof.Compactor = fileCfg.Compactor
	}

	// Inherit web config from top-level if profile doesn't specify one
	if prof.Web == nil && fileCfg.Web != nil {
		prof.Web = fileCfg.Web
	}

	// Inherit sandbox config from top-level if profile doesn't specify one
	if prof.Sandbox == nil && fileCfg.Sandbox != nil {
		prof.Sandbox = fileCfg.Sandbox
	}

	return name, prof
}

// ApplyProfile overlays the resolved profile onto FileConfig.
func (c FileConfig) ApplyProfile(profileName string, prof ProfileConfig) FileConfig {
	updated := c
	updated.ActiveProfile = profileName
	if prof.Provider != "" {
		updated.Provider = prof.Provider
	}
	if prof.Model != "" {
		updated.Model = prof.Model
	}
	if prof.BaseURL != "" {
		updated.BaseURL = prof.BaseURL
	}
	if prof.APIKey != "" {
		updated.APIKey = prof.APIKey
	}
	if prof.MemoryDir != "" {
		updated.MemoryDir = prof.MemoryDir
	}
	if len(prof.MountedMemories) > 0 {
		updated.MountedMemories = prof.MountedMemories
	}
	if prof.Compactor != nil {
		updated.Compactor = prof.Compactor
	}
	if prof.Web != nil {
		updated.Web = prof.Web
	}
	if prof.Sandbox != nil {
		updated.Sandbox = prof.Sandbox
	}
	return updated
}

// ResolveSandboxConfig returns the effective sandbox config. A missing block
// defaults to mode policy, which is the safe default: accidents are contained
// unless the user explicitly asks for off.
func ResolveSandboxConfig(sb *SandboxConfig) SandboxConfig {
	var out SandboxConfig
	if sb != nil {
		out = *sb
	}
	if strings.TrimSpace(out.Mode) == "" {
		out.Mode = string(sandbox.ModePolicy)
	}
	if strings.TrimSpace(out.ShellTimeout) == "" {
		out.ShellTimeout = "120s"
	}
	return out
}

// ResolveWebConfig returns the effective web config, applying provider defaults
// and environment fallbacks for the API key and CSE id.
func ResolveWebConfig(web *WebConfig) WebConfig {
	var out WebConfig
	if web != nil {
		out = *web
	}
	if out.Provider == "" {
		out.Provider = "google_cse"
	}
	if out.APIKey == "" && out.APIKeyEnv != "" {
		out.APIKey = os.Getenv(out.APIKeyEnv)
	}
	if out.APIKey == "" {
		out.APIKey = os.Getenv("GOOGLE_CSE_API_KEY")
	}
	if out.CX == "" && out.CXEnv != "" {
		out.CX = os.Getenv(out.CXEnv)
	}
	if out.CX == "" {
		out.CX = os.Getenv("GOOGLE_CSE_CX")
	}
	if out.MaxFetch <= 0 {
		out.MaxFetch = 5 << 20 // 5 MiB
	}
	return out
}
