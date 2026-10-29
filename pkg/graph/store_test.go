package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveListLoadDelete(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, BuilderConfig{Models: stubModels{}})

	spec := fanOutSpec()
	spec.Description = "fan out over two angles and merge"
	path, err := m.Save(spec)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if want := filepath.Join(dir, "spike.json"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	entries, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "spike" || entries[0].Nodes != 4 {
		t.Fatalf("unexpected catalog: %+v", entries)
	}
	if cat := m.Catalog(); !strings.Contains(cat, "fan out over two angles") {
		t.Fatalf("catalog missing description:\n%s", cat)
	}

	loaded, err := m.Load("spike")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Sink() != "sink" || len(loaded.Nodes) != 4 {
		t.Fatalf("round trip changed the spec: %+v", loaded)
	}
	if loaded.Budgets.MaxTokens != spec.Budgets.MaxTokens {
		t.Fatalf("budgets lost in round trip: %+v", loaded.Budgets)
	}

	if err := m.Delete("spike"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := m.Load("spike"); err == nil || !strings.Contains(err.Error(), "graph_list") {
		t.Fatalf("expected a helpful missing-graph error, got %v", err)
	}
}

func TestSaveRejectsInvalidSpec(t *testing.T) {
	m := NewManager(t.TempDir(), BuilderConfig{})
	spec := fanOutSpec()
	spec.Name = "Not Allowed"
	if _, err := m.Save(spec); err == nil {
		t.Fatal("expected the invalid name to be rejected before writing")
	}
	entries, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a rejected spec was written: %+v", entries)
	}
}

func TestNamesCannotEscapeTheGraphDirectory(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, BuilderConfig{})
	for _, name := range []string{"../evil", "a/b", "..", ".", "with space"} {
		if _, err := m.Load(name); err == nil {
			t.Fatalf("Load(%q) accepted a path-like name", name)
		}
		if err := m.Delete(name); err == nil {
			t.Fatalf("Delete(%q) accepted a path-like name", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "evil.json")); err == nil {
		t.Fatal("a spec escaped the graph directory")
	}
}

func TestUnreadableSpecIsReportedNotHidden(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, BuilderConfig{})
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(entries[0].Description, "unreadable") {
		t.Fatalf("broken spec not surfaced: %+v", entries)
	}
}

// A graph saved mid-session must be visible to the same session's next model
// call, not after a cache TTL.
func TestCatalogSeesGraphsSavedInTheSameTurn(t *testing.T) {
	m := NewManager(t.TempDir(), BuilderConfig{})
	if cat := m.Catalog(); cat != "" {
		t.Fatalf("expected an empty catalog, got:\n%s", cat)
	}
	spec := fanOutSpec()
	spec.Description = "review two angles"
	if _, err := m.Save(spec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	cat := m.Catalog()
	if !strings.Contains(cat, "spike") || !strings.Contains(cat, "review two angles") {
		t.Fatalf("catalog did not pick up the new graph:\n%s", cat)
	}
	if err := m.Delete("spike"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if cat := m.Catalog(); strings.Contains(cat, "spike") {
		t.Fatalf("catalog still lists a deleted graph:\n%s", cat)
	}
}
