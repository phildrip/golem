package fractal

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// DefaultViewBudgetBytes is the high-water mark for the fractal summary view.
// Once the view exceeds it, merges run in one batch down to the low-water mark.
// The UniiChat thesis fixes the view at 64-128 KB (average 96 KB), batching
// 128 KB down to 64 KB.
const DefaultViewBudgetBytes = 128_000

// DefaultViewLowWaterBytes is the low-water mark the view is merged down to once
// it exceeds the high-water mark (thesis sawtooth: 128 KB -> 64 KB).
const DefaultViewLowWaterBytes = 64_000

// DefaultLiveHeadCount is the number of recent raw events retained verbatim in the live head.
const DefaultLiveHeadCount = 10

// ViewConfig configures view budget and live head retention.
type ViewConfig struct {
	// ViewBudgetBytes is the high-water mark: merges start once the view exceeds it.
	ViewBudgetBytes int
	// LowWaterBytes is the low-water mark a merge batch aims for. When zero it
	// defaults to half the high-water mark.
	LowWaterBytes int
	LiveHeadCount int
}

// DefaultViewConfig returns the default view configuration.
func DefaultViewConfig() ViewConfig {
	return ViewConfig{
		ViewBudgetBytes: DefaultViewBudgetBytes,
		LowWaterBytes:   DefaultViewLowWaterBytes,
		LiveHeadCount:   DefaultLiveHeadCount,
	}
}

// ViewPart represents one tiled slice in the fractal view.
type ViewPart struct {
	Level int
	Index int
}

// Range returns the event bounds [start, end) covered by this part.
func (p ViewPart) Range() (int, int) {
	count := 1 << p.Level
	start := p.Index * count
	return start, start + count
}

// Address returns the id+n address string.
func (p ViewPart) Address() string {
	start, _ := p.Range()
	return fmt.Sprintf("%d+%d", start, 1<<p.Level)
}

// View manages the active multi-resolution summary view tiling over older events.
type View struct {
	mu         sync.RWMutex
	cfg        ViewConfig
	store      *Store
	parts      []ViewPart
	tailTo     int  // number of events covered by the tail
	dirty      bool // tiling changed since it was last persisted
	coarsening bool // true when a sawtooth batch is in progress toward LowWaterBytes
}

// LiveHeadCount returns the configured live-head message count.
func (v *View) LiveHeadCount() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.cfg.LiveHeadCount
}

// NewView constructs a View over the given Store. It loads the persisted tiling
// when it is still consistent with the store, and falls back to Rebuild only
// when there is none (OptChat mistake #3: never rebuild the view on restart).
func NewView(store *Store, cfg ViewConfig) *View {
	if cfg.ViewBudgetBytes <= 0 {
		cfg.ViewBudgetBytes = DefaultViewBudgetBytes
	}
	if cfg.LowWaterBytes <= 0 || cfg.LowWaterBytes >= cfg.ViewBudgetBytes {
		cfg.LowWaterBytes = cfg.ViewBudgetBytes / 2
	}
	if cfg.LowWaterBytes < 1 {
		cfg.LowWaterBytes = 1
	}
	if cfg.LiveHeadCount < 0 {
		cfg.LiveHeadCount = DefaultLiveHeadCount
	}

	v := &View{
		cfg:   cfg,
		store: store,
	}
	if !v.loadStateLocked() {
		v.Rebuild()
	}
	return v
}

// validTiling reports whether parts is a gap-free, ordered tiling of [0, tailTo).
func validTiling(parts []ViewPart, tailTo int) bool {
	if tailTo < 0 {
		return false
	}
	if tailTo == 0 {
		return len(parts) == 0
	}
	pos := 0
	for _, p := range parts {
		if p.Level < 0 || p.Level > 62 || p.Index < 0 {
			return false
		}
		start, end := p.Range()
		if start != pos || end <= start {
			return false
		}
		pos = end
	}
	return pos == tailTo
}

