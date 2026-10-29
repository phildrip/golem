package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// GraphsDir is where specs live, relative to the workspace: graphs are
// project-shaped procedures, so they belong with the project (and can be
// reviewed, diffed and committed like any other asset) rather than in a
// per-machine config directory.
const GraphsDir = ".golem/graphs"

// Save validates spec and writes it to the manager's directory, replacing any
// existing graph of the same name.
func (m *Manager) Save(spec *Spec) (string, error) {
	if err := spec.Validate(ValidateOptions{KnownTool: func(name string) bool { return m.builder.ToolNames()[name] }}); err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return "", errors.Wrapf(err, "creating graph directory %s", m.dir)
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", errors.Wrap(err, "encoding graph spec")
	}
	data = append(data, '\n')
	path := m.path(spec.Name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", errors.Wrapf(err, "writing %s", path)
	}
	m.invalidateCatalog()
	return path, nil
}

// Load reads one saved graph.
func (m *Manager) Load(name string) (*Spec, error) {
	if !nameRE.MatchString(name) {
		return nil, fmt.Errorf("graph name %q must be 1-64 chars of lowercase letters, digits, - or _", name)
	}
	data, err := os.ReadFile(m.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no saved graph named %q (use graph_list to see what exists)", name)
		}
		return nil, errors.Wrapf(err, "reading graph %q", name)
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, errors.Wrapf(err, "parsing %s", m.path(name))
	}
	spec.Normalize()
	return &spec, nil
}

// List returns the saved graphs, sorted by name. A spec that no longer parses
// is reported rather than hidden: a broken file is the agent's problem to fix.
func (m *Manager) List() ([]CatalogEntry, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "reading graph directory %s", m.dir)
	}
	var out []CatalogEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		spec, err := m.Load(name)
		if err != nil {
			out = append(out, CatalogEntry{Name: name, Description: "unreadable: " + err.Error()})
			continue
		}
		out = append(out, CatalogEntry{Name: spec.Name, Description: spec.Description, Nodes: len(spec.Nodes)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// catalogTTL bounds how stale the prompt catalog may be. It is read on every
// model call, so it must not stat the directory every time; it must also pick
// up a graph saved earlier in the same turn.
const catalogTTL = 2 * time.Second

// Catalog renders the prompt section the orchestrator sees each turn.
func (m *Manager) Catalog() string {
	m.mu.Lock()
	if time.Since(m.catalogAt) < catalogTTL {
		cached := m.catalog
		m.mu.Unlock()
		return cached
	}
	m.mu.Unlock()

	entries, err := m.List()
	rendered := ""
	if err == nil {
		rendered = formatCatalog(entries)
	}

	m.mu.Lock()
	m.catalog = rendered
	m.catalogAt = time.Now()
	m.mu.Unlock()
	return rendered
}

// Delete removes a saved graph.
func (m *Manager) Delete(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("graph name %q must be 1-64 chars of lowercase letters, digits, - or _", name)
	}
	path := m.path(name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no saved graph named %q", name)
		}
		return errors.Wrapf(err, "removing %s", path)
	}
	m.invalidateCatalog()
	return nil
}

// invalidateCatalog drops the cached prompt section so a graph saved in this
// turn is visible to this turn's next model call.
func (m *Manager) invalidateCatalog() {
	m.mu.Lock()
	m.catalogAt = time.Time{}
	m.mu.Unlock()
}

func (m *Manager) path(name string) string {
	return filepath.Join(m.dir, name+".json")
}
