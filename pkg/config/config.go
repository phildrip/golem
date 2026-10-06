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

// FileConfig represents the JSON structure stored in ~/.config/golem/config.json.
type FileConfig struct {
	APIKey    string `json:"api_key,omitempty"`
	Model     string `json:"model,omitempty"`
	MemoryDir string `json:"memory_dir,omitempty"`
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
