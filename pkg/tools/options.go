package tools

import "github.com/user/golem/pkg/sandbox"

// Option customises a tool at construction time.
type Option func(*options)

// options holds the cross-cutting settings every tool in this package accepts.
type options struct {
	policy *sandbox.Policy
}

// WithPolicy routes a tool's decisions through the sandbox policy. It is the
// only way in: a tool built without it behaves exactly as it did before the
// sandbox existed (mode off), which keeps the tests and mode off identical.
func WithPolicy(p *sandbox.Policy) Option {
	return func(o *options) { o.policy = p }
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

// checkRead consults the policy before a read.
func (o options) checkRead(toolName, path string) error {
	return o.policy.CheckRead(path).Err(toolName)
}

// checkWrite consults the policy before a write.
func (o options) checkWrite(toolName, path string) error {
	return o.policy.CheckWrite(path).Err(toolName)
}
