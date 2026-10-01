package settings

import (
	"os"
	"path/filepath"
	"strings"
)

// Read and Edit deny rules also apply to the files a bash command names, as
// in Claude Code: the operands of file commands it recognizes (cat, head,
// tail, sed, tee, cp, ...) and the targets of redirections (> f, >> f,
// >| f, < f). This is best effort, like Claude Code's; it cannot see a file
// a command reads without naming it ("grep -r x .") or one a script opens
// itself. The sandbox, not this, is the boundary for those.
//
// Where a named file cannot be known statically (a variable, a glob in a
// directory kiln lost track of, a command substitution as an operand, a
// "cd -" or popd before a relative path, a substitution it cannot parse),
// the command is reported unsure, and RuleHits turns that into an ask
// whenever any Read/Edit path rule sits in deny or ask: an unknowable path
// is asked about rather than allowed past a deny.

// bashFileVerdict reports whether any file the command line reads or writes
// is blocked by a Read or Edit deny rule (deny), or whether the command
// names a file kiln cannot resolve while path deny/ask rules exist
// (unsure). A file the command reads is checked as a read call would be;
// one it writes as a write call (Edit rules, and Read deny rules, which also
// block writes).
func bashFileVerdict(p Permissions, c matchCtx, cmd string) (deny, unsure bool) {
	cr := compiledFor(p, c)
	if !cr.guarded {
		return false, false
	}
	reads, writes, unsure := bashFileOperands(cmd, c.cwd, c.home)
	resolve := newRealPathMemo()
	blocked := func(rules ruleSet, files []string) bool {
		if rules.empty() {
			return false
		}
		for _, f := range files {
			if rules.blocks(f) {
				return true
			}
			if real := resolve(f); real != f && rules.blocks(real) {
				return true
			}
		}
		return false
	}
	// A file read is judged as the read tool's call; a file written as
	// write's (Edit rules, and Read denies, which also block writes).
	deny = blocked(cr.deny[classRead], reads) || blocked(cr.deny[classEdit], writes)
	return deny, unsure && !deny
}

// newRealPathMemo returns realPath with each directory resolved once: a
// command line names many files in few directories, and resolving a
// directory costs an lstat per component. A file is still checked with its
// own lstat, so a file that is itself a link is followed.
func newRealPathMemo() func(string) string {
	dirs := map[string]string{}
	return func(p string) string {
		dir, base := filepath.Dir(p), filepath.Base(p)
		real, ok := dirs[dir]
		if !ok {
			real = realPath(dir)
			dirs[dir] = real
		}
		joined := filepath.Join(real, base)
		if fi, err := os.Lstat(joined); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return realPath(joined)
		}
		return joined
	}
}

// bashFileOperands returns the absolute paths a command line reads and
// writes, resolved against cwd (following any cd/pushd earlier in the
// line), and whether some file operand could not be resolved.
func bashFileOperands(cmd, cwd, home string) (reads, writes []string, unsure bool) {
	s := &bashScan{home: home, dirs: []string{cwd}, known: true}
	s.line(cmd)
	return s.reads, s.writes, s.unsure
}

// maxScanDepth bounds nested command substitutions.
const maxScanDepth = 8

// maxExpansions bounds brace expansion; past it the word is unsure.
const maxExpansions = 256

// bashScan collects file operands across one command line.
type bashScan struct {
	home string
	// dirs are the directories a relative path may be relative to: the
	// start directory plus every cd target seen so far. A cd in "a | cd x"
	// or "(cd x)" does not change the next command's directory, so dirs
	// only grow, and a path is checked against all of them.
	dirs []string
	// known is false once a cd went somewhere kiln cannot tell (cd -,
	// popd, cd $X): a relative path after that is unsure.
	known         bool
	reads, writes []string
	unsure        bool
	depth         int
}

func (s *bashScan) line(cmd string) {
	if s.depth > maxScanDepth {
		s.unsure = true
		return
	}
	outer, bodies, ok := extractSubstitutions(cmd)
	if !ok {
		s.unsure = true
	}
	segments, _ := BashSegments(outer)
	for _, seg := range segments {
		s.segment(seg)
	}
	// A substitution runs where it appears; dirs by now holds every
	// directory the line could have been in, so check the bodies against
	// all of them.
	for _, b := range bodies {
		sub := &bashScan{home: s.home, dirs: append([]string(nil), s.dirs...), known: s.known, depth: s.depth + 1}
		sub.line(b)
		s.reads = append(s.reads, sub.reads...)
		s.writes = append(s.writes, sub.writes...)
		s.unsure = s.unsure || sub.unsure
	}
}

