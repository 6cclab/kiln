package tui

import (
	"strings"
	"sync"

	"charm.land/glamour/v2"
	glansi "charm.land/glamour/v2/ansi"
)

// Assistant prose rendering, ported from markdownTheme (app.ts:67-84).
//
// Code blocks get a two-space indent and no drawn box so they read as a
// distinct block without a heavy frame competing with the tool-call
// markers. Chroma highlighting only lights up when the fence names a
// language chroma actually recognizes — quick.Highlight falls back to a
// plaintext lexer otherwise, so an unfenced or unknown-language block comes
// out unstyled rather than mis-highlighted.
//
// # Deviations from markdownTheme
//
// markdownTheme is a set of closures pi-tui's Markdown component calls
// per-token ((t) => bold(t), etc.); glamour instead takes a declarative
// ansi.StyleConfig (hex colour strings and *bool flags) applied by its own
// renderer. This file is the value-mapping equivalent of those closures,
// not a literal port — see buildStyle's comments for each entry that has
// no glamour equivalent (codeBlockBorder and quoteBorder: glamour draws no
// border/box around a code block or blockquote at all, only an indent and
// an optional per-line indent token, so "dim border" becomes a dim indent
// token rather than a drawn line).

// ansiColor mirrors the ANSI indices theme.go's style helpers use, as the
// numeric strings lipgloss.Color (and therefore glamour's StylePrimitive.
// Color) accepts.
const (
	colorRed     = "1"
	colorGreen   = "2"
	colorYellow  = "3"
	colorCyan    = "6"
	colorGray    = "8"
	colorDefault = ""
)

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// buildStyle maps markdownTheme onto glamour's ansi.StyleConfig.
func buildStyle(plain bool) glansi.StyleConfig {
	if plain {
		// Flat text, no colour, no decoration — mirrors SetPlainMode's
		// screen-reader contract for the rest of the package.
		return glansi.StyleConfig{
			Document:  glansi.StyleBlock{},
			List:      glansi.StyleList{StyleBlock: glansi.StyleBlock{}, LevelIndent: 2},
			Item:      glansi.StylePrimitive{BlockPrefix: "- "},
			Heading:   glansi.StyleBlock{StylePrimitive: glansi.StylePrimitive{Bold: boolPtr(true), BlockSuffix: "\n"}},
			Link:      glansi.StylePrimitive{Underline: boolPtr(true)},
			LinkText:  glansi.StylePrimitive{},
			Code:      glansi.StyleBlock{},
			CodeBlock: glansi.StyleCodeBlock{StyleBlock: glansi.StyleBlock{Indent: uintPtr(2)}},
			BlockQuote: glansi.StyleBlock{
				Indent:      uintPtr(1),
				IndentToken: strPtr("| "),
			},
			HorizontalRule: glansi.StylePrimitive{Format: "\n---\n"},
			Strong:         glansi.StylePrimitive{Bold: boolPtr(true)},
			Emph:           glansi.StylePrimitive{Italic: boolPtr(true)},
			Strikethrough:  glansi.StylePrimitive{CrossedOut: boolPtr(true)},
		}
	}

	return glansi.StyleConfig{
		Document: glansi.StyleBlock{},
		List:     glansi.StyleList{StyleBlock: glansi.StyleBlock{}, LevelIndent: 2},
		// listBullet: (t) => green(t) — item's own bullet marker (glamour's
		// block_prefix carries no colour of its own, so this is baked into
		// Item's colour, which glamour applies to the whole item including
		// its prefix).
		Item: glansi.StylePrimitive{
			BlockPrefix: "• ",
			Color:       strPtr(colorGreen),
		},
		// heading: (t) => bold(t)
		Heading: glansi.StyleBlock{
			StylePrimitive: glansi.StylePrimitive{Bold: boolPtr(true), BlockSuffix: "\n"},
		},
		// link: (t) => cyan(t), linkUrl: (t) => dim(t)
		Link:     glansi.StylePrimitive{Color: strPtr(colorCyan)},
		LinkText: glansi.StylePrimitive{Faint: boolPtr(true)},
		// code: (t) => yellow(t) — inline code.
		Code: glansi.StyleBlock{
			StylePrimitive: glansi.StylePrimitive{Color: strPtr(colorYellow)},
		},
		// codeBlock: (t) => t (identity: body text keeps the terminal's
		// own colour), codeBlockIndent: "  ", codeBlockBorder: (t) =>
		// dim(t) — glamour draws no border around a code block, only an
		// indent; the closest available mapping is a dim indent token
		// (two spaces, no visible rule) rather than a drawn line.
		CodeBlock: glansi.StyleCodeBlock{
			StyleBlock: glansi.StyleBlock{
				Indent: uintPtr(2),
			},
			Chroma: chromaFromTheme(),
		},
		// quote: (t) => dim(t), quoteBorder: (t) => gray(t) — glamour's
		// nearest equivalent to a drawn quote border is the block's
		// indent token, so the "│ " marker itself carries the gray colour
		// and the quoted text is faint.
		BlockQuote: glansi.StyleBlock{
			StylePrimitive: glansi.StylePrimitive{Faint: boolPtr(true)},
			Indent:         uintPtr(1),
			IndentToken:    strPtr(Gray("│") + " "),
		},
		// hr: (t) => dim(t)
		HorizontalRule: glansi.StylePrimitive{Faint: boolPtr(true), Format: "\n───\n"},
		// bold/italic/strikethrough
		Strong:        glansi.StylePrimitive{Bold: boolPtr(true)},
		Emph:          glansi.StylePrimitive{Italic: boolPtr(true)},
		Strikethrough: glansi.StylePrimitive{CrossedOut: boolPtr(true)},
	}
}

