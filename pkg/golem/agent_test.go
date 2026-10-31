package golem

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/graph"
	"github.com/user/golem/pkg/memory/fractal"
	"github.com/user/golem/pkg/swarm"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ModelName != DefaultModelName {
		t.Errorf("model = %q, want %q", cfg.ModelName, DefaultModelName)
	}
	if cfg.LiveHeadCount != fractal.DefaultLiveHeadCount {
		t.Errorf("liveHeadCount = %d, want %d", cfg.LiveHeadCount, fractal.DefaultLiveHeadCount)
	}
	if cfg.ViewBudgetBytes != fractal.DefaultViewBudgetBytes {
		t.Errorf("viewBudget = %d, want %d", cfg.ViewBudgetBytes, fractal.DefaultViewBudgetBytes)
	}
	if cfg.CompactorGracePeriod != DefaultCompactorGracePeriod {
		t.Errorf("compactorGracePeriod = %v, want %v", cfg.CompactorGracePeriod, DefaultCompactorGracePeriod)
	}
}

func TestFractalInstructionInjection(t *testing.T) {
	tempDir := t.TempDir()
	store, err := fractal.OpenStore(tempDir)
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// Seed some history
	for i := 0; i < 5; i++ {
		_, _ = store.AppendEvent(fractal.KindUser, "user instruction")
		_ = store.SaveNode(fractal.Node{Level: 0, Index: i, Text: "user: instruction"})
	}

	view := fractal.NewView(store, fractal.ViewConfig{
		ViewBudgetBytes: 1024,
		LiveHeadCount:   2,
	})

	rendered := view.Render()
	if !strings.Contains(rendered, "<chat>") {
		t.Errorf("expected <chat> in rendered view, got %q", rendered)
	}
	if !strings.Contains(rendered, "0+1|user: instruction") {
		t.Errorf("expected level 0 node in view, got %q", rendered)
	}

	_ = context.Background()
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "503 high demand",
			err:  errors.New("Error 503, Message: This model is currently experiencing high demand. Spikes in demand are usually temporary. Please try again later., Status: UNAVAILABLE"),
			want: true,
		},
		{
			name: "429 rate limit",
			err:  errors.New("Error 429: RESOURCE_EXHAUSTED"),
			want: true,
		},
		{
			name: "timeout",
			err:  errors.New("context deadline exceeded (Client.Timeout exceeded)"),
			want: true,
		},
		{
			name: "408 request timeout",
			err:  errors.New("api error 408 from https://example.test/v1/chat/completions"),
			want: true,
		},
		{
			name: "invalid argument (not retryable)",
			err:  errors.New("Error 400: INVALID_ARGUMENT"),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientError(tc.err)
			if got != tc.want {
				t.Errorf("isTransientError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestAgentLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	cfg := Config{
		ModelName:       "gemini-flash-latest",
		APIKey:          "fake-api-key-for-testing",
		StorageDir:      tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	ctx := context.Background()
	agent, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New agent failed: %v", err)
	}
	defer agent.Close()

	if agent.Store() == nil {
		t.Errorf("expected Store() to be non-nil")
	}
	if agent.View() == nil {
		t.Errorf("expected View() to be non-nil")
	}
	if agent.Compactor() == nil {
		t.Errorf("expected Compactor() to be non-nil")
	}

	// Verify appending event and syncing view
	if _, err := agent.Store().AppendEvent(fractal.KindUser, "lifecycle test"); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	agent.View().Sync()

	liveEvents := agent.View().LiveHeadEvents()
	if len(liveEvents) != 1 || !strings.Contains(liveEvents[0].Text, "lifecycle test") {
		t.Errorf("expected live head to contain lifecycle test, got %v", liveEvents)
	}

	// Test Close
	if err := agent.Close(); err != nil {
		t.Errorf("agent.Close() failed: %v", err)
	}
}

func TestAgentMultiProviderInit(t *testing.T) {
	tempDir := t.TempDir()
	cfg := Config{
		Provider:        "opencode",
		BaseURL:         "https://api.opencode.ai/v1",
		ModelName:       "deepseek-chat",
		APIKey:          "opencode-test-key",
		StorageDir:      tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	ctx := context.Background()
	agent, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New agent with OpenCode provider failed: %v", err)
	}
	defer agent.Close()

	if agent.Store() == nil {
		t.Errorf("expected Store() to be non-nil")
	}
	if agent.View() == nil {
		t.Errorf("expected View() to be non-nil")
	}
	if agent.Compactor() == nil {
		t.Errorf("expected Compactor() to be non-nil")
	}

	// Verify appending event and syncing view
	if _, err := agent.Store().AppendEvent(fractal.KindUser, "opencode test"); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	agent.View().Sync()

	liveEvents := agent.View().LiveHeadEvents()
	if len(liveEvents) != 1 || !strings.Contains(liveEvents[0].Text, "opencode test") {
		t.Errorf("expected live head to contain opencode test, got %v", liveEvents)
	}
}

func TestAgentProfileAndMountedMemory(t *testing.T) {
	primaryDir := t.TempDir()
	workDir := t.TempDir()
	docsDir := t.TempDir()

	// Seed work store with knowledge (enough events beyond LiveHeadCount so tail folds into View)
	workStore, err := fractal.OpenStore(workDir)
	if err != nil {
		t.Fatalf("OpenStore work failed: %v", err)
	}
	for i := 0; i < 6; i++ {
		_, _ = workStore.AppendEvent(fractal.KindUser, fmt.Sprintf("work decision %d", i))
		_ = workStore.SaveNode(fractal.Node{Level: 0, Index: i, Text: fmt.Sprintf("work convention %d", i)})
	}
	workStore.Close()

	// Seed primary store with events so it has a folded tail
	primaryStore, err := fractal.OpenStore(primaryDir)
	if err != nil {
		t.Fatalf("OpenStore primary failed: %v", err)
	}
	for i := 0; i < 6; i++ {
		_, _ = primaryStore.AppendEvent(fractal.KindUser, fmt.Sprintf("primary task %d", i))
		_ = primaryStore.SaveNode(fractal.Node{Level: 0, Index: i, Text: fmt.Sprintf("primary note %d", i)})
	}
	primaryStore.Close()

	// Seed docs store
	docsStore, err := fractal.OpenStore(docsDir)
	if err != nil {
		t.Fatalf("OpenStore docs failed: %v", err)
	}
	for i := 0; i < 4; i++ {
		_, _ = docsStore.AppendEvent(fractal.KindUser, fmt.Sprintf("API doc %d", i))
		_ = docsStore.SaveNode(fractal.Node{Level: 0, Index: i, Text: fmt.Sprintf("reference %d", i)})
	}
	docsStore.Close()

	cfg := Config{
		Provider:   "opencode",
		BaseURL:    "https://api.opencode.ai/v1",
		ModelName:  "deepseek-chat",
		APIKey:     "opencode-test-key",
		StorageDir: primaryDir,
		Profile:    "work",
		MountedMemories: []config.MountedMemoryConfig{
			{Name: "shared-docs", Path: docsDir, ReadOnly: true},
		},
		LiveHeadCount:   2,
		ViewBudgetBytes: 2048,
	}

	ctx := context.Background()
	agent, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New agent failed: %v", err)
	}
	defer agent.Close()

	// Verify memory manager initialized and contains primary + mounted
	mgr := agent.MemoryManager()
	if mgr == nil {
		t.Fatalf("expected MemoryManager() to be non-nil")
	}

	// Mount work store explicitly into memory manager (simulating profile mount)
	workOpened, _ := fractal.OpenStore(workDir)
	defer workOpened.Close()
	mgr.Mount("work", workOpened, fractal.NewView(workOpened, fractal.ViewConfig{LiveHeadCount: 2, ViewBudgetBytes: 1024}), true)

	// Test Zoom across primary and mounted
	zoomWork, err := fractal.Zoom(mgr, fractal.ZoomArgs{
		Target: "work",
		ID:     0,
		N:      1,
	})
	if err != nil {
		t.Fatalf("Zoom work store failed: %v", err)
	}
	if !strings.Contains(zoomWork.Result, "work decision 0") {
		t.Errorf("expected zoomWork to contain 'work decision 0', got: %s", zoomWork.Result)
	}

	// Test Zoom on non-existent store
	zoomMissing, _ := fractal.Zoom(mgr, fractal.ZoomArgs{
		Target: "non-existent",
		ID:     0,
		N:      1,
	})
	if !strings.Contains(zoomMissing.Result, "not found") {
		t.Errorf("expected not found message, got: %s", zoomMissing.Result)
	}

	// Test Remember tool recording into profile memory
	remRes, err := fractal.Remember(mgr, "work", fractal.RememberArgs{
		Text:  "Always run tests with -race flag",
		Scope: "work",
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if !strings.Contains(remRes.Message, "Recorded to work memory") {
		t.Errorf("unexpected Remember response: %s", remRes.Message)
	}

	// Verify the remembered event exists in work store
	lastWorkEvent, has := workOpened.GetEvent(workOpened.CountEvents() - 1)
	if !has || !strings.Contains(lastWorkEvent.Text, "-race") {
		t.Errorf("expected remembered event in work store, got: %v", lastWorkEvent)
	}

	// Test RenderCombined includes mounted memories
	combined := mgr.RenderCombined()
	if !strings.Contains(combined, "<mounted_memory name=\"work\"") {
		t.Errorf("expected <mounted_memory name=\"work\"> in combined view, got:\n%s", combined)
	}
	if !strings.Contains(combined, "<chat>") {
		t.Errorf("expected primary <chat> in combined view, got:\n%s", combined)
	}

	// Test Agent.SwitchProfile hot switching
	prof, err := agent.SwitchProfile(ctx, "personal")
	if err != nil {
		t.Fatalf("SwitchProfile failed: %v", err)
	}
	if agent.Config().Profile != "personal" {
		t.Errorf("expected active profile 'personal', got: %s", agent.Config().Profile)
	}
	_ = prof
}

type testStreamHandler struct {
	onChunk func(token string)
}

func (h *testStreamHandler) OnChunk(token string) {
	if h.onChunk != nil {
		h.onChunk(token)
	}
}

func (h *testStreamHandler) OnToolStart(toolName, command string)             {}
func (h *testStreamHandler) OnToolDone(toolName, output string, exitCode int) {}

func TestAgent_ProcessStreamWithMockLLM(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`)
		flusher.Flush()

		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":" world"}}]}`)
		flusher.Flush()

		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer mockServer.Close()

	tempDir := t.TempDir()
	cfg := Config{
		Provider:        "opencode",
		BaseURL:         mockServer.URL,
		ModelName:       "test-model",
		APIKey:          "test-key",
		StorageDir:      tempDir,
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	ctx := context.Background()
	agent, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New agent failed: %v", err)
	}
	defer agent.Close()

	var chunks []string
	handler := &testStreamHandler{
		onChunk: func(token string) {
			chunks = append(chunks, token)
		},
	}

	resp, err := agent.ProcessStream(ctx, "hi", handler)
	if err != nil {
		t.Fatalf("ProcessStream failed: %v", err)
	}

	if resp != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", resp)
	}
	if len(chunks) == 0 {
		t.Errorf("expected streamed chunks, got none")
	}
}

