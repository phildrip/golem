package golem

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/memory/fractal"
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

	out := buildInstruction(mgr, view, nil, -1)
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
	view.Sync()

	if withCur := buildInstruction(mgr, view, nil, -1); !strings.Contains(withCur, "CURRENT-TURN-MARKER") {
		t.Errorf("expected the current user event in the live head when not excluded")
	}
	out2 := buildInstruction(mgr, view, nil, curEv.ID)
	if strings.Contains(out2, "CURRENT-TURN-MARKER") {
		t.Errorf("in-flight user message was duplicated into the live head")
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
