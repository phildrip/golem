package graph

import (
	"strings"
	"testing"
)

// validSpec is the shape every rejection test perturbs.
func validSpec() *Spec {
	return &Spec{
		V:              1,
		Name:           "review",
		Description:    "draft then critique then merge",
		MaxConcurrency: 2,
		Budgets:        &Budgets{MaxNodes: 6, MaxWallSeconds: 60, MaxTokens: 50_000},
		Nodes: []Node{
			{ID: "draft", Kind: KindAgent, Role: RoleGeneral, Instruction: "write the plan"},
			{ID: "critic", Kind: KindAgent, Role: RoleCritic, Instruction: "attack the plan"},
			{ID: "merge", Kind: KindJoin},
			{ID: "final", Kind: KindAgent, Role: RoleGeneral, Instruction: "fold the critique in"},
		},
		Edges: [][]string{
			{"START", "draft"},
			{"START", "critic"},
			{"draft", "merge"},
			{"critic", "merge"},
			{"merge", "final"},
		},
	}
}

func TestSpecValidates(t *testing.T) {
	spec := validSpec()
	spec.Normalize()
	if err := spec.Validate(ValidateOptions{}); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if got := spec.Sink(); got != "final" {
		t.Fatalf("sink = %q, want final", got)
	}
	if spec.Nodes[0].Tools != ToolsRead {
		t.Fatalf("default tool class = %q, want read", spec.Nodes[0].Tools)
	}
}

func TestSpecRejections(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Spec)
		want string
	}{
		{"no name", func(s *Spec) { s.Name = "Review Graphs" }, "must be 1-64 chars"},
		{"bad version", func(s *Spec) { s.V = 3 }, "understands only v=1"},
		{"no budgets", func(s *Spec) { s.Budgets = nil }, "budgets is required"},
		{"no nodes", func(s *Spec) { s.Nodes = nil; s.Edges = nil }, "nodes is empty"},
		{"unknown kind", func(s *Spec) { s.Nodes[0].Kind = "llm" }, "kind \"llm\" is not supported"},
		{"unknown role", func(s *Spec) { s.Nodes[0].Role = "reviewer" }, "role \"reviewer\" is not supported"},
		{"agent without instruction", func(s *Spec) { s.Nodes[0].Instruction = "  " }, "needs an instruction"},
		{"agent with tool", func(s *Spec) { s.Nodes[0].Tool = "read_file" }, "belong to kind=tool"},
		{"tool node without tool", func(s *Spec) {
			s.Nodes[0] = Node{ID: "draft", Kind: KindTool}
		}, "needs tool"},
		{"unknown tool", func(s *Spec) {
			s.Nodes[0] = Node{ID: "draft", Kind: KindTool, Tool: "no_such_tool"}
		}, "is not a registered tool"},
		{"join with extras", func(s *Spec) { s.Nodes[2].Instruction = "merge please" }, "a join node takes no role"},
		{"duplicate id", func(s *Spec) { s.Nodes[1].ID = "draft"; s.Edges[3][0] = "draft" }, "duplicate node id"},
		{"reserved id", func(s *Spec) { s.Nodes[0].ID = "user"; s.Edges[0][1] = "user"; s.Edges[2][0] = "user" }, "reserved"},
		{"edge to nowhere", func(s *Spec) { s.Edges[4][1] = "ghost" }, "\"ghost\" is not a node id"},
		{"edge into START", func(s *Spec) { s.Edges = append(s.Edges, []string{"final", "START"}) }, "edges into START"},
		{"duplicate edge", func(s *Spec) { s.Edges = append(s.Edges, []string{"draft", "merge"}) }, "duplicate edge"},
		{"self edge", func(s *Spec) { s.Edges = append(s.Edges, []string{"final", "final"}) }, "cannot point at itself"},
		{"fan-in without join", func(s *Spec) {
			// draft and critic both feed final directly.
			s.Nodes = s.Nodes[:3]
			s.Edges = [][]string{{"START", "draft"}, {"START", "critic"}, {"draft", "final2"}, {"critic", "final2"}}
			s.Nodes = append(s.Nodes, Node{ID: "final2", Kind: KindAgent, Role: RoleGeneral, Instruction: "x"})
		}, "fan-in must land on a join"},
		{"two sinks", func(s *Spec) { s.Edges = s.Edges[:4] }, "2 sinks"},
		{"unreachable", func(s *Spec) {
			s.Nodes = append(s.Nodes, Node{ID: "orphan", Kind: KindAgent, Role: RoleGeneral, Instruction: "x"})
			s.Edges = append(s.Edges, []string{"orphan", "final"})
			// orphan->final makes final a fan-in of 2 without a join, so the
			// reachability problem is checked only after structural ones pass.
			// Point orphan at merge instead: merge then has 3 predecessors.
			s.Edges = s.Edges[:len(s.Edges)-1]
			s.Edges = append(s.Edges, []string{"orphan", "merge"})
			// orphan is not wired from START or anything reachable, and merge
			// keeps its join kind, so reachability is the remaining problem.
			s.Nodes = append(s.Nodes, Node{ID: "tail", Kind: KindAgent, Role: RoleGeneral, Instruction: "y"})
			s.Edges = append(s.Edges, []string{"final", "tail"})
			s.Budgets.MaxNodes = 8
		}, "not reachable from START"},
		{"cycle", func(s *Spec) {
			// merge -> loop -> merge: a legal fan-in (merge is a join) that
			// also closes a loop.
			s.Nodes = append(s.Nodes, Node{ID: "loop", Kind: KindAgent, Role: RoleGeneral, Instruction: "x"})
			s.Edges = append(s.Edges, []string{"merge", "loop"}, []string{"loop", "merge"})
			s.Budgets.MaxNodes = 8
		}, "cycle"},
		{"budget too small", func(s *Spec) { s.Budgets.MaxNodes = 2 }, "but the spec lists 4 nodes"},
		{"wall clock out of range", func(s *Spec) { s.Budgets.MaxWallSeconds = 2 }, "max_wall_seconds must be between"},
		{"tokens out of range", func(s *Spec) { s.Budgets.MaxTokens = 5 }, "max_tokens must be between"},
		{"concurrency too wide", func(s *Spec) { s.MaxConcurrency = 9 }, "max_concurrency must be between"},
		{"mixes routed and plain", func(s *Spec) {
			s.Nodes = append(s.Nodes, Node{ID: "lr", Kind: KindAgent, Role: RoleGeneral, Instruction: "z"})
			s.Edges = append(s.Edges, []string{"merge", "lr", "branch"})
			s.Budgets.MaxNodes = 8
		}, "mixes routed and unrouted edges"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			tc.mut(spec)
			err := spec.Validate(ValidateOptions{KnownTool: func(name string) bool { return name == "read_file" }})
			if err == nil {
				t.Fatalf("expected rejection containing %q, spec validated", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error does not mention %q:\n%v", tc.want, err)
			}
		})
	}
}

