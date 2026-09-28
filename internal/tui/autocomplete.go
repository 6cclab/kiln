package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/andrepato/harness/internal/commands"
)

// The `/` and `@` autocomplete popup.
//
// Ported from pi-tui's CombinedAutocompleteProvider (autocomplete.js) and
// SelectList (components/select-list.js), read from
// node_modules/@earendil-works/pi-tui/dist/ in the TypeScript oracle
// checkout. Two simplifications from that source, called out where they
// matter below:
//
//   - autocompleteSeparatorRegex there is "whitespace or CJK punctuation";
//     this port only treats ASCII/Unicode whitespace as a token boundary.
//   - `fd` is not available as a Go dependency, so `@` file completion
//     walks the filesystem directly (filepath.WalkDir) instead of
//     shelling out, then applies the same scoreEntry/sort the oracle uses
//     once it has fd's results.

// AutocompleteKind says which of the three completion modes produced the
// popup, which decides how Accept splices its chosen item into the line.
type AutocompleteKind int

const (
	KindSlashCommand AutocompleteKind = iota
	KindArgument
	KindFile
)

// AutocompleteItem is one row in the popup — pi-tui's AutocompleteItem
// shape (value/label/description).
type AutocompleteItem struct {
	// Value is the literal replacement text for the completed token,
	// without the leading "/" or "@" — Accept adds those back per Kind,
	// matching applyCompletion's own per-kind assembly.
	Value       string
	Label       string
	Description string
	// Hint is a slash command's argument hint ("<path>"), drawn dim after
	// the name in the name column so every description starts at the
	// same column.
	Hint string
}

// Popup is the pure, TUI-framework-free autocomplete state: which items
// match, which one is selected, and where in the line the accepted value
// gets spliced back in. app.go owns wiring it to keystrokes and the
// editor; nothing here touches Bubbletea.
type Popup struct {
	Kind     AutocompleteKind
	Items    []AutocompleteItem
	Selected int

	// Line/Start/End identify the rune range on the current line that
	// Accept replaces — [Start,End) — mirroring applyCompletion's
	// beforePrefix/afterCursor split.
	Start, End int
}

// Move changes the selection by delta, wrapping at both ends — matching
// SelectList.handleInput's up/down wrap-around (select-list.js).
func (p *Popup) Move(delta int) {
	n := len(p.Items)
	if n == 0 {
		return
	}
	p.Selected = ((p.Selected+delta)%n + n) % n
}

// SelectedItem returns the currently highlighted item, if any.
func (p *Popup) SelectedItem() (AutocompleteItem, bool) {
	if p.Selected < 0 || p.Selected >= len(p.Items) {
		return AutocompleteItem{}, false
	}
	return p.Items[p.Selected], true
}

// Accept returns the replacement text for [Start,End) and the column the
// cursor should land at afterward, given Kind's own assembly rule
// (applyCompletion, autocomplete.js):
//
//   - KindSlashCommand: "/" + value + " " (a command name always gets a
//     trailing space so the next thing typed is its first argument).
//   - KindArgument: value alone (no slash, no forced trailing space —
//     command-specific completions decide their own punctuation).
//   - KindFile: "@" + value, with a trailing space unless the value is a
//     directory (so the user can keep completing deeper into it) — value
//     already carries pi-tui's own trailing "/" for directories.
func (p *Popup) Accept() (replacement string, cursorCol int) {
	item, ok := p.SelectedItem()
	if !ok {
		return "", p.Start
	}
	switch p.Kind {
	case KindSlashCommand:
		replacement = "/" + item.Value + " "
	case KindArgument:
		replacement = item.Value
	case KindFile:
		replacement = "@" + item.Value
		if !strings.HasSuffix(item.Value, "/") {
			replacement += " "
		}
	}
	return replacement, p.Start + len([]rune(replacement))
}

