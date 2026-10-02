package settings

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/andrepato/harness/internal/execenv"
)

// Read and Edit path rules, matched the way Claude Code matches them
// (https://code.claude.com/docs/en/permissions, "Read and Edit" and
// "Symlinks"). A rule's path is never compared to the raw argument text:
// both sides become absolute paths first.
//
//   - Anchors: "//p" is absolute, "~/p" is under $HOME, "/p" is under the
//     settings source's directory (RuleSource.Root: ~/.claude for user
//     settings, the --settings file's directory, otherwise the primary
//     working directory), and "p" or "./p" is under the current directory.
//     A rule only matches at or under its anchor.
//   - gitignore patterns: "*" stays inside one path segment, "**" spans
//     segments. A relative pattern with no slash (".env", "*.key") matches
//     at any depth. A relative single-directory pattern ("secrets/**")
//     matches at any depth in a deny or ask rule, but only at <cwd>/secrets
//     in an allow rule. Every other shape is anchored. A pattern that
//     matches a directory also covers everything under it (gitignore: so an
//     allow "Edit(src/)" covers a src directory at any depth, as gitignore
//     reads a pattern with only a trailing slash).
//   - "!p" in a deny or ask list carves p out of the relative rules listed
//     before it from the same source; it cannot reopen a file inside a
//     directory those rules block as a whole.
//   - Symlinks: a deny or ask rule applies when the requested path OR the
//     path it resolves to matches; an allow rule needs BOTH. A dangling
//     final link resolves to its (missing) target. Each rule is also tried
//     at its anchor's real location, so "//tmp/**" covers /private/tmp on
//     macOS.
//   - A deny or ask pattern that is not a valid glob still guards that
//     exact path; an invalid allow pattern approves nothing.
//   - Deny and ask rules compare paths in Unicode NFC, and ignore case where
//     the filesystem does (foldCase): ".ENV" and a decomposed "café" open
//     the same file there. Allow rules compare exactly.
//
// Rule tool names: Read and Edit are path rules. Claude Code accepts
// Write(...), MultiEdit(...), NotebookEdit(...) and Glob(...) path rules
// but never consults them, and warns at startup. kiln deliberately differs
// in one direction only: a DENY or ASK rule written that way is honoured as
// the Edit (resp. Read) rule it was meant to be, so a misspelt deny never
// leaves a file unguarded; an ALLOW rule written that way is ignored, as
// in Claude Code, so it never approves more than Claude Code would. An
// empty path ("Read()") in a deny or ask rule is taken as the bare tool
// rule; in an allow rule it approves nothing. FileRuleWarnings reports
// every such rule at startup.

// ruleList says which list a rule came from; matching depth and symlink
// handling differ between allow and deny/ask.
type ruleList int

const (
	listAllow ruleList = iota
	listDeny
	listAsk
)

// fileKind is the file-permission family of a tool or a rule.
type fileKind int

const (
	kindNone fileKind = iota
	kindRead
	kindEdit
)

// norm lower-cases a tool name and drops underscores, so Claude Code's
// MultiEdit and kiln's multi_edit compare equal.
func normTool(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "")
}

// readTools are the built-in tools Read rules apply to. kiln ships only
// read; grep, glob and ls are listed so a tool by that name (or a future
// one) is covered the way Claude Code's best-effort Read check covers it.
var readTools = map[string]bool{"read": true, "grep": true, "glob": true, "ls": true}

// editTools are the built-in tools that edit files: Edit rules apply to
// all of them.
var editTools = map[string]bool{"edit": true, "write": true, "multiedit": true, "notebookedit": true}

// readDenyCoversEdit are the edit tools a Read deny rule also blocks.
// NotebookEdit is not one of them, as in Claude Code.
var readDenyCoversEdit = map[string]bool{"edit": true, "write": true, "multiedit": true}

// toolFileKind is the family a tool call belongs to.
func toolFileKind(tool string) fileKind {
	switch t := normTool(tool); {
	case readTools[t]:
		return kindRead
	case editTools[t]:
		return kindEdit
	}
	return kindNone
}

// IsFileTool reports whether Read/Edit path rules judge this tool, so the
// gate knows to hand Decide the call's path argument.
func IsFileTool(tool string) bool { return toolFileKind(tool) != kindNone }

