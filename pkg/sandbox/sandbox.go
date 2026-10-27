// Package sandbox is the single policy decision point for every tool that can
// affect the host: shell execution, file writes and network fetches.
//
// The layer exists for two threat models. Mode "policy" contains accidents
// (rm -rf ~, git checkout ., curl | sh) by denying dangerous shapes while
// leaving normal work untouched. Mode "bwrap" is reserved for containing a
// hostile or prompt-injected model with OS-level isolation; it answers the same
// Decision so callers do not change.
//
// A decision is Allow, Deny or Confirm rather than a bool, because the same
// answer has to serve both modes: "policy" denies today, and the confirmation
// gate renders Confirm per transport once it exists. Deny and Confirm both
// surface to the model as a tool error it can route around; a blocked call does
// not kill the turn.
package sandbox

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Mode selects how much enforcement the policy applies.
type Mode string

const (
	// ModeOff reproduces the pre-sandbox behaviour exactly: everything allowed.
	ModeOff Mode = "off"
	// ModePolicy denies dangerous shapes and writes outside the workspace.
	ModePolicy Mode = "policy"
	// ModeIsolation applies the same decisions and also enforces them: shell
	// commands run under kernel confinement (Landlock), where filesystem
	// outside the allowlist is invisible and TCP is refused. Enforcement is
	// real, so a prompt-injected model cannot talk its way past a string check.
	ModeIsolation Mode = "isolation"
	// ModeBwrap is the previous name for ModeIsolation, kept so an existing
	// profile keeps working.
	ModeBwrap = ModeIsolation
)

// Network modes for confined commands.
const (
	// NetworkNone denies TCP bind and connect inside the sandbox.
	NetworkNone = "none"
	// NetworkHost leaves the network reachable.
	NetworkHost = "host"
)

// Verdict is the outcome of a policy check.
type Verdict string

const (
	// Allow lets the call through untouched.
	Allow Verdict = "allow"
	// Deny refuses the call and reports why.
	Deny Verdict = "deny"
	// Confirm means a human must approve before the call runs. Until the
	// confirmation gate exists every transport treats it as a deny that the
	// model can explain to the user.
	Confirm Verdict = "confirm"
)

// DefaultShellTimeout is the shell wall-clock budget when none is configured.
const DefaultShellTimeout = 120 * time.Second

// DefaultEnvAllow is the environment passed to shell commands in policy mode.
// Everything else (API keys, tokens, SSH agents, cloud credentials) is dropped.
var DefaultEnvAllow = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "LC_ALL",
	"TZ", "TMPDIR", "PWD", "GOFLAGS", "GOPATH", "GOCACHE", "EDITOR",
}

// ErrBlocked is the sentinel behind every BlockedError, so callers can test
// errors.Is(err, sandbox.ErrBlocked) without matching message text.
var ErrBlocked = fmt.Errorf("blocked by sandbox")

// BlockedError is returned by tools when the policy refuses a call.
type BlockedError struct {
	Tool    string
	Verdict Verdict
	Rule    string
	Reason  string
}

func (e *BlockedError) Error() string {
	if e.Verdict == Confirm {
		return fmt.Sprintf("%s blocked by sandbox: needs confirmation (%s) - %s", e.Tool, e.Rule, e.Reason)
	}
	return fmt.Sprintf("%s blocked by sandbox (%s): %s", e.Tool, e.Rule, e.Reason)
}

// Unwrap exposes ErrBlocked for errors.Is.
func (e *BlockedError) Unwrap() error { return ErrBlocked }

// Decision is the answer to one policy question.
type Decision struct {
	Verdict Verdict
	// Rule names the rule that fired, for audit logs and tests.
	Rule string
	// Reason is a human-readable explanation aimed at the model.
	Reason string
}

// Allowed reports whether the call may proceed.
func (d Decision) Allowed() bool { return d.Verdict == Allow }

// Err converts a decision into the error a tool should return, or nil.
func (d Decision) Err(tool string) error {
	if d.Verdict == Allow {
		return nil
	}
	return &BlockedError{Tool: tool, Verdict: d.Verdict, Rule: d.Rule, Reason: d.Reason}
}

