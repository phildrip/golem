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
			return listResult{Output: "No graphs available. Build one with graph_save (see the Graphs section of your instructions)."}, nil
		}
		var sb strings.Builder
		for _, e := range entries {
			desc := e.Description
			if desc == "" {
				desc = "(no description)"
			}
			src := "saved"
			if e.Builtin() {
				src = "built-in"
			}
			fmt.Fprintf(&sb, "%-28s %2d nodes  %-9s %s\n", e.Name, e.Nodes, src, desc)
		}
		sb.WriteString("\nRun either kind by name with graph_run. Saving a graph under a built-in name overrides it.")
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
		if m.isSaved(spec.Name) {
			return showResult{Output: specJSON(spec), Path: m.path(spec.Name), Source: SourceWorkspace}, nil
		}
		// A built-in has no path of its own; say so, so the model does not try to
		// edit a file that is not there.
		return showResult{
			Output: specJSON(spec) + "\n(built-in, not a file in " + GraphsDir + "; saving a graph of this name overrides it for this workspace)",
			Source: SourceBuiltin,
		}, nil
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
			"Blocks until the graph finishes, pauses for a human decision, or its budgets run out; use it once the spec validates.",
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

	stat, err := functiontool.New(functiontool.Config{
		Name: "graph_stat",
		Description: "List this workspace's graph runs, newest first: run_id, graph, status, token cost and duration. " +
			"A run that is waiting needs a human decision, which graph_resume delivers. Runs outlive the process, " +
			"so check here when a previous turn left one paused.",
	}, func(ctx agent.Context, args statArgs) (statResult, error) {
		runs, err := m.ListRuns()
		if err != nil {
			return statResult{}, err
		}
		if args.RunID != "" {
			rec, err := m.LoadRun(args.RunID)
			if err != nil {
				return statResult{}, err
			}
			return statResult{Output: describeRun(rec, true)}, nil
		}
		if len(runs) == 0 {
			return statResult{Output: "No graph runs recorded yet."}, nil
		}
		var sb strings.Builder
		for _, rec := range runs {
			sb.WriteString(describeRun(rec, false))
			sb.WriteString("\n")
		}
		sb.WriteString("\ngraph_resume(run_id, approve|answer) answers a waiting run; graph_stat(run_id) shows one run in detail.")
		return statResult{Output: sb.String()}, nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, stat)

	resume, err := functiontool.New(functiontool.Config{
		Name: "graph_resume",
		Description: "Answer a paused graph run and drive it on from where it stopped. " +
			"approve answers a tool confirmation (true lets the parked tool call run, false refuses it); " +
			"answer replies to a node that asked for input. Finished nodes do not run again.",
	}, func(ctx agent.Context, args resumeArgs) (runResult, error) {
		res, runErr := m.Resume(ctx, args.RunID, Decision{Approve: args.Approve, Answer: args.Answer})
		if res == nil {
			if runErr != nil {
				return runResult{}, runErr
			}
			return runResult{}, fmt.Errorf("graph produced no result")
		}
		text := renderResult(res)
		if runErr != nil {
			return runResult{Output: text}, nil
		}
		return runResult{Output: text}, nil
	})
	if err != nil {
		return nil, err
	}
	out = append(out, resume)

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

type statArgs struct {
	RunID string `json:"run_id,omitempty" jsonschema:"one run to describe in detail; omit to list them all"`
}

type resumeArgs struct {
	RunID   string `json:"run_id" jsonschema:"the waiting run to answer, as reported by graph_run or graph_stat"`
	Approve bool   `json:"approve,omitempty" jsonschema:"for a tool confirmation: true lets the parked tool call run, false refuses it"`
	Answer  string `json:"answer,omitempty" jsonschema:"for a node waiting on input: the reply to hand it"`
}

type statResult struct {
	Output string `json:"output"`
}

type listResult struct {
	Output string `json:"output"`
}

type showResult struct {
	Output string `json:"output"`
	Path   string `json:"path,omitempty"`
	Source string `json:"source,omitempty"`
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
	if res.Status == string(RunWaiting) {
		fmt.Fprintf(&sb, "graph %s PAUSED (run_id %s) — a node needs a human decision:\n", res.Graph, res.RunID)
		for _, w := range res.Waiting {
			node := w.Node
			if node == "" {
				node = "(unknown node)"
			}
			switch w.Kind {
			case InterruptToolConfirmation:
				fmt.Fprintf(&sb, "  - node %s wants to run %s", node, w.ToolName)
				if len(w.ToolArgs) > 0 {
					if data, err := marshalInline(w.ToolArgs); err == nil {
						fmt.Fprintf(&sb, "(%s)", data)
					}
				}
				if w.Hint != "" {
					fmt.Fprintf(&sb, " — %s", w.Hint)
				}
			case InterruptRequestInput:
				fmt.Fprintf(&sb, "  - node %s asks: %s", node, w.Hint)
				if w.Payload != nil {
					if data, err := marshalInline(w.Payload); err == nil {
						fmt.Fprintf(&sb, "\n    payload: %s", data)
					}
				}
			default:
				fmt.Fprintf(&sb, "  - node %s is blocked on %s", node, w.ToolName)
			}
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "\nAnswer with graph_resume(run_id=%q, approve=true|false) for a tool confirmation, "+
			"or answer=... for a question. The run keeps everything it has already finished: "+
			"only the parked node and the nodes after it run again.\n\n", res.RunID)
	}
	if res.CachedTokens > 0 {
		fmt.Fprintf(&sb, "<graph_result name=%q nodes=%d tokens=%d cached_tokens=%d duration=%s>\n",
			res.Graph, len(res.Nodes), res.Tokens, res.CachedTokens, res.Duration.Round(time.Millisecond))
	} else {
		fmt.Fprintf(&sb, "<graph_result name=%q nodes=%d tokens=%d duration=%s>\n",
			res.Graph, len(res.Nodes), res.Tokens, res.Duration.Round(time.Millisecond))
	}
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
		if n.Activations > 1 {
			kind = fmt.Sprintf("%s(x%d)", kind, n.Activations)
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

// describeRun renders one run record: one line when listing, the whole record
// when the caller asked for it by ID.
func describeRun(rec *RunRecord, detail bool) string {
	status := string(rec.Status)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-34s %-22s %-9s %7d tok %8s",
		rec.RunID, rec.Graph, status, rec.Tokens, rec.Duration.Round(time.Second))
	if rec.Status == RunWaiting {
		for _, w := range rec.Waiting {
			node := w.Node
			if node == "" {
				node = "?"
			}
			switch w.Kind {
			case InterruptToolConfirmation:
				fmt.Fprintf(&sb, "  waiting: %s wants %s", node, w.ToolName)
			case InterruptRequestInput:
				fmt.Fprintf(&sb, "  waiting: %s asks \"%s\"", node, clipRunes(w.Hint, 80))
			default:
				fmt.Fprintf(&sb, "  waiting: %s on %s", node, w.ToolName)
			}
		}
	}
	if rec.Err != "" {
		fmt.Fprintf(&sb, "  error: %s", clipRunes(rec.Err, 200))
	}
	if !detail {
		return sb.String()
	}
	var out strings.Builder
	out.WriteString(sb.String())
	out.WriteString("\n")
	fmt.Fprintf(&out, "input: %s\n", clipRunes(firstLine(rec.Input), 200))
	if rec.Output != "" {
		fmt.Fprintf(&out, "sink output:\n%s\n", clipRunes(rec.Output, maxNodeText))
	}
	out.WriteString("nodes:\n")
	for _, n := range rec.Nodes {
		kind := n.Kind
		if n.Role != "" {
			kind = n.Role
		}
		note := ""
		if n.Err != "" {
			note = " — " + n.Err
		}
		fmt.Fprintf(&out, "- %-20s %-10s %6d tok %8s  %s%s\n", n.ID, kind, n.Tokens,
			n.Duration.Round(time.Millisecond), clipRunes(firstLine(n.Text), 120), note)
	}
	if rec.Spec != nil {
		fmt.Fprintf(&out, "\nspec: %s (graph_show %q to print it)\n", rec.Spec.Name, rec.Spec.Name)
	}
	return out.String()
}

// marshalInline encodes a small value for a one-line tool message.
func marshalInline(v any) (string, error) {
	data, err := marshalJSON(v)
	if err != nil {
		return "", err
	}
	return clipRunes(string(data), 400), nil
}

func specJSON(spec *Spec) string {
	data, err := marshalIndent(spec)
	if err != nil {
		return fmt.Sprintf("(could not encode spec: %v)", err)
	}
	return string(data)
}
