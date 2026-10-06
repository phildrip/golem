package fractal

import (
	"strings"
	"testing"
)

func TestMemoryManager_MountAndRenderCombined(t *testing.T) {
	tempPrimary := t.TempDir()
	tempMounted := t.TempDir()

	primaryStore, err := OpenStore(tempPrimary)
	if err != nil {
		t.Fatalf("OpenStore primary failed: %v", err)
	}
	defer primaryStore.Close()

	mountedStore, err := OpenStore(tempMounted)
	if err != nil {
		t.Fatalf("OpenStore mounted failed: %v", err)
	}
	defer mountedStore.Close()

	// Seed primary store
	_, _ = primaryStore.AppendEvent(KindUser, "hello in workspace")
	_ = primaryStore.SaveNode(Node{Level: 0, Index: 0, Text: "user: hello in workspace"})

	// Seed mounted store (e.g. work profile)
	_, _ = mountedStore.AppendEvent(KindWork, "company convention: use table-driven tests")
	_ = mountedStore.SaveNode(Node{Level: 0, Index: 0, Text: "work: company convention: use table-driven tests"})

	primaryView := NewView(primaryStore, ViewConfig{LiveHeadCount: 0})
	mountedView := NewView(mountedStore, ViewConfig{LiveHeadCount: 0})

	mgr := NewMemoryManager(primaryStore, primaryView)
	mgr.Mount("work", mountedStore, mountedView, true)

	mgr.SyncAll()
	combined := mgr.RenderCombined()

	// Verify both primary and mounted memory sections are present
	if !strings.Contains(combined, `<mounted_memory name="work" read_only=true>`) {
		t.Errorf("expected mounted_memory tag in combined view, got:\n%s", combined)
	}
	if !strings.Contains(combined, "company convention") {
		t.Errorf("expected mounted memory text, got:\n%s", combined)
	}
	if !strings.Contains(combined, "<chat>") || !strings.Contains(combined, "hello in workspace") {
		t.Errorf("expected primary chat memory in combined view, got:\n%s", combined)
	}
}

func TestMultiZoomAndRememberTools(t *testing.T) {
	tempPrimary := t.TempDir()
	tempMounted := t.TempDir()

	primaryStore, _ := OpenStore(tempPrimary)
	defer primaryStore.Close()

	mountedStore, _ := OpenStore(tempMounted)
	defer mountedStore.Close()

	_, _ = primaryStore.AppendEvent(KindUser, "primary message zero")
	_, _ = mountedStore.AppendEvent(KindWork, "mounted profile message zero")

	mgr := NewMemoryManager(primaryStore, NewView(primaryStore, DefaultViewConfig()))
	mgr.Mount("work", mountedStore, NewView(mountedStore, DefaultViewConfig()), true)

	// 1. Test MultiZoomTool inspecting primary
	zoomTool, err := NewMultiZoomTool(mgr)
	if err != nil {
		t.Fatalf("NewMultiZoomTool failed: %v", err)
	}
	if zoomTool.Name() != "zoom" {
		t.Errorf("expected zoomTool name 'zoom', got %q", zoomTool.Name())
	}

	primaryZoomCall, err := Zoom(mgr, ZoomArgs{ID: 0, N: 1})
	if err != nil {
		t.Fatalf("Zoom primary failed: %v", err)
	}
	if !strings.Contains(primaryZoomCall.Result, "primary message zero") {
		t.Errorf("expected primary verbatim text, got: %v", primaryZoomCall.Result)
	}

	// 2. Test MultiZoomTool inspecting mounted 'work' store
	mountedZoomCall, err := Zoom(mgr, ZoomArgs{Target: "work", ID: 0, N: 1})
	if err != nil {
		t.Fatalf("Zoom mounted failed: %v", err)
	}
	if !strings.Contains(mountedZoomCall.Result, "mounted profile message zero") {
		t.Errorf("expected mounted verbatim text, got: %v", mountedZoomCall.Result)
	}

	// 3. Test RememberTool recording to 'work' profile store
	rememberTool, err := NewRememberTool(mgr, "work")
	if err != nil {
		t.Fatalf("NewRememberTool failed: %v", err)
	}
	if rememberTool.Name() != "remember" {
		t.Errorf("expected rememberTool name 'remember', got %q", rememberTool.Name())
	}

	remCall, err := Remember(mgr, "work", RememberArgs{Text: "always run gofumpt", Scope: "work"})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if !strings.Contains(remCall.Message, "Recorded to work memory") {
		t.Errorf("expected confirmation message, got: %v", remCall.Message)
	}

	// Verify the event was physically written to mountedStore
	if mountedStore.CountEvents() != 2 {
		t.Errorf("expected 2 events in mountedStore, got %d", mountedStore.CountEvents())
	}
	ev, ok := mountedStore.GetEvent(1)
	if !ok || !strings.Contains(ev.Text, "always run gofumpt") {
		t.Errorf("expected remembered event in mountedStore, got: %+v", ev)
	}
}
