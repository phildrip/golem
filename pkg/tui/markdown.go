package tui

import (
	"reflect"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
)

// Markdown rendering palette.
//
// glamour's built-in styles (auto/dark/dracula) paint inline code in a hot
// salmon red on a grey chip — fine for prose, jarring inside an otherwise Nordic
// UI, and it makes every file path in a reply look like an error. We therefore
// ship our own Nord-derived style so agent replies share the palette of the
// chrome around them: frost blue for structure, teal for code, and red
// reserved for things that are actually wrong.
type markdownPalette struct {
	body       string // default text
	bodyBright string // bold text, punctuation in code
	muted      string // comments, minor headings, rules
	accent     string // headings, links, function names
	accentAlt  string // sub-headings, operators, keywords
	teal       string // inline code, types, classes
	green      string // strings, insertions
	amber      string // escapes, decorators
	danger     string // deletions, code errors
	purple     string // numbers, constants
	surface    string // inline-code chip background
	border     string // block quote bar, horizontal rule
}

// nordDarkPalette mirrors the dark half of the lipgloss palette in styles.go.
var nordDarkPalette = markdownPalette{
	body:       "#E5E9F0",
	bodyBright: "#ECEFF4",
	muted:      "#7B8794",
	accent:     "#88C0D0",
	accentAlt:  "#81A1C1",
	teal:       "#8FBCBB",
	green:      "#A3BE8C",
	amber:      "#EBCB8B",
	danger:     "#BF616A",
	purple:     "#B48EAD",
	surface:    "#3B4252",
	border:     "#4C566A",
}

// nordLightPalette mirrors the light half of the lipgloss palette in styles.go.
var nordLightPalette = markdownPalette{
	body:       "#2E3440",
	bodyBright: "#20242C",
	muted:      "#6A737D",
	accent:     "#5E81AC",
	accentAlt:  "#4C6A92",
	teal:       "#35707C",
	green:      "#5F7E4E",
	amber:      "#9C7A2E",
	danger:     "#A33A44",
	purple:     "#7C6A9C",
	surface:    "#E7EAEE",
	border:     "#C2C7D0",
}

// colorPtr returns a pointer to s, or nil when s is empty so the renderer leaves
// the attribute unset and the terminal's own colour shows through.
func colorPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// stripStyleColors nils every Color/BackgroundColor declaration inside a glamour
// StyleConfig. It lets a theme built on glamour's defaults inherit the terminal
// palette instead of smuggling in the defaults' hard-coded hex values.
func stripStyleColors(v reflect.Value) {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return
		}
		stripStyleColors(v.Elem())
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if !f.CanSet() {
				continue
			}
			name := t.Field(i).Name
			if f.Kind() == reflect.Ptr && f.Type().Elem().Kind() == reflect.String &&
				(name == "Color" || name == "BackgroundColor") {
				f.Set(reflect.Zero(f.Type()))
				continue
			}
			stripStyleColors(f)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			stripStyleColors(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			stripStyleColors(v.MapIndex(k))
		}
	}
}
func boolPtr(b bool) *bool { return &b }

// ansiPalette expresses the markdown palette in ANSI colour indexes so replies
// follow the terminal's own scheme (ThemeTerminal), exactly like the chrome
// around them. Empty entries leave the attribute unset so the terminal's default
// foreground/background shows through.
var ansiPalette = markdownPalette{
	body:       "",
	bodyBright: "",
	muted:      "8",
	accent:     "6",
	accentAlt:  "4",
	teal:       "6",
	green:      "2",
	amber:      "3",
	danger:     "1",
	purple:     "5",
	surface:    "",
	border:     "8",
}

// markdownPaletteFor picks the palette matching the active theme (and, for
// Nord, the terminal background).
func markdownPaletteFor(dark bool) markdownPalette {
	if CurrentTheme() == ThemeNord {
		if dark {
			return nordDarkPalette
		}
		return nordLightPalette
	}
	return ansiPalette
}

