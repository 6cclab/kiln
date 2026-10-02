package settings

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/andrepato/harness/internal/execenv"
	"mvdan.cc/sh/v3/syntax"
)

// Read and Edit rules also apply to the files a bash command names, as in
// Claude Code: the operands of file commands it recognizes (cat, head,
// tail, sed, tee, cp, ...) and the targets of redirections (> f, >> f,
// >| f, < f). A deny rule blocks the command; an ask rule asks. This is
// best effort, like Claude Code's; it cannot see a file a command reads
// without naming it ("grep -r x .") or one a script opens itself. The
// sandbox, not this, is the boundary for those.
//
// The commands and their words come from the parsed command line
// (bash_parse.go). Paths are resolved the way the shell's open(2) resolves
// them: physically, so "sshl/../x" with sshl a link is the x beside sshl's
// target, not the x beside sshl. Both that and the lexical reading are
// checked.
//
// Where a named file cannot be known statically (a variable, a glob in a
// directory kiln lost track of or too large to expand, a command
// substitution as an operand, a "cd -" or popd before a relative path, a
// command word that is itself an expansion), the command is reported
// unsure. RuleHits passes that on, and DecideFromHits turns an Allow into
// an Ask for it whenever any Read/Edit path rule sits in deny or ask: an
// unknowable path is asked about rather than allowed past a rule. It never
// weakens a Deny.

