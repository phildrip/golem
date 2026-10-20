package tui

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// restoreTheme puts the default palette back when a test swaps it.
func restoreTheme(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = ApplyTheme(ThemeTerminal) })
}

// TestDefaultThemeIsTerminal pins the default: unless the user asks otherwise,
// Golem paints with the terminal's own palette rather than baked-in RGB.
func TestDefaultThemeIsTerminal(t *testing.T) {
	if got := CurrentTheme(); got != ThemeTerminal {
		t.Fatalf("default theme = %q, want %q", got, ThemeTerminal)
	}
}

// TestApplyTheme_TerminalUsesANSIIndexes verifies the terminal palette is
// expressed as ANSI colour indexes (which every terminal resolves against the
// user's scheme) and that body text is left unset so the terminal's default
// foreground shows through.
func TestApplyTheme_TerminalUsesANSIIndexes(t *testing.T) {
	restoreTheme(t)
	if err := ApplyTheme(ThemeTerminal); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeTerminal, err)
	}

	ansiIndex := func(name string, c lipgloss.TerminalColor, want string) {
		t.Helper()
		col, ok := c.(lipgloss.Color)
		if !ok {
			t.Fatalf("%s is %T, want an ANSI lipgloss.Color", name, c)
		}
		if string(col) != want {
			t.Errorf("%s = %q, want ANSI index %q", name, string(col), want)
		}
	}

	ansiIndex("ColorPrimary", ColorPrimary, "6")
	ansiIndex("ColorSecondary", ColorSecondary, "5")
	ansiIndex("ColorSuccess", ColorSuccess, "2")
	ansiIndex("ColorWarning", ColorWarning, "3")
	ansiIndex("ColorDanger", ColorDanger, "1")
	ansiIndex("ColorMuted", ColorMuted, "8")
	ansiIndex("ColorBorder", ColorBorder, "8")

	for _, tc := range []struct {
		name string
		c    lipgloss.TerminalColor
	}{
		{"ColorHighlight (body text)", ColorHighlight},
		{"ColorSurface (fills)", ColorSurface},
	} {
		if _, ok := tc.c.(lipgloss.NoColor); !ok {
			t.Errorf("%s is %T, want lipgloss.NoColor so the terminal default applies", tc.name, tc.c)
		}
	}
}

// TestApplyTheme_NordUsesAdaptiveRGB checks the bundled theme stays available and
// keeps its light/dark adaptive values.
func TestApplyTheme_NordUsesAdaptiveRGB(t *testing.T) {
	restoreTheme(t)
	if err := ApplyTheme(ThemeNord); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeNord, err)
	}
	if got := CurrentTheme(); got != ThemeNord {
		t.Fatalf("CurrentTheme = %q, want %q", got, ThemeNord)
	}
	adaptive, ok := ColorPrimary.(lipgloss.AdaptiveColor)
	if !ok {
		t.Fatalf("ColorPrimary is %T, want lipgloss.AdaptiveColor", ColorPrimary)
	}
	if !strings.HasPrefix(adaptive.Dark, "#") || !strings.HasPrefix(adaptive.Light, "#") {
		t.Errorf("ColorPrimary = %+v, want hex light and dark values", adaptive)
	}
}

// TestApplyTheme_RebuildsComponentStyles proves the styles are re-derived from
// the palette, not frozen at package init.
func TestApplyTheme_RebuildsComponentStyles(t *testing.T) {
	restoreTheme(t)
	if err := ApplyTheme(ThemeNord); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeNord, err)
	}
	nord := AgentBadgeStyle.GetForeground()

	if err := ApplyTheme(ThemeTerminal); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeTerminal, err)
	}
	term := AgentBadgeStyle.GetForeground()

	if reflect.DeepEqual(nord, term) {
		t.Fatalf("AgentBadgeStyle foreground did not change with the theme (still %v)", term)
	}
	if col, ok := term.(lipgloss.Color); !ok || string(col) != "6" {
		t.Errorf("terminal AgentBadgeStyle foreground = %v, want ANSI cyan", term)
	}
}

// TestApplyTheme_UnknownLeavesThemeUntouched ensures a typo degrades to the
// active palette instead of a half-applied one.
func TestApplyTheme_UnknownLeavesThemeUntouched(t *testing.T) {
	restoreTheme(t)
	if err := ApplyTheme(ThemeNord); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeNord, err)
	}
	err := ApplyTheme("solarized-bright")
	if err == nil {
		t.Fatal("expected an error for an unknown theme")
	}
	if got := CurrentTheme(); got != ThemeNord {
		t.Errorf("CurrentTheme = %q after failed switch, want %q", got, ThemeNord)
	}
	if !strings.Contains(err.Error(), ThemeTerminal) {
		t.Errorf("error %q should name the valid themes", err)
	}
}

