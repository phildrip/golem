package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"github.com/user/golem/pkg/sandbox"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ShellArgs defines the input arguments for the shell execution tool.
type ShellArgs struct {
	Command        string `json:"command" jsonschema:"The shell command to execute using bash"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"Optional command execution timeout in seconds (default: 120s, max: 1800s)"`
}

// ShellResult defines the output of a shell command execution.
type ShellResult struct {
	Output   string `json:"output" jsonschema:"The combined stdout and stderr of the command"`
	ExitCode int    `json:"exit_code" jsonschema:"Process exit status code"`
}

// ExecuteShellCommand runs a command with bash and returns its combined output
// and exit code. It is the unconfined entry point, kept for callers that have no
// policy; tool code goes through ExecuteShell.
func ExecuteShellCommand(ctx context.Context, command string, timeoutOverride ...time.Duration) (ShellResult, error) {
	return ExecuteShell(ctx, nil, command, timeoutOverride...)
}

// ExecuteShell runs command under p.
//
// In mode isolation the command runs inside a kernel-confined child: it can
// only see the workspace and the toolchain paths, it cannot write outside them,
// it cannot reach the network unless the profile allows it, and it cannot signal
// the agent that spawned it. In every other mode the command is a plain bash -c
// that at least gets the policy's working directory, environment allowlist and
// wall-clock budget.
//
// An optional timeout override can be provided (e.g. from ShellArgs.TimeoutSeconds).
// When provided and > 0, it overrides the policy's default shell timeout, clamped
// to a maximum of 30 minutes (1800s).
func ExecuteShell(ctx context.Context, p *sandbox.Policy, command string, timeoutOverride ...time.Duration) (ShellResult, error) {
	timeout := p.ShellTimeout()
	if len(timeoutOverride) > 0 && timeoutOverride[0] > 0 {
		timeout = timeoutOverride[0]
		const maxTimeout = 30 * time.Minute
		if timeout > maxTimeout {
			timeout = maxTimeout
		}
	}
	env := p.FilterEnv(os.Environ())
	if p.Mode() == sandbox.ModeIsolation {
		spec := p.IsolationSpec(command, env)
		spec.TimeoutMS = timeout.Milliseconds()
		res, err := sandbox.Run(ctx, spec)
		if err != nil {
			return ShellResult{Output: res.Output, ExitCode: res.ExitCode}, errors.Wrap(err, "shell")
		}
		return ShellResult{Output: res.Output, ExitCode: res.ExitCode}, nil
	}
	return runShellCommand(ctx, command, p.Workspace(), env, timeout)
}

// runShellCommand is the unconfined path: bash -c in the workspace, with the
// allowlisted environment and a timeout that takes the whole process group.
func runShellCommand(ctx context.Context, command, dir string, env []string, timeout time.Duration) (ShellResult, error) {
	if timeout <= 0 {
		timeout = sandbox.DefaultShellTimeout
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", command)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdin = bytes.NewReader(nil)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// A shell command is a tree, not a process: without a process group a
	// timeout would leave its children running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return ShellResult{Output: out.String(), ExitCode: -1}, fmt.Errorf("shell: command timed out after %s", timeout)
	}
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return ShellResult{}, errors.Wrap(err, "running shell command")
		}
	}

	return ShellResult{
		Output:   out.String(),
		ExitCode: exitCode,
	}, nil
}

// NewShellTool creates a new ADK Tool for executing shell commands. With a
// sandbox policy attached every command is checked before it runs, and in mode
// isolation it is also confined by the kernel.
func NewShellTool(opts ...Option) (tool.Tool, error) {
	o := newOptions(opts)
	t, err := functiontool.New(functiontool.Config{
		Name:        "shell",
		Description: "Executes a shell command on the host system using bash. Returns stdout, stderr, and the exit code. For long-running commands (benchmarks, builds, migrations, tests), specify timeout_seconds.",
	}, func(ctx agent.Context, args ShellArgs) (ShellResult, error) {
		if err := o.checkShell(ctx, args.Command); err != nil {
			return ShellResult{}, err
		}
		var timeout time.Duration
		if args.TimeoutSeconds > 0 {
			timeout = time.Duration(args.TimeoutSeconds) * time.Second
		}
		return ExecuteShell(ctx, o.policy, args.Command, timeout)
	})

	if err != nil {
		return nil, errors.Wrap(err, "creating shell function tool")
	}
	return t, nil
}
