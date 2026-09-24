package tui

import (
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

// Render draws up to maxRows item rows plus a "(n/total)" scroll indicator
// when the list is longer than that, the selected row painted in the
// `suggestion` colour — matching SelectList.render/renderItem
// (select-list.js) and app.ts's editorTheme comment on why the selected
// row uses Suggestion rather than a bold/green highlight. Every returned
// line is exactly width columns via VisibleWidth, the same invariant
// width.go documents for every other renderer in this package.
func (p *Popup) Render(width, maxRows int) []string {
	if width < 1 {
		width = 1
	}
	if maxRows < 1 {
		maxRows = 1
	}
	if len(p.Items) == 0 {
		return []string{padTo(Dim("  No matching commands"), width)}
	}

	start, end := visibleRange(p.Selected, len(p.Items), maxRows)
	primaryWidth := primaryColumnWidth(p.Items, width)

	lines := make([]string, 0, maxRows+1)
	for i := start; i < end; i++ {
		lines = append(lines, padTo(renderItem(p.Items[i], i == p.Selected, width, primaryWidth), width))
	}
	if start > 0 || end < len(p.Items) {
		scroll := "  (" + itoa(p.Selected+1) + "/" + itoa(len(p.Items)) + ")"
		lines = append(lines, padTo(Dim(truncateToWidth(scroll, width-2)), width))
	}
	return lines
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
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

// primaryColumnWidth is SelectList.getPrimaryColumnWidth: the widest label
// plus a gap, clamped to [1, defaultPrimaryColumnWidth] (this port has no
// layout override, so min==max==32 the way pi-tui's own default bounds
// collapse when minPrimaryColumnWidth/maxPrimaryColumnWidth are unset).
func primaryColumnWidth(items []AutocompleteItem, _ int) int {
	widest := 0
	for _, it := range items {
		if w := VisibleWidth(displayValue(it)) + primaryColumnGap; w > widest {
			widest = w
		}
	}
	if widest < 1 {
		widest = 1
	}
	if widest > defaultPrimaryColumnWidth {
		widest = defaultPrimaryColumnWidth
	}
	return widest
}

// renderItem is SelectList.renderItem: "→ " / "  " prefix, then either a
// two-column value+description layout (when width > 40 and a description
// exists) or a single truncated value.
func renderItem(item AutocompleteItem, selected bool, width, primaryWidth int) string {
	prefix := "  "
	if selected {
		prefix = "→ "
	}
	prefixWidth := VisibleWidth(prefix)

	desc := normalizeToSingleLine(item.Description)
	if desc != "" && width > 40 {
		effective := primaryWidth
		if max := width - prefixWidth - 4; effective > max {
			effective = max
		}
		if effective < 1 {
			effective = 1
		}
		maxPrimary := effective - primaryColumnGap
		if maxPrimary < 1 {
			maxPrimary = 1
		}
		value := truncateToWidth(displayValue(item), maxPrimary)
		spacing := strings.Repeat(" ", max0(effective-VisibleWidth(value)))
		descStart := prefixWidth + VisibleWidth(value) + len(spacing)
		remaining := width - descStart - 2
		if remaining > minDescriptionWidth {
			truncDesc := truncateToWidth(desc, remaining)
			if selected {
				return Suggestion(prefix + value + spacing + truncDesc)
			}
			return prefix + value + spacing + Dim(truncDesc)
		}
	}

	maxWidth := width - prefixWidth - 2
	if maxWidth < 1 {
		maxWidth = 1
	}
	value := truncateToWidth(displayValue(item), maxWidth)
	if selected {
		return Suggestion(prefix + value)
	}
	return prefix + value
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
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

// slashCommandSuggestions is CombinedAutocompleteProvider's command-name
// branch of getSuggestions: build one AutocompleteItem per command (label
// is "/name argHint", description is "argHint — description" or just
// whichever half exists), then fuzzy-filter by prefix.
func slashCommandSuggestions(reg *commands.Registry, prefix string) []AutocompleteItem {
	list := reg.List()
	type candidate struct {
		name string
		item AutocompleteItem
	}
	candidates := make([]candidate, 0, len(list))
	for _, c := range list {
		name := commands.QualifiedName(c)
		desc := c.Description
		if c.ArgumentHint != "" {
			if desc != "" {
				desc = c.ArgumentHint + " — " + desc
			} else {
				desc = c.ArgumentHint
			}
		}
		candidates = append(candidates, candidate{
			name: name,
			item: AutocompleteItem{Value: name, Label: name, Description: desc},
		})
	}

	getText := func(name string) string {
		if !strings.HasPrefix(prefix, "skill:") && slashCommandRe.MatchString(name) {
			return name[len("skill:"):]
		}
		return name
	}

	type scored struct {
		item  AutocompleteItem
		score float64
	}
	var matched []scored
	for _, c := range candidates {
		if prefix == "" {
			matched = append(matched, scored{c.item, 0})
			continue
		}
		if score, ok := fuzzyFilterOne(prefix, getText(c.name)); ok {
			matched = append(matched, scored{c.item, score})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].score < matched[j].score })
	out := make([]AutocompleteItem, len(matched))
	for i, m := range matched {
		out[i] = m.item
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

// fuzzyFilterOne applies fuzzyFilter's whitespace/slash tokenization to a
// single (query, text) pair: every token must match text, and the total
// score is their sum.
func fuzzyFilterOne(query, text string) (score float64, ok bool) {
	tokens := tokenizeFuzzyQuery(query)
	if len(tokens) == 0 {
		return 0, true
	}
	var total float64
	for _, tok := range tokens {
		s, matched := fuzzyMatch(tok, text)
		if !matched {
			return 0, false
		}
		total += s
	}
	return total, true
}

var fuzzyTokenRe = regexp.MustCompile(`[\s/]+`)

func tokenizeFuzzyQuery(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	parts := fuzzyTokenRe.Split(query, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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
