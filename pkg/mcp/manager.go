package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/errors"
)

// Manager coordinates multiple MCP client instances and lifecycle.
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client
	configs map[string]ServerConfig
	errors  map[string]error
}

// NewManager creates a new MCP server manager.
func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*Client),
		configs: make(map[string]ServerConfig),
		errors:  make(map[string]error),
	}
}

// LoadConfig registers server specifications and starts local stdio servers concurrently.
func (m *Manager) LoadConfig(ctx context.Context, cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, srvCfg := range cfg.Servers {
		if srvCfg.Disabled {
			continue
		}
		m.configs[name] = srvCfg

		// Remote servers (HTTP/SSE) are noted; local stdio servers are spawned
		if srvCfg.IsRemote() {
			continue
		}

		client := NewClient(name, srvCfg)
		m.clients[name] = client

		// Launch initialization in background with timeout
		go func(n string, c *Client) {
			initCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := c.Start(initCtx); err != nil {
				m.mu.Lock()
				m.errors[n] = errors.Wrapf(err, "server %s failed to initialize", n)
				m.mu.Unlock()
			}
		}(name, client)
	}
}

// StartServer manually starts a single configured server by name synchronously.
func (m *Manager) StartServer(ctx context.Context, name string) error {
	m.mu.Lock()
	cfg, ok := m.configs[name]
	if !ok {
		m.mu.Unlock()
		return errors.Errorf("unknown mcp server %q", name)
	}

	client, ok := m.clients[name]
	if !ok {
		client = NewClient(name, cfg)
		m.clients[name] = client
	}
	m.mu.Unlock()

	err := client.Start(ctx)
	m.mu.Lock()
	if err != nil {
		m.errors[name] = err
	} else {
		delete(m.errors, name)
	}
	m.mu.Unlock()
	return err
}

// ActiveClients returns all currently running clients.
func (m *Manager) ActiveClients() []*Client {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var res []*Client
	for _, c := range m.clients {
		c.mu.Lock()
		running := c.running
		c.mu.Unlock()
		if running {
			res = append(res, c)
		}
	}
	return res
}

// ServerNames returns names of all configured servers.
func (m *Manager) ServerNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var names []string
	for n := range m.configs {
		names = append(names, n)
	}
	return names
}

// ServerStatus returns the status details for a given server name.
func (m *Manager) ServerStatus(name string) (running bool, toolCount int, isRemote bool, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cfg, ok := m.configs[name]
	if !ok {
		return false, 0, false, errors.Errorf("server not found")
	}

	if cfg.IsRemote() {
		return false, 0, true, nil
	}

	client, ok := m.clients[name]
	if !ok {
		return false, 0, false, m.errors[name]
	}

	client.mu.Lock()
	running = client.running
	toolCount = len(client.tools)
	client.mu.Unlock()

	return running, toolCount, false, m.errors[name]
}

// ServerConfig returns the configuration specification for a given server name.
func (m *Manager) ServerConfig(name string) (ServerConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.configs[name]
	return cfg, ok
}

// Close gracefully stops all running MCP client processes.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.clients {
		c.Stop()
	}
	m.clients = make(map[string]*Client)
}
