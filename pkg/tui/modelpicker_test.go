package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/user/golem/pkg/config"
	"github.com/user/golem/pkg/golem"
)

func sampleModelCatalog() golem.ModelCatalog {
	return golem.ModelCatalog{
		Provider: "opencode",
		Models:   []string{"deepseek-v4.1-flash", "kimi-k3", "glm-5.3-flash"},
		Current:  "kimi-k3",
		Live:     true,
	}
}

// pickerModel returns a sized model with the picker open on the sample catalog.
func pickerModel(t *testing.T) Model {
	t.Helper()
	m := newSlashTestModel(t, 100, 30)
	newM, _ := m.Update(modelListMsg{catalog: sampleModelCatalog()})
	return newM.(Model)
}

func TestModelPickerOpensOnProviderCatalogue(t *testing.T) {
	m := pickerModel(t)

	if !m.showModelModal {
		t.Fatal("expected the model picker to open after a catalogue arrives")
	}
	if got, want := m.modelSelection, 1; got != want {
		t.Fatalf("selection = %d, want the current model at %d", got, want)
	}

	view := m.View()
	for _, want := range []string{
		"GOLEM MODEL PICKER",
		"opencode",
		"live from provider",
		"▸ kimi-k3",
		"[current]",
		"Enter: Switch",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("picker view missing %q:\n%s", want, view)
		}
	}
}

func TestModelPickerNavigationWraps(t *testing.T) {
	m := pickerModel(t)

	steps := []struct {
		key  tea.KeyType
		want int
	}{
		{tea.KeyDown, 2},
		{tea.KeyDown, 0}, // wraps past the last entry
		{tea.KeyUp, 2},   // wraps behind the first entry
		{tea.KeyUp, 1},
	}
	for _, step := range steps {
		newM, _ := m.Update(tea.KeyMsg{Type: step.key})
		m = newM.(Model)
		if m.modelSelection != step.want {
			t.Fatalf("after %v selection = %d, want %d", step.key, m.modelSelection, step.want)
		}
	}

	if m.textarea.Value() != "" {
		t.Errorf("picker keys must not reach the prompt, textarea = %q", m.textarea.Value())
	}

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = newM.(Model)
	if m.modelSelection != 2 {
		t.Fatalf("End should select the last model, got %d", m.modelSelection)
	}
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = newM.(Model)
	if m.modelSelection != 0 {
		t.Fatalf("Home should select the first model, got %d", m.modelSelection)
	}
}

func TestModelPickerEnterSwitchesAndReportsResult(t *testing.T) {
	m := pickerModel(t)

	// The picker opens on the current model, so step onto a different one first.
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newM.(Model)

	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)
	if m.showModelModal {
		t.Fatal("Enter should close the picker")
	}
	if cmd == nil {
		t.Fatal("Enter on a different model should start a switch")
	}
	if !strings.Contains(m.statusMessage, "Switching model to glm-5.3-flash") {
		t.Errorf("unexpected status while switching: %q", m.statusMessage)
	}

	// The switch runs as a tea.Cmd; without an agent it fails, and the failure
	// must surface as a chat entry rather than a silent no-op.
	msg, ok := cmd().(modelSwitchedMsg)
	if !ok {
		t.Fatalf("switch command returned %T, want modelSwitchedMsg", cmd())
	}
	if msg.err == nil {
		t.Fatal("expected the switch to fail without an agent")
	}
	newM, _ = m.Update(msg)
	m = newM.(Model)
	last := m.entries[len(m.entries)-1]
	if !strings.Contains(last.Content, "Model switch failed") {
		t.Fatalf("expected a failure entry, got %q", last.Content)
	}
	if !strings.Contains(m.statusMessage, "Model switch failed") {
		t.Errorf("expected the status line to report the failure, got %q", m.statusMessage)
	}
}

func TestModelPickerEnterOnCurrentModelCloses(t *testing.T) {
	m := pickerModel(t)

	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)
	if cmd != nil {
		t.Error("re-selecting the current model should not rebuild the client")
	}
	if m.showModelModal {
		t.Fatal("picker should close")
	}
	if !strings.Contains(m.statusMessage, "Already using kimi-k3") {
		t.Errorf("unexpected status: %q", m.statusMessage)
	}
}

