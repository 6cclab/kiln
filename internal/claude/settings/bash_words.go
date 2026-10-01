package settings

import (
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Word expansion for bash_paths.go: a parsed word (bash_parse.go) as bytes
// with their quoting, then $'…' decoding, brace expansion, ~ and $HOME,
// and globbing, each the way bash itself does them, to the extent a deny
// rule needs. Splitting a command line into commands and words is the
// parser's job (mvdan.cc/sh), not this file's.

// decodeANSI decodes the body of $'…' starting at start, as bash does.
// end is the index just past the closing "'"; ok is false when the string
// is not closed.
func decodeANSI(s string, start int) (out []byte, end int, ok bool) {
	for i := start; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			return out, i + 1, true
		}
		if c != '\\' || i+1 >= len(s) {
			out = append(out, c)
			continue
		}
		i++
		switch e := s[i]; e {
		case 'a':
			out = append(out, 7)
		case 'b':
			out = append(out, 8)
		case 'e', 'E':
			out = append(out, 27)
		case 'f':
			out = append(out, 12)
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'v':
			out = append(out, 11)
		case '\\', '\'', '"', '?':
			out = append(out, e)
		case 'c':
			if i+1 < len(s) {
				i++
				out = append(out, s[i]&0x1f)
			}
		case 'x', 'u', 'U':
			max := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
			j := i + 1
			for j < len(s) && j-i-1 < max && strings.IndexByte("0123456789abcdefABCDEF", s[j]) >= 0 {
				j++
			}
			if j == i+1 {
				out = append(out, '\\', e)
				continue
			}
			v, _ := strconv.ParseUint(s[i+1:j], 16, 32)
			if e == 'x' {
				out = append(out, byte(v))
			} else {
				out = utf8.AppendRune(out, rune(v))
			}
			i = j - 1
		default:
			if e >= '0' && e <= '7' {
				j := i
				for j < len(s) && j-i < 3 && s[j] >= '0' && s[j] <= '7' {
					j++
				}
				v, _ := strconv.ParseUint(s[i:j], 8, 16)
				out = append(out, byte(v))
				i = j - 1
				continue
			}
			out = append(out, '\\', e) // unknown escape: kept as written
		}
	}
	return out, len(s), false
}

// Quoting states of a word's bytes.
const (
	qNone   byte = iota // unquoted: globs, braces, ~ and $ are live
	qDouble             // double-quoted: only $ is live
	qLit                // single-quoted, $'…' or backslash-escaped: literal
)

// word is one shell word with each byte's quoting state. bad marks a word
// kiln could not decode (an unclosed $'…').
type word struct {
	b, q []byte
	bad  bool
}

func (w word) lit() string { return string(w.b) }

func (w word) slice(from int) word { return word{b: w.b[from:], q: w.q[from:], bad: w.bad} }

