//go:build !linux

package sandbox

import "fmt"

// Landlock is Linux-only, so other platforms cannot enforce mode isolation.
// They say so loudly rather than running commands unconfined under a label that
// claims otherwise.

// LandlockABI reports 0: no Landlock on this platform.
func LandlockABI() int { return 0 }

// IsolationSupported always fails off Linux.
func IsolationSupported() error {
	return fmt.Errorf("%w: OS isolation requires Linux (Landlock)", ErrIsolationUnsupported)
}

// IsolationFeatures describes the (absent) enforcement mechanism.
func IsolationFeatures() string { return "unavailable" }
