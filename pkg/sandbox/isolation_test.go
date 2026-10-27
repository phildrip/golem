package sandbox

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireIsolation skips a test when the host kernel cannot enforce anything.
func requireIsolation(t *testing.T) {
	t.Helper()
	if err := IsolationSupported(); err != nil {
		t.Skipf("OS isolation unavailable: %v", err)
	}
}

// testEnv is a minimal environment for the confined command: the wrapper needs
// PATH to find bash, and nothing else should be visible.
func testEnv() []string {
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/nonexistent-home", "TERM=dumb"}
}

// isolatedSpec builds a spec that grants only the workspace, deliberately not
// the defaults, so the assertions are about the mechanism rather than the
// profile's path list.
func isolatedSpec(t *testing.T, workspace, command string) Spec {
	t.Helper()
	return Spec{
		Command:     command,
		Dir:         workspace,
		Env:         testEnv(),
		TimeoutMS:   int64(20 * time.Second / time.Millisecond),
		ReadOnly:    []string{"/usr", "/bin", "/lib", "/lib64", "/etc"},
		ReadWrite:   []string{workspace, "/dev/null"},
		ScopeSignal: true,
	}
}

func TestSpecRoundTripsThroughTheWrapperEncoding(t *testing.T) {
	want := Spec{
		Command:     "echo hi",
		Dir:         "/workspace",
		Env:         []string{"PATH=/bin"},
		TimeoutMS:   1500,
		ReadOnly:    []string{"/usr"},
		ReadWrite:   []string{"/workspace"},
		AllowNet:    true,
		ScopeSignal: true,
	}
	encoded, err := encodeSpec(want)
	if err != nil {
		t.Fatalf("encodeSpec: %v", err)
	}
	got, err := decodeSpec(encoded)
	if err != nil {
		t.Fatalf("decodeSpec: %v", err)
	}
	if got.Command != want.Command || got.Dir != want.Dir || got.TimeoutMS != want.TimeoutMS ||
		got.AllowNet != want.AllowNet || got.ScopeSignal != want.ScopeSignal ||
		strings.Join(got.ReadOnly, ",") != strings.Join(want.ReadOnly, ",") ||
		strings.Join(got.ReadWrite, ",") != strings.Join(want.ReadWrite, ",") ||
		strings.Join(got.Env, ",") != strings.Join(want.Env, ",") {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestRunExecutesInTheWorkspace(t *testing.T) {
	requireIsolation(t)
	ws := t.TempDir()
	res, err := Run(context.Background(), isolatedSpec(t, ws, "pwd; echo marker-$((1+1))"))
	if err != nil {
		t.Fatalf("Run: %v (output %q)", err, res.Output)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, output %q", res.ExitCode, res.Output)
	}
	if !strings.Contains(res.Output, "marker-2") {
		t.Errorf("output %q missing the command's own output", res.Output)
	}
	if !strings.Contains(res.Output, ws) {
		t.Errorf("output %q does not show the workspace %s as cwd", res.Output, ws)
	}
}

func TestRunReportsExitCodeWithoutAnError(t *testing.T) {
	requireIsolation(t)
	ws := t.TempDir()
	res, err := Run(context.Background(), isolatedSpec(t, ws, "exit 7"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", res.ExitCode)
	}
}

func TestRunConfinesWritesToTheGrantedPaths(t *testing.T) {
	requireIsolation(t)
	ws := t.TempDir()
	outside := t.TempDir() // under /tmp, deliberately not granted in the spec

	spec := isolatedSpec(t, ws, fmt.Sprintf(
		"echo inside > %s/ok.txt && echo wrote-inside; echo nope > %s/nope.txt 2>&1; echo nope2 > /etc/nope.txt 2>&1",
		ws, outside))
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "ok.txt")); err != nil {
		t.Fatalf("write inside the workspace failed: %v (output %q)", err, res.Output)
	}
	if _, err := os.Stat(filepath.Join(outside, "nope.txt")); err == nil {
		t.Fatalf("write outside the workspace succeeded: %q", res.Output)
	}
	if !strings.Contains(res.Output, "Permission denied") {
		t.Errorf("output %q does not show the denied writes", res.Output)
	}
}

func TestRunHidesPathsThatWereNotGranted(t *testing.T) {
	requireIsolation(t)
	ws := t.TempDir()
	// /proc is never granted: it would leak the agent's own environment.
	res, err := Run(context.Background(), isolatedSpec(t, ws, "cat /etc/hostname; cat /proc/self/status"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Output, "CapEff:") {
		t.Fatalf("read of /proc/self/status was allowed: %q", res.Output)
	}
	if !strings.Contains(res.Output, "Permission denied") {
		t.Errorf("output %q does not show the denied read", res.Output)
	}
}

func TestRunDeniesNetworkByDefault(t *testing.T) {
	requireIsolation(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ws := t.TempDir()
	spec := isolatedSpec(t, ws, fmt.Sprintf("timeout 3 bash -c 'exec 3<>/dev/tcp/127.0.0.1/%d' && echo connected || echo refused", port))
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Output, "connected") {
		t.Fatalf("network reachable without allow_net: %q", res.Output)
	}
	if !strings.Contains(res.Output, "refused") {
		t.Errorf("output %q is not a clear refusal", res.Output)
	}
}

func TestRunAllowsNetworkWhenTheProfileAsks(t *testing.T) {
	requireIsolation(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	ws := t.TempDir()
	spec := isolatedSpec(t, ws, fmt.Sprintf("timeout 5 bash -c 'exec 3<>/dev/tcp/127.0.0.1/%d' && echo connected || echo refused", port))
	spec.AllowNet = true
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Output, "connected") {
		t.Fatalf("network denied even though AllowNet was set: %q", res.Output)
	}
}

func TestRunCannotSignalTheAgent(t *testing.T) {
	requireIsolation(t)
	if LandlockABI() < 6 {
		t.Skipf("signal scoping needs Landlock ABI 6, have %d", LandlockABI())
	}
	ws := t.TempDir()
	// $PPID is the wrapper's parent: this test process.
	spec := isolatedSpec(t, ws, `kill -0 $PPID 2>&1 && echo signalled || echo blocked`)
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Output, "signalled") {
		t.Fatalf("the confined command could signal the agent: %q", res.Output)
	}
	if !strings.Contains(res.Output, "blocked") {
		t.Errorf("output %q is not a clear refusal", res.Output)
	}
}

func TestRunDoesNotLeakTheWrapperSpec(t *testing.T) {
	requireIsolation(t)
	ws := t.TempDir()
	res, err := Run(context.Background(), isolatedSpec(t, ws, "env | sort"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(res.Output, envIsolationSpec) {
		t.Fatalf("the sandbox spec leaked into the command environment: %q", res.Output)
	}
	if !strings.Contains(res.Output, "PATH=") {
		t.Errorf("the supplied environment did not arrive: %q", res.Output)
	}
}

func TestRunTimesOutAndKillsTheTree(t *testing.T) {
	requireIsolation(t)
	ws := t.TempDir()
	spec := isolatedSpec(t, ws, "sleep 30 & sleep 30; echo never")
	spec.TimeoutMS = 400

	start := time.Now()
	res, err := Run(context.Background(), spec)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected a timeout error, got exit %d output %q", res.ExitCode, res.Output)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("timeout took %s: the process group was not killed", elapsed)
	}
}

func TestIsolationPathsKeepListsDisjointAndReal(t *testing.T) {
	ws := t.TempDir()
	ro, rw := isolationPathsFor(ws, []string{"/etc"}, []string{"/tmp"})
	writable := map[string]bool{}
	for _, p := range rw {
		writable[p] = true
	}
	for _, p := range ro {
		if writable[p] {
			t.Errorf("%s is granted both read-only and read-write", p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("read-only path %s does not exist: %v", p, err)
		}
	}
	if !writable[ws] {
		t.Errorf("workspace %s is not writable: %v", ws, rw)
	}
}

func TestPolicyIsolationSpecDeniesNetworkAndScopesSignals(t *testing.T) {
	ws := t.TempDir()
	p, err := New(Config{Mode: ModeIsolation}, ws)
	if err != nil {
		if err == ErrIsolationUnsupported || strings.Contains(err.Error(), "isolation is unavailable") {
			t.Skipf("OS isolation unavailable: %v", err)
		}
		t.Fatalf("New: %v", err)
	}
	spec := p.IsolationSpec("ls", []string{"PATH=/bin"})
	if spec.Dir != p.Workspace() {
		t.Errorf("spec dir = %q, want the workspace %q", spec.Dir, p.Workspace())
	}
	if spec.AllowNet {
		t.Error("isolation defaults to network denied, got AllowNet")
	}
	if !spec.ScopeSignal {
		t.Error("isolation must scope signals so a command cannot kill the agent")
	}
	found := false
	for _, path := range spec.ReadWrite {
		if path == ws {
			found = true
		}
	}
	if !found {
		t.Errorf("workspace %s missing from writable paths %v", ws, spec.ReadWrite)
	}
	if spec.TimeoutMS != p.ShellTimeout().Milliseconds() {
		t.Errorf("timeout = %dms, want %dms", spec.TimeoutMS, p.ShellTimeout().Milliseconds())
	}
}

func TestPolicyAcceptsBwrapAsTheOldNameForIsolation(t *testing.T) {
	p, err := New(Config{Mode: "bwrap"}, t.TempDir())
	if err != nil {
		if strings.Contains(err.Error(), "isolation is unavailable") {
			t.Skipf("OS isolation unavailable: %v", err)
		}
		t.Fatalf("New: %v", err)
	}
	if p.Mode() != ModeIsolation {
		t.Fatalf("mode = %s, want isolation", p.Mode())
	}
}

func TestPolicyRejectsUnknownNetworkMode(t *testing.T) {
	if _, err := New(Config{Mode: ModePolicy, Network: "sometimes"}, t.TempDir()); err == nil {
		t.Fatal("expected an error for an unknown network mode")
	}
	p, err := New(Config{Mode: ModePolicy, Network: "host"}, t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Network() != NetworkHost {
		t.Fatalf("network = %s, want host", p.Network())
	}
}
