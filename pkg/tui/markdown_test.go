package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour/ansi"
)

// nordStyleConfigForTest installs the Nord palette for the duration of the test
// (the default palette is the terminal's own) and returns the markdown style
// config it produces.
func nordStyleConfigForTest(t *testing.T, dark bool) ansi.StyleConfig {
	t.Helper()
	if err := ApplyTheme(ThemeNord); err != nil {
		t.Fatalf("ApplyTheme(%q): %v", ThemeNord, err)
	}
	t.Cleanup(func() { _ = ApplyTheme(ThemeTerminal) })
	return markdownStyleConfig(dark)
}

// colourOf returns the colour of a style primitive or fails the test.
func colourOf(t *testing.T, name string, c *string) string {
	t.Helper()
	if c == nil {
		t.Fatalf("%s: expected a colour, got nil", name)
	}
	return *c
}

// TestNordStyleConfig_DarkPalette pins the dark-mode markdown palette. The
// inline-code colour assertion is the regression guard for the "everything is
// red" bug: glamour's stock dark style painted inline code in a hot salmon red,
// so every file path in a reply looked like an error.
func TestNordStyleConfig_DarkPalette(t *testing.T) {
	cfg := nordStyleConfigForTest(t, true)

	if got := colourOf(t, "document", cfg.Document.Color); got != nordDarkPalette.body {
		t.Errorf("document colour = %s, want %s", got, nordDarkPalette.body)
	}
	if got := colourOf(t, "inline code", cfg.Code.Color); got != nordDarkPalette.teal {
		t.Errorf("inline code colour = %s, want %s (teal, not red)", got, nordDarkPalette.teal)
	}
	if got := colourOf(t, "inline code background", cfg.Code.BackgroundColor); got != nordDarkPalette.surface {
		t.Errorf("inline code background = %s, want %s", got, nordDarkPalette.surface)
	}
	if got := colourOf(t, "h1", cfg.H1.Color); got != nordDarkPalette.accent {
		t.Errorf("h1 colour = %s, want %s", got, nordDarkPalette.accent)
	}
	if got := colourOf(t, "h2", cfg.H2.Color); got != nordDarkPalette.accentAlt {
		t.Errorf("h2 colour = %s, want %s", got, nordDarkPalette.accentAlt)
	}
	if got := colourOf(t, "link", cfg.Link.Color); got != nordDarkPalette.accent {
		t.Errorf("link colour = %s, want %s", got, nordDarkPalette.accent)
	}
	if cfg.H1.BackgroundColor != nil || cfg.H2.BackgroundColor != nil {
		t.Error("headings must not paint a background, that is what made them shout")
	}
}

// TestNordStyleConfig_ChromaPalette checks syntax highlighting uses Nord tones
// and, importantly, no background: a hard-coded grey box clashes with the
// user's own terminal theme.
func TestNordStyleConfig_ChromaPalette(t *testing.T) {
	cfg := nordStyleConfigForTest(t, true)
	chroma := cfg.CodeBlock.Chroma
	if chroma == nil {
		t.Fatal("code blocks must carry a chroma palette")
	}

	cases := []struct {
		name string
		got  *string
		want string
	}{
		{"text", chroma.Text.Color, nordDarkPalette.body},
		{"comment", chroma.Comment.Color, nordDarkPalette.muted},
		{"keyword", chroma.Keyword.Color, nordDarkPalette.accentAlt},
		{"function", chroma.NameFunction.Color, nordDarkPalette.accent},
		{"string", chroma.LiteralString.Color, nordDarkPalette.green},
		{"number", chroma.LiteralNumber.Color, nordDarkPalette.purple},
		{"error", chroma.Error.Color, nordDarkPalette.danger},
	}
	for _, tc := range cases {
		if got := colourOf(t, tc.name, tc.got); got != tc.want {
			t.Errorf("chroma %s = %s, want %s", tc.name, got, tc.want)
		}
	}

	if chroma.Background.BackgroundColor != nil {
		t.Errorf("chroma background = %s, want terminal default",
			*chroma.Background.BackgroundColor)
	}
	if cfg.CodeBlock.Theme != "" {
		t.Errorf("code block theme = %q, want empty so our chroma palette wins", cfg.CodeBlock.Theme)
	}
}

// TestNordStyleConfig_LightPalette ensures light terminals get their own tones
// rather than the dark ones (which would be unreadable on white).
func TestNordStyleConfig_LightPalette(t *testing.T) {
	dark := nordStyleConfigForTest(t, true)
	light := nordStyleConfigForTest(t, false)

	if got := colourOf(t, "light document", light.Document.Color); got != nordLightPalette.body {
		t.Errorf("light document colour = %s, want %s", got, nordLightPalette.body)
	}
	if got := colourOf(t, "light inline code", light.Code.Color); got != nordLightPalette.teal {
		t.Errorf("light inline code colour = %s, want %s", got, nordLightPalette.teal)
	}
	if *dark.Document.Color == *light.Document.Color {
		t.Error("dark and light document colours must differ")
	}
	// Copying the shared base config must not alias the other variant's palette.
	if dark.CodeBlock.Chroma == light.CodeBlock.Chroma {
		t.Error("dark and light chroma palettes must not share a pointer")
	}
}

// TestNordStyleConfig_DangerOnlyForErrors makes sure the red tone is reserved
// for genuine failures, not for ordinary code or links.
func TestNordStyleConfig_DangerOnlyForErrors(t *testing.T) {
	cfg := nordStyleConfigForTest(t, true)
	red := nordDarkPalette.danger

	for name, c := range map[string]*string{
		"inline code": cfg.Code.Color,
		"link":        cfg.Link.Color,
		"h1":          cfg.H1.Color,
		"document":    cfg.Document.Color,
		"item":        cfg.Item.Color,
	} {
		if c != nil && *c == red {
			t.Errorf("%s must not use the danger red", name)
		}
	}
}

// TestNewMarkdownRenderer_RendersNord checks the renderer is usable and that a
// file path in backticks survives rendering as the inline-code token.
func TestNewMarkdownRenderer_RendersNord(t *testing.T) {
	renderer := newMarkdownRenderer(72)
	if renderer == nil {
		t.Fatal("expected a renderer")
	}

	out, err := renderer.Render("Run `~/.golem/config.json` to configure.\n")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "~/.golem/config.json") {
		t.Errorf("rendered output lost the inline code: %q", out)
	}
}

// TestNewMarkdownRenderer_ClampsWidth keeps very narrow terminals from asking
// glamour for a zero or negative wrap width.
func TestNewMarkdownRenderer_ClampsWidth(t *testing.T) {
	for _, width := range []int{-20, 0, 5, 20} {
		if renderer := newMarkdownRenderer(width); renderer == nil {
			t.Errorf("width %d: expected a renderer", width)
		}
	}
}
