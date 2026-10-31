package fractal

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// SearchArgs defines the arguments for the memory_search tool.
type SearchArgs struct {
	Query        string `json:"query,omitempty" jsonschema:"Keywords, file paths, symbol names, or error phrases to rank via BM25 across fractal memory."`
	Target       string `json:"target,omitempty" jsonschema:"Optional mounted memory store name (e.g. 'work'). Defaults to primary memory; pass '*' or 'all' to search all mounted stores."`
	TurnID       string `json:"turn_id,omitempty" jsonschema:"Optional turn/transaction GUID to retrieve all events recorded in that turn."`
	SessionID    string `json:"session_id,omitempty" jsonschema:"Optional session GUID to filter events belonging to a specific session."`
	Since        string `json:"since,omitempty" jsonschema:"Optional lower-bound timestamp (RFC3339 or YYYY-MM-DD) or relative duration (e.g. '24h', '7d')."`
	Until        string `json:"until,omitempty" jsonschema:"Optional upper-bound timestamp (RFC3339 or YYYY-MM-DD)."`
	IncludeNodes bool   `json:"include_nodes,omitempty" jsonschema:"When true, also searches higher-level compacted summary nodes (id+n) in addition to raw events (id+1)."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum number of hits to return (default 15, max 50)."`
}

// SearchHit represents a single ranked match in fractal memory.
type SearchHit struct {
	Target    string  `json:"target,omitempty"`
	Address   string  `json:"address"`
	Kind      string  `json:"kind"`
	Timestamp string  `json:"timestamp,omitempty"`
	SessionID string  `json:"session_id,omitempty"`
	TurnID    string  `json:"turn_id,omitempty"`
	Score     float64 `json:"score"`
	Snippet   string  `json:"snippet"`
}

// SearchResult is the output of the memory_search tool.
type SearchResult struct {
	Matches []SearchHit `json:"matches,omitempty"`
	Summary string      `json:"summary"`
}

const (
	defaultSearchLimit = 15
	maxSearchLimit     = 50
	bm25K1             = 1.2
	bm25B              = 0.75
	snippetMaxBytes    = 320
)