// ruleFileKind maps a rule's tool name to the path-rule family it is
// checked as. alias is true for Write/MultiEdit/NotebookEdit/Glob, the
// names Claude Code accepts but never consults for path rules.
func ruleFileKind(ruleTool string) (kind fileKind, alias bool) {
	switch normTool(ruleTool) {
	case "read":
		return kindRead, false
	case "edit":
		return kindEdit, false
	case "write", "multiedit", "notebookedit":
		return kindEdit, true
	case "glob":
		return kindRead, true
	}
	return kindNone, false
}

// fileRule is a parenthesized rule naming a file tool.
type fileRule struct {
	tool    string // as written: "Read", "Write", ...
	kind    fileKind
	alias   bool
	pattern string
}

// splitFileRule reports whether rule is a parenthesized path rule.
func splitFileRule(rule string) (fileRule, bool) {
	m := parenRule.FindStringSubmatch(strings.TrimSpace(rule))
	if m == nil {
		return fileRule{}, false
	}
	tool := strings.TrimSpace(m[1])
	kind, alias := ruleFileKind(tool)
	if kind == kindNone {
		return fileRule{}, false
	}
	return fileRule{tool: tool, kind: kind, alias: alias, pattern: m[2]}, true
}

func (f fileRule) empty() bool { return strings.TrimSpace(f.pattern) == "" }

// bareFamilyMatches reports whether a bare (no-argument) rule names this
// tool's family: Edit covers every edit tool and Read every read tool, as
// Claude Code's "Edit rules apply to all built-in tools that edit files".
// Any other bare rule (Write, Bash, ...) matches its own tool only.
func bareFamilyMatches(bare, tool string) bool {
	switch normTool(bare) {
	case "edit":
		return editTools[normTool(tool)]
	case "read":
		return readTools[normTool(tool)]
	}
	return false
}

// pathRule is one compiled Read/Edit rule.
type pathRule struct {
	kind fileKind
	neg  bool
	// rel marks a "p" or "./p" rule: only those take part in "!" carve-outs.
	rel bool
	src RuleSource
	// base is the directory segs is matched under: the anchor joined with
	// the pattern's leading literal segments, clean and as written. The
	// rule also matches under base's real location, which is resolved on
	// each decision (anchors), never cached across them: a directory can
	// become a link between two calls.
	base string
	// cmpBase is base in the form the rule compares in.
	cmpBase string
	segs    []string
	rawSegs []string // segs before loosening (deny and ask rules only)
	// loose compares in NFC and, where foldCase, without case (cmpBase and
	// segs are already in that form).
	loose bool
}

// anchors resolves rule bases to their other spellings for one decision,
// each distinct base once: its real location (symlinks resolved) and the
// path the OS reports for it (execenv.CanonicalPath: on macOS, firmlinks,
// APFS case folding and /.vol all map there).
type anchors struct {
	resolve   func(string) string
	canonical func(string) string
	alts      map[string]baseAlts
}

// baseAlts are a base's other spellings, as is and loosened.
type baseAlts struct{ raw, loose []string }

func newAnchors() *anchors {
	resolve := newRealPathMemo()
	return &anchors{resolve: resolve, canonical: newCanonicalMemo(resolve), alts: map[string]baseAlts{}}
}

// kernelPath is execenv.KernelPath; a variable so a test can count calls.
var kernelPath = execenv.KernelPath

// newCanonicalMemo is execenv.CanonicalPath for clean paths, memoised for
// one decision: the real path, then the kernel's name for it if it exists
// (one F_GETPATH), else its parent's canonical path (memoised) plus the
// leaf. Off macOS it is the real path.
func newCanonicalMemo(resolve func(string) string) func(string) string {
	memo := map[string]string{}
	var canon func(string) string
	canon = func(p string) string {
		if c, ok := memo[p]; ok {
			return c
		}
		real := resolve(p)
		c := real
		if k, ok := kernelPath(real); ok {
			c = k
		} else if parent := filepath.Dir(real); parent != real && runtime.GOOS == "darwin" {
			c = filepath.Join(canon(parent), filepath.Base(real))
		}
		memo[p] = c
		return c
	}
	return canon
}

