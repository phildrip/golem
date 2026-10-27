package web

import (
	"github.com/user/golem/pkg/approval"
	"github.com/user/golem/pkg/sandbox"
)

// Option customises a web tool at construction time.
type Option func(*options)

type options struct {
	policy *sandbox.Policy
	mode   approval.Mode
}

// WithPolicy routes web_fetch decisions through the sandbox policy, so a
// profile can restrict which hosts the agent may reach. A nil policy (or mode
// off) leaves the SSRF guard as the only gate, which is the pre-sandbox
// behaviour.
func WithPolicy(p *sandbox.Policy) Option {
	return func(o *options) { o.policy = p }
}

// WithUnattended marks a web tool set nobody is watching, so a call the policy
// would ask about is refused instead of waiting for an answer.
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

// checkNet consults the policy before reaching host, asking a human when the
// profile lists the tool in require_confirm.
func (o options) checkNet(ctx approval.Approver, toolName, host string) error {
	d := o.policy.WithConfirmRequirement(toolName, o.policy.CheckNet(host))
	return approval.Gate(ctx, o.mode, toolName, d, toolName+" "+host)
}
