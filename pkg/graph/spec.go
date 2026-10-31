// Package graph lets the agent author, validate, save and run multi-agent
// graphs on the ADK workflow engine.
//
// The value of a graph over a single turn is context isolation: every node is
// its own agent with its own prompt and tools, so a fan-out of researchers can
// each read dozens of files and the orchestrator only ever sees the merged
// result. A graph is therefore a reusable procedure the agent writes once
// ("adversarial review", "research fan-out over three angles") and replays.
package graph

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
)

// SpecVersion is the only spec version this build understands.
const SpecVersion = 1

// Hard caps. Budgets are mandatory but they are also bounded: an agent that
// writes a 200-node fan-out would otherwise self-DoS on its own quota.
const (
	MaxNodesCap        = 24
	MaxInstructionLen  = 6000
	DefaultWallSeconds = 300
	DefaultMaxTokens   = 200_000
	DefaultConcurrency = 3
	MaxConcurrencyCap  = 8
	MinWallSeconds     = 10
	MaxWallSeconds     = 1800
	MinTokenBudget     = 10_000
	MaxTokenBudget     = 1_000_000
)

// Node kinds. FunctionNode and DynamicNode both need Go closures, so an
// agent-authored spec cannot express them; that restriction is the safety
// story rather than a missing feature.
const (
	KindAgent = "agent"
	KindTool  = "tool"
	KindJoin  = "join"
)

// Agent archetypes. roles decide the system prompt and the default tool class.
const (
	RoleGeneral    = "general"
	RoleResearcher = "researcher"
	RoleCoder      = "coder"
	RoleCritic     = "critic"
)

// Tool classes for agent nodes. A "write" node gets mutating tools too, and
// those calls are still gated by the sandbox policy: a graph runs unattended,
// so anything the policy would ask a human about fails instead of hanging.
const (
	ToolsRead  = "read"
	ToolsWrite = "write"
	ToolsNone  = "none"
)

// Start is the pseudo-node every graph begins at.
const Start = "START"

// Budgets bound a run. All four are enforced: max_nodes and depth at validate
// time, concurrency and wall clock and tokens at run time.
type Budgets struct {
	MaxNodes       int `json:"max_nodes" jsonschema:"upper bound on how many nodes this graph may contain"`
	MaxWallSeconds int `json:"max_wall_seconds,omitempty" jsonschema:"wall-clock ceiling for one run, 10-1800 seconds (default 300)"`
	MaxTokens      int `json:"max_tokens,omitempty" jsonschema:"token ceiling across all nodes in one run, 10000-1000000 (default 200000)"`
}

// Node is one graph node.
type Node struct {
	ID          string         `json:"id" jsonschema:"short unique node id, lowercase letters digits - or _"`
	Kind        string         `json:"kind" jsonschema:"agent (an LLM with its own context), tool (one tool call with fixed args), or join (merge every predecessor's output)"`
	Role        string         `json:"role,omitempty" jsonschema:"for kind=agent: researcher (read-only exploration), critic (adversarial review), coder (may edit and run), general"`
	Instruction string         `json:"instruction,omitempty" jsonschema:"for kind=agent: the node's standing brief; it receives its predecessor's output as user input"`
	Model       string         `json:"model,omitempty" jsonschema:"optional per-node model override, e.g. flash or pro; defaults to the session model"`
	Tools       string         `json:"tools,omitempty" jsonschema:"for kind=agent: read (default), write, or none"`
	Tool        string         `json:"tool,omitempty" jsonschema:"for kind=tool: the registered tool name to call"`
	Args        map[string]any `json:"args,omitempty" jsonschema:"for kind=tool: fixed arguments for that tool"`
}

// Spec is the source of truth for a graph. It is JSON, versioned, and
// round-trips through disk; the runnable *workflow.Workflow is rebuilt from it
// on every load because engine objects are not serialisable.
type Spec struct {
	V              int        `json:"v,omitempty" jsonschema:"spec version, must be 1"`
	Name           string     `json:"name" jsonschema:"graph name, lowercase letters digits - or _"`
	Description    string     `json:"description,omitempty" jsonschema:"one line the orchestrator will see in the graph catalog: what this graph is for"`
	MaxConcurrency int        `json:"max_concurrency,omitempty" jsonschema:"how many nodes may run at once, 1-8 (default 3); this is the fan-out width"`
	Budgets        *Budgets   `json:"budgets" jsonschema:"required resource ceilings for one run"`
	Nodes          []Node     `json:"nodes" jsonschema:"the nodes, at least one"`
	Edges          [][]string `json:"edges" jsonschema:"edges as [from,to] or [from,to,route]; from is a node id or START. Several edges out of one node fan out; several edges into a join merge"`
}

