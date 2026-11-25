package tools

import (
	"fmt"
	"strings"
)

// Limits for the closest-match hint: past them the search costs more than
// the hint is worth, and the error goes out without one.
const (
	hintMaxFileLines = 20_000
	hintMaxOldLines  = 200
	hintMinScore     = 0.5 // mean line similarity a window needs to be shown
	hintMaxShown     = 30  // lines of the closest text quoted back
)

// withClosestMatch adds the text in content most like oldText to a "could
// not find" error, so the model can see what differs (indentation, a
// renamed identifier, a stale line) and retry with text copied from the
// file instead of guessing again. Adapted from hermes-agent's fuzzy-match
// hint for failed patches (NousResearch, MIT). Without a window similar
// enough, err is returned unchanged.
func withClosestMatch(err error, path, content, oldText string) error {
	start, end, ok := closestMatch(content, oldText)
	if !ok {
		return err
	}
	lines := strings.Split(content, "\n")[start:end]
	more := ""
	if len(lines) > hintMaxShown {
		more = fmt.Sprintf("\n[%d more lines]", len(lines)-hintMaxShown)
		lines = lines[:hintMaxShown]
	}
	//lint:ignore ST1005 continues pi's full-sentence tool error (editdiff.go)
	return fmt.Errorf("%w\nThe closest text in %s is at lines %d-%d:\n%s%s\nCompare it with your old text (whitespace, indentation, exact characters) and retry with text copied from the file.",
		err, path, start+1, end, strings.Join(lines, "\n"), more)
}

// closestMatch finds the run of lines in content most like oldText: the
// same number of lines, scored by the mean bigram similarity of
// corresponding lines with surrounding whitespace ignored. It returns the
// 0-indexed [start, end) line range.
func closestMatch(content, oldText string) (start, end int, ok bool) {
	file := strings.Split(content, "\n")
	old := strings.Split(strings.TrimRight(oldText, "\n"), "\n")
	n := len(old)
	if n == 0 || n > hintMaxOldLines || len(file) > hintMaxFileLines || n > len(file) {
		return 0, 0, false
	}
	fileGrams := make([]map[string]int, len(file))
	for i, l := range file {
		fileGrams[i] = bigrams(l)
	}
	oldGrams := make([]map[string]int, n)
	for i, l := range old {
		oldGrams[i] = bigrams(l)
	}
	best, bestAt := 0.0, -1
	for i := 0; i+n <= len(file); i++ {
		sum := 0.0
		for j := 0; j < n; j++ {
			sum += dice(fileGrams[i+j], oldGrams[j])
		}
		if score := sum / float64(n); score > best {
			best, bestAt = score, i
		}
	}
	if bestAt < 0 || best < hintMinScore {
		return 0, 0, false
	}
	return bestAt, bestAt + n, true
}

// bigrams counts the character pairs of a line with surrounding whitespace
// dropped; a line of one character counts as its own pair, and a blank
// line has none.
func bigrams(line string) map[string]int {
	r := []rune(strings.TrimSpace(line))
	m := map[string]int{}
	if len(r) == 1 {
		m[string(r)]++
	}
	for i := 0; i+1 < len(r); i++ {
		m[string(r[i:i+2])]++
	}
	return m
}

// dice is the Sørensen-Dice similarity of two bigram multisets: 1 for the
// same line, 0 for nothing in common. Two blank lines are the same.
func dice(a, b map[string]int) float64 {
	na, nb := 0, 0
	for _, c := range a {
		na += c
	}
	for _, c := range b {
		nb += c
	}
	if na+nb == 0 {
		return 1
	}
	shared := 0
	for g, ca := range a {
		shared += min(ca, b[g])
	}
	return 2 * float64(shared) / float64(na+nb)
}