// otherBases are base's other spellings in the rule's comparison form.
func (a *anchors) otherBases(r pathRule) []string {
	v, ok := a.alts[r.base]
	if !ok {
		seen := map[string]bool{r.base: true}
		for _, p := range []string{a.resolve(r.base), a.canonical(r.base)} {
			if !seen[p] {
				seen[p] = true
				v.raw = append(v.raw, p)
				v.loose = append(v.loose, loosen(p))
			}
		}
		a.alts[r.base] = v
	}
	if r.loose {
		return v.loose
	}
	return v.raw
}

// foldCase is set where the default filesystem ignores case (macOS,
// Windows): there ".ENV" opens .env, and symlink resolution does not
// canonicalize case, so a deny or ask rule must compare without it or it
// silently misses. Allow rules keep exact case, the narrower reading. A
// variable so a test can exercise both.
var foldCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// loosen is the form a deny/ask rule compares in: NFC, and where foldCase,
// Unicode case folding (cases.Fold, not strings.ToLower: APFS equates
// "ſ" with "s", "µ" with "μ", "ς" with "σ", which ToLower does not). The
// kernel's own spelling (execenv.CanonicalPath) is compared too, for
// whatever folding this does not cover.
func loosen(s string) string {
	s = norm.NFC.String(s)
	if foldCase {
		// A Caser holds state; one per call keeps loosen safe for
		// concurrent decisions.
		s = norm.NFC.String(cases.Fold().String(s))
		s = strings.Map(apfsFold, s)
	}
	return s
}

// CaseFoldPath is p as a case- and normalization-insensitive filesystem
// (foldCase: macOS, Windows) compares it, for a boundary check such as
// the gate's workspace roots. Elsewhere it is p unchanged: there "café"
// in NFC and in NFD, or "a" and "A", are different directories.
func CaseFoldPath(p string) string {
	if !foldCase {
		return p
	}
	return loosen(p)
}

// apfsFold maps the letters APFS treats as one but cases.Fold (Unicode
// 15 case folding in x/text) leaves apart, each pair to one of its two.
// Measured on an APFS case-insensitive volume by creating a file under
// one spelling and opening it under the other: Cherokee small letters
// (AB70–ABBF, 13F8–13FD) with their capitals, Garay (10D70–10D85 with
// 10D50–10D65), Cyrillic TJE (1C89/1C8A) and the Latin Extended-D letters
// added in Unicode 16 (A7CB/0264, A7CC/A7CD, A7CE/A7CF, A7D2/A7D3,
// A7D4/A7D5, A7DA/A7DB, A7DC/019B). A file whose name uses them opens
// under either spelling, so a deny rule must compare them as one.
func apfsFold(r rune) rune {
	switch {
	case r >= 0xAB70 && r <= 0xABBF:
		return r - 0xAB70 + 0x13A0
	case r >= 0x13F8 && r <= 0x13FD:
		return r - 0x13F8 + 0x13F0
	case r >= 0x10D70 && r <= 0x10D85:
		return r - 0x10D70 + 0x10D50
	}
	switch r {
	case 0x1C8A:
		return 0x1C89
	case 0xA7CB:
		return 0x0264
	case 0xA7CD, 0xA7CF, 0xA7D3, 0xA7D5, 0xA7DB:
		return r - 1
	case 0xA7DC:
		return 0x019B
	}
	return r
}

// matchCtx is what a rule is anchored against for one decision.
type matchCtx struct {
	cwd  string
	home string
}

func newMatchCtx(cwd string) matchCtx {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	home, _ := os.UserHomeDir()
	return matchCtx{cwd: filepath.Clean(cwd), home: home}
}