// extractSubstitutions pulls the bodies of $(...), `...`, <(...) and >(...)
// out of cmd, replacing each with a placeholder: "$KILN_SUBST" for a command
// substitution (its output is unknowable, so as an operand it is unsure)
// and "/dev/fd/63" for a process substitution (a pipe, not a file). ok is
// false when a substitution is not closed.
func extractSubstitutions(cmd string) (outer string, bodies []string, ok bool) {
	var out strings.Builder
	ok = true
	inDouble := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\\' && i+1 < len(cmd):
			out.WriteString(cmd[i : i+2])
			i++
		case c == '\'' && !inDouble:
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				out.WriteString(cmd[i:])
				return out.String(), bodies, ok
			}
			out.WriteString(cmd[i : i+end+2])
			i += end + 1
		case c == '"':
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
		case (c == '<' || c == '>') && !inDouble && i+1 < len(cmd) && cmd[i+1] == '(':
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
	qLit                // single-quoted or backslash-escaped: literal
)

// word is one shell word with each byte's quoting state.
type word struct {
	b, q []byte
}

func (w word) lit() string { return string(w.b) }

func (w word) slice(from int) word { return word{b: w.b[from:], q: w.q[from:]} }

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
		if (r.read || r.write) && len(r.target.b) > 0 {
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

// segment scans one command: its redirections, the wrappers in front of
// it, and the command itself.
func (s *bashScan) segment(seg string) {
	words, redirs := shellWords(seg)
	for _, r := range redirs {
		s.operand(r.target, r.read, r.write)
	}
	words, chdir := s.unwrap(words)
	if len(words) == 0 {
		return
	}
	if chdir != nil {
		// env -C dir / sudo -D dir: this command only runs there.
		saved, savedKnown := s.dirs, s.known
		s.cd(*chdir)
		defer func() { s.dirs, s.known = saved, savedKnown }()
	}
	s.command(words)
}

// shellKeywords start a compound command; the command follows them.
var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "do": true,
	"while": true, "until": true, "!": true,
}

// wrapperSpec describes a command that runs the rest of its words as a
// command, and which of its options take a value.
type wrapperSpec struct {
	value      map[string]bool // options whose value is the next word
	chdir      map[string]bool // options whose value is a working directory
	reads      map[string]bool // options whose value is a file it reads
	writes     map[string]bool // options whose value is a file it writes
	positional int             // operands before the command (timeout's duration)
}

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var wrappers = map[string]wrapperSpec{
	"sudo": {
		value: set("-u", "-g", "-C", "-h", "-p", "-r", "-t", "-T", "-U", "-D", "-R",
			"--user", "--group", "--close-from", "--host", "--prompt", "--role", "--type",
			"--command-timeout", "--other-user", "--chdir", "--chroot"),
		chdir: set("-D", "--chdir"),
	},
	"doas":    {value: set("-u", "-C")},
	"env":     {value: set("-u", "--unset", "-C", "--chdir", "-S", "--split-string"), chdir: set("-C", "--chdir")},
	"timeout": {value: set("-s", "--signal", "-k", "--kill-after"), positional: 1},
	"nice":    {value: set("-n", "--adjustment")},
	"ionice":  {value: set("-c", "-n", "-p", "-P", "-u", "-t", "--class", "--classdata")},
	"stdbuf":  {value: set("-i", "-o", "-e", "--input", "--output", "--error")},
	"nohup":   {},
	"builtin": {},
	"command": {},
	"exec":    {value: set("-a")},
	"time":    {value: set("-f", "--format", "-o", "--output"), writes: set("-o", "--output")},
	"xargs": {
		value: set("-I", "-n", "-P", "-L", "-s", "-d", "-E", "-a", "--arg-file", "--delimiter",
			"--max-args", "--max-procs", "--max-lines", "--max-chars", "--eof", "--replace"),
		reads: set("-a", "--arg-file"),
	},
}

