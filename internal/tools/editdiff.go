package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// detectLineEnding reports which line ending content predominantly uses at
// its first line break, mirroring edit-diff.js's detectLineEnding: if a
// "\r\n" occurs no later than the first bare "\n", the file is CRLF.
func detectLineEnding(content string) string {
	crlfIdx := strings.Index(content, "\r\n")
	lfIdx := strings.Index(content, "\n")
	if lfIdx == -1 {
		return "\n"
	}
	if crlfIdx == -1 {
		return "\n"
	}
	if crlfIdx < lfIdx {
		return "\r\n"
	}
	return "\n"
}

// normalizeToLF collapses CRLF and bare CR to LF, mirroring edit-diff.js's
// normalizeToLF.
func normalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// restoreLineEndings re-expands LF to the original line ending, mirroring
// edit-diff.js's restoreLineEndings.
func restoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// stripBOM removes a leading UTF-8 BOM if present, returning it separately
// so it can be restored, mirroring edit-diff.js's stripBom.
func stripBOM(content string) (bom, text string) {
	const bomChar = "\xef\xbb\xbf"
	if strings.HasPrefix(content, bomChar) {
		return bomChar, content[len(bomChar):]
	}
	return "", content
}

// smartSingleQuotes, smartDoubleQuotes, unicodeDashes and specialSpaces are
// the exact character classes edit-diff.js's normalizeForFuzzyMatch
// collapses to their ASCII equivalents.
var (
	smartSingleQuotes = regexp.MustCompile("[‘’‚‛]")
	smartDoubleQuotes = regexp.MustCompile("[“”„‟]")
	unicodeDashes     = regexp.MustCompile("[‐‑‒–—―−]")
	specialSpaces     = regexp.MustCompile("[  -   　]")
)

// normalizeForFuzzyMatch normalizes text for fuzzy matching, mirroring
// edit-diff.js's normalizeForFuzzyMatch: strip trailing whitespace from
// each line, then fold smart quotes, Unicode dashes and special spaces to
// their ASCII equivalents.
//
// Deviation: pi first applies Unicode NFKC canonical-compatibility
// normalization (text.normalize("NFKC")), which additionally folds things
// like full-width forms and typographic ligatures. Go's standard library
// has no normalization package and this port does not add
// golang.org/x/text for it, so NFKC is skipped; every explicit
// replacement pi lists (smart quotes, dashes, special spaces, trailing
// whitespace) is still applied.
func normalizeForFuzzyMatch(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r\f\v              　")
	}
	text = strings.Join(lines, "\n")
	text = smartSingleQuotes.ReplaceAllString(text, "'")
	text = smartDoubleQuotes.ReplaceAllString(text, "\"")
	text = unicodeDashes.ReplaceAllString(text, "-")
	text = specialSpaces.ReplaceAllString(text, " ")
	return text
}

// fuzzyMatch is the result of fuzzyFindText, mirroring edit-diff.js's
// return shape.
type fuzzyMatch struct {
	Found                 bool
	Index                 int
	MatchLength           int
	UsedFuzzyMatch        bool
	ContentForReplacement string
}

// fuzzyFindText finds oldText in content, trying an exact match first and
// falling back to a fuzzy match (trailing whitespace, smart quotes,
// Unicode dashes and special spaces normalized away on both sides),
// mirroring edit-diff.js's fuzzyFindText. When fuzzy matching is used, the
// returned ContentForReplacement is the fuzzy-normalized content, and
// Index/MatchLength are offsets into it.
func fuzzyFindText(content, oldText string) fuzzyMatch {
	if exactIndex := strings.Index(content, oldText); exactIndex != -1 {
		return fuzzyMatch{Found: true, Index: exactIndex, MatchLength: len(oldText), ContentForReplacement: content}
	}
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOldText := normalizeForFuzzyMatch(oldText)
	fuzzyIndex := strings.Index(fuzzyContent, fuzzyOldText)
	if fuzzyIndex == -1 {
		return fuzzyMatch{ContentForReplacement: content}
	}
	return fuzzyMatch{
		Found: true, Index: fuzzyIndex, MatchLength: len(fuzzyOldText),
		UsedFuzzyMatch: true, ContentForReplacement: fuzzyContent,
	}
}

