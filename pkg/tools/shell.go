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
	Command string `json:"command" jsonschema:"The shell command to execute using bash"`
}

// ShellResult defines the output of a shell command execution.
type ShellResult struct {
	Output   string `json:"output" jsonschema:"The combined stdout and stderr of the command"`
	ExitCode int    `json:"exit_code" jsonschema:"Process exit status code"`
}

// ExecuteShellCommand runs a command with bash and returns its combined output
// and exit code. It is the unconfined entry point, kept for callers that have no
// policy; tool code goes through ExecuteShell.
func ExecuteShellCommand(ctx context.Context, command string) (ShellResult, error) {
	return ExecuteShell(ctx, nil, command)
}

// ExecuteShell runs command under p.
//
// In mode isolation the command runs inside a kernel-confined child: it can
// only see the workspace and the toolchain paths, it cannot write outside them,
// it cannot reach the network unless the profile allows it, and it cannot signal
// the agent that spawned it. In every other mode the command is a plain bash -c
// that at least gets the policy's working directory, environment allowlist and
// wall-clock budget.
func ExecuteShell(ctx context.Context, p *sandbox.Policy, command string) (ShellResult, error) {
	env := p.FilterEnv(os.Environ())
	if p.Mode() == sandbox.ModeIsolation {
		res, err := sandbox.Run(ctx, p.IsolationSpec(command, env))
		if err != nil {
			return ShellResult{Output: res.Output, ExitCode: res.ExitCode}, errors.Wrap(err, "shell")
		}
		return ShellResult{Output: res.Output, ExitCode: res.ExitCode}, nil
	}
	return runShellCommand(ctx, command, p.Workspace(), env, p.ShellTimeout())
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
		Description: "Executes a shell command on the host system using bash. Returns stdout, stderr, and the exit code.",
	}, func(ctx agent.Context, args ShellArgs) (ShellResult, error) {
		if err := o.checkShell(ctx, args.Command); err != nil {
			return ShellResult{}, err
		}
		return ExecuteShell(ctx, o.policy, args.Command)
	})

	if err != nil {
		return nil, errors.Wrap(err, "creating shell function tool")
	}
	return t, nil
}
