package settings

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// kiln's bash analysis runs on a real parser (mvdan.cc/sh, bash dialect):
// the command line becomes a syntax tree, and every simple command in it —
// in a pipeline, a list, a subshell or group, an if/while/for/case body, a
// function body, a command or process substitution, a heredoc fed to a
// shell, or a "sh -c"/eval string, parsed in turn — is collected with its
// words evaluated statically. Permission rules (bash_rules.go), the
// read-only classification (readonly_bash.go) and the Read/Edit checks on
// the files a command names (bash_paths.go) all read that one analysis, so
// they cannot disagree with each other or with bash about where a command
// starts and ends.
//
// What a static reading cannot know is reported, never guessed: a command
// word that is an expansion ("$CMD"), a script read from a pipe, a string
// run by eval or "sh -c" that is not literal. Claude Code's permissions
// doc ("Compound commands", "Read-only commands") is the reference for
// what counts as a command and what a rule may approve.

// maxBashLen is the longest command line analysed; Claude Code prompts for
// anything longer ("Commands longer than 10,000 characters always
// prompt").
const maxBashLen = 10000

// parseBash parses src as bash. It returns the source the tree's offsets
// index and false when the line cannot be parsed (a syntax error, a
// dangling && or ||, an unclosed quote or heredoc) or is too long.
//
// mvdan.cc/sh reads a backslash-newline at the end of a comment as a line
// continuation, so it would parse "echo hi # \<NL>rm x" as one command;
// bash ends a comment at the newline whatever precedes it. Inside a
// comment such a backslash is text, so each one is replaced with a space
// (offsets do not move) and the line is parsed again.
//
// A carriage return makes the line unparseable: the parser takes it for
// blank space (and "\" CR LF for a continuation), while bash takes it for
// a word byte, so the two would split the line differently.
func parseBash(src string) (*syntax.File, string, bool) {
	if len(src) > maxBashLen || strings.ContainsRune(src, '\r') {
		return nil, src, false
	}
	for range 16 {
		f, err := syntax.NewParser(syntax.KeepComments(true), syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "")
		if err != nil {
			return nil, src, false
		}
		var patched []byte
		fix := func(c *syntax.Comment) {
			start, end := int(c.Pos().Offset()), int(c.End().Offset())
			for i := start; i+1 < end && i+1 < len(src); i++ {
				if src[i] == '\\' && src[i+1] == '\n' {
					if patched == nil {
						patched = []byte(src)
					}
					patched[i] = ' '
				}
			}
		}
		syntax.Walk(f, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.Comment); ok {
				fix(c)
			}
			return true
		})
		for i := range f.Last {
			fix(&f.Last[i])
		}
		if patched == nil {
			return f, src, true
		}
		src = string(patched)
	}
	return nil, src, false
}

// evalWord is one word of a parsed command, evaluated statically.
type evalWord struct {
	src string // as written
	// lit is the word's value after quote removal ($'…' decoded). It is
	// the whole truth only when literal is set: then nothing in the word
	// expands (an unquoted glob or brace is still literal text here).
	lit     string
	literal bool
	// w is the word as bytes with their quoting, for path expansion
	// (bash_words.go): a variable keeps its "$NAME" text, unquoted, so ~
	// and $HOME expand and anything else makes the path unknowable.
	w word
}

// text is the word as rules see it: its value when literal, otherwise as
// written.
func (e evalWord) text() string {
	if e.literal {
		return e.lit
	}
	return e.src
}

// litWord is a literal word with the given value.
func litWord(s string) evalWord {
	return evalWord{src: s, lit: s, literal: true, w: literalWord(s)}
}

// sliceWord is e from byte n of its value (an option's attached value).
func sliceWord(e evalWord, n int) evalWord {
	if n > len(e.w.b) {
		n = len(e.w.b)
	}
	w := e.w.slice(n)
	return evalWord{src: string(w.b), lit: string(w.b), literal: e.literal, w: w}
}