// TestBuildInstructionIncludesLiveHead guards the fix for the main agent
// forgetting recent context: the newest raw events must appear in the prompt,
// which previously rendered only the summary view (excluding the live head).
func TestBuildInstructionIncludesLiveHead(t *testing.T) {
	store, err := fractal.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	view := fractal.NewView(store, fractal.ViewConfig{LiveHeadCount: 10})
	mgr := fractal.NewMemoryManager(store, view)

	// A short prior exchange: fewer events than the live-head window, so the
	// summary view alone would carry nothing.
	if _, err := store.AppendEvent(fractal.KindUser, "review the swarm changes in pwd"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(fractal.KindTalk, "starting the review now"); err != nil {
		t.Fatal(err)
	}
	view.Sync()

	out := buildInstruction(mgr, view, nil, -1, "")
	if !strings.Contains(out, "<recent_live_head>") {
		t.Errorf("instruction missing <recent_live_head> block:\n%s", out)
	}
	for _, want := range []string{"review the swarm changes in pwd", "starting the review now"} {
		if !strings.Contains(out, want) {
			t.Errorf("instruction missing prior event %q from live head", want)
		}
	}

	// The in-flight user message is the turn content, so passing its event ID as
	// the exclusion must drop it from the live head; without the exclusion it is
	// present, proving the filter (not a position heuristic) does the work.
	curEv, err := store.AppendEvent(fractal.KindUser, "CURRENT-TURN-MARKER")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(fractal.KindTool, `shell: {"command":"true"}`); err != nil {
		t.Fatal(err)
	}
	view.Sync()

	if withCur := buildInstruction(mgr, view, nil, -1, ""); !strings.Contains(withCur, "CURRENT-TURN-MARKER") {
		t.Errorf("expected the current user event in the live head when not excluded")
	}
	out2 := buildInstruction(mgr, view, nil, curEv.ID, "")
	if strings.Contains(out2, "CURRENT-TURN-MARKER") {
		t.Errorf("in-flight user message was duplicated into the live head")
	}
	if strings.Contains(out2, "command") {
		t.Errorf("current-turn tool event leaked into the live head")
	}
	if !strings.Contains(out2, "review the swarm changes in pwd") {
		t.Errorf("prior turn dropped after appending the in-flight message")
	}
}

// TestFormatLiveHeadBoundsAndKeepsNewest verifies the live head is bounded and
// retains the most recent events when the cap is exceeded.
func TestFormatLiveHeadBoundsAndKeepsNewest(t *testing.T) {
	var events []fractal.Event
	for i := 0; i < 20; i++ {
		events = append(events, fractal.Event{ID: i, Kind: fractal.KindEcho, Text: strings.Repeat("x", 1000)})
	}

	out := formatLiveHead(events, -1)
	if len(out) > liveHeadMaxTotalBytes+256 {
		t.Errorf("live head size = %d, want roughly <= %d", len(out), liveHeadMaxTotalBytes)
	}
	if !strings.Contains(out, "#19 [") {
		t.Errorf("live head dropped the newest event (#19)")
	}
	if strings.Contains(out, "#0 [") {
		t.Errorf("live head kept the oldest event (#0) instead of trimming from the front")
	}

	// An excluded event ID is omitted.
	filtered := formatLiveHead(events, 19)
	if strings.Contains(filtered, "#19 [") {
		t.Errorf("excluded event #19 still present in live head")
	}
	if !strings.Contains(filtered, "#18 [") {
		t.Errorf("exclusion removed neighbours of the excluded event")
	}
}

func TestShouldRestartTurn(t *testing.T) {
	transient := errors.New("Error 503: high demand")
	permanent := errors.New("Error 400: INVALID_ARGUMENT")
	cases := []struct {
		name       string
		err        error
		attempt    int
		maxRetries int
		toolCalls  int
		streamed   int
		want       bool
	}{
		{"transient before progress", transient, 1, 3, 0, 0, true},
		{"transient after progress", transient, 1, 3, 1, 0, false},
		{"transient after streamed text", transient, 1, 3, 0, 12, false},
		{"permanent", permanent, 1, 3, 0, 0, false},
		{"no attempts left", transient, 3, 3, 0, 0, false},
	}
	for _, tc := range cases {
		if got := shouldRestartTurn(tc.err, tc.attempt, tc.maxRetries, tc.toolCalls, tc.streamed); got != tc.want {
			t.Errorf("%s: shouldRestartTurn = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTurnStateGate(t *testing.T) {
	a := &Agent{}
	if a.isTurnActive() {
		t.Fatal("turn should start inactive")
	}
	a.beginTurn(5)
	if !a.isTurnActive() {
		t.Errorf("turn should be active after beginTurn")
	}
	if a.currentTurnStartEventID() != 5 {
		t.Errorf("start id = %d, want 5", a.currentTurnStartEventID())
	}
	a.noteToolCall()
	a.noteToolCall()
	if a.turnToolCallCount() != 2 {
		t.Errorf("tool calls = %d, want 2", a.turnToolCallCount())
	}
	a.endTurn()
	if a.isTurnActive() {
		t.Errorf("turn should be inactive after endTurn")
	}
	if a.currentTurnStartEventID() != -1 || a.turnToolCallCount() != 0 {
		t.Errorf("endTurn did not reset state: id=%d calls=%d", a.currentTurnStartEventID(), a.turnToolCallCount())
	}
}

// TestProcessStreamDoesNotRestartAfterProgress proves that a transient error
// after a tool has run surfaces instead of restarting the whole turn (which
// would re-execute side effects and redo all work).
func TestProcessStreamDoesNotRestartAfterProgress(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected http.Flusher")
		}

		if n == 1 {
			// One tool call: the runner will execute it, then call the model again.
			fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"true\"}"}}]}}]}`)
			fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"finish_reason":"tool_calls"}]}`)
			fmt.Fprintf(w, "data: [DONE]\n\n")
			fl.Flush()
			return
		}

		// A transient error delivered on a 200 stream, so the HTTP retry layer
		// cannot absorb it and the turn-level guard is what matters.
		fmt.Fprintf(w, "data: %s\n\n", `{"error":{"message":"Error 503: upstream unavailable","type":"server_error"}}`)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer mockServer.Close()

	cfg := Config{
		Provider:        "opencode",
		BaseURL:         mockServer.URL,
		ModelName:       "test-model",
		APIKey:          "test-key",
		StorageDir:      t.TempDir(),
		LiveHeadCount:   4,
		ViewBudgetBytes: 4096,
	}
	agent, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New agent failed: %v", err)
	}
	defer agent.Close()

	if _, err := agent.ProcessStream(context.Background(), "run true", nil); err == nil {
		t.Fatalf("expected the transient error to surface")
	}

	mu.Lock()
	got := requests
	mu.Unlock()
	if got != 2 {
		t.Errorf("model requests = %d, want 2 (tool call + failing call, no turn restart)", got)
	}

	toolRan := false
	for _, ev := range agent.Store().GetAllEvents() {
		if ev.Kind == fractal.KindTool {
			toolRan = true
		}
	}
	if !toolRan {
		t.Errorf("expected the shell tool to have executed once")
	}
}