// Render draws up to maxRows item rows the way Claude Code lays its
// suggestions out (docs/claude-code-reference.md §4, autocomplete-slash.txt
// rows 29-32, autocomplete-at.txt rows 9-13):
//
//	/model                                  Set the AI model … (currently Opus 5 (1M
//	                                        context))
//	+ math.js
//
// Two-space indent, the value column popupValueColumn wide, the description
// wrapped in the remaining width (two columns short of the edge) onto at
// most popupDescRows rows, the last one ending in "…" when clipped. File
// items are "+ <path>", middle-truncated to keep the file name. The
// selected row is painted in the suggestion colour; there is no marker
// glyph. Every returned line is exactly width columns via VisibleWidth.
func (p *Popup) Render(width, maxRows int) []string {
	if width < 1 {
		width = 1
	}
	if maxRows < 1 {
		maxRows = 1
	}
	if len(p.Items) == 0 {
		return []string{padTo(Muted("  No matching commands"), width)}
	}

	// A clipped list gives its last row to a count of what is hidden, so
	// the list never reads as complete when it is not.
	items := maxRows
	clipped := len(p.Items) > maxRows && maxRows >= 3
	if clipped {
		items = maxRows - 1
	}
	start, end := visibleRange(p.Selected, len(p.Items), items)
	column := slashColumnFor(p.Items)
	var lines []string
	for i := start; i < end; i++ {
		selected := i == p.Selected
		for _, row := range renderItem(p.Kind, p.Items[i], selected, width, column) {
			if !selected {
				lines = append(lines, padTo(row, width))
				continue
			}
			// renderItem already raises every span of a selected row (see
			// its doc comment); pad the remainder the same way instead of
			// padTo-then-OnRaise, which would wrap a background around a
			// string whose own inner spans already ended in their own
			// resets, losing the background at the first one — the same
			// bug RenderUserMessageMeta and permissionOptionRow had.
			if pad := width - VisibleWidth(row); pad > 0 {
				row += onRaiseSpan("", strings.Repeat(" ", pad))
			}
			lines = append(lines, row)
		}
	}
	if clipped {
		hidden := len(p.Items) - (end - start)
		lines = append(lines, padTo(Faint(fmt.Sprintf("  %d more · keep typing to narrow", hidden)), width))
	}
	return lines
}

// raiseGap renders n literal spaces, on the raised background when
// selected — the bare indent/fill spans in renderItem/renderSlashCommandItem
// that sit between (or before) a coloured value/description need the same
// background as those spans do, or the raised row reads with gaps in it.
func raiseGap(selected bool, n int) string {
	if n <= 0 {
		return ""
	}
	sp := strings.Repeat(" ", n)
	if selected {
		return onRaiseSpan("", sp)
	}
	return sp
}

// popupValueColumn is where a slash command's description starts: two
// columns of indent plus a 40-column value column (autocomplete-slash.txt).
const popupValueColumn = 42

// popupDescRows caps a description at two rows, the second clipped with
// "…" (autocomplete-slash.txt row 32: "… Use whe…").
const popupDescRows = 2

// renderItem renders one item as one or more rows, per kiln's selection
// model: the selected row's command/value is amber and the whole row sits
// on the raised background (built with onRaiseSpan — see its doc comment —
// since the value and the description are two different foreground
// colours over that background); unselected rows show the command in ink
// and the description dimmed, no background.
func renderItem(kind AutocompleteKind, item AutocompleteItem, selected bool, width, column int) []string {
	paintValue := func(s string) string {
		if selected {
			return onRaiseSpan(textHex.Amber, s)
		}
		return Ink(s)
	}
	paintDesc := func(s string) string {
		if selected {
			return onRaiseSpan(textHex.Dim, s)
		}
		return Muted(s)
	}
	lead := raiseGap(selected, 2)
	if kind == KindFile {
		return []string{lead + paintValue("+ "+truncateMiddle(displayValue(item), width-4))}
	}
	if kind == KindSlashCommand {
		return renderSlashCommandItem(item, selected, width, column, paintValue, paintDesc)
	}

	value := displayValue(item)
	if kind == KindSlashCommand && !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	desc := normalizeToSingleLine(item.Description)
	descWidth := width - popupValueColumn - 2
	if desc == "" || descWidth < minDescriptionWidth {
		return []string{lead + paintValue(truncateToWidth(value, width-2))}
	}
	value = truncateToWidth(value, popupValueColumn-2-1)
	first := lead + paintValue(value) + raiseGap(selected, popupValueColumn-2-VisibleWidth(value))
	descRows := wrapPlain(desc, descWidth)
	if len(descRows) > popupDescRows {
		descRows = descRows[:popupDescRows]
		last := descRows[popupDescRows-1]
		descRows[popupDescRows-1] = truncateToWidth(last, descWidth-1) + "…"
	}
	rows := make([]string, 0, len(descRows))
	for i, d := range descRows {
		if i == 0 {
			rows = append(rows, first+paintDesc(d))
			continue
		}
		rows = append(rows, raiseGap(selected, popupValueColumn)+paintDesc(d))
	}
	return rows
}