// compilePathRule turns a rule's pattern into a pathRule. ok is false when
// the rule can match nothing (an invalid allow pattern, a "!" allow, an
// anchor that cannot be resolved).
func (c matchCtx) compilePathRule(kind fileKind, pattern string, src RuleSource, list ruleList) (pathRule, bool) {
	r := pathRule{kind: kind, src: src}
	p := strings.TrimSpace(pattern)
	if p == "" {
		return r, false
	}

	var anchor string
	anchored := false // a gitignore leading slash: no depth floating
	explicitDot := false
	switch {
	case strings.HasPrefix(p, "!"):
		if list == listAllow {
			return r, false // negation exists only in deny and ask lists
		}
		// Claude Code reads a "!" pattern relative to the current
		// directory whatever follows it; a leading slash anchors it there.
		r.neg, r.rel = true, true
		p = p[1:]
		anchor = c.cwd
		if strings.HasPrefix(p, "/") {
			anchored = true
			p = strings.TrimLeft(p, "/")
		}
	case strings.HasPrefix(p, "//"):
		anchor, anchored, p = "/", true, p[2:]
	case p == "~" || strings.HasPrefix(p, "~/"):
		if c.home == "" {
			return r, false
		}
		anchor, anchored, p = c.home, true, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")
	case strings.HasPrefix(p, "/"):
		anchor = src.Root
		if anchor == "" {
			anchor = c.cwd
		}
		anchored, p = true, p[1:]
	case strings.HasPrefix(p, "./"):
		anchor, r.rel, explicitDot, p = c.cwd, true, true, p[2:]
	default:
		anchor, r.rel = c.cwd, true
	}

	p = strings.TrimSuffix(p, "/")
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			segs = append(segs, s)
		}
	}

	valid := true
	for _, s := range segs {
		if s == ".." {
			valid = false // gitignore has no "..": not a usable pattern
			break
		}
		if s == "**" {
			continue
		}
		if _, err := path.Match(s, ""); err != nil {
			valid = false
			break
		}
	}
	if !valid {
		if list == listAllow {
			return r, false
		}
		// Still guards the exact path it names (and, being a path, what
		// is under it).
		r.base = filepath.Join(anchor, p)
		r.segs = nil
		return r.loosened(list), true
	}

	if r.rel && !anchored {
		switch {
		case len(segs) == 1 && !(explicitDot && list == listAllow):
			// A bare name matches at any depth. "./name" in an allow
			// rule stays at <cwd>/name: the narrower reading, and Claude
			// Code's own example ("Read(./.env)": the .env file in the
			// current directory).
			segs = append([]string{"**"}, segs...)
		case len(segs) == 2 && segs[1] == "**" && segs[0] != "**" && list != listAllow:
			// "dir/**" in a deny or ask rule: that directory at any depth.
			segs = append([]string{"**"}, segs...)
		}
	}

	// Fold leading literal segments into the base, so the base can be
	// resolved to its real location ("//etc/**" -> /etc and /private/etc).
	base := anchor
	for len(segs) > 0 && !hasGlobMeta(segs[0]) {
		base = filepath.Join(base, segs[0])
		segs = segs[1:]
	}
	r.base = filepath.Clean(base)
	r.segs = segs
	return r.loosened(list), true
}

// loosened puts a deny or ask rule in the form it compares in.
func (r pathRule) loosened(list ruleList) pathRule {
	r.base = filepath.Clean(r.base)
	r.cmpBase = r.base
	if list == listAllow {
		return r
	}
	r.loose = true
	r.rawSegs = r.segs // as written, for the sandbox (sandbox_rules.go)
	r.cmpBase = loosen(r.base)
	segs := make([]string, len(r.segs))
	for i, s := range r.segs {
		segs[i] = loosen(s)
	}
	r.segs = segs
	return r
}