// Config is the per-profile sandbox block (sandbox: {...} in config.json).
type Config struct {
	// Mode is off, policy or bwrap. Empty means policy.
	Mode Mode `json:"mode,omitempty"`
	// AllowWrite lists extra writable paths; the workspace is always writable.
	// Entries may be absolute, ~-relative, workspace-relative, or globs.
	AllowWrite []string `json:"allow_write,omitempty"`
	// ReadOnly lists extra readable paths for mode isolation. The system trees
	// are always readable; this adds e.g. a second checkout or a data dir.
	ReadOnly []string `json:"read_only,omitempty"`
	// Network is none (default) or host: whether confined shell commands may
	// reach the network.
	Network string `json:"network,omitempty"`
	// AllowNet lists allowed hosts for web_fetch. Empty means "no allowlist".
	AllowNet []string `json:"allow_net,omitempty"`
	// RequireConfirm lists tools that always need approval, e.g. ["shell"].
	RequireConfirm []string `json:"require_confirm,omitempty"`
	// EnvAllow overrides the environment allowlist for shell commands.
	EnvAllow []string `json:"env_allow,omitempty"`
	// ShellTimeout is a Go duration string, e.g. "120s".
	ShellTimeout string `json:"shell_timeout,omitempty"`
}

// Policy is the compiled form of Config, bound to one workspace root.
//
// A nil *Policy is valid and allows everything, so tools can be built without a
// policy in tests and in mode off.
type Policy struct {
	mode           Mode
	workspace      string
	allowWrite     []string
	readOnly       []string
	network        string
	allowNet       []string
	requireConfirm map[string]bool
	envAllow       []string
	shellTimeout   time.Duration
}

// New compiles cfg against workspaceRoot. An unknown mode is an error rather
// than a silent downgrade, so a typo cannot quietly disable enforcement.
func New(cfg Config, workspaceRoot string) (*Policy, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(string(cfg.Mode))))
	switch mode {
	case "":
		mode = ModePolicy
	case "bwrap":
		mode = ModeIsolation
	case ModeOff, ModePolicy, ModeIsolation:
	default:
		return nil, fmt.Errorf("sandbox: unknown mode %q (want off, policy or isolation)", cfg.Mode)
	}
	if mode == ModeIsolation {
		// A profile that asks for enforcement must not silently run without
		// it, so an unsupported kernel is a startup error, not a warning.
		if err := IsolationSupported(); err != nil {
			return nil, err
		}
	}

	ws := workspaceRoot
	if ws == "" {
		ws, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(ws); err == nil {
		ws = abs
	}
	if resolved, err := filepath.EvalSymlinks(ws); err == nil {
		ws = resolved
	}

	p := &Policy{
		mode:           mode,
		workspace:      ws,
		requireConfirm: make(map[string]bool, len(cfg.RequireConfirm)),
	}
	for _, tool := range cfg.RequireConfirm {
		if t := strings.TrimSpace(tool); t != "" {
			p.requireConfirm[t] = true
		}
	}
	for _, raw := range cfg.AllowWrite {
		if resolved, ok := p.resolvePattern(raw); ok {
			p.allowWrite = append(p.allowWrite, resolved)
		}
	}
	for _, raw := range cfg.ReadOnly {
		if resolved, ok := p.resolvePattern(raw); ok {
			p.readOnly = append(p.readOnly, resolved)
		}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Network)) {
	case "", NetworkNone, "deny", "off":
		p.network = NetworkNone
	case NetworkHost, "allow", "on":
		p.network = NetworkHost
	default:
		return nil, fmt.Errorf("sandbox: unknown network %q (want none or host)", cfg.Network)
	}
	for _, raw := range cfg.AllowNet {
		if h := strings.ToLower(strings.TrimSpace(raw)); h != "" {
			p.allowNet = append(p.allowNet, h)
		}
	}
	for _, name := range cfg.EnvAllow {
		if n := strings.TrimSpace(name); n != "" {
			p.envAllow = append(p.envAllow, n)
		}
	}
	p.shellTimeout = DefaultShellTimeout
	if s := strings.TrimSpace(cfg.ShellTimeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("sandbox: invalid shell_timeout %q", cfg.ShellTimeout)
		}
		p.shellTimeout = d
	}
	return p, nil
}