func countOccurrences(content, oldText string) int {
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOldText := normalizeForFuzzyMatch(oldText)
	if fuzzyOldText == "" {
		return 0
	}
	return strings.Count(fuzzyContent, fuzzyOldText)
}

// matchedEdit is one edit resolved to a byte range in replacementBaseContent.
type matchedEdit struct {
	EditIndex   int
	MatchIndex  int
	MatchLength int
	NewText     string
}

// applyReplacements applies replacements (sorted by MatchIndex, offsets
// already relative to content) in reverse order so earlier offsets stay
// valid, mirroring edit-diff.js's applyReplacements.
func applyReplacements(content string, replacements []matchedEdit) string {
	result := content
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		result = result[:r.MatchIndex] + r.NewText + result[r.MatchIndex+r.MatchLength:]
	}
	return result
}

type lineSpan struct{ start, end int }

func splitLinesWithEndings(content string) []string {
	if content == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' {
			lines = append(lines, content[start:i+1])
			start = i + 1
		}
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}

func getLineSpans(content string) []lineSpan {
	var spans []lineSpan
	offset := 0
	for _, line := range splitLinesWithEndings(content) {
		span := lineSpan{start: offset, end: offset + len(line)}
		spans = append(spans, span)
		offset = span.end
	}
	return spans
}

func getReplacementLineRange(lines []lineSpan, r matchedEdit) (startLine, endLine int, err error) {
	replacementStart := r.MatchIndex
	replacementEnd := r.MatchIndex + r.MatchLength
	startLine = -1
	for i, line := range lines {
		if replacementStart >= line.start && replacementStart < line.end {
			startLine = i
			break
		}
	}
	if startLine == -1 {
		return 0, 0, fmt.Errorf("replacement range is outside the base content")
	}
	endLine = startLine
	for endLine < len(lines) && lines[endLine].end < replacementEnd {
		endLine++
	}
	if endLine >= len(lines) {
		return 0, 0, fmt.Errorf("replacement range is outside the base content")
	}
	return startLine, endLine + 1, nil
}

// applyReplacementsPreservingUnchangedLines applies replacements matched
// against baseContent to originalContent while preserving unchanged line
// blocks from the original, mirroring edit-diff.js's
// applyReplacementsPreservingUnchangedLines. This is used when baseContent
// is the fuzzy-normalized view of originalContent: only the lines a
// replacement actually touches get rewritten from the normalized text,
// every other line keeps its original bytes.
func applyReplacementsPreservingUnchangedLines(originalContent, baseContent string, replacements []matchedEdit) (string, error) {
	originalLines := splitLinesWithEndings(originalContent)
	baseLines := getLineSpans(baseContent)
	if len(originalLines) != len(baseLines) {
		return "", fmt.Errorf("cannot preserve unchanged lines because the base content has a different line count")
	}

	type group struct {
		startLine, endLine int
		replacements       []matchedEdit
	}
	sorted := append([]matchedEdit(nil), replacements...)
	sortMatchedEdits(sorted)

	var groups []*group
	for _, r := range sorted {
		startLine, endLine, err := getReplacementLineRange(baseLines, r)
		if err != nil {
			return "", err
		}
		if n := len(groups); n > 0 && startLine < groups[n-1].endLine {
			g := groups[n-1]
			if endLine > g.endLine {
				g.endLine = endLine
			}
			g.replacements = append(g.replacements, r)
			continue
		}
		groups = append(groups, &group{startLine: startLine, endLine: endLine, replacements: []matchedEdit{r}})
	}

	originalLineIndex := 0
	var result strings.Builder
	for _, g := range groups {
		for _, line := range originalLines[originalLineIndex:g.startLine] {
			result.WriteString(line)
		}
		groupStartOffset := baseLines[g.startLine].start
		groupEndOffset := baseLines[g.endLine-1].end
		localReplacements := make([]matchedEdit, len(g.replacements))
		for i, r := range g.replacements {
			localReplacements[i] = matchedEdit{
				EditIndex: r.EditIndex, NewText: r.NewText,
				MatchIndex: r.MatchIndex - groupStartOffset, MatchLength: r.MatchLength,
			}
		}
		result.WriteString(applyReplacements(baseContent[groupStartOffset:groupEndOffset], localReplacements))
		originalLineIndex = g.endLine
	}
	for _, line := range originalLines[originalLineIndex:] {
		result.WriteString(line)
	}
	return result.String(), nil
}