func uintPtr(u uint) *uint { return &u }

// chromaFromTheme is a minimal, terminal-safe chroma palette: enough
// contrast between comments/keywords/strings/numbers to be useful, kept
// close to the ANSI seven this package otherwise uses rather than
// introducing a whole second palette. quick.Highlight falls back to a
// plaintext lexer for an unrecognized fence language, so this only ever
// shows up for a fence glamour can actually match.
// chromaColor converts the package's ANSI colour numbers into the hex form
// chroma's style parser requires (it rejects bare ANSI indices). The values
// are the xterm defaults for those indices.
func chromaColor(ansi string) *string {
	hex := map[string]string{
		"1": "#cd3131", "2": "#0dbc79", "3": "#e5e510", "4": "#2472c8",
		"5": "#bc3fbc", "6": "#11a8cd", "8": "#808080", "9": "#f14c4c",
	}[ansi]
	if hex == "" {
		return nil
	}
	return &hex
}

func chromaFromTheme() *glansi.Chroma {
	dim := func() glansi.StylePrimitive { return glansi.StylePrimitive{Color: chromaColor(colorGray)} }
	return &glansi.Chroma{
		Text:                glansi.StylePrimitive{},
		Error:               glansi.StylePrimitive{Color: chromaColor(colorRed)},
		Comment:             dim(),
		CommentPreproc:      dim(),
		Keyword:             glansi.StylePrimitive{Color: chromaColor(colorCyan)},
		KeywordReserved:     glansi.StylePrimitive{Color: chromaColor(colorCyan)},
		KeywordNamespace:    glansi.StylePrimitive{Color: chromaColor(colorCyan)},
		KeywordType:         glansi.StylePrimitive{Color: chromaColor(colorCyan)},
		Operator:            glansi.StylePrimitive{},
		Punctuation:         glansi.StylePrimitive{},
		Name:                glansi.StylePrimitive{},
		NameBuiltin:         glansi.StylePrimitive{Color: chromaColor(colorGreen)},
		NameTag:             glansi.StylePrimitive{Color: chromaColor(colorGreen)},
		NameAttribute:       glansi.StylePrimitive{Color: chromaColor(colorGreen)},
		NameClass:           glansi.StylePrimitive{Color: chromaColor(colorGreen), Bold: boolPtr(true)},
		NameFunction:        glansi.StylePrimitive{Color: chromaColor(colorGreen)},
		Literal:             glansi.StylePrimitive{Color: chromaColor(colorYellow)},
		LiteralNumber:       glansi.StylePrimitive{Color: chromaColor(colorYellow)},
		LiteralDate:         glansi.StylePrimitive{Color: chromaColor(colorYellow)},
		LiteralString:       glansi.StylePrimitive{Color: chromaColor(colorYellow)},
		LiteralStringEscape: glansi.StylePrimitive{Color: chromaColor(colorYellow)},
		GenericDeleted:      glansi.StylePrimitive{Color: chromaColor(colorRed)},
		GenericEmph:         glansi.StylePrimitive{Italic: boolPtr(true)},
		GenericInserted:     glansi.StylePrimitive{Color: chromaColor(colorGreen)},
		GenericStrong:       glansi.StylePrimitive{Bold: boolPtr(true)},
		GenericSubheading:   dim(),
	}
}

// MarkdownRenderer renders assistant prose to lines fitted to a fixed
// width, caching by (text, width) since the same finished block is
// re-rendered on every repaint until the terminal resizes.
type MarkdownRenderer struct {
	width int
	plain bool

	mu    sync.Mutex
	cache map[string][]string
}

// NewMarkdownRenderer builds a renderer for a fixed width. plain mirrors
// SetPlainMode: no colour, no chroma, ASCII-safe punctuation.
func NewMarkdownRenderer(width int, plain bool) *MarkdownRenderer {
	return &MarkdownRenderer{width: width, plain: plain, cache: make(map[string][]string)}
}

// Render renders text to lines already wrapped to the renderer's width.
func (m *MarkdownRenderer) Render(text string) []string {
	m.mu.Lock()
	if lines, ok := m.cache[text]; ok {
		m.mu.Unlock()
		return lines
	}
	m.mu.Unlock()

	lines := m.render(text)

	m.mu.Lock()
	m.cache[text] = lines
	m.mu.Unlock()
	return lines
}

func (m *MarkdownRenderer) render(text string) []string {
	width := m.width
	if width < 1 {
		width = 1
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(buildStyle(m.plain)),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return strings.Split(text, "\n")
	}
	out, err := r.Render(text)
	if err != nil {
		return strings.Split(text, "\n")
	}
	// glamour pads a trailing blank line onto every document; trimmed so a
	// rendered block doesn't leave an extra empty row in the transcript.
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return []string{}
	}
	lines := strings.Split(out, "\n")

	// glamour also right-pads every line to the word-wrap width (a code
	// block's background/margin box, even with no background colour set);
	// pi-tui's Markdown component never padded to width, only wrapped, so
	// trailing spaces are trimmed here to match — the trim is plain ASCII
	// space removal from the tail, which cannot corrupt an ANSI escape
	// sequence earlier in the line (escape bytes are never spaces).
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}
