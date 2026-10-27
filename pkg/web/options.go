package web

import "github.com/user/golem/pkg/sandbox"

// Option customises a web tool at construction time.
type Option func(*options)

type options struct {
	policy *sandbox.Policy
}

// WithPolicy routes web_fetch decisions through the sandbox policy, so a
// profile can restrict which hosts the agent may reach. A nil policy (or mode
// off) leaves the SSRF guard as the only gate, which is the pre-sandbox
// behaviour.
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