// slashCommandColumn is the narrowest command column: the design pads the
// command to 10 columns (docs/kiln-design-handoff/README.md "Palette",
// `%-10s`). slashColumnFor widens it to the longest command-and-hint in
// the list, up to slashCommandColumnMax, so descriptions share one column
// instead of each long name (or "<path>" hint) pushing its own out.
const (
	slashCommandColumn    = 10
	slashCommandColumnMax = 28
)

// slashColumnFor is the command column for a list of slash items.
func slashColumnFor(items []AutocompleteItem) int {
	col := slashCommandColumn
	for _, it := range items {
		w := 1 + VisibleWidth(strings.TrimPrefix(displayValue(it), "/"))
		if it.Hint != "" {
			w += 1 + VisibleWidth(it.Hint)
		}
		if w+1 <= slashCommandColumnMax {
			col = max(col, w+1)
		}
	}
	return col
}

// renderSlashCommandItem renders one `/` popup row: two-space indent, the
// command padded to slashCommandColumn (amber when selected, ink
// otherwise), then the description (dim), truncated — never wrapped — to
// fit width. When selected, paintValue/paintDesc (renderItem) already carry
// the raised background themselves.
func renderSlashCommandItem(item AutocompleteItem, selected bool, width, column int, paintValue, paintDesc func(string) string) []string {
	value := displayValue(item)
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	shown := VisibleWidth(value)
	line := raiseGap(selected, 2) + paintValue(value)
	if item.Hint != "" {
		line += raiseGap(selected, 1) + paintDesc(item.Hint)
		shown += 1 + VisibleWidth(item.Hint)
	}
	gap := 1 // overflow: one space before the description
	if shown < column {
		gap = column - shown
	}
	line += raiseGap(selected, gap)
	padded := strings.Repeat(" ", shown+gap)
	desc := normalizeToSingleLine(item.Description)
	if desc == "" {
		return []string{line}
	}
	descWidth := width - 2 - VisibleWidth(padded)
	if descWidth < 1 {
		return []string{line}
	}
	if VisibleWidth(desc) > descWidth {
		desc = ansi.Truncate(desc, descWidth, "…") // cut, and say so
	}
	return []string{line + paintDesc(desc)}
}

// truncateMiddle keeps the last path segment and clips the front with "…"
// when s is wider than width (autocomplete-at.txt row 13).
func truncateMiddle(s string, width int) string {
	if width < 1 {
		return ""
	}
	if VisibleWidth(s) <= width {
		return s
	}
	tail := s
	if i := strings.LastIndex(s, "/"); i > 0 {
		tail = s[i:]
	}
	if VisibleWidth(tail)+1 >= width {
		return truncateToWidth(s, width-1) + "…"
	}
	head := truncateToWidth(s, width-1-VisibleWidth(tail))
	return head + "…" + tail
}

// visibleRange is SelectList.getVisibleRange: a window of maxVisible
// items centred on selected, clamped to the list.
func visibleRange(selected, total, maxVisible int) (start, end int) {
	if total <= maxVisible {
		return 0, total
	}
	start = selected - maxVisible/2
	if start < 0 {
		start = 0
	}
	if start > total-maxVisible {
		start = total - maxVisible
	}
	end = start + maxVisible
	if end > total {
		end = total
	}
	return start, end
}

const (
	defaultPrimaryColumnWidth = 32
	primaryColumnGap          = 2
	minDescriptionWidth       = 10
)

func displayValue(item AutocompleteItem) string {
	if item.Label != "" {
		return item.Label
	}
	return item.Value
}

func normalizeToSingleLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if VisibleWidth(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "")
}

func padTo(s string, width int) string {
	w := VisibleWidth(s)
	if w >= width {
		return truncateToWidth(s, width)
	}
	return s + strings.Repeat(" ", width-w)
}

// --- building a popup from cursor state ------------------------------------

// isSeparator is this port's stand-in for pi-tui's autocompleteSeparatorRegex
// (whitespace or CJK punctuation) — whitespace only, see the file doc
// comment.
func isSeparator(r rune) bool {
	return unicode.IsSpace(r)
}

