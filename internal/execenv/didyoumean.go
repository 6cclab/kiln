package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// invisiblePlaceholder (U+25FB WHITE MEDIUM SQUARE, "◻") stands in for any
// character DidYouMeanHint treats as invisible or space-like when it
// renders a candidate file name into a hint: a Unicode space separator
// (category Zs) other than the ordinary U+0020, or a zero-width
// character. A name that would otherwise look identical to what the model
// typed renders with a visible ◻ at the exact spot that differs.
const invisiblePlaceholder = "◻"

// maxDidYouMeanEntries bounds how many directory entries DidYouMeanHint
// reads before it gives up looking for a match: one failed tool call must
// not turn into an unbounded directory scan. It is a var, not a const, so
// tests can shrink it instead of creating thousands of files on disk.
var maxDidYouMeanEntries = 2000

// zeroWidthNames are the zero-width characters DidYouMeanHint strips
// before comparing two names, named for the explanatory sentence it adds
// when that is the only difference it finds.
// The keys are written as hex code points (rune(0x...)), not character or
// \u escapes: most are zero-width or a byte-order mark, so a literal or an
// escaped one in the source risks exactly the invisible-character
// confusion this file exists to detect.
var zeroWidthNames = map[rune]string{
	rune(0x200B): "ZERO WIDTH SPACE",
	rune(0x200C): "ZERO WIDTH NON-JOINER",
	rune(0x200D): "ZERO WIDTH JOINER",
	rune(0xFEFF): "ZERO WIDTH NO-BREAK SPACE",
}

// spaceRuneNames names the Unicode space separators (category Zs) that
// matchKey folds to an ordinary U+0020 space for comparison, again for the
// explanatory sentence.
var spaceRuneNames = map[rune]string{
	' ':          "SPACE",
	rune(0x00A0): "NO-BREAK SPACE",
	rune(0x1680): "OGHAM SPACE MARK",
	rune(0x2000): "EN QUAD",
	rune(0x2001): "EM QUAD",
	rune(0x2002): "EN SPACE",
	rune(0x2003): "EM SPACE",
	rune(0x2004): "THREE-PER-EM SPACE",
	rune(0x2005): "FOUR-PER-EM SPACE",
	rune(0x2006): "SIX-PER-EM SPACE",
	rune(0x2007): "FIGURE SPACE",
	rune(0x2008): "PUNCTUATION SPACE",
	rune(0x2009): "THIN SPACE",
	rune(0x200A): "HAIR SPACE",
	rune(0x202F): "NARROW NO-BREAK SPACE",
	rune(0x205F): "MEDIUM MATHEMATICAL SPACE",
	rune(0x3000): "IDEOGRAPHIC SPACE",
}

func runeName(r rune) string {
	if name, ok := spaceRuneNames[r]; ok {
		return name
	}
	if name, ok := zeroWidthNames[r]; ok {
		return name
	}
	return "UNKNOWN CHARACTER"
}

