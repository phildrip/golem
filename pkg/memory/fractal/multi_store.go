package fractal

import (
	"fmt"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// MountedStore represents a named fractal memory store mounted into the session.
type MountedStore struct {
	Name     string
	Store    *Store
	View     *View
	ReadOnly bool
}

// StoreRegistry defines lookup for primary and mounted fractal stores.
type StoreRegistry interface {
	PrimaryStore() *Store
	GetStore(name string) (*Store, bool)
	AllMounted() []*MountedStore
}

// SingleStoreRegistry wraps a single Store to satisfy StoreRegistry.
type SingleStoreRegistry struct {
	Store *Store
}

// PrimaryStore returns the single store.
func (s *SingleStoreRegistry) PrimaryStore() *Store {
	return s.Store
}

// GetStore returns the single store if name matches or is empty.
func (s *SingleStoreRegistry) GetStore(name string) (*Store, bool) {
	clean := strings.ToLower(strings.TrimSpace(name))
	if clean == "" || clean == "primary" || clean == "workspace" {
		return s.Store, true
	}
	return nil, false
}

// AllMounted returns empty slice.
func (s *SingleStoreRegistry) AllMounted() []*MountedStore {
	return nil
}

// MemoryManager coordinates the primary workspace memory and mounted read-only reference stores.
type MemoryManager struct {
	mu           sync.RWMutex
	primaryStore *Store
	primaryView  *View
	mounted      map[string]*MountedStore
	order        []string
}

// NewMemoryManager initializes a MemoryManager with a primary store and view.
func NewMemoryManager(primaryStore *Store, primaryView *View) *MemoryManager {
	return &MemoryManager{
		primaryStore: primaryStore,
		primaryView:  primaryView,
		mounted:      make(map[string]*MountedStore),
		order:        make([]string, 0),
	}
}

// Mount adds a secondary store under a namespace.
func (m *MemoryManager) Mount(name string, store *Store, view *View, readOnly bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanName := strings.ToLower(strings.TrimSpace(name))
	if cleanName == "" {
		return
	}
	if _, exists := m.mounted[cleanName]; !exists {
		m.order = append(m.order, cleanName)
	}
	m.mounted[cleanName] = &MountedStore{
		Name:     cleanName,
		Store:    store,
		View:     view,
		ReadOnly: readOnly,
	}
}

// Unmount removes and closes a mounted secondary store.
func (m *MemoryManager) Unmount(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanName := strings.ToLower(strings.TrimSpace(name))
	ms, ok := m.mounted[cleanName]
	if !ok {
		return nil
	}
	delete(m.mounted, cleanName)

	newOrder := make([]string, 0, len(m.order))
	for _, n := range m.order {
		if n != cleanName {
			newOrder = append(newOrder, n)
		}
	}
	m.order = newOrder

	if ms.Store != nil {
		return ms.Store.Close()
	}
	return nil
}

// PrimaryStore returns the active primary store.
func (m *MemoryManager) PrimaryStore() *Store {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.primaryStore
}

// PrimaryView returns the active primary view.
func (m *MemoryManager) PrimaryView() *View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.primaryView
}

// GetStore retrieves a store by name (or returns primary store if name is empty, "primary", or "workspace").
func (m *MemoryManager) GetStore(name string) (*Store, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cleanName := strings.ToLower(strings.TrimSpace(name))
	if cleanName == "" || cleanName == "primary" || cleanName == "workspace" {
		return m.primaryStore, m.primaryStore != nil
	}

	ms, ok := m.mounted[cleanName]
	if !ok {
		return nil, false
	}
	return ms.Store, true
}

// AllMounted returns all mounted stores in insertion order.
func (m *MemoryManager) AllMounted() []*MountedStore {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]*MountedStore, 0, len(m.order))
	for _, name := range m.order {
		if ms, ok := m.mounted[name]; ok {
			res = append(res, ms)
		}
	}
	return res
}