func hasGlobMeta(s string) bool { return strings.ContainsAny(s, `*?[\`) }

// realPath is execenv.RealPath without the ok flag: an unresolvable path
// (a symlink loop) is judged as written, and the tool's own open then
// fails on it.
func realPath(p string) string {
	r, _ := execenv.RealPath(p)
	return r
}

// pathCand is one clean absolute path in both comparison forms: as is
// (allow rules) and loosened (deny and ask rules).
type pathCand struct{ raw, loose string }

// candPrefixes is prefixes(abs), each in both forms, loosened once here
// rather than once per rule.
func candPrefixes(abs string) []pathCand {
	ps := prefixes(abs)
	out := make([]pathCand, len(ps))
	for i, p := range ps {
		out[i] = pathCand{raw: p, loose: loosen(p)}
	}
	return out
}

// matchesExactly reports whether the rule's pattern matches the path
// itself. Paths and bases are clean and absolute, so "under a base" is a
// string prefix ending at a separator.
func (r pathRule) matchesExactly(c pathCand, a *anchors) bool {
	abs := c.raw
	if r.loose {
		abs = c.loose
	}
	if r.underBase(abs, r.cmpBase) {
		return true
	}
	for _, b := range a.otherBases(r) {
		if r.underBase(abs, b) {
			return true
		}
	}
	return false
}

// underBase matches the rule's segments against abs relative to base b.
func (r pathRule) underBase(abs, b string) bool {
	var rest string
	switch {
	case abs == b:
	case b == "/":
		rest = abs[1:]
	case len(abs) > len(b) && abs[len(b)] == '/' && strings.HasPrefix(abs, b):
		rest = abs[len(b)+1:]
	default:
		return false
	}
	var name []string
	if rest != "" {
		name = strings.Split(rest, "/")
	}
	return matchSegs(r.segs, name)
}

// matchSegs matches gitignore pattern segments against path segments.
func matchSegs(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegs(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], name[0]); !ok {
		return false
	}
	return matchSegs(pat[1:], name[1:])
}

// prefixes is abs and every ancestor of it, shortest first:
// "/a/b/c" -> "/", "/a", "/a/b", "/a/b/c".
func prefixes(abs string) []string {
	abs = filepath.Clean(abs)
	var out []string
	for p := abs; ; p = filepath.Dir(p) {
		out = append(out, p)
		if filepath.Dir(p) == p {
			break
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// groupKey groups the relative rules a "!" can carve out of: same source,
// same family.
type groupKey struct {
	src  RuleSource
	kind fileKind
}

// ruleSet is the rules of one list that judge one class of call, grouped
// for gitignore evaluation: relative rules from one source (and family) in
// order, so a later "!" rule carves out of earlier ones; every other rule
// on its own.
type ruleSet struct {
	single []pathRule
	groups [][]pathRule
}

func newRuleSet(rules []pathRule) ruleSet {
	var s ruleSet
	index := map[groupKey]int{}
	for _, r := range rules {
		if !r.rel {
			s.single = append(s.single, r)
			continue
		}
		k := groupKey{r.src, r.kind}
		i, seen := index[k]
		if !seen {
			i = len(s.groups)
			index[k] = i
			s.groups = append(s.groups, nil)
		}
		s.groups[i] = append(s.groups[i], r)
	}
	return s
}

func (s ruleSet) empty() bool { return len(s.single) == 0 && len(s.groups) == 0 }

// blocks applies deny/ask rules to abs with gitignore semantics: a path is
// blocked when it, or a directory above it, ends up matched (a carve-out
// cannot reopen a file inside a blocked directory).
func (s ruleSet) blocks(abs string, a *anchors) bool {
	if s.empty() {
		return false
	}
	ps := candPrefixes(abs)
	for _, r := range s.single {
		for _, p := range ps {
			if r.matchesExactly(p, a) {
				return true
			}
		}
	}
	for _, p := range ps {
		for _, g := range s.groups {
			state := false
			for _, r := range g {
				if r.matchesExactly(p, a) {
					state = !r.neg
				}
			}
			if state {
				return true
			}
		}
	}
	return false
}

// listBlocks is ruleSet.blocks for an ad hoc rule list.
func listBlocks(rules []pathRule, abs string) bool {
	return newRuleSet(rules).blocks(abs, newAnchors())
}

// callClass is the class of file tool a rule set is prepared for.
type callClass int

const (
	classRead     callClass = iota // read, grep, glob, ls
	classEdit                      // edit, write, multi_edit: Read denies apply too
	classNotebook                  // notebook_edit: Edit rules only
	numClasses
)

func classOf(tool string) (callClass, bool) {
	t := normTool(tool)
	switch {
	case readTools[t]:
		return classRead, true
	case readDenyCoversEdit[t]:
		return classEdit, true
	case editTools[t]:
		return classNotebook, true
	}
	return 0, false
}

// appliesTo reports whether a rule from list judges calls of class c. A
// Read deny also blocks edit and write, not notebook edit (Claude Code).
func (r pathRule) appliesTo(list ruleList, c callClass) bool {
	switch c {
	case classRead:
		return r.kind == kindRead
	case classEdit:
		return r.kind == kindEdit || (list == listDeny && r.kind == kindRead)
	default:
		return r.kind == kindEdit
	}
}

// compiledRules is a Permissions' path rules, compiled once for a working
// directory and home (compiledFor caches them), ready per call class.
type compiledRules struct {
	allow, deny, ask [numClasses]ruleSet
	// emptyDeny/emptyAsk are "Read()"-style rules with an empty path,
	// taken as the bare tool rule (by the tool name written).
	emptyDeny, emptyAsk []string
	// guarded is set when any Read/Edit path rule sits in deny or ask:
	// then a bash file operand kiln cannot resolve asks rather than runs.
	guarded bool
}

// sourceAt is the source of rule i, defaulting to a CLI/session rule
// (anchored at the primary working directory) when from does not cover it.
func sourceAt(from []RuleSource, i int) RuleSource {
	if i < len(from) {
		return from[i]
	}
	return RuleSource{}
}

func (c matchCtx) compile(p Permissions) *compiledRules {
	out := &compiledRules{}
	one := func(rules []string, from []RuleSource, list ruleList) (compiled []pathRule, empty []string) {
		for i, rule := range rules {
			f, ok := splitFileRule(rule)
			if !ok {
				continue
			}
			if list != listAllow {
				out.guarded = true
			}
			if f.alias && list == listAllow {
				continue
			}
			if f.empty() {
				if list != listAllow {
					empty = append(empty, f.tool)
				}
				continue
			}
			if r, ok := c.compilePathRule(f.kind, f.pattern, sourceAt(from, i), list); ok {
				compiled = append(compiled, r)
			}
		}
		return compiled, empty
	}
	allow, _ := one(p.Allow, p.AllowFrom, listAllow)
	deny, emptyDeny := one(p.Deny, p.DenyFrom, listDeny)
	ask, emptyAsk := one(p.Ask, p.AskFrom, listAsk)
	out.emptyDeny, out.emptyAsk = emptyDeny, emptyAsk
	for c := callClass(0); c < numClasses; c++ {
		pick := func(rules []pathRule, list ruleList) ruleSet {
			var picked []pathRule
			for _, r := range rules {
				if r.appliesTo(list, c) {
					picked = append(picked, r)
				}
			}
			return newRuleSet(picked)
		}
		out.allow[c] = pick(allow, listAllow)
		out.deny[c] = pick(deny, listDeny)
		out.ask[c] = pick(ask, listAsk)
	}
	return out
}

// ruleCache holds compiled rules per (cwd, home, case mode, rules,
// sources): parsed patterns and their grouping, nothing read from the
// filesystem (each decision resolves anchors afresh, see anchors). The
// key is cheap to build. The cache is small and simply cleared when full:
// a session has one rule set at a time, changing only when /permissions
// edits it.
var ruleCache = struct {
	sync.Mutex
	m map[string]*compiledRules
}{m: map[string]*compiledRules{}}

const ruleCacheMax = 32

func compiledFor(p Permissions, c matchCtx) *compiledRules {
	var b strings.Builder
	b.WriteString(c.cwd)
	b.WriteByte(0)
	b.WriteString(c.home)
	if foldCase {
		b.WriteString("\x00fold")
	}
	for _, l := range []struct {
		rules []string
		from  []RuleSource
	}{{p.Allow, p.AllowFrom}, {p.Deny, p.DenyFrom}, {p.Ask, p.AskFrom}} {
		b.WriteByte(1)
		for i, r := range l.rules {
			s := sourceAt(l.from, i)
			b.WriteString(r)
			b.WriteByte(2)
			b.WriteString(string(s.Scope))
			b.WriteByte(2)
			b.WriteString(s.File)
			b.WriteByte(2)
			b.WriteString(s.Root)
			b.WriteByte(3)
		}
	}
	key := b.String()

	ruleCache.Lock()
	defer ruleCache.Unlock()
	if cr, ok := ruleCache.m[key]; ok {
		return cr
	}
	cr := c.compile(p)
	if len(ruleCache.m) >= ruleCacheMax {
		ruleCache.m = map[string]*compiledRules{}
	}
	ruleCache.m[key] = cr
	return cr
}

// filePathVerdicts judges a file tool's path against the Read/Edit path
// rules. arg is the tool's path argument as sent; it is resolved the way
// the tools resolve it (execenv.ResolveToolPath) and then through
// symlinks. For the read tool every filename variant it may fall back to
// (execenv.ReadPathVariants) is judged too.
func filePathVerdicts(p Permissions, c matchCtx, tool, arg string) (deny, ask, allow bool) {
	cr := compiledFor(p, c)
	requested := execenv.ResolveToolPath(c.cwd, arg)
	opened := []string{requested}
	if normTool(tool) == "read" {
		opened = execenv.ReadPathVariants(requested)
	}
	// Every spelling of each path: as asked, symlinks resolved, and the
	// OS's own name for it (firmlinks, APFS case folding, /.vol). Deny and
	// ask apply when any matches; allow needs them all.
	a := newAnchors()
	var candidates []string
	for _, o := range opened {
		real := realPath(o)
		candidates = appendNew(candidates, o, real, a.canonical(real))
	}

	class, _ := classOf(tool)
	blocks := func(s ruleSet) bool {
		for _, cand := range candidates {
			if s.blocks(cand, a) {
				return true
			}
		}
		return false
	}
	deny = blocks(cr.deny[class]) || emptyRuleHits(cr.emptyDeny, tool)
	ask = blocks(cr.ask[class]) || emptyRuleHits(cr.emptyAsk, tool)

	// Allow rules have no carve-outs, so "blocks" is "some rule covers".
	allowSet := cr.allow[class]
	allow = !allowSet.empty()
	for _, cand := range candidates {
		allow = allow && allowSet.blocks(cand, a)
	}
	return deny, ask, allow
}

// appendNew appends each of xs not already in list.
func appendNew(list []string, xs ...string) []string {
	for _, x := range xs {
		if !contains(list, x) {
			list = append(list, x)
		}
	}
	return list
}

// emptyRuleHits judges "Read()"-style deny/ask rules as the bare rule.
func emptyRuleHits(tools []string, tool string) bool {
	for _, t := range tools {
		if MatchesRule(t, tool, "") {
			return true
		}
	}
	return false
}

// FileRuleWarnings lists, for a startup warning, every path rule Claude
// Code accepts but never consults: one written for Write, MultiEdit,
// NotebookEdit or Glob (kiln honours the deny and ask ones as Edit/Read
// rules and ignores the allow ones), and one with an empty path. Each
// message says what kiln does and names the rule to write instead.
// cliAllow are the --allowed-tools rules: like Claude Code, a Glob rule
// passed there is not warned about.
func FileRuleWarnings(p Permissions, cliAllow ...string) []string {
	exempt := map[string]bool{}
	for _, r := range cliAllow {
		if f, ok := splitFileRule(r); ok && normTool(f.tool) == "glob" {
			exempt[r] = true
		}
	}
	var out []string
	for _, l := range []struct {
		name  string
		rules []string
	}{{"allow", p.Allow}, {"deny", p.Deny}, {"ask", p.Ask}} {
		for _, rule := range l.rules {
			f, ok := splitFileRule(rule)
			if !ok {
				continue
			}
			if f.empty() {
				effect := "kiln applies it as " + f.tool + ", the whole tool"
				if l.name == "allow" {
					effect = "kiln ignores it"
				}
				out = append(out, "Permission rule "+rule+" in "+l.name+" has an empty path ("+effect+"); write "+f.tool+" for the whole tool, or give it a path.")
				continue
			}
			if !f.alias || (l.name == "allow" && exempt[rule]) {
				continue
			}
			want := "Edit"
			if f.kind == kindRead {
				want = "Read"
			}
			replacement := want + "(" + f.pattern + ")"
			effect := "kiln ignores it"
			if l.name != "allow" {
				effect = "kiln applies it as " + replacement
			}
			out = append(out, "Permission rule "+rule+" in "+l.name+" is not matched by file permission checks ("+effect+"); use "+replacement+" instead.")
		}
	}
	return out
}