func TestModelPickerEscClosesAndRefocusesPrompt(t *testing.T) {
	m := pickerModel(t)

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = newM.(Model)
	if m.showModelModal {
		t.Fatal("Esc should close the picker")
	}
	if got := m.View(); strings.Contains(got, "GOLEM MODEL PICKER") {
		t.Errorf("picker still rendered after Esc:\n%s", got)
	}
	if !m.textarea.Focused() {
		t.Error("prompt should regain focus after closing the picker")
	}
}

func TestModelPickerKeepsSelectionVisibleAndFitsPane(t *testing.T) {
	m := newSlashTestModel(t, 80, 20)

	catalog := golem.ModelCatalog{Provider: "opencode", Current: "model-1"}
	for i := 1; i <= 40; i++ {
		catalog.Models = append(catalog.Models, fmt.Sprintf("model-%d", i))
	}
	newM, _ := m.Update(modelListMsg{catalog: catalog})
	m = newM.(Model)

	if lines := strings.Count(m.View(), "\n") + 1; lines > m.height {
		t.Fatalf("picker frame is %d lines tall, pane is %d", lines, m.height)
	}
	if !strings.Contains(m.View(), "▸ model-1") {
		t.Fatal("expected the current model to be visible when the picker opens")
	}

	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = newM.(Model)
	view := m.View()
	if !strings.Contains(view, "▸ model-40") {
		t.Fatalf("End should scroll the highlighted last model into view:\n%s", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines > m.height {
		t.Fatalf("picker frame grew to %d lines after scrolling, pane is %d", lines, m.height)
	}
}

func TestModelPickerShowsOfflineNote(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	catalog := golem.ModelCatalog{
		Provider: "openai",
		Models:   []string{"gpt-4o"},
		Current:  "gpt-4o",
		Note:     "Could not reach openai (boom) — showing the bundled list.",
	}
	newM, _ := m.Update(modelListMsg{catalog: catalog})
	m = newM.(Model)

	view := m.View()
	if !strings.Contains(view, "bundled list (offline hint)") {
		t.Error("expected the source line to say the list is bundled")
	}
	if !strings.Contains(view, "Could not reach openai") {
		t.Error("expected the offline note to be shown")
	}
}

func TestLoadModelCatalogCmdWithoutAgent(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	msg, ok := m.loadModelCatalogCmd()().(modelListMsg)
	if !ok {
		t.Fatal("expected a modelListMsg")
	}
	if msg.catalog.Current != golem.DefaultModelName {
		t.Errorf("current = %q, want %q", msg.catalog.Current, golem.DefaultModelName)
	}
	if msg.catalog.Note == "" {
		t.Error("expected a note explaining the missing agent")
	}
}

func TestModelPickerSurvivesResize(t *testing.T) {
	m := newSlashTestModel(t, 100, 30)

	catalog := golem.ModelCatalog{Provider: "opencode", Current: "model-1"}
	for i := 1; i <= 40; i++ {
		catalog.Models = append(catalog.Models, fmt.Sprintf("model-%d", i))
	}
	newM, _ := m.Update(modelListMsg{catalog: catalog})
	m = newM.(Model)
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = newM.(Model)

	newM, _ = m.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	m = newM.(Model)

	view := m.View()
	if !strings.Contains(view, "▸ model-40") {
		t.Fatalf("selection scrolled out of view after a resize:\n%s", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines > m.height {
		t.Fatalf("picker frame is %d lines after resize, pane is %d", lines, m.height)
	}
}

// newCatalogueAgent builds a live Agent whose provider serves names from /models,
// so model switching can be exercised end-to-end without a real provider.
func newCatalogueAgent(t *testing.T, names []string, current string) *golem.Agent {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		data := make([]map[string]string, 0, len(names))
		for _, name := range names {
			data = append(data, map[string]string{"id": name})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)

	agent, err := golem.New(context.Background(), golem.Config{
		Provider:   config.ProviderOpenCode,
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		ModelName:  current,
		StorageDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("golem.New failed: %v", err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	return agent
}

// switchViaSlash drives "/model <arg>" through the returned command and the
// resulting message, returning the updated model.
func switchViaSlash(t *testing.T, m Model, input string) (Model, string) {
	t.Helper()
	newM, cmd := m.handleSlashCommand(input)
	m = newM.(Model)
	if cmd == nil {
		t.Fatalf("%q returned no command", input)
	}
	newM, _ = m.Update(cmd())
	m = newM.(Model)
	last := m.entries[len(m.entries)-1]
	return m, last.Content
}

func catalogueModel(t *testing.T) (Model, *golem.Agent) {
	t.Helper()
	agent := newCatalogueAgent(t,
		[]string{"deepseek-v4.1-flash", "deepseek-v4.1-pro", "kimi-k3"}, "kimi-k3")
	m := newSlashTestModel(t, 120, 40)
	m.agent = agent
	return m, agent
}

func TestModelCommandRejectsUnknownModelWithoutSwitching(t *testing.T) {
	m, agent := catalogueModel(t)

	got, content := switchViaSlash(t, m, "/model zzz-not-a-model")

	if !strings.Contains(content, "not served by opencode") {
		t.Fatalf("expected a rejection naming the provider, got %q", content)
	}
	if !strings.Contains(content, "/model") {
		t.Fatalf("expected the rejection to point at the list, got %q", content)
	}
	if name := agent.Config().ModelName; name != "kimi-k3" {
		t.Fatalf("model changed to %q despite failing validation", name)
	}
	if got.statusMessage != content {
		t.Fatalf("status %q does not match the transcript entry %q", got.statusMessage, content)
	}
}

func TestModelCommandSuggestsNamesForTypos(t *testing.T) {
	m, agent := catalogueModel(t)

	_, content := switchViaSlash(t, m, "/model kim-k3")

	if !strings.Contains(content, "Did you mean") || !strings.Contains(content, "kimi-k3") {
		t.Fatalf("expected kimi-k3 as a suggestion, got %q", content)
	}
	if name := agent.Config().ModelName; name != "kimi-k3" {
		t.Fatalf("model changed to %q despite a typo", name)
	}
}

func TestModelCommandRejectsAmbiguousPrefix(t *testing.T) {
	m, agent := catalogueModel(t)

	_, content := switchViaSlash(t, m, "/model deepseek")

	if !strings.Contains(content, "deepseek-v4.1-flash") || !strings.Contains(content, "deepseek-v4.1-pro") {
		t.Fatalf("expected both deepseek variants in the suggestions, got %q", content)
	}
	if name := agent.Config().ModelName; name != "kimi-k3" {
		t.Fatalf("ambiguous prefix switched to %q", name)
	}
}

func TestModelCommandSwitchesToValidatedName(t *testing.T) {
	m, agent := catalogueModel(t)

	got, content := switchViaSlash(t, m, "/model deepseek-v4.1-flash")

	if !strings.Contains(content, "Model switched to opencode:deepseek-v4.1-flash") {
		t.Fatalf("unexpected switch message %q", content)
	}
	if name := agent.Config().ModelName; name != "deepseek-v4.1-flash" {
		t.Fatalf("agent model = %q, want deepseek-v4.1-flash", name)
	}
	// The fetched catalogue is cached and kept current for the picker.
	if !got.modelCatalog.Live || got.modelCatalog.Current != "deepseek-v4.1-flash" {
		t.Fatalf("cached catalogue = %+v, want live with the new current model", got.modelCatalog)
	}
}

func TestModelCommandAcceptsCaseAndUniquePrefix(t *testing.T) {
	m, agent := catalogueModel(t)

	if _, content := switchViaSlash(t, m, "/model KIMI-K3"); !strings.Contains(content, "kimi-k3") {
		t.Fatalf("case-insensitive switch failed: %q", content)
	}
	if name := agent.Config().ModelName; name != "kimi-k3" {
		t.Fatalf("model = %q, want kimi-k3", name)
	}

	if _, content := switchViaSlash(t, m, "/model deepseek-v4.1-f"); !strings.Contains(content, "deepseek-v4.1-flash") {
		t.Fatalf("unique prefix switch failed: %q", content)
	}
	if name := agent.Config().ModelName; name != "deepseek-v4.1-flash" {
		t.Fatalf("model = %q, want deepseek-v4.1-flash", name)
	}
}

func TestModelCommandSwitchesUnverifiedWhenCatalogueOffline(t *testing.T) {
	// A provider that refuses to list models: the name cannot be checked, so
	// the switch must still happen but say so.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	agent, err := golem.New(context.Background(), golem.Config{
		Provider:   config.ProviderOpenCode,
		BaseURL:    srv.URL,
		APIKey:     "test-key",
		ModelName:  "kimi-k3",
		StorageDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("golem.New failed: %v", err)
	}
	defer agent.Close()

	m := newSlashTestModel(t, 120, 40)
	m.agent = agent

	_, content := switchViaSlash(t, m, "/model deepseek-v4.1-pro")
	if !strings.Contains(content, "not verified") {
		t.Fatalf("expected an unverified warning, got %q", content)
	}
	if name := agent.Config().ModelName; name != "deepseek-v4.1-pro" {
		t.Fatalf("model = %q, want the switch to proceed offline", name)
	}
}

func TestModelCommandWithoutArgumentShowsTheList(t *testing.T) {
	m, _ := catalogueModel(t)

	for _, input := range []string{"/model", "/model list", "/models"} {
		t.Run(input, func(t *testing.T) {
			newM, cmd := m.handleSlashCommand(input)
			m = newM.(Model)
			if cmd == nil {
				t.Fatalf("%q returned no command", input)
			}
			if !strings.Contains(m.statusMessage, "Loading models from opencode") {
				t.Fatalf("%q status = %q, want a loading hint", input, m.statusMessage)
			}

			newM, _ = m.Update(cmd())
			m = newM.(Model)
			if !m.showModelModal {
				t.Fatalf("%q did not open the model picker", input)
			}
			if len(m.modelCatalog.Models) != 3 || !m.modelCatalog.Live {
				t.Fatalf("%q catalogue = %+v, want the live list of 3", input, m.modelCatalog)
			}
			if m.modelCatalog.Current != "kimi-k3" {
				t.Fatalf("current = %q, want kimi-k3", m.modelCatalog.Current)
			}
			view := m.View()
			for _, want := range []string{"GOLEM MODEL PICKER", "deepseek-v4.1-flash", "kimi-k3", "[current]"} {
				if !strings.Contains(view, want) {
					t.Fatalf("%q picker is missing %q", input, want)
				}
			}
			m.closeModelPicker()
		})
	}
}

func TestModelPickerEnterAfterListSwitchKeepsProvider(t *testing.T) {
	m, agent := catalogueModel(t)

	newM, cmd := m.handleSlashCommand("/model")
	m = newM.(Model)
	newM, _ = m.Update(cmd())
	m = newM.(Model)

	// Move down to the first deepseek entry and confirm.
	newM, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = newM.(Model)
	if !m.showModelModal {
		t.Fatal("picker closed unexpectedly while navigating")
	}
	newM, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = newM.(Model)
	if cmd == nil {
		t.Fatal("Enter on the picker returned no switch command")
	}
	newM, _ = m.Update(cmd())
	m = newM.(Model)

	if m.showModelModal {
		t.Fatal("picker should close after switching")
	}
	cfg := agent.Config()
	if cfg.ModelName != "deepseek-v4.1-flash" || cfg.Provider != config.ProviderOpenCode {
		t.Fatalf("config = %s/%s, want opencode/deepseek-v4.1-flash", cfg.Provider, cfg.ModelName)
	}
	if m.modelCatalog.Current != "deepseek-v4.1-flash" {
		t.Fatalf("cached current = %q, want the switched model", m.modelCatalog.Current)
	}
}
