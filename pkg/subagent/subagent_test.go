package subagent

import (
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

func TestModelFactory_ResolveModelName(t *testing.T) {
	factory := NewModelFactory(&genai.ClientConfig{APIKey: "dummy"}, "gemini-flash-latest")

	tests := []struct {
		name      string
		requested string
		expected  string
	}{
		{"empty string resolves to default", "", "gemini-flash-latest"},
		{"inherit resolves to default", "inherit", "gemini-flash-latest"},
		{"default keyword resolves to default", "default", "gemini-flash-latest"},
		{"flash alias resolves to flash-latest", "flash", "gemini-flash-latest"},
		{"pro alias resolves to gemini-2.5-pro", "pro", "gemini-2.5-pro"},
		{"custom model string preserved", "gemini-1.5-pro-002", "gemini-1.5-pro-002"},
		{"case insensitive alias", "PRO", "gemini-2.5-pro"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := factory.ResolveModelName(tc.requested)
			if got != tc.expected {
				t.Errorf("ResolveModelName(%q) = %q, want %q", tc.requested, got, tc.expected)
			}
		})
	}
}

func TestRunner_SelectToolsAndPrompts(t *testing.T) {
	dummyReadOnly, _ := functiontool.New(functiontool.Config{
		Name: "dummy_read",
	}, func(ctx agent.Context, args struct{}) (string, error) {
		return "read", nil
	})

	dummyWrite, _ := functiontool.New(functiontool.Config{
		Name: "dummy_write",
	}, func(ctx agent.Context, args struct{}) (string, error) {
		return "write", nil
	})

	runner := NewRunner(RunnerConfig{
		ModelFactory:   NewModelFactory(nil, "gemini-flash-latest"),
		ReadOnlyTools:  []tool.Tool{dummyReadOnly},
		ReadWriteTools: []tool.Tool{dummyWrite},
	})

	// 1. Researcher role only gets read-only tools
	researcherTools := runner.SelectTools(RoleResearcher)
	if len(researcherTools) != 1 || researcherTools[0].Name() != "dummy_read" {
		t.Errorf("expected only dummy_read for researcher, got %v", researcherTools)
	}
	researcherPrompt := runner.SystemPromptForRole(RoleResearcher)
	if !strings.Contains(researcherPrompt, "READ-ONLY") {
		t.Errorf("expected researcher prompt to enforce READ-ONLY rules, got: %s", researcherPrompt)
	}

	// 2. Coder role gets both read-only and write tools
	coderTools := runner.SelectTools(RoleCoder)
	if len(coderTools) != 2 {
		t.Errorf("expected 2 tools for coder, got %d", len(coderTools))
	}
	coderPrompt := runner.SystemPromptForRole(RoleCoder)
	if !strings.Contains(coderPrompt, "Coding Subagent") {
		t.Errorf("expected coder prompt, got: %s", coderPrompt)
	}

	// 3. Unknown role defaults to read-only for safety
	unknownTools := runner.SelectTools("untrusted_role")
	if len(unknownTools) != 1 || unknownTools[0].Name() != "dummy_read" {
		t.Errorf("expected safety fallback to read-only for unknown role, got %v", unknownTools)
	}
}

func TestNewInvokeTool_Metadata(t *testing.T) {
	runner := NewRunner(RunnerConfig{
		ModelFactory: NewModelFactory(nil, "gemini-flash-latest"),
	})

	invokeTool, err := NewInvokeTool(runner)
	if err != nil {
		t.Fatalf("NewInvokeTool failed: %v", err)
	}

	if invokeTool.Name() != ToolName {
		t.Errorf("expected tool name %q, got %q", ToolName, invokeTool.Name())
	}
	if !strings.Contains(invokeTool.Description(), "researcher") ||
		!strings.Contains(invokeTool.Description(), "coder") ||
		!strings.Contains(invokeTool.Description(), "model") {
		t.Errorf("expected description to document roles and model, got: %s", invokeTool.Description())
	}
}