// Mode returns the compiled mode. A nil policy is mode off.
func (p *Policy) Mode() Mode {
	if p == nil {
		return ModeOff
	}
	return p.mode
}

// Network reports whether confined commands may reach the network. A nil
// policy is mode off, which leaves the network untouched.
func (p *Policy) Network() string {
	if p == nil {
		return NetworkHost
	}
	return p.network
}

// ReadOnlyPaths returns the extra readable paths the profile configured.
func (p *Policy) ReadOnlyPaths() []string {
	if p == nil {
		return nil
	}
	return p.readOnly
}

// IsolationSpec builds the description of how to run command confined: the
// workspace as cwd, the filtered environment, and the complete set of visible
// paths. It is the one place that decides what a sandboxed shell can see.
func (p *Policy) IsolationSpec(command string, env []string) Spec {
	readOnly, readWrite := isolationPathsFor(p.Workspace(), p.ReadOnlyPaths(), p.allowWrite)
	return Spec{
		Command:     command,
		Dir:         p.Workspace(),
		Env:         env,
		TimeoutMS:   p.ShellTimeout().Milliseconds(),
		ReadOnly:    readOnly,
		ReadWrite:   readWrite,
		AllowNet:    p.Network() == NetworkHost,
		ScopeSignal: true,
	}
}

// Workspace returns the resolved workspace root the policy was compiled with.
func (p *Policy) Workspace() string {
	if p == nil {
		return ""
	}
	return p.workspace
}

// ShellTimeout returns the wall-clock budget for shell commands.
func (p *Policy) ShellTimeout() time.Duration {
	if p == nil || p.shellTimeout <= 0 {
		return DefaultShellTimeout
	}
	return p.shellTimeout
}

// EnvAllowlist returns the environment variable names a shell command may see.
// An empty result means "pass everything through" (mode off).
func (p *Policy) EnvAllowlist() []string {
	if p == nil || p.mode == ModeOff {
		return nil
	}
	if len(p.envAllow) > 0 {
		return p.envAllow
	}
	return DefaultEnvAllow
}

// FilterEnv returns environ (KEY=VALUE) reduced to the allowlist.
func (p *Policy) FilterEnv(environ []string) []string {
	allow := p.EnvAllowlist()
	if allow == nil {
		return environ
	}
	keep := make(map[string]bool, len(allow))
	for _, name := range allow {
		keep[name] = true
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if ok && keep[name] {
			out = append(out, kv)
		}
	}
	return out
}

func (p *Policy) allowAll(rule, reason string) Decision {
	return Decision{Verdict: Allow, Rule: rule, Reason: reason}
}

// --- shell -----------------------------------------------------------------

// shellRule is one pattern in the dangerous-shape table.
type shellRule struct {
	rule    string
	re      *regexp.Regexp
	verdict Verdict
	reason  string
}