var pathDelimiters = map[rune]bool{' ': true, '\t': true, '"': true, '\'': true, '=': true}

// findLastDelimiter is findLastDelimiter (autocomplete.js): the rune index
// of the last path-delimiter-or-separator character in text, or -1.
func findLastDelimiter(text []rune) int {
	last := -1
	for i, r := range text {
		if pathDelimiters[r] || isSeparator(r) {
			last = i
		}
	}
	return last
}

// extractAtPrefix is extractAtPrefix (autocomplete.js), minus its quoted
// (`@"..."`) handling — this port does not support quoted mention paths.
func extractAtPrefix(text []rune) (prefix string, tokenStart int, ok bool) {
	last := findLastDelimiter(text)
	start := last + 1
	if start < len(text) && text[start] == '@' {
		return string(text[start:]), start, true
	}
	return "", 0, false
}

// BuildPopup inspects the editor's current line and cursor column and
// returns the popup that should be showing, or nil for none — the Go
// analogue of CombinedAutocompleteProvider.getSuggestions
// (autocomplete.js), called on every keystroke the way editor.js's
// updateAutocomplete is.
func BuildPopup(reg *commands.Registry, cwd string, line string, col int) *Popup {
	runes := []rune(line)
	if col > len(runes) {
		col = len(runes)
	}
	if col < 0 {
		col = 0
	}
	before := runes[:col]
	textBefore := string(before)

	// `@` file mention: checked first, exactly like getSuggestions does.
	if atPrefix, start, ok := extractAtPrefix(before); ok {
		items := fileSuggestions(cwd, strings.TrimPrefix(atPrefix, "@"))
		if len(items) == 0 {
			return nil
		}
		return &Popup{Kind: KindFile, Items: items, Start: start, End: col}
	}

	// `/` slash command / argument completion: only when the line (from
	// column 0) starts with "/", matching getSuggestions's own
	// `textBeforeCursor.startsWith("/")` — not "the token under the
	// cursor starts with /", the whole line up to the cursor.
	if strings.HasPrefix(textBefore, "/") && reg != nil {
		spaceIdx := strings.IndexAny(textBefore, " \t")
		if spaceIdx == -1 {
			prefix := textBefore[1:]
			items := slashCommandSuggestions(reg, prefix)
			if len(items) == 0 {
				return nil
			}
			// Start is 0, not 1: the replacement Accept builds already
			// includes the leading "/" (applyCompletion's own beforePrefix
			// excludes the whole "/name" prefix, slash included — see
			// autocomplete.js's beforePrefix/prefix.length math), so the
			// range being replaced has to cover the line's own leading "/"
			// too, or accepting doubles it into "//name " the way
			// registry.go's Execute doc comment describes as a real bug
			// this port hit once already.
			return &Popup{Kind: KindSlashCommand, Items: items, Start: 0, End: col}
		}
		name := textBefore[1:spaceIdx]
		cmd, ok := reg.Get(name)
		if !ok || cmd.ArgumentCompletions == nil {
			return nil
		}
		argStart := spaceIdx + 1
		argText := textBefore[argStart:]
		completions := cmd.ArgumentCompletions(argText)
		if len(completions) == 0 {
			return nil
		}
		items := make([]AutocompleteItem, len(completions))
		for i, c := range completions {
			items[i] = AutocompleteItem{Value: c.Value, Label: c.Label, Description: c.Description}
		}
		return &Popup{Kind: KindArgument, Items: items, Start: len([]rune(textBefore[:argStart])), End: col}
	}

	return nil
}

// slashCommandRe matches the "skill:" namespace pi-tui strips from the
// fuzzy-match text (but not the displayed label) when the typed prefix is
// not itself a "skill:" query — CombinedAutocompleteProvider's inline
// fuzzyFilter getText callback (autocomplete.js).
var slashCommandRe = regexp.MustCompile(`^skill:`)

