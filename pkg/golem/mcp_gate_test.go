package golem

import (
	"testing"

	"github.com/user/golem/pkg/sandbox"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// fakeMCPTool is the shape an MCP tool has: a tool.Tool plus a declaration and
// a Run method, which is what the ADK's confirmation wrapper looks for.
type fakeMCPTool struct{ name string }

func (f *fakeMCPTool) Name() string        { return f.name }
func (f *fakeMCPTool) Description() string { return "fake mcp tool" }
func (f *fakeMCPTool) IsLongRunning() bool { return false }
func (f *fakeMCPTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: f.name}
}
func (f *fakeMCPTool) ProcessRequest(agent.Context, *model.LLMRequest) error { return nil }
func (f *fakeMCPTool) Run(agent.Context, any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

type fakeMCPToolset struct{ tools []tool.Tool }

func (f *fakeMCPToolset) Name() string { return "fake-mcp" }
func (f *fakeMCPToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	return f.tools, nil
}

func newFakeMCPToolset() *fakeMCPToolset {
	return &fakeMCPToolset{tools: []tool.Tool{
		&fakeMCPTool{name: "read_notes"},
		&fakeMCPTool{name: "delete_everything"},
	}}
}

func mcpPolicy(t *testing.T, cfg sandbox.Config) *sandbox.Policy {
	t.Helper()
	p, err := sandbox.New(cfg, t.TempDir())
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	return p
}

// TestMCPToolsetIsGatedForTheOrchestrator pins the golem-9uw.7 contract: in
// policy mode an MCP tool the profile has not allowlisted is a question, and
// the wrapped toolset is not the original one.
func TestMCPToolsetIsGatedForTheOrchestrator(t *testing.T) {
	base := newFakeMCPToolset()
	p := mcpPolicy(t, sandbox.Config{Mode: sandbox.ModePolicy})

	gated := gateMCPToolset(base, p, false)
	if gated == tool.Toolset(base) {
		t.Fatal("policy mode must wrap the toolset, not pass it through")
	}
	tools, err := gated.Tools(nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want both still offered (each is a prompt)", len(tools))
	}
}

// TestMCPToolsetIsFilteredForSubagents is the delegation half: a subagent has
// nobody to ask, so only allowlisted MCP tools are offered at all.
func TestMCPToolsetIsFilteredForSubagents(t *testing.T) {
	p := mcpPolicy(t, sandbox.Config{Mode: sandbox.ModePolicy, MCPAllow: []string{"read_notes"}})

	tools, err := gateMCPToolset(newFakeMCPToolset(), p, true).Tools(nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name() != "read_notes" {
		t.Fatalf("delegate tools = %v, want only read_notes", tools)
	}

	empty := mcpPolicy(t, sandbox.Config{Mode: sandbox.ModePolicy})
	tools, err = gateMCPToolset(newFakeMCPToolset(), empty, true).Tools(nil)
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("a delegate with no allowlist got %d MCP tools, want 0", len(tools))
	}
}

// TestMCPToolsetUntouchedInModeOff keeps mode off identical to the pre-sandbox
// behaviour, as the whole sandbox layer promises.
func TestMCPToolsetUntouchedInModeOff(t *testing.T) {
	base := newFakeMCPToolset()
	p := mcpPolicy(t, sandbox.Config{Mode: sandbox.ModeOff})
	if got := gateMCPToolset(base, p, false); got != tool.Toolset(base) {
		t.Fatal("mode off must not wrap the toolset")
	}
	if got := gateMCPToolset(base, p, true); got != tool.Toolset(base) {
		t.Fatal("mode off must not filter the delegate toolset")
	}
	if got := gateMCPToolset(base, nil, true); got != tool.Toolset(base) {
		t.Fatal("a nil policy must not filter the toolset")
	}
}

// TestMCPPolicyDefaultsToConfirm covers the policy predicate directly.
func TestMCPPolicyDefaultsToConfirm(t *testing.T) {
	p := mcpPolicy(t, sandbox.Config{Mode: sandbox.ModePolicy, MCPAllow: []string{"read_notes"}})
	if p.MCPRequiresConfirmation("delete_everything") != true {
		t.Fatal("an unlisted MCP tool must need confirmation")
	}
	if p.MCPRequiresConfirmation("read_notes") {
		t.Fatal("an allowlisted MCP tool must not need confirmation")
	}
	if !p.MCPAllowed("read_notes") || p.MCPAllowed("delete_everything") {
		t.Fatal("MCPAllowed disagrees with MCPRequiresConfirmation")
	}

	off := mcpPolicy(t, sandbox.Config{Mode: sandbox.ModeOff})
	if off.MCPRequiresConfirmation("anything") {
		t.Fatal("mode off must not ask")
	}
}
