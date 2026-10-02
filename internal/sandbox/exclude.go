package sandbox

import (
	"path"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/andrepato/harness/internal/claude/settings"
)

// excluded reports whether a bash call leaves the sandbox under
// sandbox.excludedCommands, as Claude Code's settings reference describes
// it (code.claude.com/docs/en/settings-reference#sandbox-excludedcommands):
//
//   - entries use the syntax of a Bash(...) permission rule's content: an
//     exact command, a "cmd *" prefix, or a wildcard pattern;
//   - every command in the call has to match an entry;
//   - the text of the call is matched, so a script that runs docker
//     internally does not match "docker *";
//   - some calls stay sandboxed whatever the entries say: a command
//     starting with sudo, eval or xargs; a cd, pushd or popd anywhere; a
//     command substitution, subshell or control-flow block; a redirection
//     other than a file-descriptor duplicate (2>&1); a command name that
//     comes from a variable; a git clone/init/worktree add/worktree
//     move/bundle create whose path argument is absolute, starts with "~"
//     or contains a ".." segment.
//
// kiln additionally keeps a call sandboxed when it cannot parse it, when
// any word is not a plain literal (a variable or glob), when it runs a job
// in the background, and when a command carries an environment
// assignment: each is a way for the matched text to differ from what runs.
func excluded(command string, patterns []string) bool {
	if len(patterns) == 0 || strings.TrimSpace(command) == "" {
		return false
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(f.Stmts) == 0 {
		return false
	}
	var cmds [][]string
	ok := true
	var visit func(s *syntax.Stmt)
	visit = func(s *syntax.Stmt) {
		if !ok {
			return
		}
		if s.Background || s.Coprocess || s.Negated || s.Disown {
			ok = false
			return
		}
		for _, r := range s.Redirs {
			if !fdDuplicate(r) {
				ok = false
				return
			}
		}
		switch c := s.Cmd.(type) {
		case *syntax.BinaryCmd:
			// &&, ||, | and |&: each side is a command of its own.
			visit(c.X)
			visit(c.Y)
		case *syntax.CallExpr:
			if len(c.Assigns) > 0 || len(c.Args) == 0 {
				ok = false
				return
			}
			words := make([]string, 0, len(c.Args))
			for _, w := range c.Args {
				lit, isLit := literalWord(w)
				if !isLit {
					ok = false
					return
				}
				words = append(words, lit)
			}
			cmds = append(cmds, words)
		default:
			ok = false
		}
	}
	for _, s := range f.Stmts {
		visit(s)
	}
	if !ok || len(cmds) == 0 {
		return false
	}
	for _, words := range cmds {
		if staysSandboxed(words) {
			return false
		}
		text := strings.Join(words, " ")
		matched := false
		for _, p := range patterns {
			if strings.TrimSpace(p) != "" && settings.MatchesRule("Bash("+p+")", "bash", text) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// fdDuplicate reports a redirection that only duplicates a descriptor
// ("2>&1", ">&2"), which writes nowhere new.
func fdDuplicate(r *syntax.Redirect) bool {
	if r.Op != syntax.DplOut && r.Op != syntax.DplIn {
		return false
	}
	lit, ok := literalWord(r.Word)
	if !ok {
		return false
	}
	if lit == "-" {
		return true
	}
	for _, c := range lit {
		if c < '0' || c > '9' {
			return false
		}
	}
	return lit != ""
}

// literalWord returns a word's value when it is plain text: quoted or not,
// but with no expansion, substitution or glob that could make what runs
// differ from what was written.
func literalWord(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(p.Value, "*?[") {
				// A glob expands to whatever is on disk, not the text
				// matched.
				return "", false
			}
			b.WriteString(unescapeLit(p.Value))
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// unescapeLit removes the backslashes bash removes from an unquoted word.
func unescapeLit(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	esc := false
	for _, r := range s {
		if esc {
			b.WriteRune(r)
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// staysSandboxed is the list of command shapes Claude Code keeps in the
// sandbox whatever excludedCommands says, plus exec, source and "." (kiln:
// each runs something other than the text matched).
func staysSandboxed(words []string) bool {
	switch path.Base(words[0]) {
	case "sudo", "eval", "xargs", "cd", "pushd", "popd", "exec", "source", ".":
		return true
	}
	if words[0] != "git" {
		return false
	}
	// git's global options before the subcommand ("git -C dir clone …")
	// change where it writes; keep those sandboxed outright.
	if len(words) < 2 || strings.HasPrefix(words[1], "-") {
		return len(words) >= 2
	}
	sub := words[1]
	rest := words[2:]
	switch {
	case sub == "clone", sub == "init":
	case sub == "worktree" && len(rest) > 0 && (rest[0] == "add" || rest[0] == "move"):
		rest = rest[1:]
	case sub == "bundle" && len(rest) > 0 && rest[0] == "create":
		rest = rest[1:]
	default:
		return false
	}
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if strings.HasPrefix(a, "/") || strings.HasPrefix(a, "~") {
			return true
		}
		for _, seg := range strings.Split(a, "/") {
			if seg == ".." {
				return true
			}
		}
	}
	return false
}