// Search executes a multi-dimensional search (TurnID, SessionID, time range, and
// Okapi BM25 full-text ranking) over one or all fractal memory stores.
func Search(registry StoreRegistry, args SearchArgs) (SearchResult, error) {
	if registry == nil {
		return SearchResult{}, errors.New("memory registry is nil")
	}

	limit := args.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	sinceTime, err := parseSearchTime(args.Since, time.Now().UTC())
	if err != nil {
		return SearchResult{}, errors.Wrap(err, "invalid 'since' timestamp")
	}
	untilTime, err := parseSearchTime(args.Until, time.Now().UTC())
	if err != nil {
		return SearchResult{}, errors.Wrap(err, "invalid 'until' timestamp")
	}

	type namedStore struct {
		name  string
		store *Store
	}
	var targets []namedStore
	cleanTarget := strings.ToLower(strings.TrimSpace(args.Target))
	if cleanTarget == "*" || cleanTarget == "all" {
		if p := registry.PrimaryStore(); p != nil {
			targets = append(targets, namedStore{name: "primary", store: p})
		}
		for _, ms := range registry.AllMounted() {
			if ms != nil && ms.Store != nil {
				targets = append(targets, namedStore{name: ms.Name, store: ms.Store})
			}
		}
	} else {
		st, ok := registry.GetStore(args.Target)
		if !ok || st == nil {
			return SearchResult{Summary: fmt.Sprintf("Memory store %q not found", args.Target)}, nil
		}
		name := cleanTarget
		if name == "" {
			name = "primary"
		}
		targets = append(targets, namedStore{name: name, store: st})
	}

	queryTokens := tokenizeBM25(args.Query)
	turnFilter := strings.TrimSpace(args.TurnID)
	sessionFilter := strings.TrimSpace(args.SessionID)

	type candidateDoc struct {
		target    string
		address   string
		kind      string
		timestamp time.Time
		sessionID string
		turnID    string
		text      string
		tf        map[string]int
		docLen    int
		eventID   int
	}

	var docs []candidateDoc
	df := make(map[string]int, len(queryTokens))
	totalDocLen := 0

	addDoc := func(d candidateDoc) {
		if len(queryTokens) > 0 {
			tf := make(map[string]int)
			tokens := tokenizeBM25(d.text)
			d.docLen = len(tokens)
			matchedAny := false
			qSet := make(map[string]bool, len(queryTokens))
			for _, qt := range queryTokens {
				qSet[qt] = true
			}
			for _, tok := range tokens {
				if qSet[tok] {
					tf[tok]++
					matchedAny = true
				}
			}
			// Also allow substring match for exact file paths or symbols with punctuation
			rawLower := strings.ToLower(d.text)
			for _, qt := range queryTokens {
				if tf[qt] == 0 && strings.Contains(rawLower, qt) {
					tf[qt] = 1
					matchedAny = true
				}
			}
			if !matchedAny {
				return
			}
			for tok := range tf {
				df[tok]++
			}
			d.tf = tf
			if d.docLen == 0 {
				d.docLen = 1
			}
			totalDocLen += d.docLen
		}
		docs = append(docs, d)
	}

	for _, ns := range targets {
		var candidateEvents []Event
		switch {
		case turnFilter != "":
			candidateEvents = ns.store.GetEventsByTurn(turnFilter)
		case sessionFilter != "":
			candidateEvents = ns.store.GetEventsBySession(sessionFilter)
		case !sinceTime.IsZero() || !untilTime.IsZero():
			candidateEvents = ns.store.GetEventsByTimeRange(sinceTime, untilTime)
		default:
			candidateEvents = ns.store.GetAllEvents()
		}

		for _, ev := range candidateEvents {
			if turnFilter != "" && ev.TurnID != turnFilter {
				continue
			}
			if sessionFilter != "" && ev.SessionID != sessionFilter {
				continue
			}
			if !sinceTime.IsZero() && ev.Timestamp.Before(sinceTime) {
				continue
			}
			if !untilTime.IsZero() && ev.Timestamp.After(untilTime) {
				continue
			}
			addDoc(candidateDoc{
				target:    ns.name,
				address:   fmt.Sprintf("%d+1", ev.ID),
				kind:      string(ev.Kind),
				timestamp: ev.Timestamp,
				sessionID: ev.SessionID,
				turnID:    ev.TurnID,
				text:      ev.Text,
				eventID:   ev.ID,
			})
		}

		if args.IncludeNodes && turnFilter == "" && sessionFilter == "" && sinceTime.IsZero() && untilTime.IsZero() {
			for _, node := range ns.store.GetAllNodes() {
				if node.Level == 0 {
					continue // level 0 is already covered by raw events
				}
				start, _ := node.Range()
				addDoc(candidateDoc{
					target:  ns.name,
					address: node.Address(),
					kind:    fmt.Sprintf("node:L%d", node.Level),
					text:    node.Text,
					eventID: start,
				})
			}
		}
	}

	if len(docs) == 0 {
		return SearchResult{Summary: "No matching memory events found."}, nil
	}

	totalCorpus := float64(len(docs))
	for _, ns := range targets {
		if c := float64(ns.store.CountEvents()); c > totalCorpus {
			totalCorpus = c
		}
	}
	avgDocLen := 50.0
	if len(docs) > 0 && totalDocLen > 0 {
		avgDocLen = float64(totalDocLen) / float64(len(docs))
	}

	hits := make([]SearchHit, 0, len(docs))
	for _, d := range docs {
		score := 1.0
		if len(queryTokens) > 0 {
			score = 0
			for _, qt := range queryTokens {
				freq := float64(d.tf[qt])
				if freq == 0 {
					continue
				}
				docFreq := float64(df[qt])
				idf := math.Log(1.0 + (totalCorpus-docFreq+0.5)/(docFreq+0.5))
				norm := freq * (bm25K1 + 1.0) / (freq + bm25K1*(1.0-bm25B+bm25B*(float64(d.docLen)/avgDocLen)))
				score += idf * norm
			}
			// Exact phrase boost
			if len(queryTokens) > 1 && strings.Contains(strings.ToLower(d.text), strings.ToLower(strings.TrimSpace(args.Query))) {
				score *= 1.35
			}
		}

		snippet := strings.ReplaceAll(strings.TrimSpace(d.text), "\n", " ")
		if len(snippet) > snippetMaxBytes {
			snippet = TruncateUTF8(snippet, snippetMaxBytes) + "..."
		}
		var tsStr string
		if !d.timestamp.IsZero() {
			tsStr = d.timestamp.UTC().Format(time.RFC3339)
		}
		hits = append(hits, SearchHit{
			Target:    d.target,
			Address:   d.address,
			Kind:      d.kind,
			Timestamp: tsStr,
			SessionID: d.sessionID,
			TurnID:    d.turnID,
			Score:     math.Round(score*1000) / 1000,
			Snippet:   snippet,
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if len(queryTokens) > 0 && hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Timestamp > hits[j].Timestamp
	})

	if len(hits) > limit {
		hits = hits[:limit]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d memory hit(s) (use zoom(id, n) to inspect full context):\n", len(hits))
	for _, h := range hits {
		meta := h.Kind
		if h.Timestamp != "" {
			meta += " " + h.Timestamp
		}
		if h.TurnID != "" {
			meta += " turn=" + h.TurnID
		}
		if h.SessionID != "" {
			meta += " session=" + h.SessionID
		}
		prefix := h.Address
		if h.Target != "" && h.Target != "primary" {
			prefix = h.Target + ":" + h.Address
		}
		fmt.Fprintf(&sb, "- %s [%s] (score=%.2f): %s\n", prefix, meta, h.Score, h.Snippet)
	}

	return SearchResult{
		Matches: hits,
		Summary: strings.TrimSpace(sb.String()),
	}, nil
}

// NewMemorySearchTool creates the ADK memory_search tool for a StoreRegistry.
func NewMemorySearchTool(registry StoreRegistry) (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name:        "memory_search",
		Description: "Searches fractal memory across events (id+1) and summary nodes (id+n) using BM25 keyword ranking, turn_id/session_id GUID lookup, or timestamp range filtering (since/until). Returns exact id+n addresses for zoom.",
	}, func(ctx agent.Context, args SearchArgs) (SearchResult, error) {
		return Search(registry, args)
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating memory_search tool")
	}
	return t, nil
}

// tokenizeBM25 splits text into lower-cased alphanumeric and identifier tokens.
func tokenizeBM25(s string) []string {
	if s == "" {
		return nil
	}
	lower := strings.ToLower(s)
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' && r != '/'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, ".-_/")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseSearchTime parses an RFC3339 timestamp, a YYYY-MM-DD date, or a relative
// duration like "24h" / "7d" relative to now.
func parseSearchTime(raw string, now time.Time) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	if strings.HasSuffix(s, "d") {
		var days int
		if _, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &days); err == nil && days > 0 {
			return now.Add(-time.Duration(days) * 24 * time.Hour).UTC(), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d > 0 {
			d = -d
		}
		return now.Add(d).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time format %q (expected RFC3339, YYYY-MM-DD, or duration like 24h/7d)", raw)
}
