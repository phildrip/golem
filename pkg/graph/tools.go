package graph

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// NewTools returns the graph_* tools an orchestrator is given.
func NewTools(m *Manager) ([]tool.Tool, error) {
	var out []tool.Tool

	list, err := functiontool.New(functiontool.Config{
		Name: "graph_list",
		Description: "List the saved graphs for this workspace: name, node count and what each is for. " +
			"Saved graphs are reusable procedures — check here before building one from scratch.",
	}, func(ctx agent.Context, _ struct{}) (listResult, error) {
		entries, err := m.List()
		if err != nil {
			return listResult{}, err
		}
		if len(entries) == 0 {
			return listResult{Output: "No saved graphs yet. Build one with graph_save (see the Graph section of your instructions)."}, nil
		}
		var sb strings.Builder
		for _, e := range entries {
			desc := e.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Fprintf(&sb, "%-28s %2d nodes  %s\n", e.Name, e.Nodes, desc)
		}
		return listResult{Output: sb.String()}, nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, list)

	show, err := functiontool.New(functiontool.Config{
		Name:        "graph_show",
		Description: "Print a saved graph's spec as JSON, so you can adapt it instead of rewriting it from memory.",
	}, func(ctx agent.Context, args nameArgs) (showResult, error) {
		spec, err := m.Load(args.Name)
		if err != nil {
			return showResult{}, err
		}
		path := m.path(spec.Name)
		return showResult{Output: specJSON(spec), Path: path}, nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, show)

	validate, err := functiontool.New(functiontool.Config{
		Name: "graph_validate",
		Description: "Check a graph spec without saving or running it. Returns every problem at once, " +
			"so fix them in one pass. Cheap: use it freely while iterating on a spec.",
	}, func(ctx agent.Context, args specArgs) (validateResult, error) {
		spec := args.Spec
		if err := spec.Validate(ValidateOptions{KnownTool: func(name string) bool { return m.builder.ToolNames()[name] }}); err != nil {
			return validateResult{OK: false, Output: err.Error()}, nil
		}
		return validateResult{OK: true, Output: describeSpec(&spec)}, nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, validate)

	save, err := functiontool.New(functiontool.Config{
		Name: "graph_save",
		Description: "Validate a graph spec and save it under " + GraphsDir + "/<name>.json, replacing any graph of that name. " +
			"Save the graphs you expect to reuse; the name and description are how you (and future sessions) find them again.",
	}, func(ctx agent.Context, args specArgs) (saveResult, error) {
		spec := args.Spec
		path, err := m.Save(&spec)
		if err != nil {
			return saveResult{OK: false, Output: err.Error()}, nil
		}
		return saveResult{OK: true, Path: path, Output: "saved " + spec.Name + " to " + path + "\n" + describeSpec(&spec)}, nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, save)

	del, err := functiontool.New(functiontool.Config{
		Name:        "graph_delete",
		Description: "Delete a saved graph by name.",
	}, func(ctx agent.Context, args nameArgs) (deleteResult, error) {
		if err := m.Delete(args.Name); err != nil {
			return deleteResult{}, err
		}
		return deleteResult{Output: "deleted " + args.Name}, nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, del)

	run, err := functiontool.New(functiontool.Config{
		Name: "graph_run",
		Description: "Run a graph to completion inside this turn and return its sink node's output plus a per-node digest. " +
			"Each node is a separate agent with its own context, so this is how you get N independent investigations merged, " +
			"or a draft/critique/revise loop, without filling your own context with the intermediate work. " +
			"Blocks until the graph finishes or its budgets run out; use it once the spec validates.",
	}, func(ctx agent.Context, args runArgs) (runResult, error) {
		var spec *Spec
		if args.Spec != nil {
			spec = args.Spec
		} else {
			if args.Name == "" {
				return runResult{}, fmt.Errorf("give a saved graph name or an inline spec")
			}
			loaded, err := m.Load(args.Name)
			if err != nil {
				return runResult{}, err
			}
			spec = loaded
		}
		res, runErr := m.Run(ctx, spec, args.Input)
		if res == nil {
			if runErr != nil {
				return runResult{}, runErr
			}
			return runResult{}, fmt.Errorf("graph produced no result")
		}
		text := renderResult(res)
		if runErr != nil {
			// A partial result is still worth returning: the orchestrator can
			// use what completed and decide whether to retry narrower.
			return runResult{Output: text}, nil
		}
		return runResult{Output: text}, nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, run)

	return out, nil
}

// --- argument and result shapes (the model sees these as the tool schema) ---

type nameArgs struct {
	Name string `json:"name" jsonschema:"the saved graph's name"`
}

// specArgs wraps the spec in a named field: models reach for `{"spec": {...}}`
// whether or not the schema puts the fields at the root, so the schema says so
// and the first attempt is the right one.
type specArgs struct {
	Spec Spec `json:"spec" jsonschema:"the graph spec"`
}

type runArgs struct {
	Name  string `json:"name,omitempty" jsonschema:"saved graph to run; ignored when spec is given"`
	Spec  *Spec  `json:"spec,omitempty" jsonschema:"inline graph spec, for a one-off run you do not want to save first"`
	Input string `json:"input" jsonschema:"the seed text handed to every node wired from START"`
}

type listResult struct {
	Output string `json:"output"`
}

type showResult struct {
	Output string `json:"output"`
	Path   string `json:"path,omitempty"`
}

type validateResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
}

type saveResult struct {
	OK     bool   `json:"ok"`
	Path   string `json:"path,omitempty"`
	Output string `json:"output"`
}

type deleteResult struct {
	Output string `json:"output"`
}

type runResult struct {
	Output string `json:"output"`
}

// describeSpec is the one-line summary a validate or save returns.
func describeSpec(spec *Spec) string {
	roles := map[string]int{}
	for _, n := range spec.Nodes {
		if n.Kind == KindAgent {
			roles[n.Role]++
		}
	}
	var parts []string
	for role, count := range roles {
		parts = append(parts, fmt.Sprintf("%d %s", count, role))
	}
	return fmt.Sprintf("ok: %q — %d nodes (%s), sink %q, wall clock %ds, %d token ceiling, concurrency %d",
		spec.Name, len(spec.Nodes), strings.Join(parts, " + "), spec.Sink(),
		spec.Budgets.MaxWallSeconds, spec.Budgets.MaxTokens, spec.MaxConcurrency)
}

// renderResult is the tool result text: the deliverable, then what it cost.
func renderResult(res *Result) string {
	var sb strings.Builder
	if res.Err != "" {
		fmt.Fprintf(&sb, "graph %s FAILED: %s\n\n", res.Graph, res.Err)
	}
	fmt.Fprintf(&sb, "<graph_result name=%q nodes=%d tokens=%d duration=%s>\n",
		res.Graph, len(res.Nodes), res.Tokens, res.Duration.Round(time.Millisecond))
	sb.WriteString(res.Output)
	if sb.Len() > 0 && !strings.HasSuffix(res.Output, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("</graph_result>\n\n")
	sb.WriteString("Node digest:\n")
	for _, n := range res.Nodes {
		status := ""
		if n.Err != "" {
			status = " — " + n.Err
		}
		kind := n.Kind
		if n.Role != "" {
			kind = n.Role
		}
		preview := firstLine(n.Text)
		if preview == "" {
			preview = "(no text)"
		}
		fmt.Fprintf(&sb, "- %-20s %-10s %6d tok %6s  %s%s\n",
			n.ID, kind, n.Tokens, n.Duration.Round(time.Millisecond), clipRunes(preview, 160), status)
	}
	return sb.String()
}

func specJSON(spec *Spec) string {
	data, err := marshalIndent(spec)
	if err != nil {
		return fmt.Sprintf("(could not encode spec: %v)", err)
	}
	return string(data)
}