// unwrap drops shell keywords, leading NAME=value assignments and wrapper
// commands (sudo, env, timeout, xargs, ...), consuming their options and
// option values, and unwraps rtk, leaving the command that runs. Files a
// wrapper option names (time -o, xargs -a) are recorded. chdir is set when
// a wrapper runs the command in another directory.
func (s *bashScan) unwrap(words []word) (rest []word, chdir *word) {
	for len(words) > 0 {
		w := words[0].lit()
		if t := strings.TrimLeft(w, "({"); t != w {
			// "(cat f)" / "{ cat f; }": a subshell or group around a command.
			if t == "" {
				words = words[1:]
			} else {
				words = append([]word{words[0].slice(len(w) - len(t))}, words[1:]...)
			}
			continue
		}
		if shellKeywords[w] {
			words = words[1:]
			continue
		}
		if isAssignment(words[0]) {
			words = words[1:]
			continue
		}
		if w == "rtk" {
			if len(words) < 2 {
				return nil, chdir
			}
			switch words[1].lit() {
			case "read":
				words = append([]word{literalWord("cat")}, words[2:]...)
			case "proxy":
				words = words[2:]
			case "ls", "grep", "find", "tree", "git", "wc", "diff", "cat", "head", "tail", "rg":
				words = words[1:]
			default:
				return nil, chdir
			}
			continue
		}
		name := filepath.Base(w)
		spec, ok := wrappers[name]
		if !ok {
			return words, chdir
		}
		i := 1
		for i < len(words) {
			a := words[i].lit()
			if a == "--" {
				i++
				break
			}
			if !strings.HasPrefix(a, "-") || a == "-" {
				break
			}
			if name == "command" && (a == "-v" || a == "-V") {
				return nil, chdir // only looks the command up
			}
			if name == "sudo" && (a == "-e" || a == "--edit") {
				// sudoedit: the operands are files it edits.
				return append([]word{literalWord("sudoedit")}, words[i+1:]...), chdir
			}
			opt, val, hasVal := a, word{}, false
			switch {
			case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
				eq := strings.IndexByte(a, '=')
				opt, val, hasVal = a[:eq], words[i].slice(eq+1), true
				i++
			case spec.value[a]:
				if i+1 < len(words) {
					val, hasVal = words[i+1], true
				}
				i += 2
			case len(a) > 2 && a[1] != '-' && spec.value[a[:2]]:
				opt, val, hasVal = a[:2], words[i].slice(2), true
				i++
			default:
				i++
			}
			if !hasVal {
				continue
			}
			switch {
			case spec.chdir[opt]:
				v := val
				chdir = &v
			case spec.reads[opt]:
				s.operand(val, true, false)
			case spec.writes[opt]:
				s.operand(val, false, true)
			case name == "env" && (opt == "-S" || opt == "--split-string"):
				split, redirs := shellWords(val.lit())
				for _, r := range redirs {
					s.operand(r.target, r.read, r.write)
				}
				// The split string is the command line (followed by any
				// remaining words); env's own options end here.
				return s.unwrapRest(append(split, words[min(i, len(words)):]...), chdir)
			}
		}
		i += spec.positional
		if i > len(words) {
			i = len(words)
		}
		words = words[i:]
	}
	return words, chdir
}

// unwrapRest continues unwrap after env -S has spliced in its command.
func (s *bashScan) unwrapRest(words []word, chdir *word) ([]word, *word) {
	rest, inner := s.unwrap(words)
	if inner != nil {
		chdir = inner
	}
	return rest, chdir
}