func TestClipHeadTail(t *testing.T) {
	s := strings.Repeat("a", 100) + "MIDDLE" + strings.Repeat("z", 100)
	got := clipHeadTail(s, 50)
	if !strings.HasPrefix(got, "aaa") {
		t.Errorf("head not preserved: %q", got)
	}
	if !strings.HasSuffix(got, "zzz") {
		t.Errorf("tail not preserved: %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("missing truncation marker: %q", got)
	}
	if strings.Contains(got, "MIDDLE") {
		t.Errorf("middle should be elided: %q", got)
	}
	if short := clipHeadTail("ok", 50); short != "ok" {
		t.Errorf("short string changed: %q", short)
	}
}

// A cut at a raw byte offset can split a multi-byte rune, handing the model
// invalid UTF-8 (golem-01i.9).
func TestClipHeadTailIsRuneSafe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		unit  string
		limit int
	}{
		{name: "two-byte runes", unit: "é", limit: 101},
		{name: "three-byte runes", unit: "漢", limit: 100},
		{name: "four-byte runes", unit: "🚀", limit: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := strings.Repeat(tc.unit, 400)
			got := clipHeadTail(s, tc.limit)
			if !utf8.ValidString(got) {
				t.Fatalf("clipHeadTail produced invalid UTF-8 for %q", tc.name)
			}
			if len(got) > tc.limit {
				t.Errorf("len = %d, want <= %d (the marker counts against the limit)", len(got), tc.limit)
			}
			if !strings.Contains(got, "truncated") {
				t.Errorf("missing truncation marker: %q", got)
			}
		})
	}
}

