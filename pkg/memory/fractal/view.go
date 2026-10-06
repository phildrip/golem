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

// DefaultViewBudgetBytes is the budget for the fractal summary view (approx 64k tokens).
const DefaultViewBudgetBytes = 128_000

// DefaultLiveHeadCount is the number of recent raw events retained verbatim in the live head.
const DefaultLiveHeadCount = 10

// ViewConfig configures view budget and live head retention.
type ViewConfig struct {
	ViewBudgetBytes int
	LiveHeadCount   int
}

// DefaultViewConfig returns the default view configuration.
func DefaultViewConfig() ViewConfig {
	return ViewConfig{
		ViewBudgetBytes: DefaultViewBudgetBytes,
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
	mu     sync.RWMutex
	cfg    ViewConfig
	store  *Store
	parts  []ViewPart
	tailTo int // number of events covered by the tail
}

// NewView constructs a View over the given Store.
func NewView(store *Store, cfg ViewConfig) *View {
	if cfg.ViewBudgetBytes <= 0 {
		cfg.ViewBudgetBytes = DefaultViewBudgetBytes
	}
	if cfg.LiveHeadCount < 0 {
		cfg.LiveHeadCount = DefaultLiveHeadCount
	}

	v := &View{
		cfg:   cfg,
		store: store,
	}
	v.Rebuild()
	return v
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
		return
	}

	for i := v.tailTo; i < tailEnd; i++ {
		v.parts = append(v.parts, ViewPart{Level: 0, Index: i})
		v.tailTo = i + 1
		v.fitLocked()
	}
}

// fitLocked performs due-age coarsening until the view fits within ViewBudgetBytes.
func (v *View) fitLocked() {
	for {
		currentSize := v.calculateSizeBytes()
		if currentSize <= v.cfg.ViewBudgetBytes || len(v.parts) < 2 {
			break
		}

		bestIdx := -1
		var maxDue float64 = -1.0

		for idx := 0; idx < len(v.parts)-1; idx++ {
			a := v.parts[idx]
			b := v.parts[idx+1]

			// Adjacent parts must have same level, even index, and consecutive indices
			if a.Level == b.Level && (a.Index%2 == 0) && (b.Index == a.Index+1) {
				parentLevel := a.Level + 1
				parentIndex := a.Index / 2

				// Parent node must already be built in store
				if _, ok := v.store.GetNode(parentLevel, parentIndex); !ok {
					continue
				}

				start, _ := a.Range()
				weight := float64(int(1) << (a.Level + 2))
				due := float64(v.tailTo-start) / weight

				if due > maxDue {
					maxDue = due
					bestIdx = idx
				}
			}
		}

		if bestIdx == -1 {
			// No built parents available to coarsen; cannot shrink further right now
			break
		}

		// Replace the pair with parent
		a := v.parts[bestIdx]
		parent := ViewPart{Level: a.Level + 1, Index: a.Index / 2}
		v.parts = append(v.parts[:bestIdx], append([]ViewPart{parent}, v.parts[bestIdx+2:]...)...)
	}
}

func (v *View) calculateSizeBytes() int {
	size := 0
	for _, p := range v.parts {
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

	if len(v.parts) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<chat>\n")
	for _, p := range v.parts {
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

// LiveHeadEvents returns the recent raw events not covered by the fractal tail.
func (v *View) LiveHeadEvents() []Event {
	v.mu.RLock()
	defer v.mu.RUnlock()

	total := v.store.CountEvents()
	tailEnd := total - v.cfg.LiveHeadCount
	if tailEnd < 0 {
		tailEnd = 0
	}
	return v.store.EventsSlice(tailEnd, total)
}

// ZoomArgs defines arguments for the zoom tool.
type ZoomArgs struct {
	ID int `json:"id" jsonschema:"Starting message ID of the node to unpack"`
	N  int `json:"n" jsonschema:"Number of messages covered (power of 2). Pass n=1 for verbatim full message text."`
}

// ZoomResult defines output from zoom tool.
type ZoomResult struct {
	Result string `json:"result" jsonschema:"The unpacked summary lines or full message text"`
}

// NewZoomTool creates the native ADK zoom tool for inspecting memory nodes.
func NewZoomTool(store *Store) (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name:        "zoom",
		Description: "Opens line id+n of the memory view into the two constituent lines of n/2 under it. When n=1, returns the full verbatim message.",
	}, func(ctx agent.Context, args ZoomArgs) (ZoomResult, error) {
		if args.N < 1 || (args.N&(args.N-1)) != 0 {
			return ZoomResult{}, errors.Errorf("n must be a positive power of 2, got %d", args.N)
		}
		if args.ID < 0 || args.ID%args.N != 0 {
			return ZoomResult{}, errors.Errorf("id %d must be a multiple of n %d", args.ID, args.N)
		}

		if args.N == 1 {
			ev, ok := store.GetEvent(args.ID)
			if !ok {
				return ZoomResult{Result: fmt.Sprintf("No message at id %d", args.ID)}, nil
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
	})
	if err != nil {
		return nil, errors.Wrap(err, "creating zoom tool")
	}
	return t, nil
}

// DateArgs defines input for the date lookup tool.
type DateArgs struct {
	ID int `json:"id" jsonschema:"The message ID to inspect date and time for"`
}

// DateResult defines output from date lookup.
type DateResult struct {
	Timestamp string `json:"timestamp" jsonschema:"ISO8601 formatted timestamp of the message"`
}

// NewDateTool creates the native ADK date tool.
func NewDateTool(store *Store) (tool.Tool, error) {
	t, err := functiontool.New(functiontool.Config{
		Name:        "date",
		Description: "Returns the recorded date and timestamp for a specific message ID.",
	}, func(ctx agent.Context, args DateArgs) (DateResult, error) {
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