// SyncAll syncs the primary view and all mounted views.
func (m *MemoryManager) SyncAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.primaryView != nil {
		m.primaryView.Sync()
	}
	for _, ms := range m.mounted {
		if ms.View != nil {
			ms.View.Sync()
		}
	}
}

// RenderCombined returns the combined view: mounted profile/reference views followed by primary workspace view.
func (m *MemoryManager) RenderCombined() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var sb strings.Builder

	// Render mounted reference memories first
	for _, name := range m.order {
		ms := m.mounted[name]
		if ms != nil && ms.View != nil {
			rendered := ms.View.Render()
			if rendered != "" {
				sb.WriteString(fmt.Sprintf("<mounted_memory name=%q read_only=%t>\n", ms.Name, ms.ReadOnly))
				sb.WriteString(rendered)
				sb.WriteString("\n</mounted_memory>\n\n")
			}
		}
	}

	// Render primary workspace memory
	if m.primaryView != nil {
		rendered := m.primaryView.Render()
		if rendered != "" {
			sb.WriteString(rendered)
		}
	}

	return sb.String()
}

// Close closes the primary store and all mounted stores.
func (m *MemoryManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []string
	if m.primaryStore != nil {
		if err := m.primaryStore.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, ms := range m.mounted {
		if ms.Store != nil {
			if err := ms.Store.Close(); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// RememberArgs defines input for persisting global or profile-scoped knowledge.
type RememberArgs struct {
	Text  string `json:"text" jsonschema:"Key architectural decision, convention, user preference, or fact to remember."`
	Scope string `json:"scope,omitempty" jsonschema:"Optional target profile or memory name (e.g. 'work', 'personal'). Defaults to active profile memory."`
}

// RememberResult defines output from the remember tool.
type RememberResult struct {
	Message string `json:"message" jsonschema:"Confirmation of the recorded memory event."`
}

// Remember persists an important user preference, convention, architectural decision, or fact into profile/long-term memory.
func Remember(registry StoreRegistry, defaultScope string, args RememberArgs) (RememberResult, error) {
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return RememberResult{}, errors.New("cannot remember empty text")
	}

	targetScope := strings.TrimSpace(args.Scope)
	if targetScope == "" {
		targetScope = defaultScope
	}

	store, ok := registry.GetStore(targetScope)
	if !ok || store == nil {
		store = registry.PrimaryStore()
		targetScope = "primary"
	}
	if store == nil {
		return RememberResult{}, errors.New("no active memory store available to record memory")
	}

	ev, err := store.AppendEvent(KindWork, fmt.Sprintf("Remembered [%s]: %s", targetScope, text))
	if err != nil {
		return RememberResult{}, errors.Wrap(err, "appending memory event")
	}

	// Auto-sync views across registry so new event is immediately reflected in fractal tails
	if syncable, ok := registry.(interface{ SyncAll() }); ok {
		syncable.SyncAll()
	}

	// Auto-fold level 0 node for immediate tree structuring if not yet present
	if _, hasNode := store.GetNode(0, ev.ID); !hasNode {
		_ = store.SaveNode(Node{
			Level: 0,
			Index: ev.ID,
			Text:  ev.FormatLine(),
			Size:  ev.Size,
		})
	}

	return RememberResult{
		Message: fmt.Sprintf("Recorded to %s memory (event #%d): %s", targetScope, ev.ID, text),
	}, nil
}

// NewRememberTool creates a tool that persists high-level learnings to a target store.
func NewRememberTool(registry StoreRegistry, defaultScope string) (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name:        "remember",
		Description: "Persists an important user preference, convention, architectural decision, or fact into profile/long-term memory so it survives across sessions.",
	}, func(ctx agent.Context, args RememberArgs) (RememberResult, error) {
		return Remember(registry, defaultScope, args)
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating remember tool")
	}
	return t, nil
}
