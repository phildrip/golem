package sandbox

import "fmt"

// isolationPathsFor assembles the default sandbox layout plus whatever the
// profile asked for, keeping only paths that exist on this host. The result is
// the complete visible filesystem for a confined command: read-write entries
// are granted in full, read-only entries may be read and executed, and
// everything else is invisible.
func isolationPathsFor(workspace string, extraRO, extraRW []string) (readOnly, readWrite []string) {
	ro := append([]string{}, defaultReadOnlyPaths...)
	ro = append(ro, extraRO...)
	rw := append([]string{}, defaultReadWritePaths...)
	if workspace != "" {
		rw = append(rw, workspace)
	}
	rw = append(rw, extraRW...)
	gorootRO, goRW := goToolchainPaths()
	ro = append(ro, gorootRO...)
	rw = append(rw, goRW...)

	// A path granted read-write does not also need a read-only rule, and
	// Landlock rejects contradictory grants for the same object, so the two
	// lists are kept disjoint with writable winning.
	rwSet := existingPaths(rw)
	writable := make(map[string]bool, len(rwSet))
	for _, p := range rwSet {
		writable[p] = true
	}
	roSet := make([]string, 0, len(ro))
	for _, p := range existingPaths(ro) {
		if !writable[p] {
			roSet = append(roSet, p)
		}
	}
	return roSet, rwSet
}

// describeIsolation summarises a spec for the banner and logs.
func describeIsolation(spec Spec) string {
	net := "network denied"
	if spec.AllowNet {
		net = "network allowed"
	}
	return fmt.Sprintf("%d read-only + %d writable paths, %s", len(spec.ReadOnly), len(spec.ReadWrite), net)
}