// TestApplyTheme_AcceptsAliases covers the spellings the CLI and $GOLEM_THEME
// hand over for the default palette.
func TestApplyTheme_AcceptsAliases(t *testing.T) {
	restoreTheme(t)
	for _, name := range []string{"", "auto", "default", "terminal", "TERMINAL", " terminal "} {
		if err := ApplyTheme(ThemeNord); err != nil {
			t.Fatalf("ApplyTheme(%q): %v", ThemeNord, err)
		}
		if err := ApplyTheme(name); err != nil {
			t.Fatalf("ApplyTheme(%q): %v", name, err)
		}
		if got := CurrentTheme(); got != ThemeTerminal {
			t.Errorf("ApplyTheme(%q) left theme %q, want %q", name, got, ThemeTerminal)
		}
	}
}

// collectHexColors walks a glamour StyleConfig and reports every colour value
// that is a hard-coded hex string.
func collectHexColors(v reflect.Value, path string) []string {
	var found []string
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return collectHexColors(v.Elem(), path)
	case reflect.Struct:
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			name := typ.Field(i).Name
			if f.Kind() == reflect.Ptr && f.Type().Elem().Kind() == reflect.String {
				if !f.IsNil() && strings.HasPrefix(*f.Interface().(*string), "#") {
					found = append(found, path+"."+name+"="+*f.Interface().(*string))
				}
				continue
			}
			found = append(found, collectHexColors(f, path+"."+name)...)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			found = append(found, collectHexColors(v.Index(i), path+"[]")...)
		}
	}
	return found
}

// TestMarkdownStyleConfig_TerminalUsesOnlyANSIColors is the regression guard for
// the strip pass: with the default theme no hard-coded hex may survive from
// glamour's stock style, otherwise replies would clash with the terminal scheme.
func TestMarkdownStyleConfig_TerminalUsesOnlyANSIColors(t *testing.T) {
	restoreTheme(t)
	if err := ApplyTheme(ThemeTerminal); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeTerminal, err)
	}

	cfg := markdownStyleConfig(true)

	if cfg.Document.Color != nil {
		t.Errorf("document colour = %q, want unset so the terminal default applies", *cfg.Document.Color)
	}
	if cfg.Code.BackgroundColor != nil {
		t.Errorf("inline code background = %q, want no chip (terminal default)", *cfg.Code.BackgroundColor)
	}
	if cfg.Code.Color == nil || *cfg.Code.Color != ansiPalette.teal {
		t.Errorf("inline code colour = %v, want ANSI %q", cfg.Code.Color, ansiPalette.teal)
	}

	if hexes := collectHexColors(reflect.ValueOf(cfg), "cfg"); len(hexes) > 0 {
		t.Errorf("terminal theme leaked %d hard-coded hex colours: %v", len(hexes), hexes)
	}
}

// TestMarkdownStyleConfig_TerminalKeepsGeometry makes sure the colour strip does
// not also wipe the layout hints glamour's defaults provide (prefixes, table
// separators, block-quote token).
func TestMarkdownStyleConfig_TerminalKeepsGeometry(t *testing.T) {
	restoreTheme(t)
	cfg := markdownStyleConfig(true)
	if cfg.BlockQuote.IndentToken == nil {
		t.Error("block quote indent token was stripped, want it kept")
	}
	if cfg.Table.ColumnSeparator == nil || *cfg.Table.ColumnSeparator != "│" {
		t.Errorf("table column separator = %v, want │", cfg.Table.ColumnSeparator)
	}
	if cfg.H1.Prefix == "" {
		t.Error("h1 prefix was stripped, want heading geometry kept")
	}
}

// TestMarkdownStyleConfig_NordStillUsesHex guards the other direction: the Nord
// theme must keep its explicit palette.
func TestMarkdownStyleConfig_NordStillUsesHex(t *testing.T) {
	restoreTheme(t)
	if err := ApplyTheme(ThemeNord); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeNord, err)
	}
	cfg := markdownStyleConfig(true)
	if cfg.Document.Color == nil || *cfg.Document.Color != nordDarkPalette.body {
		t.Errorf("nord document colour = %v, want %q", cfg.Document.Color, nordDarkPalette.body)
	}
}

// compile-time guard that the palette fields stay strings we can compare.
var _ *string = ansi.StylePrimitive{}.Color

// ansiEscape matches SGR escape sequences.
var ansiEscape = regexp.MustCompile("\\x1b\\[[0-9;]*m")