// nordChroma maps Chroma syntax tokens onto Nord tones. The background is left
// unset so code blocks sit on the user's own terminal background instead of a
// hard-coded grey box.
func nordChroma(p markdownPalette) *ansi.Chroma {
	return &ansi.Chroma{
		Text:                ansi.StylePrimitive{Color: colorPtr(p.body)},
		Error:               ansi.StylePrimitive{Color: colorPtr(p.danger), Bold: boolPtr(true)},
		Comment:             ansi.StylePrimitive{Color: colorPtr(p.muted), Italic: boolPtr(true)},
		CommentPreproc:      ansi.StylePrimitive{Color: colorPtr(p.accentAlt)},
		Keyword:             ansi.StylePrimitive{Color: colorPtr(p.accentAlt), Bold: boolPtr(true)},
		KeywordReserved:     ansi.StylePrimitive{Color: colorPtr(p.accentAlt), Bold: boolPtr(true)},
		KeywordNamespace:    ansi.StylePrimitive{Color: colorPtr(p.accentAlt)},
		KeywordType:         ansi.StylePrimitive{Color: colorPtr(p.teal)},
		Operator:            ansi.StylePrimitive{Color: colorPtr(p.accentAlt)},
		Punctuation:         ansi.StylePrimitive{Color: colorPtr(p.bodyBright)},
		Name:                ansi.StylePrimitive{Color: colorPtr(p.body)},
		NameBuiltin:         ansi.StylePrimitive{Color: colorPtr(p.accent)},
		NameTag:             ansi.StylePrimitive{Color: colorPtr(p.accentAlt)},
		NameAttribute:       ansi.StylePrimitive{Color: colorPtr(p.teal)},
		NameClass:           ansi.StylePrimitive{Color: colorPtr(p.teal), Bold: boolPtr(true)},
		NameConstant:        ansi.StylePrimitive{Color: colorPtr(p.purple)},
		NameDecorator:       ansi.StylePrimitive{Color: colorPtr(p.amber)},
		NameException:       ansi.StylePrimitive{Color: colorPtr(p.danger)},
		NameFunction:        ansi.StylePrimitive{Color: colorPtr(p.accent)},
		NameOther:           ansi.StylePrimitive{Color: colorPtr(p.body)},
		Literal:             ansi.StylePrimitive{Color: colorPtr(p.body)},
		LiteralNumber:       ansi.StylePrimitive{Color: colorPtr(p.purple)},
		LiteralDate:         ansi.StylePrimitive{Color: colorPtr(p.amber)},
		LiteralString:       ansi.StylePrimitive{Color: colorPtr(p.green)},
		LiteralStringEscape: ansi.StylePrimitive{Color: colorPtr(p.amber)},
		GenericDeleted:      ansi.StylePrimitive{Color: colorPtr(p.danger)},
		GenericEmph:         ansi.StylePrimitive{Italic: boolPtr(true)},
		GenericInserted:     ansi.StylePrimitive{Color: colorPtr(p.green)},
		GenericStrong:       ansi.StylePrimitive{Bold: boolPtr(true)},
		GenericSubheading:   ansi.StylePrimitive{Color: colorPtr(p.muted)},
	}
}

// emphasisOnlyChroma returns a chroma palette that sets no colours at all, only
// emphasis. It keeps code blocks legible under the terminal theme without
// injecting RGB values the user never chose (chroma cannot express ANSI indexes).
func emphasisOnlyChroma() *ansi.Chroma {
	b := boolPtr
	return &ansi.Chroma{
		Text:                ansi.StylePrimitive{},
		Error:               ansi.StylePrimitive{Bold: b(true)},
		Comment:             ansi.StylePrimitive{Italic: b(true)},
		CommentPreproc:      ansi.StylePrimitive{},
		Keyword:             ansi.StylePrimitive{Bold: b(true)},
		KeywordReserved:     ansi.StylePrimitive{Bold: b(true)},
		KeywordNamespace:    ansi.StylePrimitive{},
		KeywordType:         ansi.StylePrimitive{Italic: b(true)},
		Operator:            ansi.StylePrimitive{},
		Punctuation:         ansi.StylePrimitive{},
		Name:                ansi.StylePrimitive{},
		NameBuiltin:         ansi.StylePrimitive{},
		NameTag:             ansi.StylePrimitive{},
		NameAttribute:       ansi.StylePrimitive{Italic: b(true)},
		NameClass:           ansi.StylePrimitive{Bold: b(true)},
		NameConstant:        ansi.StylePrimitive{},
		NameDecorator:       ansi.StylePrimitive{},
		NameException:       ansi.StylePrimitive{Bold: b(true)},
		NameFunction:        ansi.StylePrimitive{},
		NameOther:           ansi.StylePrimitive{},
		Literal:             ansi.StylePrimitive{},
		LiteralNumber:       ansi.StylePrimitive{},
		LiteralDate:         ansi.StylePrimitive{},
		LiteralString:       ansi.StylePrimitive{},
		LiteralStringEscape: ansi.StylePrimitive{Bold: b(true)},
		GenericDeleted:      ansi.StylePrimitive{},
		GenericEmph:         ansi.StylePrimitive{Italic: b(true)},
		GenericInserted:     ansi.StylePrimitive{},
		GenericStrong:       ansi.StylePrimitive{Bold: b(true)},
		GenericSubheading:   ansi.StylePrimitive{Italic: b(true)},
	}
}

