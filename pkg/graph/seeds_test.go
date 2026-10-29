package graph

import (
	"strings"
	"testing"
)

func TestSeedsAreValidGraphs(t *testing.T) {
	names := SeedNames()
	if len(names) == 0 {
		t.Fatal("no seeded graphs are embedded")
	}
	for _, name := range names {
		spec, ok, err := LoadSeed(name)
		if !ok {
			t.Fatalf("seed %q is not loadable but is listed", name)
		}
		if err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
		if spec.Name != name {
			t.Fatalf("seed file %q declares name %q", name, spec.Name)
		}
		if err := spec.Validate(ValidateOptions{}); err != nil {
			t.Fatalf("seed %q does not validate: %v", name, err)
		}
		if spec.Description == "" {
			t.Fatalf("seed %q has no description, so the catalog cannot advertise it", name)
		}
		// A seed that names a tool would be validated against "any name is
		// known" above, which proves nothing: seeds stay tool-free until a test
		// can build them against a real registry.
		for _, n := range spec.Nodes {
			if n.Kind == KindTool {
				t.Fatalf("seed %q has a tool node %q, which this test cannot validate", name, n.ID)
			}
		}
	}
}

func TestSeedsAreListedAsBuiltin(t *testing.T) {
	m := NewManager(t.TempDir(), BuilderConfig{})
	entries, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]CatalogEntry{}
	for _, e := range entries {
		seen[e.Name] = e
	}
	for _, name := range SeedNames() {
		e, ok := seen[name]
		if !ok {
			t.Fatalf("seed %q missing from the catalog: %+v", name, entries)
		}
		if !e.Builtin() {
			t.Fatalf("seed %q is listed as %q, want %q", name, e.Source, SourceBuiltin)
		}
		if e.Nodes < 2 {
			t.Fatalf("seed %q lists %d nodes", name, e.Nodes)
		}
	}
	if cat := m.Catalog(); !strings.Contains(cat, "built-in") {
		t.Fatalf("the prompt catalog does not mark seeds as built-in:\n%s", cat)
	}
}

func TestLoadSeedReturnsAFreshCopy(t *testing.T) {
	name := SeedNames()[0]
	first, _, err := LoadSeed(name)
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if len(first.Nodes) == 0 {
		t.Fatalf("seed %q has no nodes", name)
	}
	first.Name = "clobbered"
	first.Nodes[0].Instruction = "clobbered"
	first.Edges = nil

	second, _, err := LoadSeed(name)
	if err != nil {
		t.Fatalf("LoadSeed: %v", err)
	}
	if second.Name != name || second.Nodes[0].Instruction == "clobbered" || len(second.Edges) == 0 {
		t.Fatalf("a caller mutated the embedded seed: %+v", second)
	}
}

func TestSavedGraphShadowsASeed(t *testing.T) {
	m := NewManager(t.TempDir(), BuilderConfig{Models: stubModels{}})
	name := "adversarial-review"

	spec, ok, err := LoadSeed(name)
	if !ok || err != nil {
		t.Fatalf("expected %q to be a seed: ok=%v err=%v", name, ok, err)
	}
	spec.Description = "our own version"
	if _, err := m.Save(spec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var matches []CatalogEntry
	for _, e := range entries {
		if e.Name == name {
			matches = append(matches, e)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected one catalog entry for %q, got %+v", name, matches)
	}
	if matches[0].Builtin() || matches[0].Description != "our own version" {
		t.Fatalf("the saved graph does not shadow the seed: %+v", matches[0])
	}

	loaded, err := m.Load(name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Description != "our own version" {
		t.Fatalf("Load returned the seed instead of the saved graph: %+v", loaded)
	}

	// Deleting the saved copy restores the built-in rather than leaving a hole.
	if err := m.Delete(name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	restored, err := m.Load(name)
	if err != nil {
		t.Fatalf("Load after delete: %v", err)
	}
	if restored.Description == "our own version" {
		t.Fatal("deleting the saved graph did not fall back to the seed")
	}
}

func TestDeletingABuiltinIsRefused(t *testing.T) {
	m := NewManager(t.TempDir(), BuilderConfig{})
	err := m.Delete(SeedNames()[0])
	if err == nil {
		t.Fatal("expected deleting a built-in to fail")
	}
	if !strings.Contains(err.Error(), "built-in") || !strings.Contains(err.Error(), "override") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestLoadUnknownNamePointsAtGraphList(t *testing.T) {
	m := NewManager(t.TempDir(), BuilderConfig{})
	_, err := m.Load("no-such-graph")
	if err == nil || !strings.Contains(err.Error(), "graph_list") {
		t.Fatalf("expected a pointer to graph_list, got %v", err)
	}
}