func sortMatchedEdits(edits []matchedEdit) {
	for i := 1; i < len(edits); i++ {
		for j := i; j > 0 && edits[j-1].MatchIndex > edits[j].MatchIndex; j-- {
			edits[j-1], edits[j] = edits[j], edits[j-1]
		}
	}
}

// editInput is one requested replacement.
type editInput struct {
	OldText string
	NewText string
}

// applyEditsResult is the outcome of applyEditsToNormalizedContent.
type applyEditsResult struct {
	BaseContent string
	NewContent  string
}

// applyEditsToNormalizedContent applies one or more exact-text
// replacements to LF-normalized content, mirroring edit-diff.js's
// applyEditsToNormalizedContent: every edit is matched against the same
// original content, replacements are applied in reverse offset order so
// earlier offsets stay valid, and if any edit needed fuzzy matching the
// whole operation runs in fuzzy-normalized space and is then overlaid
// back onto the original bytes line by line so untouched lines keep their
// original bytes.
func applyEditsToNormalizedContent(normalizedContent string, edits []editInput, path string) (applyEditsResult, error) {
	normalizedEdits := make([]editInput, len(edits))
	for i, e := range edits {
		normalizedEdits[i] = editInput{OldText: normalizeToLF(e.OldText), NewText: normalizeToLF(e.NewText)}
	}
	for i, e := range normalizedEdits {
		if e.OldText == "" {
			return applyEditsResult{}, emptyOldTextError(path, i, len(normalizedEdits))
		}
	}

	usedFuzzyMatch := false
	for _, e := range normalizedEdits {
		if fuzzyFindText(normalizedContent, e.OldText).UsedFuzzyMatch {
			usedFuzzyMatch = true
			break
		}
	}
	replacementBaseContent := normalizedContent
	if usedFuzzyMatch {
		replacementBaseContent = normalizeForFuzzyMatch(normalizedContent)
	}

	matchedEdits := make([]matchedEdit, 0, len(normalizedEdits))
	for i, e := range normalizedEdits {
		match := fuzzyFindText(replacementBaseContent, e.OldText)
		if !match.Found {
			return applyEditsResult{}, notFoundError(path, i, len(normalizedEdits))
		}
		occurrences := countOccurrences(replacementBaseContent, e.OldText)
		if occurrences > 1 {
			return applyEditsResult{}, duplicateError(path, i, len(normalizedEdits), occurrences)
		}
		matchedEdits = append(matchedEdits, matchedEdit{
			EditIndex: i, MatchIndex: match.Index, MatchLength: match.MatchLength, NewText: e.NewText,
		})
	}
	sortMatchedEdits(matchedEdits)
	for i := 1; i < len(matchedEdits); i++ {
		prev, cur := matchedEdits[i-1], matchedEdits[i]
		if prev.MatchIndex+prev.MatchLength > cur.MatchIndex {
			//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
			return applyEditsResult{}, fmt.Errorf(
				"edits[%d] and edits[%d] overlap in %s. Merge them into one edit or target disjoint regions.",
				prev.EditIndex, cur.EditIndex, path)
		}
	}

	baseContent := normalizedContent
	var newContent string
	if usedFuzzyMatch {
		merged, err := applyReplacementsPreservingUnchangedLines(normalizedContent, replacementBaseContent, matchedEdits)
		if err != nil {
			return applyEditsResult{}, err
		}
		newContent = merged
	} else {
		newContent = applyReplacements(replacementBaseContent, matchedEdits)
	}

	if baseContent == newContent {
		return applyEditsResult{}, noChangeError(path, len(normalizedEdits))
	}
	return applyEditsResult{BaseContent: baseContent, NewContent: newContent}, nil
}

