package tools

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// declaredTool builds a function tool from args, so the test exercises the
// schema the ADK generates rather than a hand-written one.
func declaredTool[T any](t *testing.T, name string, want T) tool.Tool {
	t.Helper()
	tl, err := functiontool.New(functiontool.Config{Name: name}, func(_ agent.Context, args T) (map[string]any, error) {
		return map[string]any{"args": args}, nil
	})
	if err != nil {
		t.Fatalf("functiontool.New(%s): %v", name, err)
	}
	return tl
}

// runnable is the exported half of the interface the ADK flow uses to execute a
// tool: Run is called with the same map the callbacks saw.
type runnable interface {
	Run(ctx agent.Context, args any) (map[string]any, error)
}

func run(t *testing.T, tl tool.Tool, args map[string]any) (map[string]any, error) {
	t.Helper()
	r, ok := tl.(runnable)
	if !ok {
		t.Fatalf("tool %s does not expose Run", tl.Name())
	}
	return r.Run(&agent.ContextMock{}, args)
}

type zoomLikeArgs struct {
	ID int `json:"id" jsonschema:"Starting message ID of the node to unpack"`
	N  int `json:"n" jsonschema:"Number of messages covered"`
}

// TestRepairArgsLetsStringifiedNumbersRun is the bug from the TUI report: the
// model sent {"id":"5441","n":"1"}, validation rejected the call with
// `type: 5441 has type "string", want "integer"`, and the tool never ran.
func TestRepairArgsLetsStringifiedNumbersRun(t *testing.T) {
	tl := declaredTool(t, "zoom", zoomLikeArgs{})

	_, err := run(t, tl, map[string]any{"id": "5441", "n": "1"})
	if err == nil || !strings.Contains(err.Error(), `want "integer"`) {
		t.Fatalf("unrepaired call err = %v, want a schema type error", err)
	}

	args := map[string]any{"id": "5441", "n": "1"}
	RepairArgs(tl, args)
	if got, want := args["id"], int64(5441); got != want {
		t.Fatalf("repaired id = %#v, want %#v", got, want)
	}
	out, err := run(t, tl, args)
	if err != nil {
		t.Fatalf("repaired call failed: %v", err)
	}
	// The handler echoes what it was given; the output schema renders it back as
	// a map, so compare the numbers as text.
	echoed, ok := out["args"].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want the echoed args map", out["args"])
	}
	if got := fmt.Sprint(echoed["id"]); got != "5441" {
		t.Fatalf("handler saw id %v, want 5441", echoed["id"])
	}
	if got := fmt.Sprint(echoed["n"]); got != "1" {
		t.Fatalf("handler saw n %v, want 1", echoed["n"])
	}
}

type shellLikeArgs struct {
	Command string `json:"command" jsonschema:"Shell command to run"`
}

// TestRepairArgsLeavesStringArgumentsAlone pins the limit of the rewrite: a
// string parameter whose value happens to look like a number is still a string.
// Coercing "123" into an integer here would break the tool it was meant for.
func TestRepairArgsLeavesStringArgumentsAlone(t *testing.T) {
	tl := declaredTool(t, "shell", shellLikeArgs{})
	for _, value := range []string{"123", "1.5", "true", " 42 "} {
		args := map[string]any{"command": value}
		RepairArgs(tl, args)
		if got := args["command"]; got != value {
			t.Fatalf("command %q became %#v, want it untouched", value, got)
		}
	}
}

type flagArgs struct {
	Label   string  `json:"label"`
	Force   bool    `json:"force"`
	Ratio   float64 `json:"ratio"`
	Retries int     `json:"retries"`
}

// TestRepairArgsCoversBooleansAndFloats checks the other scalar types a model
// stringifies, and that already-correct arguments are left as they are.
func TestRepairArgsCoversBooleansAndFloats(t *testing.T) {
	tl := declaredTool(t, "flag", flagArgs{})
	args := map[string]any{
		"label":   "7",
		"force":   "true",
		"ratio":   " 2.5 ",
		"retries": 3,
	}
	RepairArgs(tl, args)

	if got := args["label"]; got != "7" {
		t.Errorf("label = %#v, want the string \"7\"", got)
	}
	if got := args["force"]; got != true {
		t.Errorf("force = %#v, want true", got)
	}
	if got, want := args["ratio"], 2.5; got != want {
		t.Errorf("ratio = %#v, want %#v", got, want)
	}
	if got, want := args["retries"], 3; got != want {
		t.Errorf("retries = %#v, want the original %#v", got, want)
	}
	if _, err := run(t, tl, args); err != nil {
		t.Fatalf("repaired call failed: %v", err)
	}
}

type nestedArgs struct {
	Name string     `json:"name"`
	Opts nestedOpts `json:"opts"`
}

type nestedOpts struct {
	Limit int `json:"limit"`
}

// TestRepairArgsDescendsIntoObjects covers the object-typed parameters the MCP
// and patch tools use: the numbers inside them were stringified too.
func TestRepairArgsDescendsIntoObjects(t *testing.T) {
	tl := declaredTool(t, "nested", nestedArgs{})
	args := map[string]any{"name": "x", "opts": map[string]any{"limit": "10"}}
	RepairArgs(tl, args)

	opts, ok := args["opts"].(map[string]any)
	if !ok {
		t.Fatalf("opts = %#v, want a map", args["opts"])
	}
	if got, want := opts["limit"], int64(10); got != want {
		t.Fatalf("nested limit = %#v, want %#v", got, want)
	}
	if _, err := run(t, tl, args); err != nil {
		t.Fatalf("repaired call failed: %v", err)
	}
}

// TestRepairArgsKeepsUnparseableValues: a value that is not a clean literal is
// left for the tool's own error, which says more than a silent guess would.
func TestRepairArgsKeepsUnparseableValues(t *testing.T) {
	tl := declaredTool(t, "zoom", zoomLikeArgs{})
	args := map[string]any{"id": "not-a-number", "n": "1.5"}
	RepairArgs(tl, args)
	if got := args["id"]; got != "not-a-number" {
		t.Errorf("id = %#v, want it untouched", got)
	}
	if got := args["n"]; got != "1.5" {
		t.Errorf("n = %#v, want it untouched", got)
	}
}

// opaqueTool is a tool with no declaration, like the model-side built-ins.
type opaqueTool struct{}

func (opaqueTool) Name() string        { return "opaque" }
func (opaqueTool) Description() string { return "no schema" }
func (opaqueTool) IsLongRunning() bool { return false }

// TestRepairArgsIgnoresToolsWithoutASchema: without a declaration there is
// nothing to compare against, so nothing is touched (and nothing panics).
func TestRepairArgsIgnoresToolsWithoutASchema(t *testing.T) {
	args := map[string]any{"id": "5441"}
	RepairArgs(opaqueTool{}, args)
	RepairArgs(nil, args)
	if got := args["id"]; got != "5441" {
		t.Fatalf("id = %#v, want it untouched", got)
	}
}