func evalShellWord(sw *syntax.Word, src string) evalWord {
	e := evalWord{src: src[sw.Pos().Offset():sw.End().Offset()], literal: true}
	add := func(s string, q byte) {
		for i := 0; i < len(s); i++ {
			e.w.b = append(e.w.b, s[i])
			e.w.q = append(e.w.q, q)
		}
	}
	for _, p := range sw.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			v := p.Value
			for i := 0; i < len(v); i++ {
				if v[i] == '\\' && i+1 < len(v) {
					i++
					if v[i] != '\n' {
						add(v[i:i+1], qLit)
					}
					continue
				}
				if v[i] == '\\' && int(p.End().Offset()) == len(src) {
					continue // a backslash ending the input escapes nothing; bash drops it
				}
				add(v[i:i+1], qNone)
			}
		case *syntax.SglQuoted:
			if !p.Dollar {
				add(p.Value, qLit)
				break
			}
			out, _, ok := decodeANSI(p.Value+"'", 0)
			if !ok {
				e.w.bad = true
			}
			add(string(out), qLit)
		case *syntax.DblQuoted:
			for _, in := range p.Parts {
				lit, ok := in.(*syntax.Lit)
				if !ok {
					e.literal = false
					add(expansionText(in, src), qDouble)
					continue
				}
				v := lit.Value
				for i := 0; i < len(v); i++ {
					if v[i] == '\\' && i+1 < len(v) && strings.IndexByte("$`\"\\\n", v[i+1]) >= 0 {
						i++
						if v[i] != '\n' {
							add(v[i:i+1], qLit)
						}
						continue
					}
					add(v[i:i+1], qDouble)
				}
			}
		default:
			e.literal = false
			if _, ok := p.(*syntax.ExtGlob); ok {
				e.w.bad = true
			}
			add(expansionText(p, src), qNone)
		}
	}
	e.lit = string(e.w.b)
	return e
}

// expansionText is how an expansion appears in a word's bytes: a
// variable as written (so expandHome can tell $HOME from the rest), a
// command or arithmetic substitution as a variable that never expands,
// and a process substitution as the pipe it becomes.
func expansionText(p syntax.WordPart, src string) string {
	switch p.(type) {
	case *syntax.ParamExp:
		return src[p.Pos().Offset():p.End().Offset()]
	case *syntax.ProcSubst:
		return "/dev/fd/63"
	}
	return "$KILN_SUBST"
}

// bashCmd is one simple command as permission rules see it.
type bashCmd struct {
	// forms are the texts deny and ask rules are matched against: as
	// written, and with every leading assignment and wrapper stripped
	// (and the command word's base name), so "FOO=1 nice /bin/rm x" is
	// also "rm x". The inner command of "find -exec" is one too.
	forms []string
	// allow is the text allow rules are matched against: Claude Code's
	// safe wrappers and safe-variable assignments stripped, nothing more,
	// with quotes removed; allowRaw is the same words as written, quotes
	// kept, for a rule that spells the command exactly.
	allow, allowRaw string
	// allowFull and allowFullRaw are the command as written, nothing
	// stripped, quotes removed and kept.
	allowFull, allowFullRaw string
	// exactOnly is set when only an exact rule (no "*") may approve it:
	// an exec wrapper (sudo, env, watch, …) or find running or deleting.
	exactOnly bool
	// readOnly reports a command kiln's read-only set covers;
	// local that it also names no path outside the working directory.
	readOnly, local bool
	// name and line are the command that runs once wrappers are gone (its
	// base name, and its words as text); literal reports every one of
	// those words literal.
	name, line string
	literal    bool
}

// bashAnalysis is everything kiln knows about one command line.
type bashAnalysis struct {
	home  string
	paths bool // follow file operands (bash_paths.go); touches the disk

	parsed bool
	cmds   []bashCmd
	// unknown is set when something runs that kiln cannot name: a command
	// word that is an expansion, a script from a pipe, a non-literal eval
	// or "sh -c" string, nesting past maxScanDepth.
	unknown bool

	// The read-only classification: roOK stays set while nothing in the
	// line's structure rules it out (a substitution, a subshell, a
	// background job, an assignment, a heredoc, a redirect that writes, a
	// word that expands); the rest are Claude Code's cd combinations.
	roOK               bool
	sawCd, cdAway, git bool
	relRedirect        bool
	nonLocalRedirect   bool
	// outsideWrite is set by an output redirect to a path outside the
	// working directory or one that expands (no allow rule covers it).
	outsideWrite bool
	// words is every word and redirect target, for the workspace check
	// (CommandWords); literalWords is false once one is not literal.
	words        []string
	literalWords bool
	// docLines are the lines of heredoc bodies: data, not commands, but
	// deny and ask rules are matched against them too (the stricter
	// reading, which kiln has always had).
	docLines []string
	cwd      string

	// File operands (paths only), resolved against dirs.
	dirs          []string
	known         bool
	reads, writes []string
	unsure        bool
	budget        *globBudget

	src     string
	depth   int
	pending [][]*syntax.Stmt
}

