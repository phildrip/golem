package tools

import (
	"bytes"
	"os/exec"
)

// ExecuteShellCommand runs a shell command and returns its combined output (stdout + stderr).
func ExecuteShellCommand(command string) (string, error) {
	cmd := exec.Command("bash", "-c", command) // Use bash to allow for command chaining, etc.
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", err // Return an empty string and the error for better error handling
	}

	return out.String() + stderr.String(), nil
}