// loadStateLocked restores a persisted tiling and catches it up to the current
// tail. It returns false only when the saved tiling is unusable (geometrically
// invalid, or extending past the tail because events were removed or the
// live-head configuration changed), in which case the caller rebuilds.
func (v *View) loadStateLocked() bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	st, ok := v.store.LoadViewState()
	if !ok {
		return false
	}

	total := v.store.CountEvents()
	expectedTail := total - v.cfg.LiveHeadCount
	if expectedTail < 0 {
		expectedTail = 0
	}
	if st.TailTo > expectedTail || !validTiling(st.Parts, st.TailTo) {
		return false
	}

	v.parts = st.Parts
	v.tailTo = st.TailTo
	v.dirty = false

	// Catch up events appended since the last save (e.g. a crash between the
	// event write and the view save) without rebuilding from message 0.
	if v.tailTo < expectedTail {
		for i := v.tailTo; i < expectedTail; i++ {
			v.parts = append(v.parts, ViewPart{Level: 0, Index: i})
			v.tailTo = i + 1
			v.fitLocked()
		}
		v.dirty = true
		v.saveLocked()
	}

	return true
}

// saveLocked persists the tiling when it changed. A failed write is non-fatal:
// the view stays usable in memory and the state is retried on the next save.
func (v *View) saveLocked() {
	if !v.dirty {
		return
	}
	if err := v.store.SaveViewState(ViewState{Parts: v.parts, TailTo: v.tailTo}); err != nil {
		return
	}
	v.dirty = false
}

// Rebuild folds the view from message 0 up to T_tail = totalEvents - LiveHeadCount.
func (v *View) Rebuild() {
	v.mu.Lock()
	defer v.mu.Unlock()

	total := v.store.CountEvents()
	tailEnd := total - v.cfg.LiveHeadCount
	if tailEnd < 0 {
		tailEnd = 0
	}

	v.parts = make([]ViewPart, 0, tailEnd)
	v.tailTo = 0

	for i := 0; i < tailEnd; i++ {
		v.parts = append(v.parts, ViewPart{Level: 0, Index: i})
		v.tailTo = i + 1
		v.fitLocked()
	}

	if tailEnd > 0 {
		v.dirty = true
		v.saveLocked()
	}
}

// Sync updates the view as new events are added to the store.
func (v *View) Sync() {
	v.mu.Lock()
	defer v.mu.Unlock()

	total := v.store.CountEvents()
	tailEnd := total - v.cfg.LiveHeadCount
	if tailEnd < 0 {
		tailEnd = 0
	}

	if tailEnd <= v.tailTo {
		v.fitLocked()
		v.saveLocked()
		return
	}

	for i := v.tailTo; i < tailEnd; i++ {
		v.parts = append(v.parts, ViewPart{Level: 0, Index: i})
		v.tailTo = i + 1
		v.fitLocked()
	}

	v.dirty = true
	v.saveLocked()
}

// pairDue returns the merge age of the sibling pair whose left half is part a,
// measured from the pair's LAST message (OptChat 3.2):
//
//	due = (T - last) / 2^level
//
// Measuring from the pair's FIRST message is wrong: near ties it merges old
// pairs and rewrites old lines that rollback-push would keep. a must be the
// even-indexed left sibling; the pair then spans [end, end+width).
func (v *View) pairDue(a ViewPart) float64 {
	width := int(1) << a.Level
	_, end := a.Range()
	last := end + width - 1
	return float64(v.tailTo-last) / float64(width)
}

// bestMergeIndex returns the index of the most-due mergeable sibling pair, or
// -1 if none is available. A pair is mergeable when its two halves are
// adjacent same-level siblings (even left index) whose parent node is built.
// On equal due, the oldest (lowest index) pair wins, matching rollback-push.
func (v *View) bestMergeIndex() int {
	return v.bestMergeIndexFor(v.parts)
}

