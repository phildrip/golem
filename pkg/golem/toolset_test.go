package golem

import (
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"
)

// fakeTool implements the ADK tool.Tool surface; declaringTool is optional so
// both shapes can be exercised.
type fakeTool struct {
	name string
}

func (f fakeTool) Name() string        { return f.name }
func (f fakeTool) Description() string { return "fake" }
func (f fakeTool) IsLongRunning() bool { return false }

type fakeDeclaringTool struct {
	fakeTool
	decl *genai.FunctionDeclaration
}

func (f fakeDeclaringTool) Declaration() *genai.FunctionDeclaration { return f.decl }
func (f fakeDeclaringTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	return nil, nil
}

func TestToolDeclarationsCarriesSchemas(t *testing.T) {
	withDecl := fakeDeclaringTool{
		fakeTool: fakeTool{name: "shell"},
		decl:     &genai.FunctionDeclaration{Name: "shell", Description: "run a command"},
	}
	noDecl := fakeTool{name: "zoom"}

	decls := toolDeclarations([]tool.Tool{withDecl, noDecl, nil})
	if len(decls) != 1 {
		t.Fatalf("declarations = %d, want 1 (only tools that declare a schema)", len(decls))
	}
	if decls[0].Name != "shell" {
		t.Errorf("declaration name = %q, want shell", decls[0].Name)
	}
}