// slashCommandSuggestions builds one AutocompleteItem per command (label
// is "/name argHint", description is "argHint — description" or just
// whichever half exists), then filters by prefix on the typed text,
// case-insensitively.
//
// Unlike the "@" file and argument popups (fuzzy — see fuzzyMatch
// below), the slash palette filters by prefix: fuzzy matching let "/co"
// surface "/doctor" (its letters "d-o-c-t-o-r" happen to contain a subject
// out-of-order match for "c"/"o" against pi-tui's fuzzy.js scoring) ahead
// of, or alongside, the commands a user typing "/co" actually means
// ("/context", "/compact") — a slash command's whole point is that the
// user knows its name and is typing it left to right, so prefix is both
// the correct match semantics and the simpler one.
func slashCommandSuggestions(reg *commands.Registry, prefix string) []AutocompleteItem {
	list := reg.List()

	getText := func(name string) string {
		if !strings.HasPrefix(prefix, "skill:") && slashCommandRe.MatchString(name) {
			return name[len("skill:"):]
		}
		return name
	}

	lowerPrefix := strings.ToLower(prefix)
	out := make([]AutocompleteItem, 0, len(list))
	for _, c := range list {
		name := commands.QualifiedName(c)
		if prefix != "" && !strings.HasPrefix(strings.ToLower(getText(name)), lowerPrefix) {
			continue
		}
		out = append(out, AutocompleteItem{Value: name, Label: name, Description: c.Description, Hint: c.ArgumentHint})
	}
	return out
}

// --- fuzzy matching, ported from pi-tui's fuzzy.js --------------------------

// fuzzyMatch mirrors fuzzy.js's fuzzyMatch: lower score is a better match.
// ok is false when query's characters do not all appear, in order, in
// text (including via the alphanumeric/numeric-alpha "swap" fallback that
// file also implements).
func fuzzyMatch(query, text string) (score float64, ok bool) {
	ql := strings.ToLower(query)
	tl := strings.ToLower(text)

	match := func(q string) (float64, bool) {
		if len(q) == 0 {
			return 0, true
		}
		qr := []rune(q)
		tr := []rune(tl)
		if len(qr) > len(tr) {
			return 0, false
		}
		queryIndex := 0
		var s float64
		lastMatch := -1
		consecutive := 0
		for queryIndex < len(qr) {
			idx := indexOfRune(tr, qr[queryIndex], lastMatch+1)
			if idx == -1 {
				break
			}
			isWordBoundary := idx == 0 || isWordBoundaryRune(tr[idx-1])
			if lastMatch == idx-1 {
				consecutive++
				s -= float64(consecutive) * 5
			} else {
				consecutive = 0
				if lastMatch >= 0 {
					s += float64(idx-lastMatch-1) * 2
				}
			}
			if isWordBoundary {
				s -= 10
			}
			s += float64(idx) * 0.1
			lastMatch = idx
			queryIndex++
		}
		if queryIndex < len(qr) {
			return 0, false
		}
		if q == tl {
			s -= 100
		}
		return s, true
	}

	primary, primaryOK := match(ql)
	if primaryOK {
		return primary, true
	}
	swapped := swapAlphaNumeric(ql)
	if swapped == "" {
		return primary, false
	}
	swappedScore, swappedOK := match(swapped)
	if !swappedOK {
		return primary, false
	}
	return swappedScore + 5, true
}

var alphaNumericRe = regexp.MustCompile(`^([a-z]+)([0-9]+)$`)
var numericAlphaRe = regexp.MustCompile(`^([0-9]+)([a-z]+)$`)

func swapAlphaNumeric(q string) string {
	if m := alphaNumericRe.FindStringSubmatch(q); m != nil {
		return m[2] + m[1]
	}
	if m := numericAlphaRe.FindStringSubmatch(q); m != nil {
		return m[2] + m[1]
	}
	return ""
}

func isWordBoundaryRune(r rune) bool {
	switch r {
	case ' ', '-', '_', '.', '/', ':':
		return true
	}
	return false
}

func indexOfRune(rs []rune, target rune, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i < len(rs); i++ {
		if rs[i] == target {
			return i
		}
	}
	return -1
}

// --- `@` file completion -----------------------------------------------------

const (
	fileWalkLimit    = 4000 // cap on entries visited, a stand-in for fd's own speed
	fileResultsLimit = 20   // topEntries.slice(0, 20) in getFuzzyFileSuggestions
)

type fileCandidate struct {
	relPath string // relative to the scoped base directory
	isDir   bool
}

