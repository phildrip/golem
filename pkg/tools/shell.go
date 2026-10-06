package tools

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/pkg/errors"
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

// ExecuteShellCommand runs a command with bash and returns its combined output and exit code.
func ExecuteShellCommand(ctx context.Context, command string) (ShellResult, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
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

// NewShellTool creates a new ADK Tool for executing shell commands.
func NewShellTool() (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name:        "shell",
		Description: "Executes a shell command on the host system using bash. Returns stdout, stderr, and the exit code.",
	}, func(ctx agent.Context, args ShellArgs) (ShellResult, error) {
		res, err := ExecuteShellCommand(ctx, args.Command)
		if err != nil {
			return ShellResult{}, errors.Wrap(err, "shell tool execution")
		}
		return res, nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating shell function tool")
	}
	return t, nil
}
