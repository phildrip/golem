package tools

import (
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/sandbox"
)

// Option customises a tool at construction time.
type Option func(*options)

// options holds the cross-cutting settings every tool in this package accepts.
type options struct {
	policy *sandbox.Policy
	mode   approval.Mode
}

// WithPolicy routes a tool's decisions through the sandbox policy. It is the
// only way in: a tool built without it behaves exactly as it did before the
// sandbox existed (mode off), which keeps the tests and mode off identical.
func WithPolicy(p *sandbox.Policy) Option {
	return func(o *options) { o.policy = p }
}

// WithUnattended marks a tool set that no human is watching, so a call the
// policy would ask about is refused instead of waiting for an answer. Subagents
// get this: a delegate must not approve on the user's behalf.
func WithUnattended() Option {
	return func(o *options) { o.mode = approval.Unattended }
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// gate enforces d for toolName. action is the exact thing being approved - the
// command line, the path, the patch - so the prompt never paraphrases it.
func (o options) gate(ctx approval.Approver, toolName string, d sandbox.Decision, action string) error {
	return approval.Gate(ctx, o.mode, toolName, o.policy.WithConfirmRequirement(toolName, d), action)
}

// checkRead consults the policy before a read.
func (o options) checkRead(ctx approval.Approver, toolName, path string) error {
	return o.gate(ctx, toolName, o.policy.CheckRead(path), toolName+" "+path)
}

// checkWrite consults the policy before a write. action says what is being
// written, e.g. "write_file notes.md" or "hashline_patch main.go (SWAP 12:ab)".
func (o options) checkWrite(ctx approval.Approver, toolName, path, action string) error {
	if action == "" {
		action = toolName + " " + path
	}
	return o.gate(ctx, toolName, o.policy.CheckWrite(path), action)
}

// checkShell consults the policy before running a command.
func (o options) checkShell(ctx approval.Approver, command string) error {
	return o.gate(ctx, "shell", o.policy.CheckShell(command, o.policy.Workspace()), command)
}

// effectiveBaseDir returns baseDir unless the path is outside baseDir and
// explicitly granted by the sandbox policy's allow_write (or read_only when
// allowReadOnly is true), in which case it returns the matched policy root so
// resolveSafePath confines the operation to that allowed tree.
func (o options) effectiveBaseDir(baseDir, path string, allowReadOnly bool) string {
	if _, err := resolveSafePath(baseDir, path); err == nil {
		return baseDir
	}
	if o.policy != nil {
		if allowedRoot := o.policy.AllowedExternalRoot(path, allowReadOnly); allowedRoot != "" {
			return allowedRoot
		}
	}
	return baseDir
}
