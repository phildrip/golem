package sandbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// OS isolation, mode "isolation".
//
// The shell tool cannot confine itself: a policy check inspects a string, and a
// string is not a sandbox. Mode isolation therefore re-executes the golem
// binary as a short-lived wrapper (see isolation_linux.go) that applies kernel
// confinement to itself and then execs bash. Nothing outside golem is involved
// - no bubblewrap, no nsjail, no root, no daemon - because Linux enforces this
// in-process and unprivileged.
//
// The wrapper is our own executable rather than a helper binary so that
// `go build` produces the whole sandbox: the same binary is the agent and the
// confinement shim, and tests exercise the real path.

// envIsolationSpec carries the encoded Spec from the parent to the wrapper.
// The wrapper clears it before exec so the confined command never sees it.
const envIsolationSpec = "GOLEM_SANDBOX_SPEC"

// isolationExitSetupFailure is the wrapper's exit status when it cannot build
// the sandbox. It mirrors env(1)'s 125 so a broken sandbox is never mistaken
// for the command's own exit status.
const isolationExitSetupFailure = 125

// ErrIsolationUnsupported reports a host that cannot enforce mode isolation.
var ErrIsolationUnsupported = errors.New("sandbox: OS isolation is unavailable on this host")

// Spec is everything the wrapper needs to confine one command. It crosses a
// process boundary as JSON, so it stays a plain value with no pointers.
type Spec struct {
	// Command is the shell command, run as bash -c.
	Command string `json:"command"`
	// Dir is the working directory, normally the workspace.
	Dir string `json:"dir,omitempty"`
	// Env is the complete environment for the command; nothing is inherited.
	Env []string `json:"env,omitempty"`
	// TimeoutMS bounds wall-clock time. Zero means DefaultShellTimeout.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
	// ReadOnly lists paths the command may read and execute but not write.
	ReadOnly []string `json:"read_only,omitempty"`
	// ReadWrite lists paths the command may modify freely.
	ReadWrite []string `json:"read_write,omitempty"`
	// AllowNet permits outbound TCP; false denies bind and connect.
	AllowNet bool `json:"allow_net,omitempty"`
	// ScopeSignal stops the command signalling processes outside the sandbox,
	// so a hostile model cannot kill or pause the agent that spawned it.
	ScopeSignal bool `json:"scope_signal,omitempty"`
}

// Result is the outcome of one isolated execution.
type Result struct {
	Output   string
	ExitCode int
}

// Run executes spec in a confined child and returns its combined output.
//
// The child is our own binary: Run sets the spec in its environment and the
// package init in isolation_linux.go turns it into a sandbox and an exec. A
// setup failure surfaces as an error rather than as a command that silently ran
// unconfined.
func Run(ctx context.Context, spec Spec) (Result, error) {
	if err := IsolationSupported(); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(spec.Command) == "" {
		return Result{Output: "", ExitCode: 0}, nil
	}

	encoded, err := encodeSpec(spec)
	if err != nil {
		return Result{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return Result{}, fmt.Errorf("sandbox: locating the golem binary: %w", err)
	}

	timeout := DefaultShellTimeout
	if spec.TimeoutMS > 0 {
		timeout = time.Duration(spec.TimeoutMS) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, exe)
	// The wrapper only needs PATH to find bash; the command gets spec.Env.
	cmd.Env = append(append([]string{}, spec.Env...), envIsolationSpec+"="+encoded)
	cmd.Stdin = bytes.NewReader(nil)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// Its own process group so a timeout takes the whole tree with it, and a
	// dead golem cannot leave the sandbox running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	runErr := cmd.Run()
	output := out.String()
	if runCtx.Err() == context.DeadlineExceeded {
		return Result{Output: output, ExitCode: -1}, fmt.Errorf("sandbox: command timed out after %s", timeout)
	}

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return Result{Output: output, ExitCode: -1}, fmt.Errorf("sandbox: running isolated command: %w", runErr)
		}
		exitCode = exitErr.ExitCode()
		if exitCode == isolationExitSetupFailure && strings.Contains(output, "golem sandbox:") {
			return Result{Output: output, ExitCode: exitCode}, fmt.Errorf("sandbox: %s", lastSandboxError(output))
		}
	}
	return Result{Output: output, ExitCode: exitCode}, nil
}

// lastSandboxError extracts the wrapper's own diagnostic from combined output.
func lastSandboxError(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "golem sandbox:") {
			return strings.TrimPrefix(lines[i], "golem sandbox:")
		}
	}
	return output
}

// encodeSpec and decodeSpec are split out so the wrapper and the tests can
// round-trip a spec without spawning a process.
func encodeSpec(spec Spec) (string, error) {
	payload, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("sandbox: encoding isolation spec: %w", err)
	}
	return base64.StdEncoding.EncodeToString(payload), nil
}

func decodeSpec(raw string) (Spec, error) {
	var spec Spec
	payload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return spec, fmt.Errorf("decoding isolation spec: %w", err)
	}
	if err := json.Unmarshal(payload, &spec); err != nil {
		return spec, fmt.Errorf("decoding isolation spec: %w", err)
	}
	return spec, nil
}

// --- default sandbox layout ------------------------------------------------

// defaultReadOnlyPaths are the system trees a command needs in order to run at
// all: binaries, shared objects, and the configuration a shell reads at start
// up. They are readable and executable, and anything not listed is invisible.
var defaultReadOnlyPaths = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32",
	"/etc", "/opt", "/usr/local", "/nix", "/run/current-system",
}

// defaultReadWritePaths are paths that must stay writable for ordinary work to
// succeed: the temp dir every toolchain assumes, the device nodes a shell
// writes to, and the build caches that turn a cold build into a fast one.
var defaultReadWritePaths = []string{
	"/tmp", "/dev/null", "/dev/zero", "/dev/full", "/dev/random", "/dev/urandom",
	"~/.cache/go-build", "~/.cache/pip", "~/.npm", "~/.cargo/registry", "~/.gitconfig",
}

// expandHome turns a leading ~ into the user's home directory.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
}

// existingPaths drops entries that are not on this host, so one default list
// can span distros without failing rule creation.
func existingPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		resolved := filepath.Clean(expandHome(p))
		if seen[resolved] {
			continue
		}
		if _, err := os.Stat(resolved); err != nil {
			continue
		}
		seen[resolved] = true
		out = append(out, resolved)
	}
	return out
}

// goToolchainPaths returns the read-only and writable paths the Go toolchain
// needs, derived from the environment so a GOPATH elsewhere still builds.
func goToolchainPaths() (readOnly, readWrite []string) {
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		readOnly = append(readOnly, goroot)
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		readWrite = append(readWrite, gopath)
	}
	if cache := os.Getenv("GOCACHE"); cache != "" {
		readWrite = append(readWrite, cache)
	}
	if modCache := os.Getenv("GOMODCACHE"); modCache != "" {
		readWrite = append(readWrite, modCache)
	}
	return readOnly, readWrite
}