// A limit smaller than the marker cannot hold one; clip instead of exploding.
func TestClipHeadTailBelowMarkerSize(t *testing.T) {
	s := strings.Repeat("é", 100)
	got := clipHeadTail(s, 8)
	if !utf8.ValidString(got) {
		t.Errorf("invalid UTF-8: %q", got)
	}
	if len(got) > 8 {
		t.Errorf("len = %d, want <= 8", len(got))
	}
}

func TestAgentSwitchModelKeepsProvider(t *testing.T) {
	cfg := Config{
		Provider:        "opencode",
		BaseURL:         "https://api.opencode.ai/v1",
		ModelName:       "deepseek-chat",
		APIKey:          "opencode-test-key",
		StorageDir:      t.TempDir(),
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}

	ctx := context.Background()
	agent, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New agent failed: %v", err)
	}
	defer agent.Close()

	updated, err := agent.SwitchModel(ctx, "qwen3-coder-plus")
	if err != nil {
		t.Fatalf("SwitchModel failed: %v", err)
	}
	if updated.ModelName != "qwen3-coder-plus" {
		t.Errorf("cfg.ModelName = %q, want qwen3-coder-plus", updated.ModelName)
	}
	if updated.Provider != "opencode" {
		t.Errorf("switching models must keep the provider, got %q", updated.Provider)
	}
	if got := agent.dynModel.Name(); got != "qwen3-coder-plus" {
		t.Errorf("active model is %q, want qwen3-coder-plus", got)
	}

	// An empty name must not silently wipe the configured model.
	if _, err := agent.SwitchModel(ctx, "  "); err == nil {
		t.Error("expected an error for an empty model name")
	}
	if got := agent.Config().ModelName; got != "qwen3-coder-plus" {
		t.Errorf("failed switch changed the model to %q", got)
	}
}