var (
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	idRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// Normalize fills defaults so callers and validators agree on what an omitted
// field means. It mutates the spec.
func (s *Spec) Normalize() {
	if s.V == 0 {
		s.V = SpecVersion
	}
	if s.MaxConcurrency == 0 {
		s.MaxConcurrency = DefaultConcurrency
	}
	if s.Budgets == nil {
		return
	}
	if s.Budgets.MaxWallSeconds == 0 {
		s.Budgets.MaxWallSeconds = DefaultWallSeconds
	}
	if s.Budgets.MaxTokens == 0 {
		s.Budgets.MaxTokens = DefaultMaxTokens
	}
}

// ValidationError carries every problem at once: a model that gets one error
// per attempt burns three round trips fixing what it could have fixed in one.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "graph spec rejected: " + e.Problems[0]
	}
	return "graph spec rejected:\n - " + strings.Join(e.Problems, "\n - ")
}

// ValidateOptions supplies the environment the spec is checked against.
type ValidateOptions struct {
	// KnownTool reports whether a tool node names a tool that will exist at
	// build time. Nil means "do not check" (used when only shape matters).
	KnownTool func(name string) bool
}

// Validate checks the spec and returns a *ValidationError listing everything
// wrong with it. Nothing here executes anything.
func (s *Spec) Validate(opts ValidateOptions) error {
	s.Normalize()
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if s.V != SpecVersion {
		add("v is %d but this build understands only v=%d", s.V, SpecVersion)
	}
	if !nameRE.MatchString(s.Name) {
		add("name %q must be 1-64 chars of lowercase letters, digits, - or _", s.Name)
	}
	if s.Budgets == nil {
		add("budgets is required: give max_nodes (and optionally max_wall_seconds, max_tokens) so a fan-out cannot run away")
	}

	// --- nodes -----------------------------------------------------------
	if len(s.Nodes) == 0 {
		add("nodes is empty: a graph needs at least one node")
	}
	if len(s.Nodes) > MaxNodesCap {
		add("%d nodes exceeds the %d-node cap", len(s.Nodes), MaxNodesCap)
	}
	seen := make(map[string]bool, len(s.Nodes))
	for i, n := range s.Nodes {
		where := fmt.Sprintf("nodes[%d]", i)
		if n.ID == "" {
			add("%s has no id", where)
			continue
		}
		where = fmt.Sprintf("nodes[%d] %q", i, n.ID)
		if !idRE.MatchString(n.ID) {
			add("%s: id must be 1-32 chars of lowercase letters, digits, - or _", where)
		}
		if n.ID == "user" || n.ID == Start {
			add("%s: id %q is reserved", where, n.ID)
		}
		if seen[n.ID] {
			add("%s: duplicate node id", where)
		}
		seen[n.ID] = true

		switch n.Kind {
		case KindAgent:
			if n.Role == "" {
				n.Role = RoleGeneral
				s.Nodes[i].Role = RoleGeneral
			}
			switch n.Role {
			case RoleGeneral, RoleResearcher, RoleCoder, RoleCritic:
			default:
				add("%s: role %q is not supported; use researcher, critic, coder or general", where, n.Role)
			}
			if strings.TrimSpace(n.Instruction) == "" {
				add("%s: kind=agent needs an instruction: it is the node's standing brief", where)
			} else if len(n.Instruction) > MaxInstructionLen {
				add("%s: instruction is %d bytes, over the %d-byte limit", where, len(n.Instruction), MaxInstructionLen)
			}
			if n.Tools == "" {
				n.Tools = ToolsRead
				s.Nodes[i].Tools = ToolsRead
			}
			switch n.Tools {
			case ToolsRead, ToolsWrite, ToolsNone:
			default:
				add("%s: tools %q is not supported; use read, write or none", where, n.Tools)
			}
			if n.Tool != "" || len(n.Args) > 0 {
				add("%s: tool/args belong to kind=tool, not kind=agent", where)
			}
		case KindTool:
			if n.Tool == "" {
				add("%s: kind=tool needs tool: the registered tool name to call", where)
			} else if opts.KnownTool != nil && !opts.KnownTool(n.Tool) {
				add("%s: tool %q is not a registered tool", where, n.Tool)
			}
			if n.Instruction != "" || n.Role != "" || n.Tools != "" || n.Model != "" {
				add("%s: role/instruction/tools/model belong to kind=agent; a tool node only takes tool and args", where)
			}
		case KindJoin:
			if n.Instruction != "" || n.Role != "" || n.Tool != "" || n.Tools != "" || n.Model != "" || len(n.Args) > 0 {
				add("%s: a join node takes no role, instruction, tools, model, tool or args; it merges its predecessors", where)
			}
		case "":
			add("%s: kind is required: agent, tool or join", where)
		default:
			add("%s: kind %q is not supported; use agent, tool or join (function and dynamic nodes need Go code, so they cannot be authored as data)", where, n.Kind)
		}
	}

	// --- edges -----------------------------------------------------------
	out := make(map[string][]edge)
	in := make(map[string][]edge)
	edgeSeen := make(map[string]bool)
	for i, e := range s.Edges {
		where := fmt.Sprintf("edges[%d]", i)
		if len(e) != 2 && len(e) != 3 {
			add("%s: an edge is [from,to] or [from,to,route], got %d fields", where, len(e))
			continue
		}
		from, to := e[0], e[1]
		if from != Start && !seen[from] {
			add("%s: from %q is neither START nor a node id", where, from)
		}
		if to == Start {
			add("%s: edges into START are not allowed", where)
		} else if !seen[to] {
			add("%s: to %q is not a node id", where, to)
		}
		if from == to {
			add("%s: node %q cannot point at itself", where, to)
		}
		key := from + "\x00" + to
		if edgeSeen[key] {
			add("%s: duplicate edge %s->%s", where, from, to)
		}
		edgeSeen[key] = true
		edge := edge{from: from, to: to}
		if len(e) == 3 {
			edge.route = strings.TrimSpace(e[2])
			if edge.route == "" {
				add("%s: route is empty; drop the third field for an unconditional edge", where)
			}
		}
		out[from] = append(out[from], edge)
		in[to] = append(in[to], edge)
	}

	hasRoutes := false
	for _, edges := range out {
		for _, e := range edges {
			if e.route != "" {
				hasRoutes = true
				break
			}
		}
		if hasRoutes {
			break
		}
	}

	// Sink check: an unrouted graph has a single sink node with no outgoing
	// edge; a conditionally routed graph may terminate at one sink per branch
	// or reconverge at a single terminal node.
	var sinks []string
	for _, n := range s.Nodes {
		if len(out[n.ID]) == 0 {
			sinks = append(sinks, n.ID)
		}
	}
	switch {
	case len(sinks) == 0:
		add("every node has an outgoing edge, so the graph has no sink; the result of a run is the one node with no outgoing edge")
	case len(sinks) > 1 && !hasRoutes:
		add("the graph has %d sinks (%s); exactly one node may have no outgoing edge, and its output is the graph result", len(sinks), strings.Join(sinks, ", "))
	}

	// Routes: an unconditional edge fires always, so mixing the two makes the
	// routed edge's condition meaningless.
	for node, edges := range out {
		routed, plain := 0, 0
		for _, e := range edges {
			if e.route == "" {
				plain++
			} else {
				routed++
			}
		}
		if routed > 0 && plain > 0 {
			add("node %q mixes routed and unrouted edges; either route every edge out of it or none", node)
		}
	}

	// Fan-in must land on a join unless the graph uses conditional routes (where
	// mutually exclusive branches reconverge on a single downstream node).
	for to, edges := range in {
		if len(edges) < 2 {
			continue
		}
		if kind := s.kindOf(to); kind != KindJoin && !hasRoutes {
			add("node %q has %d incoming edges but kind=%s; fan-in must land on a join node that merges them", to, len(edges), kind)
		}
		if kind := s.kindOf(to); kind == KindJoin {
			for _, e := range edges {
				if e.route != "" {
					add("edge %s->%s is routed but %q is a join, which waits for every predecessor; routes cannot feed a join", e.from, to, to)
				}
			}
		}
	}
	for _, n := range s.Nodes {
		if n.Kind == KindJoin && len(in[n.ID]) < 2 {
			add("join %q has %d predecessor(s); a join needs at least 2 to merge", n.ID, len(in[n.ID]))
		}
	}

	if len(problems) == 0 {
		problems = append(problems, s.checkReachable(seen, out, in)...)
		problems = append(problems, s.checkAcyclic(out)...)
		problems = append(problems, s.checkBudgets()...)
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

type edge struct {
	from, to, route string
}

func (s *Spec) kindOf(id string) string {
	for _, n := range s.Nodes {
		if n.ID == id {
			return n.Kind
		}
	}
	return ""
}

func (s *Spec) checkReachable(seen map[string]bool, out, in map[string][]edge) []string {
	var problems []string
	reached := map[string]bool{}
	queue := []string{Start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range out[cur] {
			if !reached[e.to] {
				reached[e.to] = true
				queue = append(queue, e.to)
			}
		}
	}
	for _, n := range s.Nodes {
		if !seen[n.ID] {
			continue
		}
		if !reached[n.ID] {
			problems = append(problems, fmt.Sprintf("node %q is not reachable from START; add an edge from START or from a node that is", n.ID))
		}
	}
	return problems
}

func (s *Spec) checkAcyclic(out map[string][]edge) []string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var path []string
	var found []string

	var visit func(node string) bool
	visit = func(node string) bool {
		color[node] = grey
		path = append(path, node)
		for _, e := range out[node] {
			// Conditional routed edges ([from, to, route]) may close bounded
			// verify-fix retry loops enforced by budgets.max_nodes; only
			// unconditional cycles are rejected (golem-8c6.5).
			if e.route != "" {
				continue
			}
			switch color[e.to] {
			case grey:
				cycle := append([]string{}, path...)
				for i := len(cycle) - 1; i >= 0; i-- {
					if cycle[i] == e.to {
						cycle = append(cycle[i:], e.to)
						break
					}
				}
				found = append(found, fmt.Sprintf("cycle %s: an unconditional cycle never terminates; use a routed edge [from, to, route] for bounded retry loops", strings.Join(cycle, " -> ")))
				return true
			case white:
				if visit(e.to) {
					return true
				}
			}
		}
		path = path[:len(path)-1]
		color[node] = black
		return false
	}

	for _, start := range append([]string{Start}, s.nodeIDs()...) {
		if color[start] == white && visit(start) {
			return found
		}
	}
	return found
}

func (s *Spec) nodeIDs() []string {
	ids := make([]string, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

func (s *Spec) checkBudgets() []string {
	b := s.Budgets
	var problems []string
	if b.MaxNodes <= 0 {
		problems = append(problems, "budgets.max_nodes must be a positive integer")
	} else if b.MaxNodes < len(s.Nodes) {
		problems = append(problems, fmt.Sprintf("budgets.max_nodes is %d but the spec lists %d nodes", b.MaxNodes, len(s.Nodes)))
	} else if b.MaxNodes > MaxNodesCap {
		problems = append(problems, fmt.Sprintf("budgets.max_nodes %d is above the %d-node cap", b.MaxNodes, MaxNodesCap))
	}
	if b.MaxWallSeconds < MinWallSeconds || b.MaxWallSeconds > MaxWallSeconds {
		problems = append(problems, fmt.Sprintf("budgets.max_wall_seconds must be between %d and %d, got %d", MinWallSeconds, MaxWallSeconds, b.MaxWallSeconds))
	}
	if b.MaxTokens < MinTokenBudget || b.MaxTokens > MaxTokenBudget {
		problems = append(problems, fmt.Sprintf("budgets.max_tokens must be between %d and %d, got %d", MinTokenBudget, MaxTokenBudget, b.MaxTokens))
	}
	if s.MaxConcurrency < 1 || s.MaxConcurrency > MaxConcurrencyCap {
		problems = append(problems, fmt.Sprintf("max_concurrency must be between 1 and %d, got %d", MaxConcurrencyCap, s.MaxConcurrency))
	}
	if s.MaxConcurrency > len(s.Nodes) {
		problems = append(problems, fmt.Sprintf("max_concurrency %d is wider than the graph's %d nodes", s.MaxConcurrency, len(s.Nodes)))
	}
	return problems
}

// Sink returns the id of the primary node whose output is the graph result.
func (s *Spec) Sink() string {
	sinks := s.Sinks()
	if len(sinks) > 0 {
		return sinks[0]
	}
	return ""
}

// Sinks returns the ids of all terminal nodes (nodes with no outgoing edges)
// in spec order.
func (s *Spec) Sinks() []string {
	out := map[string]int{}
	for _, e := range s.Edges {
		if len(e) >= 2 {
			out[e[0]]++
		}
	}
	var sinks []string
	for _, n := range s.Nodes {
		if out[n.ID] == 0 {
			sinks = append(sinks, n.ID)
		}
	}
	return sinks
}

// outgoingRoutes returns the non-empty route labels on edges leaving nodeID.
func (s *Spec) outgoingRoutes(nodeID string) []string {
	var routes []string
	for _, e := range s.Edges {
		if len(e) == 3 && e[0] == nodeID {
			if r := strings.TrimSpace(e[2]); r != "" {
				routes = append(routes, r)
			}
		}
	}
	return routes
}

// WorkflowName is the engine-level name a run is registered under. The engine
// keys per-session run state by this name, so it carries a short spec hash:
// editing a graph must not inherit the run state of the old shape.
func (s *Spec) WorkflowName() string {
	return "graph-" + s.Name + "-" + s.Hash()
}

// Hash returns a short stable fingerprint of the spec's shape.
func (s *Spec) Hash() string {
	h := fnv.New32a()
	for _, n := range s.Nodes {
		fmt.Fprintf(h, "%s|%s|%s|%s|", n.ID, n.Kind, n.Role, n.Tools)
	}
	for _, e := range s.Edges {
		fmt.Fprintf(h, "%s>", strings.Join(e, ","))
	}
	return fmt.Sprintf("%08x", h.Sum32())
}