// maxScanDepth bounds nested command lines ("sh -c", eval, a heredoc fed
// to a shell); past it what runs is unknown.
const maxScanDepth = 8

// maxExpansions bounds brace expansion; past it the word is unsure.
const maxExpansions = 256

// analyzeBash analyses one command line. With paths set it also resolves
// the files it names, against cwd and home.
func analyzeBash(cmd, cwd, home string, paths bool) *bashAnalysis {
	a := &bashAnalysis{home: home, paths: paths, roOK: true, literalWords: true,
		cwd: cwd, dirs: []string{cwd}, known: true, budget: &globBudget{}}
	f, src, ok := parseBash(cmd)
	if !ok {
		return a
	}
	a.parsed = true
	a.run(f.Stmts, src)
	return a
}

// dark records that something runs which kiln cannot see.
func (a *bashAnalysis) dark() {
	a.unknown = true
	a.unsure = true
	a.roOK = false
}

// run analyses a parsed script, then the command and process
// substitutions found in it, each from the directories reached by then
// (a cd inside one does not leak out).
func (a *bashAnalysis) run(stmts []*syntax.Stmt, src string) {
	savedSrc, savedPending := a.src, a.pending
	a.src, a.pending = src, nil
	a.stmts(stmts, stdinSource{})
	for len(a.pending) > 0 {
		p := a.pending[0]
		a.pending = a.pending[1:]
		a.scoped(func() { a.stmts(p, stdinSource{}) })
	}
	a.src, a.pending = savedSrc, savedPending
}

// scoped runs fn and then restores the directory state.
func (a *bashAnalysis) scoped(fn func()) {
	dirs, known := append([]string(nil), a.dirs...), a.known
	fn()
	a.dirs, a.known = dirs, known
}

// nested analyses text as a command line run by this one ("sh -c",
// eval, a heredoc fed to a shell).
func (a *bashAnalysis) nested(text string) {
	a.roOK = false
	if a.depth >= maxScanDepth {
		a.dark()
		return
	}
	// The nested interpreter may not be bash: /bin/sh is dash on many
	// Linux systems, and dash reads $'…' and $"…" as a "$" followed by an
	// ordinary quoted string. Words using them can't be evaluated safely.
	if strings.Contains(text, "$'") || strings.Contains(text, "$\"") {
		a.dark()
		return
	}
	f, src, ok := parseBash(text)
	if !ok {
		a.dark()
		return
	}
	a.depth++
	a.scoped(func() { a.run(f.Stmts, src) })
	a.depth--
}

// subs queues the command and process substitutions in n, which run
// wherever they appear.
//
// Two constructs bash 3.2 (the /bin/bash kiln runs) reads differently from
// the parser are unknown: a case clause inside a substitution (bash 3.2
// ends "$(" at the first ")" of a case pattern), and anything bash
// evaluates as arithmetic — $((…)), $[…], an array subscript in an
// expansion or assignment — since a quoted string there is evaluated as
// code ("$(( 'a[$(cmd)]' ))" runs cmd).
func (a *bashAnalysis) subs(n syntax.Node) {
	syntax.Walk(n, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst:
			a.roOK = false
			a.caseInside(n)
			a.pending = append(a.pending, n.Stmts)
			return false
		case *syntax.ProcSubst:
			a.roOK = false
			a.caseInside(n)
			a.pending = append(a.pending, n.Stmts)
			return false
		case *syntax.ArithmExp:
			a.dark()
		case *syntax.ParamExp:
			a.roOK = false
			if n.Index != nil {
				a.dark()
			}
		case *syntax.Assign:
			if n.Index != nil {
				a.dark()
			}
		case *syntax.ArrayElem:
			if n.Index != nil {
				a.dark()
			}
		case *syntax.ExtGlob:
			a.roOK = false
		}
		return true
	})
}

// caseInside marks a substitution holding a case clause unknown.
func (a *bashAnalysis) caseInside(n syntax.Node) {
	syntax.Walk(n, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.CaseClause); ok {
			a.dark()
			return false
		}
		return true
	})
}

func (a *bashAnalysis) slice(n syntax.Node) string {
	return a.src[n.Pos().Offset():n.End().Offset()]
}

func (a *bashAnalysis) eval(w *syntax.Word) evalWord { return evalShellWord(w, a.src) }

func (a *bashAnalysis) stmts(list []*syntax.Stmt, in stdinSource) {
	for _, s := range list {
		a.stmt(s, in)
	}
}

