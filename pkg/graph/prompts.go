package graph

import (
	"fmt"
	"strings"
	"time"
)

// Event is one progress notification from a running graph. The orchestrator's
// transports render these the way they render subagents, so a graph is visible
// while it runs instead of a tool call that looks hung for two minutes.
type Event struct {
	Graph    string
	Node     string
	Role     string
	Model    string
	Kind     string // start, done, error
	Prompt   string // the node's brief, shown when it starts
	Text     string
	Err      string
	Duration time.Duration
}

// Event kinds.
const (
	EventStart = "start"
	EventDone  = "done"
	EventError = "error"
)

// memoryGuidance is appended to a node's prompt when a mounted memory is
// available: nodes share the orchestrator's fractal store, so a critic can look
// up the user's earlier rulings instead of guessing at them.
const memoryGuidance = `

## Shared memory
You share Golem's append-only fractal memory with the orchestrator and every
other node.
- zoom(id, n) inspects a compacted node (id+n) or returns the verbatim message
  when n=1. date(id) gives an event's timestamp.
- Use it when a decision or requirement predates your input; do not guess at
  what the user asked earlier when you can look it up.`

// outputContract is the one rule every node must follow: the node's final
// message IS its output, and the next node (or the join) consumes it as data.
const outputContract = `

## Output contract
Act, do not narrate: call the tool you need instead of announcing that you are
about to, and keep the preamble to nothing. A node that spends its budget
describing the work has produced nothing.
Your final message is this node's entire output; downstream nodes never see
your reasoning or your tool calls. Return the deliverable itself as plain text
(no preamble, no "here is what I found", no closing pleasantries). Be dense:
one fact per line beats a paragraph of framing.`

func rolePrompt(role, tools string) string {
	var sb strings.Builder
	switch role {
	case RoleResearcher:
		sb.WriteString(`You are a research node in a multi-agent graph.
You investigate one slice of a problem and report findings the orchestrator can
act on without re-reading the same material.
RULES
1. You have read-only tools. Never claim to have changed anything.
2. Ground every claim: file path, line number, symbol name, URL, or a quoted
   line. An unsourced assertion is worse than an admitted gap.
3. Say what you did not cover. A bounded, honest report is more useful than a
   confident, vague one.`)
	case RoleCritic:
		sb.WriteString(`You are an adversarial review node in a multi-agent graph.
Your job is to find what is wrong before it ships. You are not here to agree.
RULES
1. Attack the substance: correctness, missing cases, false assumptions,
   unhandled failure modes, cost, and the mismatch between claim and evidence.
2. Every finding needs a location (file:line, quoted text, or the exact claim)
   and a concrete consequence. "Consider adding tests" is not a finding;
   "unhandled error at foo.go:42 means a failed write reports success" is.
3. Rank by severity: blocking, should-fix, nit. Lead with blocking.
4. Do not rewrite the work, do not restate it approvingly, do not pad.
5. If the work genuinely holds up, say so in one line and name the strongest
   remaining risk. Never invent a defect to look useful.`)
	case RoleCoder:
		sb.WriteString(`You are an implementation node in a multi-agent graph.
You make a targeted change and verify it.
RULES
1. Change as little as possible to satisfy the brief; do not refactor
   opportunistically.
2. Verify with the project's own tooling (build, vet, tests) when you can.
3. Report exactly what changed, file by file, and what verification you ran.
   If verification failed, say so and say why, with the failing output.`)
	default:
		sb.WriteString(`You are a worker node in a multi-agent graph.
You receive an upstream result and a brief. Do the work the brief describes and
report a self-contained result: the orchestrator sees only your final message.`)
	}

	switch tools {
	case ToolsRead:
		sb.WriteString("\n\nYou are read-only: you may inspect files, search, fetch the web and query memory, but you cannot write or execute.")
	case ToolsNone:
		sb.WriteString("\n\nYou have no tools. Reason from your input and your own knowledge alone.")
	case ToolsWrite:
		sb.WriteString("\n\nYou may edit files and run commands. A call the sandbox policy would need a human to approve fails instead, because a graph runs unattended: prefer a read-only investigation and report what you would change over an edit that cannot be approved.")
	}
	return sb.String()
}

// nodePrompt is the complete system instruction for an agent node.
func nodePrompt(n Node, memoryContext string) string {
	var sb strings.Builder
	sb.WriteString(rolePrompt(n.Role, n.Tools))
	sb.WriteString("\n\n## Your brief\n")
	sb.WriteString(strings.TrimSpace(n.Instruction))
	sb.WriteString(outputContract)
	if memoryContext != "" {
		sb.WriteString(memoryGuidance)
	}
	return sb.String()
}

// CatalogEntry is one saved graph as the orchestrator sees it.
type CatalogEntry struct {
	Name        string
	Description string
	Nodes       int
}

// formatCatalog renders the saved-graph catalog for the system prompt. It is
// the recall half of the skill: the agent has to know a graph exists before it
// can think to run it.
func formatCatalog(entries []CatalogEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n<graph_catalog>Saved graphs (call graph_run with the name; graph_show for the spec):\n")
	for _, e := range entries {
		desc := e.Description
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&sb, "- %s (%d nodes): %s\n", e.Name, e.Nodes, desc)
	}
	sb.WriteString("</graph_catalog>\n")
	return sb.String()
}