// A model fixing one problem per attempt is the failure mode this prevents.
func TestValidationReportsEveryProblemAtOnce(t *testing.T) {
	spec := validSpec()
	spec.Name = "Bad Name"
	spec.Nodes[0].Role = "reviewer"
	spec.Nodes[2].Instruction = "merge this"
	err := spec.Validate(ValidateOptions{})
	if err == nil {
		t.Fatal("expected rejection")
	}
	var verr *ValidationError
	if !asValidationError(err, &verr) {
		t.Fatalf("error is not *ValidationError: %T", err)
	}
	if len(verr.Problems) < 3 {
		t.Fatalf("expected at least 3 problems, got %d: %v", len(verr.Problems), verr.Problems)
	}
}

func asValidationError(err error, target **ValidationError) bool {
	v, ok := err.(*ValidationError)
	if ok {
		*target = v
	}
	return ok
}

func TestWorkflowNameIsStablePerShape(t *testing.T) {
	a := validSpec()
	a.Normalize()
	b := validSpec()
	b.Normalize()
	if a.WorkflowName() != b.WorkflowName() {
		t.Fatalf("same shape produced different workflow names: %s vs %s", a.WorkflowName(), b.WorkflowName())
	}
	b.Nodes[0].Instruction = "write a different plan"
	if a.WorkflowName() != b.WorkflowName() {
		// Instruction text is deliberately not part of the fingerprint: editing
		// a brief should not orphan a paused run's state.
		t.Fatalf("instruction changed the workflow name")
	}
	b.Edges = append(b.Edges[:4], b.Edges[4:]...)
	b.Nodes[1].ID = "skeptic"
	if a.WorkflowName() == b.WorkflowName() {
		t.Fatalf("different topology produced the same workflow name")
	}
}