func (a *bashAnalysis) stmt(s *syntax.Stmt, in stdinSource) {
	if s.Background || s.Coprocess || s.Negated || s.Disown {
		a.roOK = false
	}
	in = a.redirects(s.Redirs, in)
	if s.Cmd != nil {
		a.command(s.Cmd, in)
	}
}

// leaf records a construct that is not a simple command but still needs
// a rule to be approved ([[ ]], (( )), let, declare/export).
func (a *bashAnalysis) leaf(n syntax.Node) {
	a.roOK = false
	a.subs(n)
	t := a.slice(n)
	a.cmds = append(a.cmds, bashCmd{forms: []string{t}, allow: t})
}

func (a *bashAnalysis) command(c syntax.Command, in stdinSource) {
	switch c := c.(type) {
	case *syntax.CallExpr:
		a.call(c, in)
	case *syntax.BinaryCmd:
		if c.Op != syntax.AndStmt && c.Op != syntax.OrStmt && c.Op != syntax.Pipe {
			a.roOK = false
		}
		a.stmt(c.X, in)
		if c.Op == syntax.Pipe || c.Op == syntax.PipeAll {
			a.stmt(c.Y, stdinSource{}) // reads the pipe
		} else {
			a.stmt(c.Y, in)
		}
	case *syntax.Subshell:
		a.roOK = false
		a.stmts(c.Stmts, in)
	case *syntax.Block:
		a.roOK = false
		a.stmts(c.Stmts, in)
	case *syntax.IfClause:
		a.roOK = false
		for cl := c; cl != nil; cl = cl.Else {
			a.stmts(cl.Cond, in)
			a.stmts(cl.Then, in)
		}
	case *syntax.WhileClause:
		a.roOK = false
		a.stmts(c.Cond, in)
		a.stmts(c.Do, in)
	case *syntax.ForClause:
		a.roOK = false
		if _, arith := c.Loop.(*syntax.CStyleLoop); arith {
			a.dark() // for ((…)): arithmetic, see subs
		}
		a.subs(c.Loop)
		a.stmts(c.Do, in)
	case *syntax.CaseClause:
		a.roOK = false
		a.subs(c.Word)
		for _, item := range c.Items {
			for _, p := range item.Patterns {
				a.subs(p)
			}
			a.stmts(item.Stmts, in)
		}
	case *syntax.FuncDecl:
		// The body counts as commands: defining a function is how a line
		// would otherwise hide one ("f() { rm x; }; f").
		a.roOK = false
		a.stmt(c.Body, in)
	case *syntax.TimeClause:
		// "time" runs its pipeline. Before a simple command it is read as
		// the time program would be (its -o names a file it writes), and
		// stripped for allow rules like Claude Code's other wrappers.
		a.roOK = false
		if c.Stmt == nil {
			break
		}
		if call, ok := c.Stmt.Cmd.(*syntax.CallExpr); ok && len(call.Assigns) == 0 && len(call.Args) > 0 &&
			!c.Stmt.Background && !c.Stmt.Negated {
			a.call(call, a.redirects(c.Stmt.Redirs, in), litWord("time"))
			break
		}
		a.stmt(c.Stmt, in)
	case *syntax.CoprocClause:
		a.roOK = false
		a.stmt(c.Stmt, stdinSource{})
	case *syntax.ArithmCmd, *syntax.LetClause:
		a.leaf(c)
		a.dark() // arithmetic, see subs
	case *syntax.TestClause:
		a.leaf(c)
		// [[ a -eq b ]] evaluates its operands as arithmetic.
		syntax.Walk(c, func(n syntax.Node) bool {
			if b, ok := n.(*syntax.BinaryTest); ok {
				switch b.Op {
				case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq, syntax.TsLss, syntax.TsGtr:
					a.dark()
				}
			}
			return true
		})
	case *syntax.DeclClause:
		a.leaf(c)
	default:
		a.dark()
	}
}