// matchKey normalises s for "did you mean" matching: NFC (so a
// filesystem's NFD-decomposed spelling, as macOS stores it, compares equal
// to the precomposed NFC spelling a model's path argument usually uses),
// every Unicode space separator (category Zs: ordinary space, NBSP,
// narrow no-break space, the various widths at U+2000-U+200A, U+205F,
// U+3000, ...) folded to a plain U+0020, and every zero-width character
// (U+200B/C/D, U+FEFF) removed outright. Two names are the same file-name
// candidate for DidYouMeanHint's purposes exactly when matchKey agrees on
// them.
func matchKey(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	for _, r := range s {
		if _, zw := zeroWidthNames[r]; zw {
			continue
		}
		if unicode.Is(unicode.Zs, r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// foldKey is matchKey with case folded away too, for the last-resort
// case-insensitive comparison DidYouMeanHint tries only after an exact
// (matchKey) comparison finds nothing.
func foldKey(s string) string {
	return strings.ToLower(matchKey(s))
}

// displayInvisibles renders name (NFC-normalised) for use inside a "Did
// you mean" hint: every zero-width character and every Unicode space
// separator other than the ordinary U+0020 is replaced with
// invisiblePlaceholder, so a name that would otherwise read identically to
// what the model typed shows visibly where it differs.
func displayInvisibles(name string) string {
	name = norm.NFC.String(name)
	var b strings.Builder
	for _, r := range name {
		if _, zw := zeroWidthNames[r]; zw {
			b.WriteString(invisiblePlaceholder)
			continue
		}
		if r != ' ' && unicode.Is(unicode.Zs, r) {
			b.WriteString(invisiblePlaceholder)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// describeWritten names the rune the model actually wrote, for
// invisibleDifference's sentence: "a regular space (U+0020)" for the
// ordinary case, else "U+XXXX NAME".
func describeWritten(r rune) string {
	if r == ' ' {
		return "a regular space (U+0020)"
	}
	return fmt.Sprintf("U+%04X %s", r, runeName(r))
}

// invisibleDifference reports, for a requested name and a directory entry
// matchKey found equal to it, the single rune position where they differ
// in their raw (NFC, un-folded) spelling — which, given the match, can
// only be a space-separator or zero-width rune substitution. ok is false
// when no such single differing position can be identified (for example
// the two names are not the same length after NFC, which this does not
// attempt to realign), so the caller omits the explanatory sentence
// rather than guess.
func invisibleDifference(requested, candidate string) (string, bool) {
	req := []rune(norm.NFC.String(requested))
	cand := []rune(norm.NFC.String(candidate))
	if len(req) != len(cand) {
		return "", false
	}
	for i := range req {
		if req[i] == cand[i] {
			continue
		}
		reqIsSpace := req[i] == ' ' || unicode.Is(unicode.Zs, req[i])
		candIsSpace := cand[i] == ' ' || unicode.Is(unicode.Zs, cand[i])
		if !reqIsSpace || !candIsSpace {
			return "", false
		}
		return fmt.Sprintf(
			"the file name has U+%04X %s where you wrote %s, at position %d",
			cand[i], runeName(cand[i]), describeWritten(req[i]), i+1,
		), true
	}
	return "", false
}

// DidYouMeanHint builds the hint text a path-taking tool (read, edit,
// write) appends to a bare not-found error for absPath, or "" if it has
// none to offer.
//
// It lists absPath's immediate parent directory with a single os.ReadDir
// call — capped at maxDidYouMeanEntries entries, never recursing into
// subdirectories and never following a symlink past the directory entry
// itself — and compares each entry's name against absPath's basename
// under matchKey's rules: NFC vs NFD and every Unicode space variant fold
// together, zero-width characters are ignored. Only when that turns up
// nothing is a case-insensitive (foldKey) comparison tried, and those
// matches are listed after, never mixed with, the exact ones. When an
// exact match differs from what was asked for only by which
// invisible/space character was used, the hint names that character and
// the position it is at.
//
// DidYouMeanHint never scans a directory under macOS's /.vol (IsVolPath),
// and never scans at all when DidYouMeanDirAllowed is set and refuses the
// parent directory — see that field's doc comment for what this is, and
// is not, a substitute for.
func (e *Env) DidYouMeanHint(absPath string) string {
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)

	if IsVolPath(dir) {
		return ""
	}
	if e.DidYouMeanDirAllowed != nil && !e.DidYouMeanDirAllowed(dir) {
		return ""
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	wantExact := matchKey(base)
	wantFold := foldKey(base)

	var exact, caseOnly []string
	for i, entry := range entries {
		if i >= maxDidYouMeanEntries {
			break
		}
		name := entry.Name()
		if name == base {
			continue
		}
		switch {
		case matchKey(name) == wantExact:
			exact = append(exact, name)
		case foldKey(name) == wantFold:
			caseOnly = append(caseOnly, name)
		}
	}
	if len(exact) == 0 && len(caseOnly) == 0 {
		return ""
	}
	sort.Strings(exact)
	sort.Strings(caseOnly)

	quoted := make([]string, 0, len(exact)+len(caseOnly))
	for _, n := range exact {
		quoted = append(quoted, `"`+displayInvisibles(n)+`"`)
	}
	for _, n := range caseOnly {
		quoted = append(quoted, `"`+displayInvisibles(n)+`"`)
	}

	hint := fmt.Sprintf("Did you mean %s?", strings.Join(quoted, " or "))
	if len(exact) == 1 {
		if diff, ok := invisibleDifference(base, exact[0]); ok {
			hint += " (" + diff + ")"
		}
	}
	return hint
}