var shellRules = []shellRule{
	{
		rule:    "git-destructive",
		re:      regexp.MustCompile(`\bgit\s+(checkout\s+(--\s+)?\.\s*$|reset\s+--hard|clean\s+-[a-zA-Z]*[fdx])`),
		verdict: Deny,
		reason:  "this discards uncommitted work in the workspace; use git stash or ask the user",
	},
	{
		rule:    "disk-write",
		re:      regexp.MustCompile(`\b(mkfs(\.\w+)?|mkswap)\b|\bdd\b[^|;&]*\bof=/dev/`),
		verdict: Deny,
		reason:  "writing to block devices destroys filesystems",
	},
	{
		rule:    "device-clobber",
		re:      regexp.MustCompile(`>>?\s*/dev/(sd|nvme|hd|vd|mmcblk)`),
		verdict: Deny,
		reason:  "redirecting into a block device destroys data",
	},
	{
		rule:    "privilege-escalation",
		re:      regexp.MustCompile(`(^|[|;&(]\s*)\b(sudo|doas)\b`),
		verdict: Deny,
		reason:  "the agent must not escalate privileges",
	},
	{
		rule:    "power-state",
		re:      regexp.MustCompile(`\b(shutdown|reboot|poweroff|halt)\b|systemctl\s+(poweroff|reboot|halt|suspend)`),
		verdict: Deny,
		reason:  "power and session state belong to the human",
	},
	{
		rule:    "curl-pipe-shell",
		re:      regexp.MustCompile(`\b(curl|wget)\b[^|;&]*\|\s*(sudo\s+)?(ba|z|da)?sh\b`),
		verdict: Deny,
		reason:  "piping a download into a shell executes unreviewed code",
	},
	{
		rule:    "fork-bomb",
		re:      regexp.MustCompile(`:\s*\(\s*\)\s*\{`),
		verdict: Deny,
		reason:  "fork bomb",
	},
	{
		rule:    "chmod-world-root",
		re:      regexp.MustCompile(`\bchmod\s+(-[a-zA-Z]+\s+)*(777|a\+rwx)\s+/(\s|$)`),
		verdict: Deny,
		reason:  "opening / to the world breaks the machine",
	},
	{
		rule:    "force-push",
		re:      regexp.MustCompile(`\bgit\s+push\b[^|;&]*(--force\b|--force-with-lease\b|\s-f\b)`),
		verdict: Confirm,
		reason:  "a force push rewrites shared history",
	},
	{
		rule:    "branch-delete",
		re:      regexp.MustCompile(`\bgit\s+branch\s+(-[a-zA-Z]+\s+)*-D\b`),
		verdict: Confirm,
		reason:  "deleting a branch can lose unpushed commits",
	},
}

// rmRecursive matches a recursive rm invocation.
var rmRecursive = regexp.MustCompile(`\brm\s+(-[a-zA-Z]+\s+)*-[a-zA-Z]*[rR][a-zA-Z]*\b`)

// catastrophicPaths are roots a recursive delete must never touch.
var catastrophicPaths = map[string]bool{
	"/": true, "/*": true, "/home": true, "/root": true, "/etc": true,
	"/usr": true, "/var": true, "/boot": true, "/bin": true, "/sbin": true,
	"/lib": true, "/opt": true, "/srv": true, "/sys": true, "/proc": true,
	"~": true, "~/": true, "~/*": true, "$HOME": true, "${HOME}": true,
}

// CheckShell decides whether command may run with dir as its working directory.
func (p *Policy) CheckShell(command, dir string) Decision {
	if p == nil || p.mode == ModeOff {
		return p.allowAll("mode-off", "sandbox disabled")
	}
	if p.requireConfirm["shell"] {
		return Decision{Verdict: Confirm, Rule: "require-confirm", Reason: "shell is in require_confirm"}
	}
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return p.allowAll("empty", "nothing to run")
	}
	for _, r := range shellRules {
		if r.re.MatchString(cmd) {
			return Decision{Verdict: r.verdict, Rule: r.rule, Reason: r.reason}
		}
	}
	if d, ok := p.checkRecursiveRemove(cmd); ok {
		return d
	}
	return p.allowAll("default", "no dangerous shape matched")
}

// checkRecursiveRemove inspects rm -r targets, which are the most common way to
// lose a home directory by accident.
func (p *Policy) checkRecursiveRemove(cmd string) (Decision, bool) {
	if !rmRecursive.MatchString(cmd) {
		return Decision{}, false
	}
	for _, field := range strings.Fields(cmd) {
		target := strings.Trim(field, `"';`)
		if !catastrophicPaths[target] {
			continue
		}
		return Decision{
			Verdict: Deny,
			Rule:    "rm-recursive-catastrophic",
			Reason:  fmt.Sprintf("recursive delete of %s would destroy the machine", target),
		}, true
	}
	return Decision{}, false
}

// --- filesystem ------------------------------------------------------------