// redirects records a statement's redirections and returns the standard
// input its command reads.
func (a *bashAnalysis) redirects(rs []*syntax.Redirect, in stdinSource) stdinSource {
	for _, r := range rs {
		if r.Word != nil {
			a.subs(r.Word)
		}
		if r.Hdoc != nil {
			a.subs(r.Hdoc)
			// An expansion in an unquoted heredoc body that spans lines may
			// span the delimiter line, where bash 3.2 ends the heredoc and
			// the parser does not.
			for _, p := range r.Hdoc.Parts {
				if _, lit := p.(*syntax.Lit); !lit && strings.Contains(a.slice(p), "\n") {
					a.dark()
				}
			}
		}
		switch r.Op {
		case syntax.Hdoc, syntax.DashHdoc:
			a.roOK = false
			text := a.hdocText(r)
			for _, l := range strings.Split(text, "\n") {
				if l = strings.TrimSpace(l); l != "" {
					a.docLines = append(a.docLines, l)
				}
			}
			in = stdinSource{script: text, scripted: true}
			continue
		case syntax.WordHdoc:
			a.roOK = false
			e := a.eval(r.Word)
			if t, ok := expandHome(e.w, a.home); ok {
				in = stdinSource{script: t.lit(), scripted: true}
			} else {
				in = stdinSource{unknown: true}
			}
			continue
		}
		if r.Word == nil {
			continue
		}
		e := a.eval(r.Word)
		t := e.w.lit()
		dup := r.Op == syntax.DplIn || r.Op == syntax.DplOut
		if dup && (t == "-" || isDigits(t)) {
			// Duplicating or closing a descriptor: no file behind it.
			if !(r.Op == syntax.DplOut && (t == "1" || t == "2")) {
				a.roOK = false
			}
			continue
		}
		a.target(e)
		switch r.Op {
		case syntax.RdrIn:
			a.operand(e, true, false)
			switch {
			case isStdinPath(t):
				// "< /dev/stdin": still whatever stdin already was.
			case isFdPath(t):
				in = stdinSource{unknown: true} // "< <(cmd)": cmd's output
			default:
				in = stdinSource{file: true}
			}
		case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll:
			if !(e.literal && e.lit == "/dev/null") {
				a.roOK = false
			}
			a.writeTarget(e)
			a.operand(e, false, true)
		case syntax.DplIn:
			a.roOK = false
			a.operand(e, true, false)
		case syntax.DplOut:
			a.roOK = false // ">&file" writes file
			a.writeTarget(e)
			a.operand(e, false, true)
		default: // <>, and anything newer
			a.roOK = false
			a.writeTarget(e)
			a.operand(e, true, true)
		}
	}
	return in
}

// writeTarget notes an output redirect target outside the working
// directory, or one kiln cannot spell out: Claude Code asks for those
// whatever allow rule matches the command ("A rule such as Bash(git
// commit *) allows the command, not the target").
func (a *bashAnalysis) writeTarget(e evalWord) {
	if !e.literal {
		a.outsideWrite = true
		return
	}
	switch t := e.lit; {
	case t == "/dev/null" || t == "/dev/stdout" || t == "/dev/stderr":
	case strings.HasPrefix(t, "~"):
		a.outsideWrite = true
	case filepath.IsAbs(t):
		rel, err := filepath.Rel(a.cwd, t)
		if a.cwd == "" || err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			a.outsideWrite = true
		}
	case !localPath(t):
		a.outsideWrite = true
	}
}

// target notes a redirect target for the read-only checks.
func (a *bashAnalysis) target(e evalWord) {
	if !e.literal {
		a.literalWords = false
		a.roOK = false
		return
	}
	a.words = append(a.words, e.lit)
	if e.lit == "/dev/null" {
		return
	}
	if !filepath.IsAbs(e.lit) {
		a.relRedirect = true
	}
	if !localPath(e.lit) {
		a.nonLocalRedirect = true
	}
}

// hdocText is the text of a heredoc: as written for a quoted delimiter;
// for an unquoted one with backslash escapes applied (bash expands the
// rest, which a shell fed it would then run). <<- strips leading tabs.
func (a *bashAnalysis) hdocText(r *syntax.Redirect) string {
	if r.Hdoc == nil {
		return ""
	}
	quoted := false
	for _, p := range r.Word.Parts {
		switch p := p.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			quoted = true
		case *syntax.Lit:
			if strings.Contains(p.Value, `\`) {
				quoted = true
			}
		}
	}
	var b strings.Builder
	for _, p := range r.Hdoc.Parts {
		lit, ok := p.(*syntax.Lit)
		if !ok {
			b.WriteString(a.slice(p))
			continue
		}
		v := lit.Value
		if quoted {
			b.WriteString(v)
			continue
		}
		for i := 0; i < len(v); i++ {
			if v[i] == '\\' && i+1 < len(v) && strings.IndexByte("$`\\\n", v[i+1]) >= 0 {
				i++
				if v[i] != '\n' {
					b.WriteByte(v[i])
				}
				continue
			}
			b.WriteByte(v[i])
		}
	}
	text := b.String()
	if r.Op == syntax.DashHdoc {
		lines := strings.Split(text, "\n")
		for i := range lines {
			lines[i] = strings.TrimLeft(lines[i], "\t")
		}
		text = strings.Join(lines, "\n")
	}
	return text
}