func (v *View) bestMergeIndexFor(parts []ViewPart) int {
	bestIdx := -1
	var maxDue float64 = -1.0

	for idx := 0; idx < len(parts)-1; idx++ {
		a := parts[idx]
		b := parts[idx+1]

		// Adjacent parts must have same level, even index, and consecutive indices.
		if a.Level != b.Level || a.Index%2 != 0 || b.Index != a.Index+1 {
			continue
		}

		// Parent node must already be built in store.
		if _, ok := v.store.GetNode(a.Level+1, a.Index/2); !ok {
			continue
		}

		if due := v.pairDue(a); due > maxDue {
			maxDue = due
			bestIdx = idx
		}
	}

	return bestIdx
}

// fitLocked performs due-age coarsening once the view exceeds its high-water
// mark, merging in a single batch down toward the low-water mark (OptChat 3.2).
// Between batches the view only grows at its end; a small append below the
// high-water mark never triggers a merge, so old lines do not churn.
func (v *View) fitLocked() {
	if v.calculateSizeBytes() > v.cfg.ViewBudgetBytes {
		v.coarsening = true
	}
	if !v.coarsening {
		return
	}

	for {
		currentSize := v.calculateSizeBytes()
		if currentSize <= v.cfg.LowWaterBytes {
			v.coarsening = false
			break
		}
		if len(v.parts) < 2 {
			break
		}

		bestIdx := v.bestMergeIndex()
		if bestIdx == -1 {
			// No built parents available to coarsen; cannot shrink further right now.
			// Keep v.coarsening = true so Sync() finishes the batch once parents are built.
			break
		}

		// Replace the pair with parent
		a := v.parts[bestIdx]
		parent := ViewPart{Level: a.Level + 1, Index: a.Index / 2}
		v.parts = append(v.parts[:bestIdx], append([]ViewPart{parent}, v.parts[bestIdx+2:]...)...)
		v.dirty = true
	}
}

func (v *View) calculateSizeBytes() int {
	return v.sizeOf(v.parts)
}

// sizeOf sums the byte size of a tiling, using a placeholder estimate for
// unbuilt nodes.
func (v *View) sizeOf(parts []ViewPart) int {
	size := 0
	for _, p := range parts {
		if node, ok := v.store.GetNode(p.Level, p.Index); ok {
			size += node.Size
		} else {
			size += 64 // placeholder estimate
		}
	}
	return size
}

// Render returns the full <chat>...</chat> formatted view string.
func (v *View) Render() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.renderParts(v.parts)
}

// RenderCompacted renders the view coarsened further to fit targetBytes, using
// the same merge order but only pairs whose parent is already built. It is used
// as compaction context, which needs less detail than a turn (thesis section 4:
// the compaction view is 16-32 KB, merged with the same sawtooth).
func (v *View) RenderCompacted(targetBytes int) string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.renderCompactedLocked(targetBytes, -1)
}

// RenderCompactedEndingAt renders the view up to, but not including, event
// endExclusive, coarsened to fit targetBytes. A node's compaction view ends
// where that node starts (thesis section 4): the node's own text is the task,
// so later events are not evidence its summary may use, and the view is bounded
// by the node rather than by the whole log.
func (v *View) RenderCompactedEndingAt(targetBytes, endExclusive int) string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.renderCompactedLocked(targetBytes, endExclusive)
}

func (v *View) renderCompactedLocked(targetBytes, endExclusive int) string {
	parts := v.parts
	if endExclusive >= 0 {
		parts = v.partsEndingAt(parts, endExclusive)
	}

	if targetBytes <= 0 || len(parts) < 2 || v.sizeOf(parts) <= targetBytes {
		return v.renderParts(parts)
	}

	parts = append([]ViewPart(nil), parts...)
	// Only pairs whose parent is already built are merged, so every rendered
	// line is real text. When no built parent is left the view stays longer
	// than targetBytes instead of padding the prompt with placeholders: the
	// compaction context is a hint, and "(not summarized yet)" tells the model
	// nothing while still costing tokens.
	for v.sizeOf(parts) > targetBytes {
		idx := v.bestMergeIndexFor(parts)
		if idx == -1 {
			break
		}
		a := parts[idx]
		parent := ViewPart{Level: a.Level + 1, Index: a.Index / 2}
		parts = append(parts[:idx], append([]ViewPart{parent}, parts[idx+2:]...)...)
	}
	return v.renderParts(parts)
}