func literalWord(s string) word {
	q := make([]byte, len(s))
	for i := range q {
		q[i] = qLit
	}
	return word{b: []byte(s), q: q}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// braceExpand performs bash brace expansion ("a{b,c}d" -> abd, acd) on
// unquoted braces. ok is false past limit results.
func braceExpand(w word, limit int) ([]word, bool) {
	for i := 0; i < len(w.b); i++ {
		if w.b[i] != '{' || w.q[i] != qNone || (i > 0 && w.b[i-1] == '$' && w.q[i-1] != qLit) {
			continue
		}
		depth, commas, end := 0, []int{}, -1
		for j := i; j < len(w.b) && end < 0; j++ {
			if w.q[j] != qNone {
				continue
			}
			switch w.b[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = j
				}
			case ',':
				if depth == 1 {
					commas = append(commas, j)
				}
			}
		}
		if end < 0 {
			continue
		}
		if len(commas) == 0 {
			// A sequence, "{a..e}" or "{1..10..2}".
			items, ok := braceSequence(w.b[i+1:end], w.q[i+1:end], limit)
			if !ok {
				continue
			}
			if items == nil {
				return nil, false // too many
			}
			var out []word
			for _, it := range items {
				nw := word{bad: w.bad}
				nw.b = append(append(append([]byte{}, w.b[:i]...), it...), w.b[end+1:]...)
				nw.q = append(append(append([]byte{}, w.q[:i]...), make([]byte, len(it))...), w.q[end+1:]...)
				more, ok := braceExpand(nw, limit-len(out))
				if !ok {
					return nil, false
				}
				out = append(out, more...)
				if len(out) > limit {
					return nil, false
				}
			}
			return out, true
		}
		bounds := append(append([]int{i}, commas...), end)
		var out []word
		for k := 0; k+1 < len(bounds); k++ {
			nw := word{bad: w.bad}
			nw.b = append(append(append([]byte{}, w.b[:i]...), w.b[bounds[k]+1:bounds[k+1]]...), w.b[end+1:]...)
			nw.q = append(append(append([]byte{}, w.q[:i]...), w.q[bounds[k]+1:bounds[k+1]]...), w.q[end+1:]...)
			more, ok := braceExpand(nw, limit-len(out))
			if !ok {
				return nil, false
			}
			out = append(out, more...)
			if len(out) > limit {
				return nil, false
			}
		}
		return out, true
	}
	return []word{w}, true
}

// braceSequence expands the inside of a sequence brace, "a..e",
// "1..10", "10..1..3", "01..10" (zero-padded), as bash does. ok is false
// when it is not a sequence (bash leaves the braces as they are); items
// is nil when it would yield more than limit items.
func braceSequence(b, q []byte, limit int) (items [][]byte, ok bool) {
	for _, c := range q {
		if c != qNone {
			return nil, false
		}
	}
	parts := strings.Split(string(b), "..")
	if len(parts) != 2 && len(parts) != 3 {
		return nil, false
	}
	step := 1
	if len(parts) == 3 {
		s, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, false
		}
		if s < 0 {
			s = -s
		}
		if s != 0 {
			step = s
		}
	}
	emit := func(from, to int, format func(int) string) ([][]byte, bool) {
		n := (max(from, to)-min(from, to))/step + 1
		if n > limit {
			return nil, true
		}
		var out [][]byte
		for k := 0; k < n; k++ {
			v := from + k*step
			if from > to {
				v = from - k*step
			}
			out = append(out, []byte(format(v)))
		}
		return out, true
	}
	if a, errA := strconv.Atoi(parts[0]); errA == nil {
		z, errZ := strconv.Atoi(parts[1])
		if errZ != nil {
			return nil, false
		}
		width := 0
		for _, p := range parts[:2] {
			d := strings.TrimPrefix(p, "-")
			if len(d) > 1 && d[0] == '0' {
				width = max(width, len(p))
			}
		}
		return emit(a, z, func(v int) string {
			s := strconv.Itoa(v)
			if width > 0 {
				neg := v < 0
				s = strings.TrimPrefix(s, "-")
				pad := width
				if neg {
					pad--
				}
				for len(s) < pad {
					s = "0" + s
				}
				if neg {
					s = "-" + s
				}
			}
			return s
		})
	}
	if len(parts[0]) == 1 && len(parts[1]) == 1 && isLetter(parts[0][0]) && isLetter(parts[1][0]) {
		return emit(int(parts[0][0]), int(parts[1][0]), func(v int) string { return string(rune(v)) })
	}
	return nil, false
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// expandHome applies ~ and $HOME expansion to w. ok is false for a word
// with any other live expansion ($VAR, ${…}, $KILN_SUBST, ~user) or one
// that could not be decoded.
func expandHome(w word, home string) (word, bool) {
	if w.bad {
		return w, false
	}
	b, q := w.b, w.q
	withHome := func(rest int) {
		hw := literalWord(home)
		b = append(hw.b, b[rest:]...)
		q = append(hw.q, q[rest:]...)
	}
	switch {
	case len(b) > 0 && b[0] == '~' && q[0] == qNone:
		if len(b) > 1 && b[1] != '/' {
			return w, false // ~user
		}
		if home == "" {
			return w, false
		}
		withHome(1)
	case hasLivePrefix(b, q, "${HOME}"):
		withHome(len("${HOME}"))
	case hasLivePrefix(b, q, "$HOME") && (len(b) == 5 || !isNameByte(b[5])):
		withHome(len("$HOME"))
	}
	for i, c := range b {
		if c == '$' && q[i] != qLit && i+1 < len(b) && (isNameByte(b[i+1]) || strings.IndexByte("{(@*#?$!-", b[i+1]) >= 0) {
			return w, false
		}
	}
	return word{b: b, q: q}, true
}