// notFoundError, duplicateError, emptyOldTextError and noChangeError
// reproduce edit-diff.js's exact user-facing error text (capitalized,
// full sentences) verbatim, which is why they carry ST1005 suppressions
// throughout: the point of this port is that models and users see the
// same message pi's TypeScript harness would have shown.

func notFoundError(path string, editIndex, totalEdits int) error {
	if totalEdits == 1 {
		//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
		return fmt.Errorf("Could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.", path)
	}
	//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
	return fmt.Errorf("Could not find edits[%d] in %s. The oldText must match exactly including all whitespace and newlines.", editIndex, path)
}

func duplicateError(path string, editIndex, totalEdits, occurrences int) error {
	if totalEdits == 1 {
		//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
		return fmt.Errorf("Found %d occurrences of the text in %s. The text must be unique. Please provide more context to make it unique.", occurrences, path)
	}
	//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
	return fmt.Errorf("Found %d occurrences of edits[%d] in %s. Each oldText must be unique. Please provide more context to make it unique.", occurrences, editIndex, path)
}

func emptyOldTextError(path string, editIndex, totalEdits int) error {
	if totalEdits == 1 {
		//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
		return fmt.Errorf("oldText must not be empty in %s.", path)
	}
	//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
	return fmt.Errorf("edits[%d].oldText must not be empty in %s.", editIndex, path)
}

func noChangeError(path string, totalEdits int) error {
	if totalEdits == 1 {
		//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
		return fmt.Errorf("No changes made to %s. The replacement produced identical content. This might indicate an issue with special characters or the text not existing as expected.", path)
	}
	//lint:ignore ST1005 mirrors pi's exact user-facing tool error text
	return fmt.Errorf("No changes made to %s. The replacements produced identical content.", path)
}

// generateUnifiedPatch generates a standard unified diff, mirroring
// edit-diff.js's generateUnifiedPatch (which uses the `diff` package's
// createTwoFilesPatch with FILE_HEADERS_ONLY). This port uses
// github.com/aymanbagabas/go-udiff, already a transitive dependency of
// this module, instead of hand-rolling a diff algorithm; the hunk content
// is the same standard unified-diff format, though exact header
// punctuation may differ cosmetically from js-diff's.
func generateUnifiedPatch(path, oldContent, newContent string) string {
	return udiff.Unified(path, path, oldContent, newContent)
}

// diffStringResult is the outcome of generateDiffString.
type diffStringResult struct {
	Diff             string
	FirstChangedLine int
	HasFirstChanged  bool
}

type diffPart struct {
	text           string
	added, removed bool
}

// linesFromEdits converts go-udiff's byte-offset Edits (relative to
// oldContent) into pi's diffLines-style part sequence: alternating equal,
// removed and added runs covering the whole of oldContent/newContent.
func linesFromEdits(oldContent string, edits []udiff.Edit) []diffPart {
	var parts []diffPart
	cursor := 0
	for _, e := range edits {
		if e.Start > cursor {
			parts = append(parts, diffPart{text: oldContent[cursor:e.Start]})
		}
		if e.End > e.Start {
			parts = append(parts, diffPart{text: oldContent[e.Start:e.End], removed: true})
		}
		if e.New != "" {
			parts = append(parts, diffPart{text: e.New, added: true})
		}
		cursor = e.End
	}
	if cursor < len(oldContent) {
		parts = append(parts, diffPart{text: oldContent[cursor:]})
	}
	return parts
}

