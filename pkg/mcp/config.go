package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/config"
)

// ServerConfig defines the execution specification for a single MCP server.
type ServerConfig struct {
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	ServerURL string            `json:"serverUrl,omitempty"`
	Type      string            `json:"type,omitempty"`
	Disabled  bool              `json:"disabled,omitempty"`
}

// Config represents a collection of named MCP servers.
type Config struct {
	Servers map[string]ServerConfig `json:"mcpServers"`
}

// DiscoveryPaths returns Golem-specific configuration locations in priority order:
// 1. Workspace-local Golem config: <workspace>/.golem/mcp.json
// 2. User Golem config directory: <UserDir>/mcp.json (~/.golem/mcp.json or ~/.config/golem/mcp.json)
func DiscoveryPaths(workspaceDir string) []string {
	var paths []string

	// 1. Workspace-local configs (highest priority)
	if workspaceDir != "" {
		paths = append(paths, filepath.Join(config.WorkspaceDir(workspaceDir), "mcp.json"))
	}

	// 2. User Golem config directory
	if userDir, err := config.UserDir(); err == nil && userDir != "" {
		paths = append(paths, filepath.Join(userDir, "mcp.json"))
	}

	// 3. Fallback compatibility: check ~/.config/golem/mcp.json if distinct from UserDir
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		fallback := filepath.Join(home, ".config", "golem", "mcp.json")
		alreadyIncluded := false
		for _, p := range paths {
			if p == fallback {
				alreadyIncluded = true
				break
			}
		}
		if !alreadyIncluded {
			paths = append(paths, fallback)
		}
	}

	return paths
}

// rawUnifiedConfig handles both {"mcpServers": ...} (Cursor/Claude/Antigravity) and {"servers": ...} (VS Code).
type rawUnifiedConfig struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
	Servers    map[string]ServerConfig `json:"servers"`
}

// ParseConfigData parses and normalizes JSON data in either mcpServers or servers format.
func ParseConfigData(data []byte) (Config, error) {
	var raw rawUnifiedConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, errors.Wrap(err, "unmarshaling mcp config json")
	}

	merged := make(map[string]ServerConfig)

	// Ingest servers (VS Code format)
	for name, srv := range raw.Servers {
		srv = expandEnvInConfig(srv)
		merged[name] = srv
	}

	// Ingest mcpServers (Cursor / Antigravity format, overrides servers if both present)
	for name, srv := range raw.MCPServers {
		srv = expandEnvInConfig(srv)
		merged[name] = srv
	}

	return Config{Servers: merged}, nil
}

// LoadFile reads and parses an MCP configuration file from path.
func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, errors.Wrapf(err, "reading mcp config file at %s", path)
	}
	return ParseConfigData(data)
}

// DiscoverAndMerge searches discovery paths and merges all found configurations.
// Workspace configs take precedence over user home configs.
func DiscoverAndMerge(workspaceDir string) (Config, []string, error) {
	paths := DiscoveryPaths(workspaceDir)
	merged := make(map[string]ServerConfig)
	var loadedFiles []string

	// Iterate backwards so higher-priority files override lower-priority ones
	for i := len(paths) - 1; i >= 0; i-- {
		path := paths[i]
		if _, err := os.Stat(path); err != nil {
			continue
		}

		cfg, err := LoadFile(path)
		if err != nil {
			// Skip corrupted or unreadable files gracefully
			continue
		}

		loadedFiles = append(loadedFiles, path)
		for name, srv := range cfg.Servers {
			merged[name] = srv
		}
	}

	return Config{Servers: merged}, loadedFiles, nil
}

// expandEnvInConfig expands environment variables ($VAR or ${VAR}) in Command, Args, and Env map.
func expandEnvInConfig(cfg ServerConfig) ServerConfig {
	cfg.Command = os.ExpandEnv(cfg.Command)
	if cfg.URL != "" {
		cfg.URL = os.ExpandEnv(cfg.URL)
	}
	if cfg.ServerURL != "" {
		cfg.ServerURL = os.ExpandEnv(cfg.ServerURL)
	}

	var newArgs []string
	for _, arg := range cfg.Args {
		newArgs = append(newArgs, os.ExpandEnv(arg))
	}
	cfg.Args = newArgs

	if cfg.Env != nil {
		newEnv := make(map[string]string, len(cfg.Env))
		for k, v := range cfg.Env {
			newEnv[k] = os.ExpandEnv(v)
		}
		cfg.Env = newEnv
	}

	return cfg
}

// EndpointURL returns either URL or ServerURL if defined.
func (s ServerConfig) EndpointURL() string {
	if s.URL != "" {
		return s.URL
	}
	return s.ServerURL
}

// IsRemote returns true if the server config points to an HTTP/SSE endpoint rather than a local binary.
func (s ServerConfig) IsRemote() bool {
	u := strings.ToLower(s.EndpointURL())
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}
