package graph

import (
	"embed"
	"encoding/json"
	"path"
	"sort"
	"strings"

	"github.com/pkg/errors"
)

// Seeded graphs are procedures that pay for themselves on the first reuse and
// that a model reliably gets wrong when it writes them from scratch: an
// adversarial review whose critics are independent instead of a single critic
// grading its own homework, and a research fan-out that verifies its own claims
// instead of laundering a guess into a brief.
//
// They are embedded rather than written into the workspace so an upgrade ships
// a better prompt, and they are read-only: saving a graph under the same name
// shadows one, which is how you customise it without losing the reference.
//
//go:embed seeds/*.json
var seedFS embed.FS

// SeedNames returns the seeded graph names, sorted.
func SeedNames() []string {
	entries, err := seedFS.ReadDir("seeds")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names
}

// LoadSeed returns a fresh copy of the seeded graph called name. A seed is
// parsed on every call rather than cached: they are a few kilobytes, and a
// caller that mutates the result must not be able to corrupt the next run.
func LoadSeed(name string) (*Spec, bool, error) {
	if !nameRE.MatchString(name) {
		return nil, false, nil
	}
	data, err := seedFS.ReadFile(path.Join("seeds", name+".json"))
	if err != nil {
		return nil, false, nil
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		// A seed that will not parse is a build defect, not a user mistake:
		// report it loudly instead of pretending the graph does not exist.
		return nil, true, errors.Wrapf(err, "the built-in graph %q is corrupt", name)
	}
	spec.Normalize()
	return &spec, true, nil
}

// SeedCatalog lists the seeded graphs as catalog entries.
func SeedCatalog() []CatalogEntry {
	var out []CatalogEntry
	for _, name := range SeedNames() {
		spec, ok, err := LoadSeed(name)
		if !ok {
			continue
		}
		if err != nil {
			out = append(out, CatalogEntry{Name: name, Source: SourceBuiltin, Description: "unreadable: " + err.Error()})
			continue
		}
		out = append(out, CatalogEntry{
			Name:        spec.Name,
			Description: spec.Description,
			Nodes:       len(spec.Nodes),
			Source:      SourceBuiltin,
		})
	}
	return out
}