// specialVars are shell variables whose assignment changes what later
// commands run or how they split (Claude Code: "Writes to special shell
// variables" prompt).
var specialVars = set("PATH", "IFS", "BASH_ENV", "ENV", "PROMPT_COMMAND", "CDPATH", "LD_PRELOAD", "LD_LIBRARY_PATH")

// assignment is one leading NAME=value of a simple command.
type assignment struct {
	name, text string
	plain      bool // the value is literal and harmless (safeEnvValue)
}

func (a *bashAnalysis) call(c *syntax.CallExpr, in stdinSource, prefix ...evalWord) {
	var assigns []assignment
	for _, as := range c.Assigns {
		a.subs(as)
		a.roOK = false
		name := ""
		if as.Name != nil {
			name = as.Name.Value
		}
		plain := false
		if as.Value != nil && !as.Append && as.Index == nil && as.Array == nil {
			v := a.eval(as.Value)
			plain = v.literal && safeEnvValue(v.lit)
		}
		assigns = append(assigns, assignment{name: name, text: a.slice(as), plain: plain})
	}
	for _, w := range c.Args {
		a.subs(w)
	}
	if len(c.Args) == 0 {
		// Only assignments: still something a rule must approve.
		var texts []string
		for _, as := range assigns {
			texts = append(texts, as.text)
		}
		t := strings.Join(texts, " ")
		a.cmds = append(a.cmds, bashCmd{forms: []string{t}, allow: t})
		return
	}
	words := append([]evalWord(nil), prefix...)
	for _, w := range c.Args {
		e := a.eval(w)
		if e.literal {
			a.words = append(a.words, e.lit)
			// A variable name with a subscript ("a[$(cmd)]") given to
			// unset, read, printf -v, declare and the like is evaluated as
			// arithmetic: the substitution in it runs.
			if strings.Contains(e.lit, "[") && strings.ContainsAny(e.lit, "$`") {
				a.dark()
			}
		} else {
			a.literalWords = false
			a.roOK = false
		}
		// Brace expansion comes first in bash, so "{rm,-rf} x" runs
		// "rm -rf x": a word becomes the words it expands to.
		alts, ok := braceExpand(e.w, maxExpansions)
		if !ok {
			a.dark()
			alts = []word{e.w}
		}
		if len(alts) == 1 && string(alts[0].b) == string(e.w.b) {
			words = append(words, e)
			continue
		}
		a.roOK = false
		for _, alt := range alts {
			ew := evalWord{src: string(alt.b), lit: string(alt.b), literal: e.literal, w: alt}
			if !e.literal {
				ew.src = e.src
			}
			words = append(words, ew)
		}
	}
	a.simple(assigns, words, in)
}

// joinWords is words as rule text, after any assignment texts.
func joinWords(pre []string, words []evalWord) string {
	parts := append([]string(nil), pre...)
	for _, w := range words {
		parts = append(parts, w.text())
	}
	return strings.Join(parts, " ")
}