// partsEndingAt clips a tiling so it ends exactly at endExclusive, splitting the
// one part that straddles the boundary into its children (a built parent
// implies its children are built).
func (v *View) partsEndingAt(parts []ViewPart, endExclusive int) []ViewPart {
	out := make([]ViewPart, 0, len(parts))
	for _, p := range parts {
		start, end := p.Range()
		if end <= endExclusive {
			out = append(out, p)
			continue
		}
		if start >= endExclusive {
			// The tiling is ordered, so nothing after this part qualifies.
			break
		}
		out = append(out, v.splitToBoundary(p, endExclusive)...)
	}
	return out
}

// splitToBoundary descends through a part until it is cut at endExclusive.
func (v *View) splitToBoundary(p ViewPart, endExclusive int) []ViewPart {
	if p.Level == 0 {
		return nil
	}

	var out []ViewPart
	for _, child := range []ViewPart{
		{Level: p.Level - 1, Index: 2 * p.Index},
		{Level: p.Level - 1, Index: 2*p.Index + 1},
	} {
		start, end := child.Range()
		if end <= endExclusive {
			out = append(out, child)
			continue
		}
		if start >= endExclusive {
			break
		}
		out = append(out, v.splitToBoundary(child, endExclusive)...)
	}
	return out
}

// renderParts formats a tiling as the <chat>...</chat> view. Callers hold v.mu.
func (v *View) renderParts(parts []ViewPart) string {
	if len(parts) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<chat>\n")
	for _, p := range parts {
		addr := p.Address()
		if node, ok := v.store.GetNode(p.Level, p.Index); ok {
			cleanText := strings.ReplaceAll(node.Text, "\n", " ")
			sb.WriteString(fmt.Sprintf("%s|%s\n", addr, cleanText))
		} else {
			sb.WriteString(fmt.Sprintf("%s|(not summarized yet: zoom it)\n", addr))
		}
	}
	sb.WriteString("</chat>")
	return sb.String()
}

// TailTo returns the event index [0, TailTo) currently covered by the summary view.
func (v *View) TailTo() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.tailTo
}

// LiveHeadEvents returns the recent raw events not covered by the fractal tail.
// Starting at v.tailTo ensures there is never a gap between <chat> and
// <recent_live_head>, and keeps the live-head start boundary stable during an
// in-flight turn while v.Sync() is deferred.
func (v *View) LiveHeadEvents() []Event {
	v.mu.RLock()
	defer v.mu.RUnlock()

	total := v.store.CountEvents()
	tailEnd := total - v.cfg.LiveHeadCount
	if tailEnd < 0 {
		tailEnd = 0
	}
	if v.tailTo > 0 && v.tailTo < tailEnd {
		tailEnd = v.tailTo
	}
	return v.store.EventsSlice(tailEnd, total)
}

// ZoomArgs defines arguments for the zoom tool.
type ZoomArgs struct {
	Target string `json:"target,omitempty" jsonschema:"Optional mounted memory store name (e.g. 'work' or 'shared-docs'). Defaults to primary memory if omitted."`
	ID     int    `json:"id" jsonschema:"Starting message ID of the node to unpack"`
	N      int    `json:"n" jsonschema:"Number of messages covered (power of 2). Pass n=1 for verbatim full message text."`
}

// ZoomResult defines output from zoom tool.
type ZoomResult struct {
	Result string `json:"result" jsonschema:"The unpacked summary lines or full message text"`
}

// NewZoomTool creates the native ADK zoom tool for inspecting memory nodes in a single store.
func NewZoomTool(store *Store) (tool.Tool, error) {
	return NewMultiZoomTool(&SingleStoreRegistry{Store: store})
}

