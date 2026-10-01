package settings

import (
	"path/filepath"
	"strings"
)

// Read and Edit deny rules also apply to the files a bash command names, as
// in Claude Code: the operands of file commands it recognizes (cat, head,
// tail, sed, tee, cp, ...) and the targets of redirections (> f, >> f,
// < f). Like Claude Code's, this is best effort, and only for deny rules: it
// cannot see a file a command reads without naming it ("grep -r x ."), one
// a script opens itself, or one named through a variable other than $HOME
// or a command substitution. The sandbox, not this, is the boundary for
// those.

// bashFileDenied reports whether any file the command line reads or writes
// is blocked by a Read or Edit deny rule. A file the command reads is
// checked as a read call would be; one it writes as a write call (Edit
// rules, and Read deny rules, which also block writes).
func bashFileDenied(p Permissions, c matchCtx, cmd string) bool {
	reads, writes := bashFileOperands(cmd, c.cwd, c.home)
	if len(reads) == 0 && len(writes) == 0 {
		return false
	}
	readRules := c.compileList(p.Deny, p.DenyFrom, listDeny, "read")
	writeRules := c.compileList(p.Deny, p.DenyFrom, listDeny, "write")
	blocked := func(rules []pathRule, files []string) bool {
		if len(rules) == 0 {
			return false
		}
		for _, f := range files {
			if listBlocks(rules, f) {
				return true
			}
			if real := realPath(f); real != f && listBlocks(rules, real) {
				return true
			}
		}
		return false
	}
	return blocked(readRules, reads) || blocked(writeRules, writes)
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
	"vi": roleWrite, "vim": roleWrite, "nano": roleWrite,
	"tee": roleWrite, "touch": roleWrite, "rm": roleWrite, "rmdir": roleWrite,
	"mkdir": roleWrite, "truncate": roleWrite, "shred": roleWrite,
	"chmod": roleWrite, "chown": roleWrite, "chgrp": roleWrite, "mv": roleWrite,
	"cp": roleLastWrite, "ln": roleLastWrite, "install": roleLastWrite, "rsync": roleLastWrite,
	"grep": roleFirstExpr, "egrep": roleFirstExpr, "fgrep": roleFirstExpr,
	"rg": roleFirstExpr, "sed": roleFirstExpr, "awk": roleFirstExpr, "jq": roleFirstExpr,
}

// prefixCommands run the rest of their words as a command.
var prefixCommands = map[string]bool{
	"sudo": true, "command": true, "nohup": true, "time": true, "nice": true,
	"exec": true, "builtin": true, "doas": true,
}

// bashFileOperands returns the absolute paths a command line reads and
// writes, resolved against cwd (following any "cd dir" earlier in the
// line). Words it cannot resolve (an unknown variable, a command
// substitution) are skipped.
func bashFileOperands(cmd, cwd, home string) (reads, writes []string) {
	segments, _ := BashSegments(cmd)
	dir := cwd
	resolve := func(w string) (string, bool) {
		w, ok := expandHome(w, home)
		if !ok || w == "" || w == "-" {
			return "", false
		}
		if filepath.IsAbs(w) {
			return filepath.Clean(w), true
		}
		return filepath.Clean(filepath.Join(dir, w)), true
	}
	for _, seg := range segments {
		words, redirs := shellWords(seg)
		for _, r := range redirs {
			if p, ok := resolve(r.target); ok {
				if r.read {
					reads = append(reads, p)
				}
				if r.write {
					writes = append(writes, p)
				}
			}
		}
		words = stripPrefixes(words)
		if len(words) == 0 {
			continue
		}
		name, args := filepath.Base(words[0]), words[1:]
		if name == "cd" {
			target := home
			if len(args) > 0 {
				target = args[0]
			}
			if p, ok := resolve(target); ok {
				dir = p
			}
			continue
		}
		role, known := fileCommands[name]
		if !known {
			continue
		}
		ops, inPlace, scriptGiven := operands(name, args)
		switch role {
		case roleRead:
			if name == "sort" || name == "uniq" {
				ops, writes = splitOutputOperand(name, args, ops, writes, resolve)
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
			variants := []string{o}
			if t := strings.TrimRight(o, ")"); t != o {
				variants = append(variants, t) // "(cat f)": the ")" closes a subshell
			}
			for _, v := range variants {
				p, ok := resolve(v)
				if !ok {
					continue
				}
				switch {
				case role == roleWrite, role == roleLastWrite && i == len(ops)-1:
					writes = append(writes, p)
				default:
					reads = append(reads, p)
				}
			}
		}
	}
	return reads, writes
}

// operands are a command's non-flag words. inPlace reports sed -i / -I;
// scriptGiven reports -e/-f (grep, sed, awk, jq: the script is then not
// the first operand). Words after "--" are operands even if they start
// with "-". Flag values ("head -n 5") come through as operands; a stray
// "5" only costs a deny-rule lookup that will not match.
func operands(name string, args []string) (ops []string, inPlace, scriptGiven bool) {
	dashdash := false
	for _, a := range args {
		switch {
		case dashdash:
			ops = append(ops, a)
		case a == "--":
			dashdash = true
		case strings.HasPrefix(a, "-") && a != "-":
			if (name == "sed") && (strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "-I") || strings.HasPrefix(a, "--in-place")) {
				inPlace = true
			}
			if a == "-e" || a == "-f" || strings.HasPrefix(a, "--regexp") || strings.HasPrefix(a, "--file") || strings.HasPrefix(a, "--expression") {
				scriptGiven = true
			}
			// "--file=x" / "-o=x" style values name a file too.
			if _, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "--") {
				ops = append(ops, v)
			}
		default:
			ops = append(ops, a)
		}
	}
	return ops, inPlace, scriptGiven
}