// markdownStyleConfig builds a glamour StyleConfig for the active theme. It
// starts from glamour's dark/light defaults so lists, tables and task items keep
// their geometry, then replaces every colour with a palette entry.
func markdownStyleConfig(dark bool) ansi.StyleConfig {
	base := styles.DarkStyleConfig
	if !dark {
		base = styles.LightStyleConfig
	}
	p := markdownPaletteFor(dark)

	cfg := base
	if CurrentTheme() != ThemeNord {
		// Terminal theme: drop glamour's hex so every colour either comes from
		// the ANSI palette below or is left to the terminal.
		stripStyleColors(reflect.ValueOf(&cfg))
	}

	// Only recolour the document; its block prefix/suffix and margin own the
	// vertical rhythm of the reply, so they must survive.
	cfg.Document.StylePrimitive.Color = colorPtr(p.body)

	cfg.BlockQuote.StylePrimitive = ansi.StylePrimitive{Color: colorPtr(p.muted)}
	cfg.BlockQuote.IndentToken = colorPtr("│ ")

	heading := func(prefix, color string, bold bool) ansi.StyleBlock {
		return ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix: prefix,
				Color:  colorPtr(color),
				Bold:   boolPtr(bold),
			},
		}
	}
	cfg.Heading = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{Bold: boolPtr(true), Color: colorPtr(p.accent)}}
	cfg.H1 = heading("▌ ", p.accent, true)
	cfg.H2 = heading("▌ ", p.accentAlt, true)
	cfg.H3 = heading("▎ ", p.body, true)
	cfg.H4 = heading("▏ ", p.bodyBright, true)
	cfg.H5 = heading("▏ ", p.muted, true)
	cfg.H6 = heading("▏ ", p.muted, false)

	cfg.Emph = ansi.StylePrimitive{Italic: boolPtr(true)}
	cfg.Strong = ansi.StylePrimitive{Bold: boolPtr(true), Color: colorPtr(p.bodyBright)}
	cfg.HorizontalRule = ansi.StylePrimitive{Color: colorPtr(p.border)}

	cfg.Link = ansi.StylePrimitive{Color: colorPtr(p.accent), Underline: boolPtr(true)}
	cfg.LinkText = ansi.StylePrimitive{Color: colorPtr(p.teal), Bold: boolPtr(true)}

	// Preserve BlockPrefix ("• " for Item, ". " for Enumeration) from the base
	// style so ordered lists render as "1. Text" rather than "1Text".
	cfg.Item.Color = colorPtr(p.accent)
	if cfg.Item.BlockPrefix == "" {
		cfg.Item.BlockPrefix = "• "
	}
	cfg.Enumeration.Color = colorPtr(p.accentAlt)
	if cfg.Enumeration.BlockPrefix == "" {
		cfg.Enumeration.BlockPrefix = ". "
	}

	// Inline code: a quiet teal chip rather than glamour's red-on-grey.
	cfg.Code = ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
		Prefix:          " ",
		Suffix:          " ",
		Color:           colorPtr(p.teal),
		BackgroundColor: colorPtr(p.surface),
	}}

	codeBlock := cfg.CodeBlock
	codeBlock.StylePrimitive = ansi.StylePrimitive{Color: colorPtr(p.body)}
	codeBlock.Theme = ""
	if CurrentTheme() == ThemeNord {
		codeBlock.Chroma = nordChroma(p)
	} else {
		// Chroma only understands RGB hex or named colours and panics outright on
		// ANSI *indexes* ("invalid entry ... unknown style element"), so a
		// terminal-native code block keeps its shape (bold keywords, italic
		// comments) and leaves every colour to the terminal's own foreground.
		codeBlock.Chroma = emphasisOnlyChroma()
	}
	cfg.CodeBlock = codeBlock

	cfg.Table.StyleBlock.StylePrimitive = ansi.StylePrimitive{Color: colorPtr(p.body)}
	cfg.Table.CenterSeparator = colorPtr("┼")
	cfg.Table.ColumnSeparator = colorPtr("│")
	cfg.Table.RowSeparator = colorPtr("─")

	cfg.DefinitionTerm = ansi.StylePrimitive{Bold: boolPtr(true), Color: colorPtr(p.accentAlt)}
	cfg.DefinitionDescription = ansi.StylePrimitive{Color: colorPtr(p.body)}

	return cfg
}

// newMarkdownRenderer returns a markdown renderer that wraps at the given
// column width using the active theme style (Nord adapts to the terminal
// background, the terminal theme inherits the terminal palette). A nil
// renderer is returned if glamour rejects the style; callers already guard
// against that.
// RenderMarkdown renders markdown text at the given width with the active
// theme's palette. The TUI itself renders through the model's cached renderer;
// this helper exists so tools (and tests) can preview exactly the same output.
func RenderMarkdown(width int, text string) string {
	renderer := newMarkdownRenderer(width)
	if renderer == nil {
		return text
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return out
}

func newMarkdownRenderer(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyleConfig(lipgloss.HasDarkBackground())),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	return renderer
}

// closeUnbalancedCodeFence appends a closing ``` fence when streaming text is
// mid-way through a fenced code block so Glamour renders the live code block
// instead of treating the fence line as plain text.
func closeUnbalancedCodeFence(text string) string {
	var inFence bool
	var fenceMarker string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if !inFence {
				inFence = true
				fenceMarker = marker
			} else if marker == fenceMarker && strings.TrimSpace(trimmed[3:]) == "" {
				inFence = false
				fenceMarker = ""
			}
		}
	}
	if !inFence {
		return text
	}
	if !strings.HasSuffix(text, "\n") {
		return text + "\n" + fenceMarker
	}
	return text + fenceMarker
}