// The graph tools are part of the agent's tool surface, and the graph skill is
// in the static prompt: a tool the model is never told about is dead weight.
func TestAgentWiresGraphToolsAndSkill(t *testing.T) {
	cfg := Config{
		ModelName:       "gemini-flash-latest",
		APIKey:          "fake-api-key-for-testing",
		StorageDir:      t.TempDir(),
		LiveHeadCount:   4,
		ViewBudgetBytes: 1024,
	}
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	if a.Graphs() == nil {
		t.Fatal("expected a graph manager")
	}
	if !strings.HasSuffix(a.Graphs().Dir(), graph.GraphsDir) {
		t.Fatalf("graph dir %q is not the workspace graph directory", a.Graphs().Dir())
	}

	have := map[string]bool{}
	for _, name := range a.ToolNames() {
		have[name] = true
	}
	for _, want := range []string{"graph_list", "graph_show", "graph_validate", "graph_save", "graph_run", "graph_stat", "graph_resume", "graph_delete"} {
		if !have[want] {
			t.Errorf("tool %q missing from the tool surface (%v)", want, a.ToolNames())
		}
	}

	out := buildInstruction(nil, nil, nil, -1, a.Graphs().Catalog())
	for _, want := range []string{"graph_run", "adversarial review", "budgets"} {
		if !strings.Contains(out, want) {
			t.Errorf("system prompt lacks the graph skill (%q missing)", want)
		}
	}
}

