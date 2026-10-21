package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/user/golem/pkg/golem"
)

// modelListMsg carries a provider's model catalogue back to the UI. Fetching is
// done in a tea.Cmd so a slow provider never freezes the TUI.
type modelListMsg struct {
	catalog golem.ModelCatalog
}

// modelSwitchedMsg reports the outcome of a hot model switch. requested and
// suggestions are set when a typed name could not be resolved against the
// provider catalogue, so the UI can point at the names that do exist.
type modelSwitchedMsg struct {
	provider    string
	name        string
	requested   string
	suggestions []string
	unverified  bool
	catalog     golem.ModelCatalog
	haveCatalog bool
	err         error
}

// modelCatalogFreshFor is how long a fetched catalogue is trusted when
// validating a typed model name, so "/model deepseek-v4.1-flash" right after
// opening the picker costs no extra round-trip.
const modelCatalogFreshFor = 2 * time.Minute

// loadModelCatalogCmd asks the active provider which models it can serve.
func (m Model) loadModelCatalogCmd() tea.Cmd {
	agent := m.agent
	if agent == nil {
		return func() tea.Msg {
			return modelListMsg{catalog: golem.ModelCatalog{
				Current: golem.DefaultModelName,
				Note:    "No active agent — showing the bundled list.",
			}}
		}
	}
	return func() tea.Msg {
		return modelListMsg{catalog: agent.ModelCatalog(context.Background())}
	}
}

// switchModelCmd hot-swaps the primary model, keeping the provider, credentials
// and mounted memories untouched.
func (m Model) switchModelCmd(name string) tea.Cmd {
	agent := m.agent
	if agent == nil {
		return func() tea.Msg {
			return modelSwitchedMsg{name: name, err: fmt.Errorf("no active agent")}
		}
	}
	return func() tea.Msg {
		cfg, err := agent.SwitchModel(context.Background(), name)
		return modelSwitchedMsg{provider: cfg.Provider, name: cfg.ModelName, err: err}
	}
}

// resolveModelCmd validates a typed model name against the provider catalogue
// before switching. A recently fetched catalogue is reused; an unreachable
// provider cannot confirm the name, so the switch proceeds with a warning
// rather than blocking a legitimate model behind a network failure.
func (m Model) resolveModelCmd(name string) tea.Cmd {
	agent := m.agent
	catalog := m.modelCatalog
	fresh := len(catalog.Models) > 0 && !m.modelCatalogAt.IsZero() &&
		time.Since(m.modelCatalogAt) < modelCatalogFreshFor

	if agent == nil {
		return func() tea.Msg {
			return modelSwitchedMsg{name: name, requested: name, err: fmt.Errorf("no active agent")}
		}
	}

	return func() tea.Msg {
		ctx := context.Background()
		if !fresh {
			catalog = agent.ModelCatalog(ctx)
		}
		msg := modelSwitchedMsg{requested: name}
		if len(catalog.Models) > 0 {
			msg.catalog, msg.haveCatalog = catalog, true
		}

		if !catalog.Live {
			// Nothing to validate against: switch and say it is unverified.
			cfg, err := agent.SwitchModel(ctx, name)
			msg.provider, msg.name, msg.err = cfg.Provider, cfg.ModelName, err
			msg.unverified = err == nil
			return msg
		}

		match, suggestions, ok := catalog.MatchModel(name)
		if !ok {
			msg.suggestions = suggestions
			msg.err = fmt.Errorf("%q is not served by %s", name, catalog.Provider)
			return msg
		}

		cfg, err := agent.SwitchModel(ctx, match)
		msg.provider, msg.name, msg.err = cfg.Provider, cfg.ModelName, err
		return msg
	}
}

// openModelPicker shows the catalogue, preselecting the model in use.
func (m *Model) openModelPicker(catalog golem.ModelCatalog) {
	m.modelCatalog = catalog
	m.modelCatalogAt = time.Now()
	m.modelSelection = 0
	for i, name := range catalog.Models {
		if name == catalog.Current {
			m.modelSelection = i
			break
		}
	}
	m.showModelModal = true
	m.textarea.Blur()
	m.syncModelPicker()
}

// modelSwitchMessage turns a switch result into one line of prose, listing the
// names that do exist when the typed model turned out not to.
func (m *Model) modelSwitchMessage(msg modelSwitchedMsg) string {
	if msg.err != nil {
		if len(msg.suggestions) > 0 {
			return fmt.Sprintf("Model switch failed: %v. Did you mean: %s? Type /model to pick from the list.",
				msg.err, strings.Join(msg.suggestions, ", "))
		}
		return fmt.Sprintf("Model switch failed: %v. Type /model to pick from the list.", msg.err)
	}

	// Keep the cached catalogue in sync so reopening the picker marks the new
	// current model instead of pointing at the old one.
	if len(m.modelCatalog.Models) > 0 {
		m.modelCatalog.Current = msg.name
	}

	if msg.unverified {
		return fmt.Sprintf("Model switched to %s:%s (provider catalogue unavailable — name not verified)", msg.provider, msg.name)
	}
	return fmt.Sprintf("Model switched to %s:%s", msg.provider, msg.name)
}