// fileSuggestions is getFuzzyFileSuggestions (autocomplete.js): walk the
// workspace under cwd (optionally scoped to the directory named by a
// query containing "/", via resolveScopedFuzzyQuery there), score every
// entry with scoreEntry, and return the top fileResultsLimit as
// AutocompleteItems whose Value already carries pi-tui's buildCompletionValue
// shape (trailing "/" for directories, "@"-free — BuildPopup/Accept add
// the "@").
func fileSuggestions(cwd, query string) []AutocompleteItem {
	baseDir := cwd
	displayBase := ""
	scanQuery := query
	if idx := strings.LastIndex(query, "/"); idx != -1 {
		displayBase = query[:idx+1]
		scanQuery = query[idx+1:]
		candidate := displayBase
		if strings.HasPrefix(candidate, "~/") {
			home, _ := os.UserHomeDir()
			candidate = filepath.Join(home, candidate[2:])
		} else if !strings.HasPrefix(candidate, "/") {
			candidate = filepath.Join(cwd, candidate)
		}
		info, err := os.Stat(candidate)
		if err != nil || !info.IsDir() {
			return nil
		}
		baseDir = candidate
	}

	entries := walkForCandidates(baseDir, fileWalkLimit)
	type scoredEntry struct {
		fileCandidate
		score float64
	}
	scored := make([]scoredEntry, 0, len(entries))
	for _, e := range entries {
		s := scoreEntry(e.relPath, scanQuery, e.isDir)
		if s <= 0 {
			continue
		}
		scored = append(scored, scoredEntry{e, s})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		di, dj := depthOf(scored[i].relPath), depthOf(scored[j].relPath)
		if di != dj {
			return di < dj
		}
		if len(scored[i].relPath) != len(scored[j].relPath) {
			return len(scored[i].relPath) < len(scored[j].relPath)
		}
		return scored[i].relPath < scored[j].relPath
	})
	if len(scored) > fileResultsLimit {
		scored = scored[:fileResultsLimit]
	}

	out := make([]AutocompleteItem, 0, len(scored))
	for _, e := range scored {
		display := displayBase + e.relPath
		value := display
		label := filepath.Base(e.relPath)
		if e.isDir {
			value += "/"
			label += "/"
		}
		out = append(out, AutocompleteItem{Value: value, Label: label, Description: display})
	}
	return out
}

func depthOf(relPath string) int {
	if relPath == "" {
		return 0
	}
	return strings.Count(relPath, "/")
}

// scoreEntry is scoreEntry (autocomplete.js): higher is better here (this
// port keeps the oracle's sign so "score > 0 keeps it" reads the same),
// sorted descending by fileSuggestions above (the oracle sorts its own
// higher-is-better score descending too, despite fuzzyMatch's unrelated
// lower-is-better convention — two different scoring functions in the same
// file).
func scoreEntry(relPath, query string, isDir bool) float64 {
	name := filepath.Base(relPath)
	lowerName := strings.ToLower(name)
	lowerQuery := strings.ToLower(query)
	var score float64
	switch {
	case query == "":
		score = 1
	case lowerName == lowerQuery:
		score = 100
	case strings.HasPrefix(lowerName, lowerQuery):
		score = 80
	case strings.Contains(lowerName, lowerQuery):
		score = 50
	case strings.Contains(strings.ToLower(relPath), lowerQuery):
		score = 30
	default:
		return 0
	}
	if isDir && score > 0 {
		score += 10
	}
	return score
}

// walkForCandidates recursively lists baseDir's contents (files and
// directories, hidden included, `.git` excluded — matching the oracle's
// fd invocation flags), stopping after limit entries as a stand-in for
// fd's own speed rather than an exact behavioural match.
func walkForCandidates(baseDir string, limit int) []fileCandidate {
	var out []fileCandidate
	visited := 0
	var walk func(dir, relPrefix string) bool // returns false to stop
	walk = func(dir, relPrefix string) bool {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if e.Name() == ".git" {
				continue
			}
			rel := e.Name()
			if relPrefix != "" {
				rel = relPrefix + "/" + e.Name()
			}
			isDir := e.IsDir()
			out = append(out, fileCandidate{relPath: rel, isDir: isDir})
			visited++
			if visited >= limit {
				return false
			}
			if isDir {
				if !walk(filepath.Join(dir, e.Name()), rel) {
					return false
				}
			}
		}
		return true
	}
	walk(baseDir, "")
	return out
}