// splitPartLines splits a diff part's text into lines the way pi's
// generateDiffString does: part.value.split("\n"), dropping a trailing
// empty element produced by a trailing newline.
func splitPartLines(text string) []string {
	raw := strings.Split(text, "\n")
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	return raw
}

// generateDiffString generates a display-oriented diff with line numbers
// and collapsed context, mirroring edit-diff.js's generateDiffString
// exactly (contextLines defaults to 4): +/- prefixed lines get the line
// number in the file they belong to, and a run of unchanged lines longer
// than 2*contextLines is collapsed to a leading/trailing slice plus a
// " ... " marker.
func generateDiffString(oldContent, newContent string) diffStringResult {
	const contextLines = 4
	edits := udiff.Lines(oldContent, newContent)
	parts := linesFromEdits(oldContent, edits)

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	lineNumWidth := len(strconv.Itoa(maxLineNum))

	var output []string
	oldLineNum, newLineNum := 1, 1
	lastWasChange := false
	result := diffStringResult{}

	padLine := func(n int) string { return fmt.Sprintf("%*d", lineNumWidth, n) }
	padBlank := func() string { return strings.Repeat(" ", lineNumWidth) }

	for i, part := range parts {
		raw := splitPartLines(part.text)
		if part.added || part.removed {
			if !result.HasFirstChanged {
				result.FirstChangedLine = newLineNum
				result.HasFirstChanged = true
			}
			for _, line := range raw {
				if part.added {
					output = append(output, fmt.Sprintf("+%s %s", padLine(newLineNum), line))
					newLineNum++
				} else {
					output = append(output, fmt.Sprintf("-%s %s", padLine(oldLineNum), line))
					oldLineNum++
				}
			}
			lastWasChange = true
			continue
		}

		nextIsChange := i < len(parts)-1 && (parts[i+1].added || parts[i+1].removed)
		hasLeading := lastWasChange
		hasTrailing := nextIsChange

		switch {
		case hasLeading && hasTrailing:
			if len(raw) <= contextLines*2 {
				for _, line := range raw {
					output = append(output, fmt.Sprintf(" %s %s", padLine(oldLineNum), line))
					oldLineNum++
					newLineNum++
				}
			} else {
				leading := raw[:contextLines]
				trailing := raw[len(raw)-contextLines:]
				skipped := len(raw) - len(leading) - len(trailing)
				for _, line := range leading {
					output = append(output, fmt.Sprintf(" %s %s", padLine(oldLineNum), line))
					oldLineNum++
					newLineNum++
				}
				output = append(output, fmt.Sprintf(" %s ...", padBlank()))
				oldLineNum += skipped
				newLineNum += skipped
				for _, line := range trailing {
					output = append(output, fmt.Sprintf(" %s %s", padLine(oldLineNum), line))
					oldLineNum++
					newLineNum++
				}
			}
		case hasLeading:
			shown := raw
			if len(shown) > contextLines {
				shown = raw[:contextLines]
			}
			skipped := len(raw) - len(shown)
			for _, line := range shown {
				output = append(output, fmt.Sprintf(" %s %s", padLine(oldLineNum), line))
				oldLineNum++
				newLineNum++
			}
			if skipped > 0 {
				output = append(output, fmt.Sprintf(" %s ...", padBlank()))
				oldLineNum += skipped
				newLineNum += skipped
			}
		case hasTrailing:
			skipped := len(raw) - contextLines
			if skipped < 0 {
				skipped = 0
			}
			if skipped > 0 {
				output = append(output, fmt.Sprintf(" %s ...", padBlank()))
				oldLineNum += skipped
				newLineNum += skipped
			}
			for _, line := range raw[skipped:] {
				output = append(output, fmt.Sprintf(" %s %s", padLine(oldLineNum), line))
				oldLineNum++
				newLineNum++
			}
		default:
			oldLineNum += len(raw)
			newLineNum += len(raw)
		}
		lastWasChange = false
	}

	result.Diff = strings.Join(output, "\n")
	return result
}
