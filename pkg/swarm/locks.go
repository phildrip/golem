package swarm

import (
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// MutatingToolResources maps a mutating tool name to the argument keys that
// identify the file/resource it writes to. The first present key wins.
var MutatingToolResources = map[string][]string{
	"write_file":     {"file_path"},
	"replace_regex":  {"file_path"},
	"hashline_patch": {"file_path"},
}

// toolResourcePath extracts the target resource path from a tool invocation.
// It returns ok=false for tools that do not mutate a single workspace file
// (e.g. shell, read-only tools, swarm tools), which lock enforcement ignores.
func toolResourcePath(toolName string, args map[string]any) (string, bool) {
	keys, ok := MutatingToolResources[toolName]
	if !ok {
		return "", false
	}
	for _, k := range keys {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return canonicalResource(v), true
		}
	}
	return "", false
}

// canonicalResource normalizes a resource path so that equivalent paths
// ("pkg/./a.go", "pkg/a.go") collide consistently for lock checking.
func canonicalResource(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	p = filepath.Clean(p)
	p = filepath.ToSlash(p)
	return strings.TrimPrefix(p, "./")
}

// CheckToolLock returns an error when a mutating tool targets a resource that
// is currently advisory-locked by a DIFFERENT owner. It is a no-op for
// read-only tools, shell, and when the bus is nil.
//
// This is advisory: it prevents an agent from blindly clobbering a file a peer
// declared, but it cannot stop a determined writer (e.g. arbitrary shell).
func CheckToolLock(bus *Bus, owner string, toolName string, args map[string]any) error {
	if bus == nil {
		return nil
	}
	resource, ok := toolResourcePath(toolName, args)
	if !ok {
		return nil
	}
	for _, l := range bus.ListLocks() {
		if l.Owner == owner {
			continue
		}
		if canonicalResource(l.Resource) == resource {
			return errors.Errorf("resource %q is advisory-locked by %q; release_resource or wait for it to expire before %s", resource, l.Owner, toolName)
		}
	}
	return nil
}