// renderWithTrueColor renders markdown through the real style config with a
// forced colour profile, because the test binary is not a TTY (glamour would
// otherwise emit plain text and hide both the palette and the code-block path).
func renderWithTrueColor(t *testing.T, markdown string) string {
	t.Helper()
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyleConfig(true)),
		glamour.WithWordWrap(60),
		glamour.WithColorProfile(termenv.TrueColor),
	)
	if err != nil {
		t.Fatalf("NewTermRenderer: %v", err)
	}
	out, err := r.Render(markdown)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

// TestRenderMarkdown_TerminalThemeStaysOnPalette is the "inline code in a file
// path must not look like an error" regression guard, now for the terminal
// theme: the output must use ANSI indexes only, never 24-bit RGB.
func TestRenderMarkdown_TerminalThemeStaysOnPalette(t *testing.T) {
	restoreTheme(t)
	if err := ApplyTheme(ThemeTerminal); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeTerminal, err)
	}

	out := renderWithTrueColor(t, "Config lives in `~/.golem/config.json`.\n")
	if !strings.Contains(out, "~/.golem/config.json") {
		t.Fatalf("rendered output lost the inline code: %q", out)
	}
	if strings.Contains(out, "38;2;") {
		t.Errorf("terminal theme emitted 24-bit RGB: %q", out)
	}
	if !strings.Contains(out, "\x1b[36m") {
		t.Errorf("inline code is not ANSI cyan (esc[36m): %q", out)
	}
}

// TestRenderMarkdown_TerminalThemeFencedCode renders a fenced block. This used to
// panic outright ("invalid entry ... unknown style element") because chroma only
// accepts RGB/named colours and rejected the ANSI indexes.
func TestRenderMarkdown_TerminalThemeFencedCode(t *testing.T) {
	restoreTheme(t)
	if err := ApplyTheme(ThemeTerminal); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeTerminal, err)
	}

	out := stripANSI(renderWithTrueColor(t, "```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n"))
	if !strings.Contains(out, "func main() {") || !strings.Contains(out, `fmt.Println("hi")`) {
		t.Fatalf("fenced code did not render: %q", out)
	}
}

// TestEmphasisOnlyChroma_HasNoColours pins the reason the terminal theme can use
// chroma at all: no colour may be set.
func TestEmphasisOnlyChroma_HasNoColours(t *testing.T) {
	if hexes := collectHexColors(reflect.ValueOf(emphasisOnlyChroma()), "chroma"); len(hexes) > 0 {
		t.Errorf("emphasis-only chroma carries colours: %v", hexes)
	}
	if ansiCols := collectSetColors(reflect.ValueOf(emphasisOnlyChroma()), "chroma"); len(ansiCols) > 0 {
		t.Errorf("emphasis-only chroma sets colour fields: %v", ansiCols)
	}
}

// stripANSI removes SGR escape sequences so tests can assert on text.
func stripANSI(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

// collectSetColors reports every non-nil *string field named Color or
// BackgroundColor.
func collectSetColors(v reflect.Value, path string) []string {
	var found []string
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return collectSetColors(v.Elem(), path)
	case reflect.Struct:
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			name := typ.Field(i).Name
			if f.Kind() == reflect.Ptr && f.Type().Elem().Kind() == reflect.String &&
				(name == "Color" || name == "BackgroundColor") {
				if !f.IsNil() {
					found = append(found, path+"."+name)
				}
				continue
			}
			found = append(found, collectSetColors(f, path+"."+name)...)
		}
	}
	return found
}

// TestView_RendersInBothThemes is a smoke test for the whole screen: a theme
// swap must not leave a style half-built (NoColor foregrounds, empty borders,
// unstyled badges) in a way that breaks layout.
func TestView_RendersInBothThemes(t *testing.T) {
	restoreTheme(t)
	m := newScrollTestModel(t)

	for _, theme := range []string{ThemeTerminal, ThemeNord} {
		if err := ApplyTheme(theme); err != nil {
			t.Fatalf("ApplyTheme(%q): %v", theme, err)
		}
		out := m.View()
		if !strings.Contains(out, "GOLEM") {
			t.Errorf("theme %q: view lost the header:\n%s", theme, out)
		}
		if !strings.Contains(out, "Ctrl+G: Mouse") {
			t.Errorf("theme %q: view lost the mouse hint:\n%s", theme, out)
		}
	}

	// With capture released the status line must say so, so the user knows why
	// tmux suddenly scrolls.
	if err := ApplyTheme(ThemeTerminal); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeTerminal, err)
	}
	m.SetMouseEnabled(false)
	m.statusMessage = ""
	if out := m.View(); !strings.Contains(out, "[MOUSE OFF]") {
		t.Errorf("view does not advertise released mouse capture:\n%s", out)
	}
}