func hasLivePrefix(b, q []byte, prefix string) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == prefix && q[0] != qLit
}

func isNameByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// hasLiveGlob reports an unquoted *, ? or [ in w.
func hasLiveGlob(w word) bool {
	for i, c := range w.b {
		if w.q[i] == qNone && (c == '*' || c == '?' || c == '[') {
			return true
		}
	}
	return false
}

// Glob expansion limits for one command line: past either, the operand is
// unsure rather than the gate stalling on "cat /*/*/*/*/*".
const (
	maxGlobMatches = 1000
	maxGlobDirs    = 200
)

// globBudget is shared by a scan and its nested scans.
type globBudget struct{ matches, dirs int }

// glob expands the word w (already ~-expanded) as bash does, under base
// (the directory a relative word is relative to; ignored when w is
// absolute), returning the matching paths unclean (a ".." in them is
// walked physically by whoever resolves them). As in bash without dotglob,
// "*", "?" and "[…]" do not match a leading "." unless the pattern
// segment itself starts with one, and "**" is "*". ok is false when the
// budget runs out.
func glob(w word, base string, budget *globBudget) (matches []string, ok bool) {
	start := base
	b, q := w.b, w.q
	if len(b) > 0 && b[0] == '/' {
		start = "/"
	}
	cur := []string{start}
	for len(b) > 0 {
		// Next segment, up to "/".
		n := 0
		for n < len(b) && b[n] != '/' {
			n++
		}
		seg := word{b: b[:n], q: q[:n]}
		if n < len(b) {
			b, q = b[n+1:], q[n+1:]
		} else {
			b, q = nil, nil
		}
		if n == 0 {
			continue
		}
		if !hasLiveGlob(seg) {
			for i := range cur {
				cur[i] = joinRaw(cur[i], seg.lit())
			}
			continue
		}
		var pat strings.Builder
		for i, c := range seg.b {
			if seg.q[i] != qNone && strings.IndexByte(`*?[\`, c) >= 0 {
				pat.WriteByte('\\')
			}
			pat.WriteByte(c)
		}
		// A leading "." must be matched explicitly. POSIX leaves open
		// whether a bracket expression ("[.]env") can; matches only feed
		// deny and ask checks, so the wider reading is the safe one.
		dotOK := seg.b[0] == '.' || (seg.b[0] == '[' && seg.q[0] == qNone && len(seg.b) > 1 && seg.b[1] != '!' && seg.b[1] != '^')
		var next []string
		for _, dir := range cur {
			budget.dirs++
			if budget.dirs > maxGlobDirs {
				return nil, false
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if name[0] == '.' && !dotOK {
					continue
				}
				if m, _ := path.Match(pat.String(), name); !m {
					continue
				}
				budget.matches++
				if budget.matches > maxGlobMatches {
					return nil, false
				}
				next = append(next, joinRaw(dir, name))
			}
		}
		cur = next
		if len(cur) == 0 {
			return nil, true
		}
	}
	return cur, true
}

// joinRaw joins without cleaning, so a ".." stays for physical resolution.
func joinRaw(dir, name string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}