// TestBuildInstructionPrefixCacheStability verifies that during an in-flight
// turn (excludeFrom >= 0), appending tool calls to the store does not mutate
// <memory_context> or <recent_live_head>, and that volatile <swarm_inbox> sits
// at the very end after the static and memory layers.
func TestBuildInstructionPrefixCacheStability(t *testing.T) {
	store, err := fractal.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}
	defer store.Close()

	// Seed 6 events and their level-0 summaries with LiveHeadCount = 3
	// so events 0..2 are in <chat> and 3..5 are in <recent_live_head>.
	for i := 0; i < 6; i++ {
		_, _ = store.AppendEvent(fractal.KindUser, fmt.Sprintf("prior msg %d", i))
		_ = store.SaveNode(fractal.Node{Level: 0, Index: i, Text: fmt.Sprintf("sum %d", i), Size: 8})
	}
	view := fractal.NewView(store, fractal.ViewConfig{LiveHeadCount: 3, ViewBudgetBytes: 4096})
	mgr := fractal.NewMemoryManager(store, view)
	bus := swarm.NewBus()
	defer bus.Close()
	bus.RegisterAgent(swarm.OrchestratorID, "s1")

	catalog := "\n<graph_catalog>test-graph</graph_catalog>\n"

	// Start a new turn at event 6.
	mgr.SyncAll()
	userEv, err := store.AppendEvent(fractal.KindUser, "turn prompt")
	if err != nil {
		t.Fatal(err)
	}

	step1 := buildInstruction(mgr, view, bus, userEv.ID, catalog)

	// Simulate 4 tool calls/outputs appended mid-turn.
	for i := 0; i < 4; i++ {
		_, _ = store.AppendEvent(fractal.KindTool, fmt.Sprintf("tool call %d", i))
		_, _ = store.AppendEvent(fractal.KindEcho, fmt.Sprintf("tool output %d", i))
	}

	step2 := buildInstruction(mgr, view, bus, userEv.ID, catalog)
	if step1 != step2 {
		t.Fatalf("mid-turn tool events mutated system instruction prefix:\n--- step1 ---\n%s\n--- step2 ---\n%s", step1, step2)
	}

	// Delivering a swarm message must only append to the tail, keeping step1 as an exact prefix.
	_, _ = bus.Send(swarm.Message{Sender: "worker-1", Recipient: swarm.OrchestratorID, Body: "done"})
	step3 := buildInstruction(mgr, view, bus, userEv.ID, catalog)
	if !strings.HasPrefix(step3, step1) {
		t.Errorf("swarm inbox notification invalidated prefix before the tail")
	}

	// Verify layer ordering: <graph_catalog> -> <chat> -> <recent_live_head> -> <swarm_inbox>
	catIdx := strings.Index(step3, "<graph_catalog>")
	memIdx := strings.LastIndex(step3, "<chat>")
	liveIdx := strings.Index(step3, "<recent_live_head>")
	inboxIdx := strings.Index(step3, "<swarm_inbox>")
	if !(catIdx > 0 && memIdx > catIdx && liveIdx > memIdx && inboxIdx > liveIdx) {
		t.Errorf("unexpected layer order: catalog=%d memory=%d live=%d inbox=%d", catIdx, memIdx, liveIdx, inboxIdx)
	}
}