// simple analyses one simple command.
func (a *bashAnalysis) simple(assigns []assignment, words []evalWord, in stdinSource) {
	var assignTexts []string
	for _, as := range assigns {
		assignTexts = append(assignTexts, as.text)
	}
	cmd := bashCmd{forms: []string{joinWords(assignTexts, words), joinWords(nil, words)}}

	u := a.unwrap(words)
	if u.dark || !words[0].literal || (len(u.words) > 0 && !u.words[0].literal) {
		a.dark()
	}
	if len(u.words) > 0 {
		cmd.name = filepath.Base(u.words[0].text())
		cmd.line = joinWords(nil, u.words)
		cmd.literal = true
		for _, w := range u.words {
			cmd.literal = cmd.literal && w.literal
		}
		cmd.forms = append(cmd.forms, cmd.line)
		if base := filepath.Base(u.words[0].text()); base != u.words[0].text() {
			cmd.forms = append(cmd.forms, joinWords([]string{base}, u.words[1:]))
		}
		cmd.forms = append(cmd.forms, findExecForms(u.words)...)
	}

	// Allow rules see the command with Claude Code's safe wrappers and
	// safe-variable assignments stripped, and nothing else.
	cc, kept := ccStrip(assigns, words)
	cmd.allow = joinWords(kept, cc)
	raw := append([]string(nil), kept...)
	for _, w := range cc {
		raw = append(raw, w.src)
	}
	cmd.allowRaw = strings.Join(raw, " ")
	// The command as written, wrappers and assignments kept, too: an exact
	// rule such as Bash(timeout 3 pnpm start:*) names it that way.
	cmd.allowFull = joinWords(assignTexts, words)
	full := append([]string(nil), assignTexts...)
	for _, w := range words {
		full = append(full, w.src)
	}
	cmd.allowFullRaw = strings.Join(full, " ")
	for _, w := range words {
		if w.literal && filepath.Base(w.lit) == "xargs" {
			// What xargs runs is matched as itself, never as "xargs …".
			cmd.allowFull, cmd.allowFullRaw = "", ""
		}
		if !w.literal || !ccWrappers[filepath.Base(w.lit)] {
			break
		}
	}
	if len(kept) == 0 && len(cc) > 0 && cc[0].literal {
		name := filepath.Base(cc[0].lit)
		cmd.exactOnly = execWrappers[name] || (name == "find" && findRuns(cc[1:]))
	}
	if u.execWrapped || (len(u.words) > 0 && filepath.Base(u.words[0].text()) == "find" && findRuns(u.words[1:])) {
		cmd.exactOnly = true
	}

	// Read-only: kiln's set, on the command as written.
	lits := make([]string, 0, len(words))
	allLit := len(assigns) == 0
	for _, w := range words {
		if !w.literal || w.w.bad {
			allLit = false
			break
		}
		lits = append(lits, w.lit)
	}
	if allLit {
		cmd.readOnly = readOnlyInvocation(lits) && !globWithWriteFlags(words)
		cmd.local = cmd.readOnly && localArgs(lits[1:])
	}
	// Claude Code's cd combinations: git after a cd elsewhere, and a
	// relative redirect after any cd, are not read-only.
	if len(u.words) > 0 && u.words[0].literal {
		switch filepath.Base(u.words[0].lit) {
		case "git":
			a.git = true
		case "cd", "pushd", "popd":
			a.sawCd = true
			var args []string
			for _, w := range u.words[1:] {
				args = append(args, w.text())
			}
			if !a.sameDir(firstOperand(args)) {
				a.cdAway = true
			}
		}
	}
	for _, as := range assigns {
		if specialVars[as.name] {
			a.roOK = false
		}
	}
	a.cmds = append(a.cmds, cmd)
	a.runs(u, in)
}

// sameDir reports a cd target that is the working directory itself, so
// the cd is a no-op ("." always; an absolute or relative spelling of the
// working directory when it is known). "" (cd to home) is not.
func (a *bashAnalysis) sameDir(t string) bool {
	if t == "." || t == "./" {
		return true
	}
	if t == "" || a.cwd == "" || strings.HasPrefix(t, "~") || strings.HasPrefix(t, "-") {
		return false
	}
	p := t
	if !filepath.IsAbs(p) {
		p = filepath.Join(a.cwd, p)
	}
	p, cwd := filepath.Clean(p), filepath.Clean(a.cwd)
	if p == cwd {
		return true
	}
	// Another spelling of the same directory (/var vs /private/var) is
	// resolved on disk; a differently named link to it counts as a cd
	// elsewhere, the cautious reading.
	return filepath.Base(p) == filepath.Base(cwd) && realPath(p) == realPath(cwd)
}

// firstOperand is the first argument that is not an option, "" if none.
func firstOperand(args []string) string {
	for _, w := range args {
		if !strings.HasPrefix(w, "-") || w == "-" {
			return w
		}
	}
	return ""
}

// localPath reports a path that stays in the working directory: relative,
// no "~", no "..".
func localPath(p string) bool {
	if p == "/dev/null" {
		return true
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "~") {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return false
		}
	}
	return true
}

// localArgs reports arguments (and --opt=value values) that are all
// local paths, as far as a word can be one.
func localArgs(args []string) bool {
	for _, w := range args {
		if _, v, ok := strings.Cut(w, "="); ok && strings.HasPrefix(w, "-") {
			w = v
		}
		if !localPath(w) {
			return false
		}
	}
	return true
}

// globWithWriteFlags reports an unquoted glob given to a command that has
// write- or exec-capable flags: the glob could expand to one ("-delete").
func globWithWriteFlags(words []evalWord) bool {
	switch filepath.Base(words[0].lit) {
	case "find", "sort", "sed", "git":
	default:
		return false
	}
	for _, w := range words[1:] {
		if hasLiveGlob(w.w) {
			return true
		}
	}
	return false
}