// isAssignment reports whether w is NAME=value with an unquoted "=".
func isAssignment(w word) bool {
	eq := -1
	for i, c := range w.b {
		if c == '=' && w.q[i] == qNone {
			eq = i
			break
		}
	}
	if eq <= 0 {
		return false
	}
	for i, c := range w.b[:eq] {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// operandRole says what a file command does with its operands.
type operandRole int

const (
	roleRead      operandRole = iota // every operand is read
	roleWrite                        // every operand is written (or removed)
	roleLastWrite                    // cp/ln: all read, the last is written
	roleFirstExpr                    // grep/sed/awk/jq: first operand is a script, the rest are read
)

// fileCommands are the programs whose operands are files.
var fileCommands = map[string]operandRole{
	"cat": roleRead, "head": roleRead, "tail": roleRead, "less": roleRead,
	"more": roleRead, "nl": roleRead, "wc": roleRead, "sort": roleRead,
	"uniq": roleRead, "diff": roleRead, "cmp": roleRead, "comm": roleRead,
	"file": roleRead, "stat": roleRead, "strings": roleRead, "od": roleRead,
	"xxd": roleRead, "hexdump": roleRead, "base64": roleRead, "md5": roleRead,
	"md5sum": roleRead, "shasum": roleRead, "sha1sum": roleRead,
	"sha256sum": roleRead, "tac": roleRead, "rev": roleRead, "column": roleRead,
	"source": roleRead, ".": roleRead, "ls": roleRead, "cut": roleRead,
	"paste": roleRead, "fold": roleRead, "fmt": roleRead, "expand": roleRead,
	"readlink": roleRead, "realpath": roleRead, "bat": roleRead, "view": roleRead,
	"vi": roleWrite, "vim": roleWrite, "nano": roleWrite, "sudoedit": roleWrite,
	"tee": roleWrite, "touch": roleWrite, "rm": roleWrite, "rmdir": roleWrite,
	"mkdir": roleWrite, "truncate": roleWrite, "shred": roleWrite,
	"chmod": roleWrite, "chown": roleWrite, "chgrp": roleWrite, "mv": roleWrite,
	"cp": roleLastWrite, "ln": roleLastWrite, "install": roleLastWrite, "rsync": roleLastWrite,
	"grep": roleFirstExpr, "egrep": roleFirstExpr, "fgrep": roleFirstExpr,
	"rg": roleFirstExpr, "sed": roleFirstExpr, "awk": roleFirstExpr, "jq": roleFirstExpr,
}

// command scans the command that runs: cd/pushd/popd move the directory,
// dd names its files in if=/of=, and a known file command's operands are
// files.
func (s *bashScan) command(words []word) {
	name := filepath.Base(words[0].lit())
	args := words[1:]
	switch name {
	case "cd", "pushd":
		s.changeDir(name, args)
		return
	case "popd":
		s.known = false
		return
	case "dd":
		for _, a := range args {
			lit := a.lit()
			switch {
			case strings.HasPrefix(lit, "if="):
				s.operand(a.slice(3), true, false)
			case strings.HasPrefix(lit, "of="):
				s.operand(a.slice(3), false, true)
			}
		}
		return
	}
	role, known := fileCommands[name]
	if !known {
		return
	}
	ops, inPlace, scriptGiven := s.operands(name, args)
	switch role {
	case roleRead:
		if name == "uniq" && len(ops) >= 2 {
			s.operand(ops[1], false, true)
			ops = ops[:1]
		}
	case roleFirstExpr:
		if !scriptGiven && len(ops) > 0 {
			ops = ops[1:]
		}
		if inPlace {
			role = roleWrite
		}
	}
	for i, o := range ops {
		write := role == roleWrite || (role == roleLastWrite && i == len(ops)-1)
		s.operand(o, !write, write)
		if t := strings.TrimRight(o.lit(), ")"); t != o.lit() && t != "" {
			// "(cat f)": the ")" closes a subshell.
			s.operand(word{b: o.b[:len(t)], q: o.q[:len(t)]}, !write, write)
		}
	}
}

// changeDir follows cd/pushd: options are skipped; "-", "+N"/"-N", a bare
// pushd, or a target kiln cannot resolve lose track of the directory.
func (s *bashScan) changeDir(name string, args []word) {
	var target *word
	for i := 0; i < len(args); i++ {
		a := args[i].lit()
		if a == "--" {
			if i+1 < len(args) {
				target = &args[i+1]
			}
			break
		}
		if a == "-" || ((strings.HasPrefix(a, "+") || strings.HasPrefix(a, "-")) && isDigits(a[1:])) {
			s.known = false
			return
		}
		if strings.HasPrefix(a, "-") {
			continue // -P, -L, -e, -@, pushd -n
		}
		target = &args[i]
		break
	}
	if target == nil {
		if name == "pushd" {
			s.known = false // swaps the top two stack entries
			return
		}
		home := literalWord(s.home)
		target = &home
	}
	s.cd(*target)
}

// cd adds the directories target may resolve to.
func (s *bashScan) cd(target word) {
	dirs, ok := s.expand(target)
	if !ok || len(dirs) == 0 {
		s.known = false
		return
	}
	for _, d := range dirs {
		if !contains(s.dirs, d) {
			s.dirs = append(s.dirs, d)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// operands are a command's non-flag words. inPlace reports sed -i / -I;
// scriptGiven reports -e/-f (grep, sed, awk, jq: the script is then not
// the first operand). Words after "--" are operands even if they start
// with "-". Flag values ("head -n 5") come through as operands; a stray
// "5" only costs a deny-rule lookup that will not match. "--file=x" values
// are operands, and sort's -o target is written.
func (s *bashScan) operands(name string, args []word) (ops []word, inPlace, scriptGiven bool) {
	dashdash := false
	for i, w := range args {
		a := w.lit()
		switch {
		case dashdash:
			ops = append(ops, w)
		case a == "--":
			dashdash = true
		case strings.HasPrefix(a, "-") && a != "-":
			if name == "sed" && (strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "-I") || strings.HasPrefix(a, "--in-place")) {
				inPlace = true
			}
			if a == "-e" || a == "-f" || strings.HasPrefix(a, "--regexp") || strings.HasPrefix(a, "--file") || strings.HasPrefix(a, "--expression") {
				scriptGiven = true
			}
			if name == "sort" && a == "-o" && i+1 < len(args) {
				s.operand(args[i+1], false, true)
			}
			if eq := strings.IndexByte(a, '='); eq > 0 && strings.HasPrefix(a, "--") {
				ops = append(ops, w.slice(eq+1))
			}
		default:
			ops = append(ops, w)
		}
	}
	return ops, inPlace, scriptGiven
}

// operand records the files w may name, or marks the scan unsure.
func (s *bashScan) operand(w word, read, write bool) {
	paths, ok := s.expand(w)
	if !ok {
		s.unsure = true
		return
	}
	if read {
		s.reads = append(s.reads, paths...)
	}
	if write {
		s.writes = append(s.writes, paths...)
	}
}

// expand resolves a word to the absolute paths it can name: brace
// expansion, ~ and $HOME, then every directory it may be relative to, and
// for an unquoted glob every match (plus the literal, which is what the
// command gets when nothing matches). ok is false when that cannot be
// known: another variable, a command substitution, ~user, a relative path
// after the directory was lost, or too many expansions.
func (s *bashScan) expand(w word) (paths []string, ok bool) {
	alts, ok := braceExpand(w, maxExpansions)
	if !ok {
		return nil, false
	}
	for _, a := range alts {
		text, pattern, isGlob, ok := expandWord(a, s.home)
		if !ok {
			return nil, false
		}
		if text == "" {
			continue
		}
		bases := []string{""}
		if !filepath.IsAbs(text) {
			if !s.known {
				return nil, false
			}
			bases = s.dirs
		}
		for _, b := range bases {
			lit := filepath.Clean(filepath.Join(b, text))
			if b == "" {
				lit = filepath.Clean(text)
			}
			paths = append(paths, lit)
			if isGlob {
				pat := pattern
				if b != "" {
					pat = filepath.Join(escapeGlob(b), pattern)
				}
				matches, _ := filepath.Glob(pat)
				paths = append(paths, matches...)
			}
		}
	}
	return paths, true
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
			var nw word
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

// expandWord applies ~ and $HOME expansion. pattern is the text as a
// filepath.Glob pattern (quoted metacharacters escaped) and isGlob reports
// an unquoted *, ? or [. ok is false for anything else that expands.
func expandWord(w word, home string) (text, pattern string, isGlob, ok bool) {
	b, q := w.b, w.q
	homeWord := func(rest int) {
		hw := literalWord(home)
		b = append(hw.b, b[rest:]...)
		q = append(hw.q, q[rest:]...)
	}
	switch {
	case len(b) > 0 && b[0] == '~' && q[0] == qNone:
		if len(b) > 1 && b[1] != '/' {
			return "", "", false, false // ~user
		}
		if home == "" {
			return "", "", false, false
		}
		homeWord(1)
	case hasLivePrefix(b, q, "${HOME}"):
		homeWord(len("${HOME}"))
	case hasLivePrefix(b, q, "$HOME") && (len(b) == 5 || !isNameByte(b[5])):
		homeWord(len("$HOME"))
	}
	var tb, pb strings.Builder
	for i, c := range b {
		if c == '$' && q[i] != qLit && i+1 < len(b) && (isNameByte(b[i+1]) || strings.IndexByte("{(@*#?$!-", b[i+1]) >= 0) {
			return "", "", false, false
		}
		tb.WriteByte(c)
		if q[i] != qNone && strings.IndexByte(`*?[\`, c) >= 0 {
			pb.WriteByte('\\')
		}
		if q[i] == qNone && strings.IndexByte("*?[", c) >= 0 {
			isGlob = true
		}
		pb.WriteByte(c)
	}
	return tb.String(), pb.String(), isGlob, true
}

func hasLivePrefix(b, q []byte, prefix string) bool {
	return len(b) >= len(prefix) && string(b[:len(prefix)]) == prefix && q[0] != qLit
}

func isNameByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// escapeGlob escapes a literal directory for use in a glob pattern.
func escapeGlob(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(`*?[\`, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