// splitOutputOperand moves sort's -o target and uniq's second operand to
// the written set.
func splitOutputOperand(name string, args, ops, writes []string, resolve func(string) (string, bool)) ([]string, []string) {
	if name == "uniq" && len(ops) >= 2 {
		if p, ok := resolve(ops[1]); ok {
			writes = append(writes, p)
		}
		return ops[:1], writes
	}
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			if p, ok := resolve(args[i+1]); ok {
				writes = append(writes, p)
			}
		}
	}
	return ops, writes
}

// stripPrefixes drops leading VAR=value assignments and command prefixes
// (sudo, nohup, ...) and unwraps rtk, leaving the command that runs.
func stripPrefixes(words []string) []string {
	for len(words) > 0 {
		w := words[0]
		if t := strings.TrimLeft(w, "({"); t != w {
			// "(cat f)" / "{ cat f; }": a subshell or group around a command.
			if t == "" {
				words = words[1:]
			} else {
				words = append([]string{t}, words[1:]...)
			}
			continue
		}
		switch {
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "="):
			words = words[1:]
		case prefixCommands[w]:
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				words = words[1:] // sudo -u x is not handled; its flags are
			}
		case w == "rtk":
			u, ok := unwrap(words)
			if !ok {
				return nil
			}
			return u
		default:
			return words
		}
	}
	return words
}

// expandHome expands a leading ~, $HOME or ${HOME}. ok is false for a
// word that still holds a variable or a substitution kiln cannot resolve.
func expandHome(w, home string) (string, bool) {
	switch {
	case w == "~" || w == "$HOME" || w == "${HOME}":
		w = home
	case strings.HasPrefix(w, "~/"):
		w = filepath.Join(home, w[2:])
	case strings.HasPrefix(w, "$HOME/"):
		w = filepath.Join(home, w[len("$HOME/"):])
	case strings.HasPrefix(w, "${HOME}/"):
		w = filepath.Join(home, w[len("${HOME}/"):])
	}
	if home == "" && strings.HasPrefix(w, "~") {
		return "", false
	}
	if strings.ContainsAny(w, "$`") {
		return "", false
	}
	return w, true
}

// redirect is one redirection in a command.
type redirect struct {
	target      string
	read, write bool
}

// shellWords splits one command (a BashSegments segment) into its words,
// with quotes removed and backslash escapes applied, and its redirections.
// A quoted word keeps a literal "$" (single quotes) out of expandHome's way
// by escaping nothing further: "$HOME" in double quotes still expands.
func shellWords(seg string) (words []string, redirs []redirect) {
	var cur strings.Builder
	inWord := false
	pending := -1 // index into redirs awaiting its target word
	flush := func() {
		if !inWord {
			return
		}
		w := cur.String()
		cur.Reset()
		inWord = false
		if pending >= 0 {
			redirs[pending].target = w
			pending = -1
			return
		}
		words = append(words, w)
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c == '\'':
			end := strings.IndexByte(seg[i+1:], '\'')
			if end < 0 {
				cur.WriteString(seg[i+1:])
				i = len(seg)
			} else {
				cur.WriteString(seg[i+1 : i+1+end])
				i += end + 1
			}
			inWord = true
		case c == '"':
			j := i + 1
			for ; j < len(seg) && seg[j] != '"'; j++ {
				if seg[j] == '\\' && j+1 < len(seg) {
					j++
				}
				cur.WriteByte(seg[j])
			}
			i = j
			inWord = true
		case c == '\\' && i+1 < len(seg):
			cur.WriteByte(seg[i+1])
			i++
			inWord = true
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '<' || c == '>' || (c == '&' && i+1 < len(seg) && seg[i+1] == '>'):
			// A word of digits right before the operator is its fd.
			if inWord && isDigits(cur.String()) {
				cur.Reset()
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
					// skip the fd word
					for i+1 < len(seg) && (seg[i+1] == ' ' || seg[i+1] == '\t') {
						i++
					}
					for i+1 < len(seg) && seg[i+1] != ' ' && seg[i+1] != '\t' {
						i++
					}
					continue
				}
				r.write = op == ">&"
				r.read = op == "<&"
			default: // "<<", "<<-", "<<<": a heredoc delimiter or a string
				pending = -2
			}
			if pending == -2 {
				// Consume the next word without recording it.
				pending = len(redirs)
				redirs = append(redirs, redirect{})
				continue
			}
			pending = len(redirs)
			redirs = append(redirs, r)
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flush()
	var out []redirect
	for _, r := range redirs {
		if (r.read || r.write) && r.target != "" {
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