type slowSummarizer struct {
	delay time.Duration
	calls int
}

func (s *slowSummarizer) Summarize(ctx context.Context, contextView, content string, isMerge bool) (string, error) {
	s.calls++
	select {
	case <-time.After(s.delay):
		return "summarized: done", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestAgentClose_CompactorGracePeriodCompletesCleanly(t *testing.T) {
	store, err := fractal.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	if _, err := store.AppendEvent(fractal.KindTalk, strings.Repeat("long text to compress ", 100)); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	view := fractal.NewView(store, fractal.ViewConfig{LiveHeadCount: 0})
	slowSub := &slowSummarizer{delay: 40 * time.Millisecond}
	compactor := fractal.NewCompactor(store, view, slowSub, fractal.DefaultCompactorConfig())

	workerCtx, workerCancel := context.WithCancel(context.Background())
	a := &Agent{
		store:            store,
		view:             view,
		compactor:        compactor,
		health:           newHealth(),
		compactorTrigger: make(chan struct{}, 1),
		workerCancel:     workerCancel,
		cfg: Config{
			CompactorGracePeriod: 500 * time.Millisecond,
		},
	}

	a.workerWg.Add(1)
	go func() {
		defer a.workerWg.Done()
		a.startCompactorWorker(workerCtx)
	}()

	// Trigger compaction and close immediately
	a.triggerCompactor()

	closeErr := a.Close()
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	// Verify idempotency
	if err := a.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}

	if slowSub.calls == 0 {
		t.Errorf("expected slowSummarizer to be called")
	}
	if a.Health().Total() != 0 {
		t.Errorf("expected 0 health problems, got %d: %s", a.Health().Total(), a.Health().Summary())
	}
}

func TestAgentClose_CompactorGracePeriodTimeoutCancelsCleanly(t *testing.T) {
	store, err := fractal.OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore failed: %v", err)
	}

	if _, err := store.AppendEvent(fractal.KindTalk, strings.Repeat("long text to compress ", 100)); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	view := fractal.NewView(store, fractal.ViewConfig{LiveHeadCount: 0})
	slowSub := &slowSummarizer{delay: 1 * time.Second}
	compactor := fractal.NewCompactor(store, view, slowSub, fractal.DefaultCompactorConfig())

	workerCtx, workerCancel := context.WithCancel(context.Background())
	a := &Agent{
		store:            store,
		view:             view,
		compactor:        compactor,
		health:           newHealth(),
		compactorTrigger: make(chan struct{}, 1),
		workerCancel:     workerCancel,
		cfg: Config{
			CompactorGracePeriod: 30 * time.Millisecond,
		},
	}

	a.workerWg.Add(1)
	go func() {
		defer a.workerWg.Done()
		a.startCompactorWorker(workerCtx)
	}()

	a.triggerCompactor()

	closeErr := a.Close()
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	// When grace period expires and worker context is canceled, no error should be logged or recorded in health
	if a.Health().Total() != 0 {
		t.Errorf("expected 0 health problems on shutdown cancellation, got %d: %s", a.Health().Total(), a.Health().Summary())
	}
}