// execWrappers run their arguments as a command; Claude Code does not let
// a prefix rule approve them ("Exec wrappers such as watch, setsid,
// ionice, and flock can't be auto-approved by a prefix rule"), nor kiln
// the privilege and environment wrappers.
var execWrappers = set("watch", "setsid", "ionice", "flock", "sudo", "doas", "pkexec", "env", "exec", "chroot", "unshare", "nsenter")

// findRuns reports find arguments that run a command or delete files.
func findRuns(args []evalWord) bool {
	for _, w := range args {
		switch w.text() {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete":
			return true
		}
	}
	return false
}

// findExecForms are the commands "find … -exec cmd … ;" runs, as rule text.
func findExecForms(words []evalWord) []string {
	if len(words) == 0 || filepath.Base(words[0].text()) != "find" {
		return nil
	}
	var out []string
	for i := 1; i < len(words); i++ {
		switch words[i].text() {
		case "-exec", "-execdir", "-ok", "-okdir":
		default:
			continue
		}
		j := i + 1
		for j < len(words) && words[j].text() != ";" && words[j].text() != "+" {
			j++
		}
		if j > i+1 {
			out = append(out, joinWords(nil, words[i+1:j]))
		}
		i = j
	}
	return out
}

// safeEnvVars may lead a command without stopping an allow rule from
// matching past them ("NODE_ENV=test npm test" is "npm test"): each only
// names a setting, never a program, library or path to load. Variables
// that can (PATH, LD_PRELOAD, NODE_OPTIONS, GOFLAGS, PYTHONPATH, …) are
// deliberately absent. A deny or ask rule matches past any assignment.
var safeEnvVars = set(
	"NODE_ENV", "RUST_LOG", "RUST_BACKTRACE",
	"GOOS", "GOARCH", "CGO_ENABLED", "GO111MODULE", "GOEXPERIMENT",
	"PYTHONUNBUFFERED", "PYTHONDONTWRITEBYTECODE",
	"LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "LC_TIME", "TZ",
	"TERM", "COLORTERM", "NO_COLOR", "FORCE_COLOR", "CLICOLOR",
)

// safeEnvValue reports a value that is plain text: letters, digits and
// "_./:-" (no expansion, quoting or separator).
func safeEnvValue(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(isNameByte(c) || strings.IndexByte("./:-", c) >= 0) {
			return false
		}
	}
	return true
}

// ccWrappers are the wrappers Claude Code strips before matching an
// allow rule: each runs its argument list as the command.
var ccWrappers = set("timeout", "time", "nice", "nohup", "stdbuf", "command", "builtin", "noglob")

// ccStrip is a command as allow rules see it: leading safe-variable
// assignments dropped, then Claude Code's safe wrappers (with their
// options) and a bare xargs. kept are the assignments it could not drop;
// an allow rule does not match past them.
func ccStrip(assigns []assignment, words []evalWord) (rest []evalWord, kept []string) {
	i := 0
	for i < len(assigns) && safeEnvVars[assigns[i].name] && assigns[i].plain {
		i++
	}
	for _, as := range assigns[i:] {
		kept = append(kept, as.text)
	}
	if len(kept) > 0 {
		return words, kept
	}
	for len(words) > 1 && words[0].literal {
		name := words[0].lit
		if name == "xargs" {
			if words[1].literal && !strings.HasPrefix(words[1].lit, "-") {
				words = words[1:]
				continue
			}
			break
		}
		if !ccWrappers[name] {
			break
		}
		if name == "command" && (words[1].text() == "-v" || words[1].text() == "-V") {
			break // a lookup, not a run
		}
		n, ok := skipOptions(wrappers[name], words)
		if !ok || n >= len(words) {
			break
		}
		words = words[n:]
	}
	return words, nil
}

// skipOptions returns the index of the command a wrapper runs, past its
// options, their values and its positional operands. ok is false when an
// option value is not literal (it could expand to anything).
func skipOptions(spec wrapperSpec, words []evalWord) (int, bool) {
	i := 1
	for i < len(words) {
		a := words[i]
		if !a.literal {
			return i, false
		}
		if a.lit == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a.lit, "-") || a.lit == "-" {
			break
		}
		if spec.value[a.lit] {
			if i+1 < len(words) && !words[i+1].literal {
				return i, false
			}
			i += 2
			continue
		}
		i++
	}
	return min(i+spec.positional, len(words)), true
}