// Zoom executes the zoom logic against the primary or mounted memory store.
func Zoom(registry StoreRegistry, args ZoomArgs) (ZoomResult, error) {
	if args.N < 1 || (args.N&(args.N-1)) != 0 {
		return ZoomResult{}, errors.Errorf("n must be a positive power of 2, got %d", args.N)
	}
	if args.ID < 0 || args.ID%args.N != 0 {
		return ZoomResult{}, errors.Errorf("id %d must be a multiple of n %d", args.ID, args.N)
	}

	store, ok := registry.GetStore(args.Target)
	if !ok || store == nil {
		return ZoomResult{Result: fmt.Sprintf("Memory store %q not found", args.Target)}, nil
	}

	if args.N == 1 {
		ev, ok := store.GetEvent(args.ID)
		if !ok {
			return ZoomResult{Result: fmt.Sprintf("No message at id %d in %s", args.ID, args.Target)}, nil
		}
		return ZoomResult{Result: fmt.Sprintf("%d+1|%s", args.ID, ev.FormatLine())}, nil
	}

	// n > 1: get two children of size n/2
	childN := args.N / 2
	child0ID := args.ID
	child1ID := args.ID + childN

	child0, ok0 := store.GetNodeByAddress(child0ID, childN)
	child1, ok1 := store.GetNodeByAddress(child1ID, childN)

	var lines []string
	if ok0 {
		lines = append(lines, fmt.Sprintf("%d+%d|%s", child0ID, childN, child0.Text))
	} else {
		lines = append(lines, fmt.Sprintf("%d+%d|(not summarized yet)", child0ID, childN))
	}

	if ok1 {
		lines = append(lines, fmt.Sprintf("%d+%d|%s", child1ID, childN, child1.Text))
	} else {
		lines = append(lines, fmt.Sprintf("%d+%d|(not summarized yet)", child1ID, childN))
	}

	return ZoomResult{Result: strings.Join(lines, "\n")}, nil
}

// NewMultiZoomTool creates a zoom tool capable of inspecting primary and mounted memory stores.
func NewMultiZoomTool(registry StoreRegistry) (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name:        "zoom",
		Description: "Opens line id+n of the memory view into the two constituent lines of n/2 under it. When n=1, returns the full verbatim message. Optional target specifies mounted memory name.",
	}, func(ctx agent.Context, args ZoomArgs) (ZoomResult, error) {
		return Zoom(registry, args)
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating zoom tool")
	}
	return t, nil
}

// DateArgs defines input for the date lookup tool.
type DateArgs struct {
	Target string `json:"target,omitempty" jsonschema:"Optional mounted memory store name. Defaults to primary memory if omitted."`
	ID     int    `json:"id" jsonschema:"The message ID to inspect date and time for"`
}

// DateResult defines output from date lookup.
type DateResult struct {
	Timestamp string `json:"timestamp" jsonschema:"ISO8601 formatted timestamp of the message"`
}

// NewDateTool creates the native ADK date tool for a single store.
func NewDateTool(store *Store) (tool.Tool, error) {
	return NewMultiDateTool(&SingleStoreRegistry{Store: store})
}

// NewMultiDateTool creates a date tool supporting primary and mounted memory stores.
func NewMultiDateTool(registry StoreRegistry) (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name:        "date",
		Description: "Returns the recorded date and timestamp for a specific message ID in primary or mounted memory.",
	}, func(ctx agent.Context, args DateArgs) (DateResult, error) {
		store, ok := registry.GetStore(args.Target)
		if !ok || store == nil {
			return DateResult{Timestamp: fmt.Sprintf("Memory store %q not found", args.Target)}, nil
		}

		ev, ok := store.GetEvent(args.ID)
		if !ok {
			return DateResult{Timestamp: fmt.Sprintf("Message ID %d not found", args.ID)}, nil
		}
		return DateResult{Timestamp: ev.Timestamp.Format(time.RFC3339)}, nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating date tool")
	}
	return t, nil
}