// bashFileVerdict judges the files the analysed command line reads or
// writes: deny when a Read or Edit deny rule covers one, ask when an ask
// rule does, unsure when the command names a file kiln cannot resolve. All
// three are false unless some Read/Edit path rule is in deny or ask. A file
// the command reads is checked as a read call would be; one it writes as a
// write call (Edit rules, and Read deny rules, which also block writes).
func bashFileVerdict(p Permissions, c matchCtx, an *bashAnalysis) (deny, ask, unsure bool) {
	cr := compiledFor(p, c)
	if !cr.guarded {
		return false, false, false
	}
	reads, writes, unsure := an.reads, an.writes, an.unsure
	a := newAnchors()
	// Every spelling of a file: as written (cleaned), as open(2) resolves
	// it, and the OS's own name for it (execenv.CanonicalPath, memoised per
	// directory for this decision: a command can name hundreds of files).
	spellings := map[string][]string{}
	spell := func(f string) []string {
		if s, ok := spellings[f]; ok {
			return s
		}
		lexical := filepath.Clean(f)
		var real string
		if hasDotSegment(f) {
			real = realPath(f) // physical: a ".." after a link
		} else {
			real = a.resolve(lexical)
		}
		s := appendNew(nil, lexical, real, a.canonical(real))
		spellings[f] = s
		return s
	}
	for _, f := range append(append([]string(nil), reads...), writes...) {
		for _, s := range spell(f) {
			if isVolPath(s) {
				unsure = true // /.vol/<dev>/<inode>: no rule can name it
			}
		}
	}
	covered := func(rules ruleSet, files []string) bool {
		if rules.empty() {
			return false
		}
		for _, f := range files {
			for _, s := range spell(f) {
				if rules.blocks(s, a) {
					return true
				}
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

// isVolPath reports a path under macOS's /.vol, which opens files by
// device and inode number (any case: the root volume ignores it).
func isVolPath(p string) bool { return execenv.IsVolPath(p) }

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

// stdinSource is a command's standard input as far as a redirection
// says: a heredoc or herestring (script holds its text), a file, or
// nothing known (a pipe, or the terminal).
type stdinSource struct {
	script   string
	scripted bool // a heredoc or herestring: script is the text
	file     bool // "< file"
	unknown  bool // a herestring kiln cannot expand
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
	"pkexec":  {value: set("--user")},
	"env":     {value: set("-u", "--unset", "-C", "--chdir", "-S", "--split-string"), chdir: set("-C", "--chdir")},
	"timeout": {value: set("-s", "--signal", "-k", "--kill-after"), positional: 1},
	"nice":    {value: set("-n", "--adjustment")},
	"ionice":  {value: set("-c", "-n", "-p", "-P", "-u", "-t", "--class", "--classdata")},
	"stdbuf":  {value: set("-i", "-o", "-e", "--input", "--output", "--error")},
	"nohup":   {},
	"builtin": {},
	"command": {},
	"noglob":  {},
	"setsid":  {},
	"watch":   {value: set("-n", "--interval", "-q", "--equexit")},
	"flock":   {value: set("-w", "--timeout", "-E", "--conflict-exit-code"), positional: 1},
	"exec":    {value: set("-a")},
	"time":    {value: set("-f", "--format", "-o", "--output"), writes: set("-o", "--output")},
	"xargs": {
		value: set("-I", "-n", "-P", "-L", "-s", "-d", "-E", "-a", "--arg-file", "--delimiter",
			"--max-args", "--max-procs", "--max-lines", "--max-chars", "--eof", "--replace"),
		reads: set("-a", "--arg-file"),
	},
}

// unwrapped is the command a simple command runs once every wrapper in
// front of it is gone.
type unwrapped struct {
	words []evalWord
	chdir *evalWord // the directory a wrapper runs it in (env -C, sudo -D)
	// optReads and optWrites are files wrapper options name (xargs -a,
	// time -o).
	optReads, optWrites []evalWord
	argsFromStdin       bool // under xargs: arguments come from stdin
	dark                bool // a wrapper hides what runs
	execWrapped         bool // an exec wrapper (sudo, env, watch, …) was stripped
}

// unwrap drops leading NAME=value words, wrapper commands (sudo, env,
// timeout, xargs, watch, …) with their options and option values, and rtk,
// leaving the command that runs.
func (a *bashAnalysis) unwrap(words []evalWord) (u unwrapped) {
	for len(words) > 0 {
		if !words[0].literal {
			break
		}
		w := words[0].lit
		if isAssignment(words[0].w) {
			words = words[1:]
			continue
		}
		if w == "rtk" {
			if len(words) < 2 {
				words = nil
				break
			}
			switch words[1].text() {
			case "read":
				words = append([]evalWord{litWord("cat")}, words[2:]...)
			case "proxy":
				words = words[2:]
			case "ls", "grep", "find", "tree", "git", "wc", "diff", "cat", "head", "tail", "rg":
				words = words[1:]
			default:
				u.words = words
				return u
			}
			continue
		}
		name := filepath.Base(w)
		spec, ok := wrappers[name]
		if !ok {
			break
		}
		if name == "xargs" {
			u.argsFromStdin = true
		}
		if execWrappers[name] {
			u.execWrapped = true
		}
		i := 1
		for i < len(words) {
			opt := words[i].text()
			if opt == "--" {
				i++
				break
			}
			if !strings.HasPrefix(opt, "-") || opt == "-" {
				break
			}
			if name == "command" && (opt == "-v" || opt == "-V") {
				return u // only looks the command up
			}
			if name == "sudo" && (opt == "-e" || opt == "--edit") {
				// sudoedit: the operands are files it edits.
				u.words = append([]evalWord{litWord("sudoedit")}, words[i+1:]...)
				return u
			}
			if name == "flock" && (opt == "-c" || opt == "--command") {
				break
			}
			var val evalWord
			hasVal := false
			switch {
			case strings.HasPrefix(opt, "--") && strings.Contains(opt, "="):
				eq := strings.IndexByte(opt, '=')
				val, hasVal = sliceWord(words[i], eq+1), true
				opt = opt[:eq]
				i++
			case spec.value[opt]:
				if i+1 < len(words) {
					val, hasVal = words[i+1], true
				}
				i += 2
			case len(opt) > 2 && opt[1] != '-' && spec.value[opt[:2]]:
				val, hasVal = sliceWord(words[i], 2), true
				opt = opt[:2]
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
				u.chdir = &v
			case spec.reads[opt]:
				u.optReads = append(u.optReads, val)
			case spec.writes[opt]:
				u.optWrites = append(u.optWrites, val)
			case name == "env" && (opt == "-S" || opt == "--split-string"):
				// The split string is the command line, followed by any
				// remaining words; env's own options end there.
				split, ok := splitWords(val)
				if !ok {
					u.dark = true
					return u
				}
				words = append(split, words[min(i, len(words)):]...)
				i = 0
			}
			if i == 0 {
				break
			}
		}
		if i == 0 {
			continue // env -S: unwrap the split string
		}
		i += spec.positional
		if i > len(words) {
			i = len(words)
		}
		words = words[i:]
		if name == "flock" && len(words) >= 2 && (words[0].text() == "-c" || words[0].text() == "--command") {
			// "flock file -c cmd": cmd is a shell command line.
			words = append([]evalWord{litWord("sh"), litWord("-c")}, words[1:]...)
		}
	}
	u.words = words
	return u
}

// splitWords splits env -S's string into words the way a simple command
// is split, or fails.
func splitWords(val evalWord) ([]evalWord, bool) {
	if !val.literal {
		return nil, false
	}
	f, src, ok := parseBash(val.lit)
	if !ok || len(f.Stmts) != 1 {
		return nil, false
	}
	c, isCall := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !isCall || len(c.Assigns) > 0 || len(f.Stmts[0].Redirs) > 0 {
		return nil, false
	}
	var out []evalWord
	for _, w := range c.Args {
		out = append(out, evalShellWord(w, src))
	}
	return out, true
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
	"ls": roleRead, "cut": roleRead,
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

// runs follows what an unwrapped command does: the files wrapper options
// name, then the command itself (in the wrapper's directory, if it set
// one).
func (a *bashAnalysis) runs(u unwrapped, in stdinSource) {
	for _, w := range u.optReads {
		a.operand(w, true, false)
	}
	for _, w := range u.optWrites {
		a.operand(w, false, true)
	}
	if len(u.words) == 0 {
		return
	}
	if u.chdir != nil {
		// env -C dir / sudo -D dir: this command only runs there.
		saved, savedKnown := append([]string(nil), a.dirs...), a.known
		a.cd(*u.chdir)
		defer func() { a.dirs, a.known = saved, savedKnown }()
	}
	a.command1(u.words, in, u.argsFromStdin)
}

// command1 follows the command that runs: cd/pushd/popd move the
// directory, dd names its files in if=/of=, bash -c and eval run a command
// line, source and a shell run a script, and a known file command's
// operands are files. A command word that is itself an expansion ($CMD,
// ${IFS} tricks) is unknown: kiln cannot tell what runs.
func (a *bashAnalysis) command1(words []evalWord, in stdinSource, argsFromStdin bool) {
	cw, ok := expandHome(words[0].w, a.home)
	if !ok {
		a.dark()
		return
	}
	name := filepath.Base(cw.lit())
	args := words[1:]
	switch {
	case name == "cd" || name == "pushd":
		a.changeDir(name, args)
		return
	case name == "popd":
		a.known = false
		return
	case name == "dd":
		for _, w := range args {
			lit := w.w.lit()
			switch {
			case strings.HasPrefix(lit, "if="):
				a.operand(sliceWord(w, 3), true, false)
			case strings.HasPrefix(lit, "of="):
				a.operand(sliceWord(w, 3), false, true)
			}
		}
		return
	case name == "eval":
		var parts []string
		for _, w := range args {
			ew, ok := expandHome(w.w, a.home)
			if !ok {
				a.dark()
				return
			}
			parts = append(parts, ew.lit())
		}
		a.nested(strings.Join(parts, " "))
		return
	case shells[name]:
		a.shell(args, in, argsFromStdin)
		return
	case name == "source" || name == ".":
		// The file is read, and run as commands; from stdin or a process
		// substitution that is the stdin script or unknowable.
		for _, w := range args {
			if lit := w.text(); strings.HasPrefix(lit, "-") && lit != "-" {
				continue
			}
			a.script(w, in)
			return
		}
		return
	}
	role, known := fileCommands[name]
	if !known {
		return
	}
	if dir, rest, ok := targetDirOption(name, args); ok {
		// cp/mv/ln/install -t DIR src...: DIR is the destination, each
		// source lands in it, and the sources are read (moved, for mv).
		a.operand(dir, false, true)
		srcs, _, _ := a.operands(name, rest)
		for _, s := range srcs {
			a.operand(s, name != "mv", name == "mv")
			if dir.literal && s.literal {
				a.operand(litWord(joinRaw(dir.text(), filepath.Base(s.text()))), false, true)
			}
		}
		return
	}
	ops, inPlace, scriptGiven := a.operands(name, args)
	switch role {
	case roleRead:
		if name == "uniq" && len(ops) >= 2 {
			a.operand(ops[1], false, true)
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
		a.operand(o, !write, write)
	}
}

// targetDirValueOpts are, per command taking -t/--target-directory, the
// other short options whose value follows them (GNU coreutils): a cluster
// ends at one of them.
var targetDirValueOpts = map[string]string{
	"cp": "S", "mv": "S", "ln": "S", "install": "gmoS",
}

// targetDirOption finds a destination given by option (GNU cp, mv, ln,
// install: -t DIR, -tDIR, -vt DIR, --target-directory[=]DIR and its
// unambiguous abbreviations) and returns it with the remaining words.
// The last one given wins, as with getopt.
func targetDirOption(name string, args []evalWord) (dir evalWord, rest []evalWord, ok bool) {
	valueOpts, takes := targetDirValueOpts[name]
	if !takes {
		return evalWord{}, nil, false
	}
	const long = "--target-directory"
	for i := 0; i < len(args); i++ {
		w := args[i].text()
		if w == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case strings.HasPrefix(w, "--t") && strings.HasPrefix(long, strings.SplitN(w, "=", 2)[0]):
			if eq := strings.IndexByte(w, '='); eq > 0 {
				dir, ok = sliceWord(args[i], eq+1), true
			} else if i+1 < len(args) {
				dir, ok = args[i+1], true
				i++
			}
			continue
		case strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && len(w) > 1:
			found, skipValue := false, false
			for j := 1; j < len(w); j++ {
				c := w[j]
				if c == 't' {
					if j+1 < len(w) {
						dir, ok = sliceWord(args[i], j+1), true
					} else if i+1 < len(args) {
						dir, ok = args[i+1], true
						i++
					}
					found = true
					break
				}
				if strings.IndexByte(valueOpts, c) >= 0 {
					// The rest of the cluster, or the next word, is its
					// value (install -m 0755): not a source.
					skipValue = j == len(w)-1
					break
				}
			}
			if found {
				continue
			}
			if skipValue {
				rest = append(rest, args[i])
				i++
				continue
			}
		}
		rest = append(rest, args[i])
	}
	return dir, rest, ok
}

// shellValueOpts are bash/sh/zsh/dash/ksh options whose value is the next
// word (so it is not the script operand).
var shellValueOpts = set("-O", "+O", "-o", "+o", "--rcfile", "--init-file")

// isStdinPath reports a script operand that is standard input.
func isStdinPath(p string) bool {
	switch p {
	case "-", "/dev/stdin", "/dev/fd/0", "/proc/self/fd/0":
		return true
	}
	return false
}

// isFdPath reports another /dev/fd/N (a process substitution's pipe).
func isFdPath(p string) bool {
	return strings.HasPrefix(p, "/dev/fd/") || strings.HasPrefix(p, "/proc/self/fd/")
}

// shell follows what a shell runs: "bash -c 'cmd'" (any option cluster
// holding c: -c, -lc, -ec) as that command line; "bash script" as reading
// the script file; and a shell reading its script from standard input
// ("bash -s", "bash -", "bash /dev/stdin", or no script argument) as the
// heredoc or herestring fed to it. Options that take a value (-O extglob,
// --rcfile f) are skipped with it. Under xargs the command line or its
// arguments come from stdin, and with stdin from a pipe or the terminal
// what runs is unknowable.
func (a *bashAnalysis) shell(args []evalWord, in stdinSource, argsFromStdin bool) {
	if argsFromStdin {
		a.dark()
		return
	}
	fromStdin := false
	for i := 0; i < len(args); i++ {
		w := args[i].text()
		if shellValueOpts[w] {
			i++
			continue
		}
		operand := w == "--" || w == "-" || !(strings.HasPrefix(w, "-") || strings.HasPrefix(w, "+"))
		if operand {
			if w == "--" {
				i++
			}
			if i < len(args) && !fromStdin {
				a.script(args[i], in)
				return
			}
			break // -s: the rest are the script's arguments
		}
		if strings.HasPrefix(w, "--") {
			continue
		}
		if strings.HasPrefix(w, "-") && strings.Contains(w, "c") {
			// -c: the next word is the command line.
			if i+1 >= len(args) {
				a.dark() // "sh -c" with the command from elsewhere
				return
			}
			script, ok := expandHome(args[i+1].w, a.home)
			if !ok {
				a.dark()
				return
			}
			a.nested(script.lit())
			return
		}
		if strings.Contains(w, "s") {
			fromStdin = true
		}
		if strings.HasSuffix(w, "o") || strings.HasSuffix(w, "O") {
			i++ // "-eo pipefail": the cluster's last option takes the next word
		}
	}
	a.stdinScript(in)
}

// script handles a script operand (of a shell, source or "."): stdin is
// the stdin script; a process substitution's pipe is unknowable (its
// output is the script); anything else is a file read.
func (a *bashAnalysis) script(w evalWord, in stdinSource) {
	ew, ok := expandHome(w.w, a.home)
	switch {
	case !ok:
		a.dark()
	case isStdinPath(ew.lit()):
		a.stdinScript(in)
	case isFdPath(ew.lit()):
		a.dark()
	default:
		a.operand(w, true, false)
	}
}

// stdinScript follows the script a command reads from standard input.
func (a *bashAnalysis) stdinScript(in stdinSource) {
	switch {
	case in.scripted:
		a.nested(in.script)
	case in.file:
		// "bash < script": the file was recorded as read; like
		// "bash script", its contents are not seen.
	default:
		a.dark() // a pipe ("echo 'cat .env' | sh"), the terminal, <(…)
	}
}

// changeDir follows cd/pushd: options are skipped; "-", "+N"/"-N", a bare
// pushd, or a target kiln cannot resolve lose track of the directory.
func (a *bashAnalysis) changeDir(name string, args []evalWord) {
	var target *evalWord
	for i := 0; i < len(args); i++ {
		w := args[i].text()
		if w == "--" {
			if i+1 < len(args) {
				target = &args[i+1]
			}
			break
		}
		if w == "-" || ((strings.HasPrefix(w, "+") || strings.HasPrefix(w, "-")) && isDigits(w[1:])) {
			a.known = false
			return
		}
		if strings.HasPrefix(w, "-") {
			continue // -P, -L, -e, -@, pushd -n
		}
		target = &args[i]
		break
	}
	if target == nil {
		if name == "pushd" {
			a.known = false // swaps the top two stack entries
			return
		}
		home := litWord(a.home)
		target = &home
	}
	a.cd(*target)
}

// cd adds the directories target may resolve to. A cd in "a | cd x" or
// "(cd x)" does not change the next command's directory, so the
// directories only grow, and a relative path is checked against all of
// them.
func (a *bashAnalysis) cd(target evalWord) {
	if !a.paths {
		return
	}
	dirs, ok := a.expand(target.w)
	if !ok || len(dirs) == 0 {
		a.known = false
		return
	}
	for _, d := range dirs {
		if !contains(a.dirs, d) {
			a.dirs = append(a.dirs, d)
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
func (a *bashAnalysis) operands(name string, args []evalWord) (ops []evalWord, inPlace, scriptGiven bool) {
	dashdash := false
	for i, w := range args {
		lit := w.text()
		switch {
		case dashdash:
			ops = append(ops, w)
		case lit == "--":
			dashdash = true
		case strings.HasPrefix(lit, "-") && lit != "-":
			if name == "sed" && (strings.HasPrefix(lit, "-i") || strings.HasPrefix(lit, "-I") || strings.HasPrefix(lit, "--in-place")) {
				inPlace = true
			}
			if lit == "-e" || lit == "-f" || strings.HasPrefix(lit, "--regexp") || strings.HasPrefix(lit, "--file") || strings.HasPrefix(lit, "--expression") {
				scriptGiven = true
			}
			if name == "sort" && lit == "-o" && i+1 < len(args) {
				a.operand(args[i+1], false, true)
			}
			if eq := strings.IndexByte(lit, '='); eq > 0 && strings.HasPrefix(lit, "--") {
				ops = append(ops, sliceWord(w, eq+1))
			}
		default:
			ops = append(ops, w)
		}
	}
	return ops, inPlace, scriptGiven
}

// operand records the files w may name, or marks the command unsure.
func (a *bashAnalysis) operand(w evalWord, read, write bool) {
	if !a.paths {
		return
	}
	paths, ok := a.expand(w.w)
	if !ok {
		a.unsure = true
		return
	}
	if read {
		a.reads = append(a.reads, paths...)
	}
	if write {
		a.writes = append(a.writes, paths...)
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
func (a *bashAnalysis) expand(w word) (paths []string, ok bool) {
	alts, ok := braceExpand(w, maxExpansions)
	if !ok {
		return nil, false
	}
	for _, alt := range alts {
		ew, ok := expandHome(alt, a.home)
		if !ok {
			return nil, false
		}
		text := ew.lit()
		if text == "" {
			continue
		}
		bases := []string{"/"}
		if !strings.HasPrefix(text, "/") {
			if !a.known {
				return nil, false
			}
			bases = a.dirs
		}
		for _, b := range bases {
			lit := text
			if !strings.HasPrefix(text, "/") {
				lit = joinRaw(b, text)
			}
			paths = append(paths, lit)
			if hasLiveGlob(ew) {
				matches, ok := glob(ew, b, a.budget)
				if !ok {
					return nil, false
				}
				paths = append(paths, matches...)
			}
		}
	}
	return paths, true
}
