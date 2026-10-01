package settings

import (
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The shell-lexing half of bash_paths.go: heredocs, substitutions, quoting
// (including $'…' and $"…"), words, brace expansion and globbing, each the
// way bash itself does them, to the extent a deny rule needs.

// ansiEnd returns the index just past the "'" closing an ANSI-C string
// whose body starts at start ($'…': a backslash escapes the next byte,
// so \' does not close it). closed is false when there is none.
func ansiEnd(s string, start int) (end int, closed bool) {
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '\'':
			return i + 1, true
		}
	}
	return len(s), false
}

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

// stripHeredocs removes heredoc bodies from cmd (they are input, not
// commands), keeping the "<<WORD" operators. expanding are the bodies whose
// delimiter was unquoted: bash expands $(…) and backticks in those, so
// their substitutions still run and must be scanned.
func stripHeredocs(cmd string) (out string, expanding []string) {
	type pending struct {
		delim        string
		tabs, quoted bool
	}
	var b strings.Builder
	var docs []pending
	i := 0
	for i < len(cmd) {
		c := cmd[i]
		switch {
		case c == '\\' && i+1 < len(cmd):
			b.WriteString(cmd[i : i+2])
			i += 2
		case c == '\'':
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				b.WriteString(cmd[i:])
				return b.String(), expanding
			}
			b.WriteString(cmd[i : i+end+2])
			i += end + 2
		case c == '$' && i+1 < len(cmd) && cmd[i+1] == '\'':
			end, _ := ansiEnd(cmd, i+2)
			b.WriteString(cmd[i:end])
			i = end
		case c == '"':
			j := i + 1
			for j < len(cmd) && cmd[j] != '"' {
				if cmd[j] == '\\' {
					j++
				}
				j++
			}
			end := min(j+1, len(cmd))
			b.WriteString(cmd[i:end])
			i = end
		case strings.HasPrefix(cmd[i:], "<<") && !strings.HasPrefix(cmd[i:], "<<<"):
			j := i + 2
			p := pending{}
			if j < len(cmd) && cmd[j] == '-' {
				p.tabs = true
				j++
			}
			for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
				j++
			}
			var delim strings.Builder
			for j < len(cmd) && strings.IndexByte(" \t\n;|&<>()", cmd[j]) < 0 {
				switch cmd[j] {
				case '\'', '"':
					p.quoted = true
					q := cmd[j]
					k := strings.IndexByte(cmd[j+1:], q)
					if k < 0 {
						delim.WriteString(cmd[j+1:])
						j = len(cmd)
						continue
					}
					delim.WriteString(cmd[j+1 : j+1+k])
					j += k + 2
				case '\\':
					p.quoted = true
					if j+1 < len(cmd) {
						delim.WriteByte(cmd[j+1])
					}
					j += 2
				default:
					delim.WriteByte(cmd[j])
					j++
				}
			}
			p.delim = delim.String()
			b.WriteString(cmd[i:min(j, len(cmd))])
			i = j
			if p.delim != "" {
				docs = append(docs, p)
			}
		case c == '\n' && len(docs) > 0:
			b.WriteByte('\n')
			i++
			for _, d := range docs {
				var body strings.Builder
				for i < len(cmd) {
					nl := strings.IndexByte(cmd[i:], '\n')
					line := cmd[i:]
					if nl >= 0 {
						line = cmd[i : i+nl]
						i += nl + 1
					} else {
						i = len(cmd)
					}
					check := line
					if d.tabs {
						check = strings.TrimLeft(check, "\t")
					}
					if check == d.delim {
						break
					}
					body.WriteString(line)
					body.WriteByte('\n')
				}
				if !d.quoted {
					expanding = append(expanding, body.String())
				}
			}
			docs = nil
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), expanding
}

// extractSubstitutions pulls the bodies of $(...), `...`, <(...) and >(...)
// out of cmd, replacing each with a placeholder: "$KILN_SUBST" for a command
// substitution (its output is unknowable, so as an operand it is unsure)
// and "/dev/fd/63" for a process substitution (a pipe, not a file). ok is
// false when a substitution is not closed. literalQuotes is for a heredoc
// body, where quotes are text and only $(…) and backticks are live.
func extractSubstitutions(cmd string, literalQuotes bool) (outer string, bodies []string, ok bool) {
	var out strings.Builder
	ok = true
	inDouble := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\\' && i+1 < len(cmd):
			out.WriteString(cmd[i : i+2])
			i++
		case c == '$' && i+1 < len(cmd) && cmd[i+1] == '\'' && !inDouble && !literalQuotes:
			end, closed := ansiEnd(cmd, i+2)
			if !closed {
				ok = false
			}
			out.WriteString(cmd[i:end])
			i = end - 1
		case c == '\'' && !inDouble && !literalQuotes:
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				out.WriteString(cmd[i:])
				return out.String(), bodies, ok
			}
			out.WriteString(cmd[i : i+end+2])
			i += end + 1
		case c == '"' && !literalQuotes:
			inDouble = !inDouble
			out.WriteByte(c)
		case c == '`':
			j := i + 1
			var body strings.Builder
			for ; j < len(cmd) && cmd[j] != '`'; j++ {
				if cmd[j] == '\\' && j+1 < len(cmd) && (cmd[j+1] == '`' || cmd[j+1] == '\\' || cmd[j+1] == '$') {
					j++
				}
				body.WriteByte(cmd[j])
			}
			if j >= len(cmd) {
				ok = false
			}
			bodies = append(bodies, body.String())
			out.WriteString("$KILN_SUBST")
			i = j
		case c == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
			body, end, closed := parenBody(cmd, i+2)
			if !closed {
				ok = false
			}
			bodies = append(bodies, body)
			out.WriteString("$KILN_SUBST")
			i = end
		case (c == '<' || c == '>') && !inDouble && !literalQuotes && i+1 < len(cmd) && cmd[i+1] == '(':
			body, end, closed := parenBody(cmd, i+2)
			if !closed {
				ok = false
			}
			bodies = append(bodies, body)
			out.WriteString("/dev/fd/63")
			i = end
		default:
			out.WriteByte(c)
		}
	}
	return out.String(), bodies, ok
}

