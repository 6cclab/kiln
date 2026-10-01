package settings

import (
	"os"
	"path/filepath"
	"strings"
)

// Read and Edit rules also apply to the files a bash command names, as in
// Claude Code: the operands of file commands it recognizes (cat, head,
// tail, sed, tee, cp, ...) and the targets of redirections (> f, >> f,
// >| f, < f). A deny rule blocks the command; an ask rule asks. This is
// best effort, like Claude Code's; it cannot see a file a command reads
// without naming it ("grep -r x .") or one a script opens itself. The
// sandbox, not this, is the boundary for those.
//
// Paths are resolved the way the shell's open(2) resolves them: physically,
// so "sshl/../x" with sshl a link is the x beside sshl's target, not the x
// beside sshl. Both that and the lexical reading are checked.
//
// Where a named file cannot be known statically (a variable, a glob in a
// directory kiln lost track of or too large to expand, a command
// substitution as an operand, a "cd -" or popd before a relative path, a
// command word that is itself an expansion), the command is reported
// unsure. RuleHits passes that on, and DecideFromHits turns an Allow into
// an Ask for it whenever any Read/Edit path rule sits in deny or ask: an
// unknowable path is asked about rather than allowed past a rule. It never
// weakens a Deny.

// bashFileVerdict judges the files the command line reads or writes:
// deny when a Read or Edit deny rule covers one, ask when an ask rule
// does, unsure when the command names a file kiln cannot resolve. All
// three are false unless some Read/Edit path rule is in deny or ask. A file
// the command reads is checked as a read call would be; one it writes as a
// write call (Edit rules, and Read deny rules, which also block writes).
func bashFileVerdict(p Permissions, c matchCtx, cmd string) (deny, ask, unsure bool) {
	cr := compiledFor(p, c)
	if !cr.guarded {
		return false, false, false
	}
	reads, writes, unsure := bashFileOperands(cmd, c.cwd, c.home)
	a := newAnchors()
	resolve := newRealPathMemo()
	covered := func(rules ruleSet, files []string) bool {
		if rules.empty() {
			return false
		}
		for _, f := range files {
			lexical := filepath.Clean(f)
			if rules.blocks(lexical, a) {
				return true
			}
			var real string
			if hasDotSegment(f) {
				real = realPath(f) // physical: a ".." after a link
			} else {
				real = resolve(lexical)
			}
			if real != lexical && rules.blocks(real, a) {
				return true
			}
		}
		return false
	}
	if covered(cr.deny[classRead], reads) || covered(cr.deny[classEdit], writes) {
		return true, false, false
	}
	ask = covered(cr.ask[classRead], reads) || covered(cr.ask[classEdit], writes)
	return false, ask, unsure
}

// hasDotSegment reports a "." or ".." component in p.
func hasDotSegment(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." {
			return true
		}
	}
	return false
}

