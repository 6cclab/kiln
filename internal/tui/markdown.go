package tui

import (
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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

// The colour entries markdownTheme maps onto (colorRed, colorGreen, ...)
// used to be package-level consts pointing at the design's fixed dark-theme
// hexes (hexRed, hexGreen, ...) baked in at compile time — exactly the bug
// this file was flagged for: SetTerminalBackground recomputes the text
// tokens' *style()* helpers for a light background, but a const string
// never sees that. buildStyle now reads theme.CurrentTextHex() each call
// instead, so assistant markdown follows the same background-aware tokens
// as the rest of the transcript.

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// buildStyle maps markdownTheme onto glamour's ansi.StyleConfig.
func buildStyle(plain bool) glansi.StyleConfig {
	tx := CurrentTextHex()
	colorGreen := tx.Green  // list bullets, insertions, names
	colorYellow := tx.Amber // inline code, literals/strings/numbers
	colorCyan := tx.Blue    // links, keywords
	if plain {
		// Flat text, no colour, no decoration — mirrors SetPlainMode's
		// screen-reader contract for the rest of the package.
		return glansi.StyleConfig{
			Document: glansi.StyleBlock{},
			List:     glansi.StyleList{StyleBlock: glansi.StyleBlock{}, LevelIndent: 2},
			Item:     glansi.StylePrimitive{BlockPrefix: "- "},
			// See the non-plain branch below: with no Enumeration set,
			// glamour glues the numeral straight onto the item text.
			Enumeration: glansi.StylePrimitive{Suffix: ". "},
			Heading:     glansi.StyleBlock{StylePrimitive: glansi.StylePrimitive{Bold: boolPtr(true), BlockSuffix: "\n"}},
			Link:        glansi.StylePrimitive{Underline: boolPtr(true)},
			LinkText:    glansi.StylePrimitive{},
			Code:        glansi.StyleBlock{},
			CodeBlock:   glansi.StyleCodeBlock{StyleBlock: glansi.StyleBlock{Indent: uintPtr(2)}},
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
		// Ordered list marker: glamour renders the numeral itself as the
		// ItemElement's own Prefix (ansi/listitem.go), unstyled and with no
		// separator, so with no Enumeration style configured the number is
		// glued straight onto the item text ("1Point me..."). Suffix runs
		// right after that numeral (ansi/baseelement.go's doRender renders
		// Style.Suffix immediately after the token, before BlockSuffix), so
		// ". " here reproduces the bullet's "marker + space" shape as
		// "1. ". Colour matches the bullet's listBullet mapping above.
		Enumeration: glansi.StylePrimitive{
			Suffix: ". ",
			Color:  strPtr(colorGreen),
		},
		// heading: (t) => bold(t)
		Heading: glansi.StyleBlock{
			StylePrimitive: glansi.StylePrimitive{Bold: boolPtr(true), BlockSuffix: "\n"},
		},
		// link: (t) => cyan(t), linkUrl: (t) => dim(t). glamour's LinkText
		// styles the label and Link the URL printed after it.
		LinkText: glansi.StylePrimitive{Color: strPtr(colorCyan)},
		Link:     glansi.StylePrimitive{Color: strPtr(tx.Dim)},
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
			Chroma: chromaFromTheme(tx),
		},
		// quote: (t) => dim(t), quoteBorder: (t) => gray(t) — glamour's
		// nearest equivalent to a drawn quote border is the block's
		// indent token, so the "│ " marker itself carries the gray colour
		// and the quoted text is faint.
		BlockQuote: glansi.StyleBlock{
			StylePrimitive: glansi.StylePrimitive{Faint: boolPtr(true)},
			Indent:         uintPtr(1),
			IndentToken:    strPtr(Muted("│") + " "),
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
func chromaColor(c string) *string {
	if c == "" {
		return nil
	}
	// kiln colours are already hex; pass them through. (Legacy ANSI
	// indices, if any remain, map to their xterm defaults.)
	if strings.HasPrefix(c, "#") {
		hex := c
		return &hex
	}
	hex := map[string]string{
		"1": "#cd3131", "2": "#0dbc79", "3": "#e5e510", "4": "#2472c8",
		"5": "#bc3fbc", "6": "#11a8cd", "8": "#808080", "9": "#f14c4c",
	}[c]
	if hex == "" {
		return nil
	}
	return &hex
}

func chromaFromTheme(tx TextHex) *glansi.Chroma {
	dim := func() glansi.StylePrimitive { return glansi.StylePrimitive{Color: chromaColor(tx.Dim)} }
	return &glansi.Chroma{
		Text:                glansi.StylePrimitive{},
		Error:               glansi.StylePrimitive{Color: chromaColor(tx.Red)},
		Comment:             dim(),
		CommentPreproc:      dim(),
		Keyword:             glansi.StylePrimitive{Color: chromaColor(tx.Blue)},
		KeywordReserved:     glansi.StylePrimitive{Color: chromaColor(tx.Blue)},
		KeywordNamespace:    glansi.StylePrimitive{Color: chromaColor(tx.Blue)},
		KeywordType:         glansi.StylePrimitive{Color: chromaColor(tx.Blue)},
		Operator:            glansi.StylePrimitive{},
		Punctuation:         glansi.StylePrimitive{},
		Name:                glansi.StylePrimitive{},
		NameBuiltin:         glansi.StylePrimitive{Color: chromaColor(tx.Green)},
		NameTag:             glansi.StylePrimitive{Color: chromaColor(tx.Green)},
		NameAttribute:       glansi.StylePrimitive{Color: chromaColor(tx.Green)},
		NameClass:           glansi.StylePrimitive{Color: chromaColor(tx.Green), Bold: boolPtr(true)},
		NameFunction:        glansi.StylePrimitive{Color: chromaColor(tx.Green)},
		Literal:             glansi.StylePrimitive{Color: chromaColor(tx.Amber)},
		LiteralNumber:       glansi.StylePrimitive{Color: chromaColor(tx.Amber)},
		LiteralDate:         glansi.StylePrimitive{Color: chromaColor(tx.Amber)},
		LiteralString:       glansi.StylePrimitive{Color: chromaColor(tx.Amber)},
		LiteralStringEscape: glansi.StylePrimitive{Color: chromaColor(tx.Amber)},
		GenericDeleted:      glansi.StylePrimitive{Color: chromaColor(tx.Red)},
		GenericEmph:         glansi.StylePrimitive{Italic: boolPtr(true)},
		GenericInserted:     glansi.StylePrimitive{Color: chromaColor(tx.Green)},
		GenericStrong:       glansi.StylePrimitive{Bold: boolPtr(true)},
		GenericSubheading:   dim(),
	}
}

// MarkdownRenderer renders assistant prose to lines fitted to a fixed
// width, caching by (text, width) since the same finished block is
// re-rendered on every repaint until the terminal resizes.
//
// The cache also has to account for a third axis: buildStyle's colours
// (colorRed etc., ultimately theme.go's KilnAmber/KilnGreen/... tokens) can
// change out from under it when SetTerminalBackground runs — a light-bg
// terminal recolouring every text token after some assistant prose has
// already been rendered and cached with the pre-detection dark-design
// hexes baked into its ANSI escapes. genAtCache pins each cached entry to
// theme.ThemeGeneration() at render time; a generation mismatch is treated
// as a cache miss so a background-change re-renders every previously
// cached block instead of serving stale colours forever.
type MarkdownRenderer struct {
	width int
	plain bool

	mu       sync.Mutex
	cache    map[string][]string
	cacheGen int
}

// NewMarkdownRenderer builds a renderer for a fixed width. plain mirrors
// SetPlainMode: no colour, no chroma, ASCII-safe punctuation.
func NewMarkdownRenderer(width int, plain bool) *MarkdownRenderer {
	return &MarkdownRenderer{width: width, plain: plain, cache: make(map[string][]string)}
}

// Render renders text to lines already wrapped to the renderer's width.
func (m *MarkdownRenderer) Render(text string) []string {
	gen := ThemeGeneration()

	m.mu.Lock()
	if m.cacheGen != gen {
		// The active token set changed since anything currently cached was
		// rendered (or this is the first render) — every cached entry may
		// have the wrong colours baked in, so drop them all rather than
		// track staleness per entry.
		m.cache = make(map[string][]string)
		m.cacheGen = gen
	}
	if lines, ok := m.cache[text]; ok {
		m.mu.Unlock()
		return lines
	}
	m.mu.Unlock()

	lines := m.render(text)

	m.mu.Lock()
	if m.cacheGen == gen {
		m.cache[text] = lines
	}
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
	// glamour pads a trailing blank line onto every document, and a leading
	// one whenever the first block is not a paragraph or heading (its
	// NewElement hardcodes Entering "\n" for blockquotes, lists, tables, code
	// blocks and rules without checking for a first child). Trimming blank
	// edges covers every block kind; the body then starts on the row under
	// the block label.
	lines = trimBlankLines(lines)
	lines = hangListItems(lines, width, m.plain)
	return compactTables(lines, m.plain)
}

// listMarker matches a rendered list item's leading indent and marker:
// "• " (or the plain-mode "- ") and ordered "12. ".
var listMarker = regexp.MustCompile(`^( *)(• |- |\d+\. )`)

// hangListItems finishes glamour's list output:
//   - each item is re-wrapped so its wrapped rows line up under the item's
//     text, not its marker (glamour wraps an item as one block at the
//     list's own indent, so a long item's second row started back at the
//     bullet's column). A continuation row is a non-blank row at the item's
//     own indent that is not itself a marker row; deeper rows (a nested
//     list, a code block) and blank rows end the item;
//   - the marker is drawn green (listBullet), which glamour's Item colour
//     never reached (it styles the text, not the block prefix);
//   - a run of blank rows right after a list collapses to one: a nested
//     list and its parent each close with their own blank row. Only after
//     a list row, so blank lines inside a code block are left alone.
func hangListItems(lines []string, width int, plain bool) []string {
	out := make([]string, 0, len(lines))
	afterList := false
	for i := 0; i < len(lines); i++ {
		m := listMarker.FindStringSubmatch(ansi.Strip(lines[i]))
		if m == nil {
			if lines[i] == "" && afterList && len(out) > 0 && out[len(out)-1] == "" {
				continue
			}
			if lines[i] != "" {
				afterList = false
			}
			out = append(out, lines[i])
			continue
		}
		afterList = true
		lead, hang := len(m[1]), len(m[1])+utf8.RuneCountInString(m[2])
		body := []string{ansi.TruncateLeft(lines[i], hang, "")}
		j := i + 1
		for ; j < len(lines); j++ {
			plain := ansi.Strip(lines[j])
			if strings.TrimSpace(plain) == "" || len(plain)-len(strings.TrimLeft(plain, " ")) != lead || listMarker.MatchString(plain) {
				break
			}
			body = append(body, ansi.TruncateLeft(lines[j], lead, ""))
		}
		prefix := m[1] + m[2]
		if !plain {
			prefix = m[1] + KilnGreen(strings.TrimRight(m[2], " ")) + " "
		}
		if j == i+1 || width-hang < 8 {
			out = append(out, prefix+body[0])
			continue
		}
		wrapped := strings.Split(ansi.Wrap(strings.Join(body, " "), width-hang, ""), "\n")
		for k, row := range wrapped {
			if k == 0 {
				out = append(out, prefix+row)
			} else {
				out = append(out, strings.Repeat(" ", hang)+row)
			}
		}
		i = j - 1
	}
	return out
}

// trimBlankLines drops leading and trailing empty lines.
func trimBlankLines(lines []string) []string {
	start, end := 0, len(lines)
	for start < end && lines[start] == "" {
		start++
	}
	for end > start && lines[end-1] == "" {
		end--
	}
	return lines[start:end]
}

// Table glyphs glamour draws with its default table style. glamour has no
// Table entry in ansi.StyleConfig to override (buildStyle's plain branch
// above only reaches List/Heading/BlockQuote/... — there is nothing to set
// for tables), so its raw output always uses these regardless of plain
// mode; compactTable below parses glamour's own rows against these three
// but writes tableColSepPlain/tableRuleRunPlain/tableCrossPlain back out
// in plain mode instead, or a table would be the one place screen-reader
// mode still drew box-drawing characters (defect *screen-reader-mode-
// leaves-box-drawing-rules).
const (
	tableColSep  = "│"
	tableRuleRun = "─"
	tableCross   = "┼"
)

// tableColSepPlain, tableRuleRunPlain and tableCrossPlain are what
// compactTable writes in place of tableColSep/tableRuleRun/tableCross when
// rendering in plain mode.
const (
	tableColSepPlain  = "|"
	tableRuleRunPlain = "-"
	tableCrossPlain   = "+"
)

// compactTables shrinks every table glamour rendered to its content width
// and draws its rules in the hairline Rule colour. glamour sizes a table to
// the full word-wrap width, spreading the columns across the screen, and
// leaves the rules in the terminal's default foreground, the loudest line
// on screen. Cells are kept as glamour styled them (inline code, bold,
// links); only the padding glamour added around them is removed.
func compactTables(lines []string, plain bool) []string {
	for i := 0; i < len(lines); i++ {
		cols := tableRuleColumns(lines[i])
		if cols < 2 {
			continue
		}
		start := i
		for start > 0 && strings.Count(lines[start-1], tableColSep) == cols-1 {
			start--
		}
		end := i + 1
		for end < len(lines) && strings.Count(lines[end], tableColSep) == cols-1 {
			end++
		}
		compactTable(lines[start:end], i-start, cols, plain)
		i = end - 1
	}
	return lines
}

// tableRuleColumns reports the column count of a table's header rule
// ("────┼────"), or 0 when line is not one.
func tableRuleColumns(line string) int {
	if !strings.Contains(line, tableCross) {
		return 0
	}
	rest := strings.ReplaceAll(strings.ReplaceAll(line, tableRuleRun, ""), tableCross, "")
	if strings.TrimSpace(rest) != "" {
		return 0
	}
	return strings.Count(line, tableCross) + 1
}

// compactTable rewrites one table's rows in place. rule is the index of the
// header rule within rows.
func compactTable(rows []string, rule, cols int, plain bool) {
	cells := make([][]string, len(rows))
	for r, row := range rows {
		if r == rule {
			cells[r] = strings.Split(row, tableCross)
		} else {
			cells[r] = strings.Split(row, tableColSep)
		}
	}
	// Per column, the padding every row shares on each side, less the one
	// space kept between a cell and a divider.
	trimL := make([]int, cols)
	trimR := make([]int, cols)
	for c := 0; c < cols; c++ {
		trimL[c], trimR[c] = -1, -1
		for r := range rows {
			if r == rule {
				continue
			}
			cell := cells[r][c]
			if strings.TrimSpace(cell) == "" {
				continue
			}
			l := len(cell) - len(strings.TrimLeft(cell, " "))
			rt := len(cell) - len(strings.TrimRight(cell, " "))
			if c == cols-1 {
				rt = 1 << 30 // trailing spaces were trimmed; nothing to align against
			}
			if trimL[c] < 0 || l < trimL[c] {
				trimL[c] = l
			}
			if trimR[c] < 0 || rt < trimR[c] {
				trimR[c] = rt
			}
		}
		if trimL[c] < 0 {
			trimL[c], trimR[c] = 0, 0
		}
		trimL[c] = max(trimL[c]-1, 0)
		if c == cols-1 {
			trimR[c] = 0
		} else {
			trimR[c] = max(trimR[c]-1, 0)
		}
	}
	colour := func(s string) string {
		if plain {
			return s
		}
		return Rule(s)
	}
	cross, ruleRun, colSep := tableCross, tableRuleRun, tableColSep
	if plain {
		cross, ruleRun, colSep = tableCrossPlain, tableRuleRunPlain, tableColSepPlain
	}
	for r := range rows {
		var b strings.Builder
		for c, cell := range cells[r] {
			if r == rule {
				n := utf8.RuneCountInString(cell) - trimL[c] - trimR[c]
				if c == cols-1 {
					n = maxCellWidth(cells, rule, c, trimL[c])
				}
				if c > 0 {
					b.WriteString(colour(cross))
				}
				b.WriteString(colour(strings.Repeat(ruleRun, max(n, 1))))
				continue
			}
			if c > 0 {
				b.WriteString(colour(colSep))
			}
			cut := cell
			if strings.TrimSpace(cut) != "" {
				cut = cut[min(trimL[c], len(cut)):]
				cut = cut[:len(cut)-min(trimR[c], len(cut))]
			} else {
				w := len(cut) - trimL[c] - trimR[c]
				cut = strings.Repeat(" ", max(w, 0))
			}
			b.WriteString(cut)
		}
		rows[r] = strings.TrimRight(b.String(), " ")
	}
}

// maxCellWidth is the widest visible cell in column c after compaction,
// plus one space of padding, sizing the last column's stretch of rule.
func maxCellWidth(cells [][]string, rule, c, trimL int) int {
	w := 0
	for r := range cells {
		if r == rule || c >= len(cells[r]) {
			continue
		}
		cell := strings.TrimRight(cells[r][c], " ")
		w = max(w, lipgloss.Width(cell[min(trimL, len(cell)):]))
	}
	return w + 1
}