// parenBody returns the text from start up to the ")" that closes an
// already-open "(", skipping quoted text and nested parentheses, and the
// index of that ")". closed is false when there is none (end is then the
// last index).
func parenBody(cmd string, start int) (body string, end int, closed bool) {
	depth := 1
	for i := start; i < len(cmd); i++ {
		switch cmd[i] {
		case '\\':
			i++
		case '$':
			if i+1 < len(cmd) && cmd[i+1] == '\'' {
				e, _ := ansiEnd(cmd, i+2)
				i = e - 1
			}
		case '\'':
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				return cmd[start:], len(cmd) - 1, false
			}
			i += j + 1
		case '"':
			for i++; i < len(cmd) && cmd[i] != '"'; i++ {
				if cmd[i] == '\\' {
					i++
				}
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return cmd[start:i], i, true
			}
		}
	}
	return cmd[start:], len(cmd) - 1, false
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

// redirect is one redirection in a command.
type redirect struct {
	target      word
	read, write bool
}

// shellWords splits one command (a BashSegments segment) into its words,
// quotes removed and each byte's quoting state kept, and its redirections.
func shellWords(seg string) (words []word, redirs []redirect) {
	var cur word
	inWord := false
	pending := -1 // index into redirs awaiting its target word
	skipNext := false
	add := func(c, q byte) {
		cur.b = append(cur.b, c)
		cur.q = append(cur.q, q)
		inWord = true
	}
	flush := func() {
		if !inWord {
			return
		}
		w := cur
		cur = word{}
		inWord = false
		switch {
		case skipNext:
			skipNext = false
		case pending >= 0:
			redirs[pending].target = w
			pending = -1
		default:
			words = append(words, w)
		}
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c == '$' && i+1 < len(seg) && seg[i+1] == '\'':
			decoded, end, ok := decodeANSI(seg, i+2)
			for _, d := range decoded {
				add(d, qLit)
			}
			inWord = true
			if !ok {
				cur.bad = true
			}
			i = end - 1
		case c == '$' && i+1 < len(seg) && seg[i+1] == '"':
			// $"…" is a translatable string: a double-quoted one.
			inWord = true
		case c == '\'':
			end := strings.IndexByte(seg[i+1:], '\'')
			body := seg[i+1:]
			if end >= 0 {
				body = seg[i+1 : i+1+end]
			}
			for j := 0; j < len(body); j++ {
				add(body[j], qLit)
			}
			inWord = true
			if end < 0 {
				i = len(seg)
			} else {
				i += end + 1
			}
		case c == '"':
			inWord = true
			j := i + 1
			for ; j < len(seg) && seg[j] != '"'; j++ {
				if seg[j] == '\\' && j+1 < len(seg) && strings.IndexByte("\"\\$`", seg[j+1]) >= 0 {
					j++
					add(seg[j], qLit)
					continue
				}
				add(seg[j], qDouble)
			}
			i = j
		case c == '\\' && i+1 < len(seg):
			add(seg[i+1], qLit)
			i++
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '<' || c == '>' || (c == '&' && i+1 < len(seg) && seg[i+1] == '>'):
			// A word of digits right before the operator is its fd.
			if inWord && isDigits(string(cur.b)) {
				cur = word{}
				inWord = false
			}
			flush()
			op, n := redirOperator(seg[i:])
			i += n - 1
			r := redirect{}
			switch op {
			case "<":
				r.read = true
			case "<>":
				r.read, r.write = true, true
			case ">", ">>", ">|", "&>", "&>>":
				r.write = true
			case ">&", "<&":
				// ">&file" writes file; ">&2" and ">&-" duplicate an fd.
				rest := strings.TrimLeft(seg[i+1:], " \t")
				if rest == "" || rest[0] == '-' || (rest[0] >= '0' && rest[0] <= '9') {
					skipNext = true
					continue
				}
				r.write = op == ">&"
				r.read = op == "<&"
			default: // "<<", "<<-", "<<<": a heredoc delimiter or a string
				skipNext = true
				continue
			}
			pending = len(redirs)
			redirs = append(redirs, r)
		default:
			add(c, qNone)
		}
	}
	flush()
	var out []redirect
	for _, r := range redirs {
		if (r.read || r.write) && (len(r.target.b) > 0 || r.target.bad) {
			out = append(out, r)
		}
	}
	return words, out
}

// redirOperator returns the redirection operator at the start of s and its
// length.
func redirOperator(s string) (string, int) {
	for _, op := range []string{"&>>", "<<<", "<<-", "&>", ">>", ">|", ">&", "<&", "<>", "<<", "<", ">"} {
		if strings.HasPrefix(s, op) {
			return op, len(op)
		}
	}
	return s[:1], 1
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
		if end < 0 || len(commas) == 0 {
			continue
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