// newRealPathMemo returns realPath for clean paths with each directory
// resolved once: a decision checks many paths in few directories, and
// resolving a directory costs an lstat per component. A path's last
// component is still checked with its own lstat, so a link there is
// followed.
func newRealPathMemo() func(string) string {
	dirs := map[string]string{}
	return func(p string) string {
		dir, base := filepath.Dir(p), filepath.Base(p)
		if dir == p {
			return realPath(p)
		}
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
// writes (unclean: a ".." in them is for physical resolution), resolved
// against cwd and following any cd/pushd earlier in the line, and whether
// some file operand could not be resolved.
func bashFileOperands(cmd, cwd, home string) (reads, writes []string, unsure bool) {
	s := &bashScan{home: home, dirs: []string{cwd}, known: true, budget: &globBudget{}}
	s.line(cmd)
	return s.reads, s.writes, s.unsure
}

// maxScanDepth bounds nested command lines (substitutions, bash -c, eval).
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
	budget        *globBudget
}

// nested scans a command line that runs inside this one (a substitution,
// bash -c, eval) and merges what it finds.
func (s *bashScan) nested(cmd string) {
	sub := &bashScan{home: s.home, dirs: append([]string(nil), s.dirs...), known: s.known, depth: s.depth + 1, budget: s.budget}
	sub.line(cmd)
	s.reads = append(s.reads, sub.reads...)
	s.writes = append(s.writes, sub.writes...)
	s.unsure = s.unsure || sub.unsure
}

func (s *bashScan) line(cmd string) {
	if s.depth > maxScanDepth {
		s.unsure = true
		return
	}
	// Heredoc bodies are input, not commands; only the substitutions in
	// an unquoted-delimiter body run.
	cmd, docs := stripHeredocs(cmd)
	outer, bodies, ok := extractSubstitutions(cmd, false)
	if !ok {
		s.unsure = true
	}
	for _, d := range docs {
		_, more, ok := extractSubstitutions(d, true)
		if !ok {
			s.unsure = true
		}
		bodies = append(bodies, more...)
	}
	segments, _ := BashSegments(outer)
	for _, seg := range segments {
		s.segment(seg)
	}
	// A substitution runs where it appears; dirs by now holds every
	// directory the line could have been in, so check the bodies against
	// all of them.
	for _, b := range bodies {
		s.nested(b)
	}
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
				rest, inner := s.unwrap(append(split, words[min(i, len(words)):]...))
				if inner != nil {
					chdir = inner
				}
				return rest, chdir
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

// shells run a command string given with -c, or a script file.
var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true}

// command scans the command that runs: cd/pushd/popd move the directory,
// dd names its files in if=/of=, bash -c and eval run a command line, and a
// known file command's operands are files. A command word that is itself
// an expansion ($CMD, ${IFS} tricks) is unsure: kiln cannot tell what runs.
func (s *bashScan) command(words []word) {
	cw, ok := expandHome(words[0], s.home)
	if !ok {
		s.unsure = true
		return
	}
	name := filepath.Base(cw.lit())
	args := words[1:]
	switch {
	case name == "cd" || name == "pushd":
		s.changeDir(name, args)
		return
	case name == "popd":
		s.known = false
		return
	case name == "dd":
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
	case name == "eval":
		var parts []string
		for _, a := range args {
			if _, ok := expandHome(a, s.home); !ok {
				s.unsure = true
				return
			}
			parts = append(parts, a.lit())
		}
		s.nested(strings.Join(parts, " "))
		return
	case shells[name]:
		s.shell(args)
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

// shell scans "bash -c 'cmd'" (any option cluster holding c: -c, -lc, -ec)
// as the command line it runs, and "bash script" as reading the script.
func (s *bashScan) shell(args []word) {
	for i := 0; i < len(args); i++ {
		a := args[i].lit()
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			if a == "--" {
				i++
			}
			if i < len(args) {
				s.operand(args[i], true, false) // the script file
			}
			return
		}
		if strings.HasPrefix(a, "--") || !strings.Contains(a, "c") {
			if a == "-o" || a == "+o" {
				i++ // -o option-name
			}
			continue
		}
		// -c: the next word is the command line.
		if i+1 >= len(args) {
			return
		}
		script, ok := expandHome(args[i+1], s.home)
		if !ok {
			s.unsure = true
			return
		}
		s.nested(script.lit())
		return
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
// "5" only costs a rule lookup that will not match. "--file=x" values are
// operands, and sort's -o target is written.
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

// expand resolves a word to the absolute paths it can name, unclean (the
// shell walks "a/../b" physically, so the ".." is kept for RealPath):
// brace expansion, ~ and $HOME, then every directory it may be relative
// to, and for an unquoted glob every match (plus the literal, which is
// what the command gets when nothing matches). ok is false when that
// cannot be known: another variable, a command substitution, ~user, a
// relative path after the directory was lost, too many expansions or glob
// matches.
func (s *bashScan) expand(w word) (paths []string, ok bool) {
	alts, ok := braceExpand(w, maxExpansions)
	if !ok {
		return nil, false
	}
	for _, alt := range alts {
		ew, ok := expandHome(alt, s.home)
		if !ok {
			return nil, false
		}
		text := ew.lit()
		if text == "" {
			continue
		}
		bases := []string{"/"}
		if !strings.HasPrefix(text, "/") {
			if !s.known {
				return nil, false
			}
			bases = s.dirs
		}
		for _, b := range bases {
			lit := text
			if !strings.HasPrefix(text, "/") {
				lit = joinRaw(b, text)
			}
			paths = append(paths, lit)
			if hasLiveGlob(ew) {
				matches, ok := glob(ew, b, s.budget)
				if !ok {
					return nil, false
				}
				paths = append(paths, matches...)
			}
		}
	}
	return paths, true
}