// closeModelPicker hides the picker and gives the prompt its focus back.
func (m *Model) closeModelPicker() {
	m.showModelModal = false
	if !m.isBlurred {
		m.textarea.Focus()
	}
}

// moveModelSelection walks the list, wrapping at both ends.
func (m *Model) moveModelSelection(delta int) {
	n := len(m.modelCatalog.Models)
	if n == 0 {
		return
	}
	m.modelSelection = (m.modelSelection + delta%n + n) % n
	m.syncModelPicker()
}

// modelPickerContent renders the modal body and reports which line holds the
// highlighted model, so the viewport can keep that line on screen.
func (m Model) modelPickerContent() (string, int) {
	catalog := m.modelCatalog
	provider := catalog.Provider
	if provider == "" {
		provider = "unknown"
	}

	var lines []string
	lines = append(lines, ModalTitleStyle.Render("GOLEM MODEL PICKER"), "")

	header := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	lines = append(lines, header.Render("▍ Provider: "+provider))
	lines = append(lines, lipgloss.NewStyle().Foreground(ColorMuted).Render("  Current: "+modelPickerCurrent(catalog)))
	source := "bundled list (offline hint)"
	if catalog.Live {
		source = "live from provider"
	}
	lines = append(lines, lipgloss.NewStyle().Foreground(ColorMuted).Render("  Source:  "+source))
	if catalog.Note != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorWarning).Italic(true).Render("  "+catalog.Note))
	}
	lines = append(lines, "")

	selectionLine := len(lines) + m.modelSelection
	if len(catalog.Models) == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(ColorWarning).Render("  No models reported."))
	} else {
		for i, name := range catalog.Models {
			line := "  " + name
			if i == m.modelSelection {
				line = lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render("▸ " + name)
			}
			if name == catalog.Current {
				line += lipgloss.NewStyle().Foreground(ColorSuccess).Render("  [current]")
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(ColorMuted).Render(
		"Enter switches model without a restart; provider, credentials and mounted memories are kept."))

	return strings.Join(lines, "\n"), selectionLine
}

func modelPickerCurrent(catalog golem.ModelCatalog) string {
	if strings.TrimSpace(catalog.Current) == "" {
		return "(unknown)"
	}
	return catalog.Current
}

// syncModelPicker re-renders the body and scrolls just enough to keep the
// highlighted row visible in the modal viewport.
func (m *Model) syncModelPicker() {
	content, selectionLine := m.modelPickerContent()
	m.modelViewport.SetContent(content)

	height := m.modelViewport.Height
	if height <= 0 {
		return
	}
	offset := m.modelViewport.YOffset
	if selectionLine < offset {
		offset = selectionLine
	}
	if selectionLine >= offset+height {
		offset = selectionLine - height + 1
	}
	maxOffset := max(0, m.modelViewport.TotalLineCount()-height)
	if offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}
	m.modelViewport.SetYOffset(offset)
}

// handleModelModalKey drives the picker: arrows/j/k choose, Enter switches,
// Esc/q closes.
func (m Model) handleModelModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		return m, tea.Quit
	case tea.KeyEsc:
		m.closeModelPicker()
		return m, nil
	case tea.KeyUp:
		m.moveModelSelection(-1)
		return m, nil
	case tea.KeyDown:
		m.moveModelSelection(1)
		return m, nil
	case tea.KeyPgUp:
		m.modelViewport.HalfViewUp()
		return m, nil
	case tea.KeyPgDown:
		m.modelViewport.HalfViewDown()
		return m, nil
	case tea.KeyHome:
		m.moveModelSelection(-m.modelSelection)
		return m, nil
	case tea.KeyEnd:
		m.moveModelSelection(len(m.modelCatalog.Models) - 1 - m.modelSelection)
		return m, nil
	case tea.KeyEnter:
		if len(m.modelCatalog.Models) == 0 {
			return m, nil
		}
		name := m.modelCatalog.Models[m.modelSelection]
		if strings.TrimSpace(name) == "" || name == m.modelCatalog.Current {
			m.closeModelPicker()
			m.setStatus("Already using " + name)
			return m, nil
		}
		m.closeModelPicker()
		m.setStatus("Switching model to " + name + "…")
		return m, m.switchModelCmd(name)
	}

	switch msg.String() {
	case "q":
		m.closeModelPicker()
	case "j":
		m.moveModelSelection(1)
	case "k":
		m.moveModelSelection(-1)
	}
	return m, nil
}

// renderModelModalView draws the picker in the same double frame as the other
// inspectors.
func (m Model) renderModelModalView() string {
	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(ColorPrimary).
		Padding(1, 2).
		Width(m.width - 6).
		Height(m.height - 4)

	footerHint := StatusBarKeyHintStyle.Render("\n[↑/↓: Choose • Enter: Switch • Esc/q: Back to Chat]")
	return modalStyle.Render(m.modelViewport.View() + footerHint)
}