// CheckRead decides whether path may be read. Reads stay unrestricted by
// default: the file tools already confine them to the workspace with
// resolveSafePath, and denying memory or system reads would break the agent.
func (p *Policy) CheckRead(path string) Decision {
	if p == nil || p.mode == ModeOff {
		return p.allowAll("mode-off", "sandbox disabled")
	}
	return p.allowAll("read-unrestricted", "reads are confined by the file tools themselves")
}

// CheckWrite decides whether path may be written.
func (p *Policy) CheckWrite(path string) Decision {
	if p == nil || p.mode == ModeOff {
		return p.allowAll("mode-off", "sandbox disabled")
	}
	target := p.resolvePath(path)
	if p.underWorkspace(target) {
		return p.allowAll("workspace", "inside the workspace")
	}
	for _, pattern := range p.allowWrite {
		if matchPath(pattern, target) {
			return p.allowAll("allow-write", "matches allow_write "+pattern)
		}
	}
	return Decision{
		Verdict: Deny,
		Rule:    "write-outside-workspace",
		Reason:  fmt.Sprintf("%s is outside the workspace %s and not in allow_write", target, p.workspace),
	}
}

// --- network ---------------------------------------------------------------

// CheckNet decides whether rawURL may be fetched. An empty allow_net means the
// web package's SSRF guard is the only gate, which is today's behaviour.
func (p *Policy) CheckNet(rawURL string) Decision {
	if p == nil || p.mode == ModeOff {
		return p.allowAll("mode-off", "sandbox disabled")
	}
	if len(p.allowNet) == 0 {
		return p.allowAll("net-unrestricted", "no allow_net configured")
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return Decision{Verdict: Deny, Rule: "net-unparseable", Reason: "cannot parse host from " + rawURL}
	}
	host := strings.ToLower(u.Hostname())
	for _, allowed := range p.allowNet {
		if allowed == "*" || host == allowed || strings.HasSuffix(host, "."+strings.TrimPrefix(allowed, "*.")) {
			return p.allowAll("allow-net", "host "+host+" is allowlisted")
		}
	}
	return Decision{
		Verdict: Deny,
		Rule:    "net-not-allowed",
		Reason:  fmt.Sprintf("host %s is not in allow_net", host),
	}
}

// --- helpers ---------------------------------------------------------------

// resolvePath turns a tool-supplied path into a cleaned absolute path rooted at
// the workspace when relative.
func (p *Policy) resolvePath(path string) string {
	if path == "" {
		return p.workspace
	}
	target := path
	if strings.HasPrefix(target, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			target = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(target, "~"), "/"))
		}
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(p.workspace, target)
	}
	target = filepath.Clean(target)
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		return resolved
	}
	// The file may not exist yet; resolve the deepest existing ancestor so a
	// symlinked directory cannot smuggle a write outside the workspace.
	if resolved, ok := p.resolveExistingAncestor(target); ok {
		return resolved
	}
	return target
}

func (p *Policy) resolveExistingAncestor(target string) (string, bool) {
	dir := target
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(resolved, filepath.Base(dir)), true
		}
		dir = parent
	}
}

func (p *Policy) underWorkspace(target string) bool {
	if p.workspace == "" {
		return true
	}
	return target == p.workspace || strings.HasPrefix(target, p.workspace+string(filepath.Separator))
}

// resolvePattern expands one allow_write entry against the workspace.
func (p *Policy) resolvePattern(raw string) (string, bool) {
	entry := strings.TrimSpace(raw)
	if entry == "" {
		return "", false
	}
	// Bare relative entries are workspace-relative, not daemon-cwd relative.
	if strings.HasPrefix(entry, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			entry = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(entry, "~"), "/"))
		}
	} else if !filepath.IsAbs(entry) {
		entry = filepath.Join(p.workspace, entry)
	}
	return filepath.Clean(entry), true
}

// matchPath reports whether target is pattern, sits under pattern, or matches it
// as a glob.
func matchPath(pattern, target string) bool {
	if pattern == "" {
		return false
	}
	if pattern == target || strings.HasPrefix(target, pattern+string(filepath.Separator)) {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		if ok, err := filepath.Match(pattern, target); err == nil && ok {
			return true
		}
	}
	return false
}
