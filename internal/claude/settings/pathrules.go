package settings

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

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
//     matches a directory also covers everything under it.
//   - "!p" in a deny or ask list carves p out of the relative rules listed
//     before it from the same source; it cannot reopen a file inside a
//     directory those rules block as a whole.
//   - Symlinks: a deny or ask rule applies when the requested path OR the
//     path it resolves to matches; an allow rule needs BOTH. Each rule is
//     also tried at its anchor's real location, so "//tmp/**" covers
//     /private/tmp on macOS.
//   - A deny or ask pattern that is not a valid glob still guards that
//     exact path; an invalid allow pattern approves nothing.
//
// Rule tool names: Read and Edit are path rules. Claude Code accepts
// Write(...), MultiEdit(...), NotebookEdit(...) and Glob(...) path rules
// but never consults them, and warns at startup. kiln deliberately differs
// in one direction only: a DENY or ASK rule written that way is honoured as
// the Edit (resp. Read) rule it was meant to be, so a misspelt deny never
// leaves a file unguarded; an ALLOW rule written that way is ignored, as
// in Claude Code, so it never approves more than Claude Code would.
// FileRuleWarnings reports every such rule at startup.

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
func norm(name string) string {
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
	switch t := norm(tool); {
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
	switch norm(ruleTool) {
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

// splitFileRule reports whether rule is a parenthesized path rule, and
// returns its family, alias flag and pattern.
func splitFileRule(rule string) (kind fileKind, alias bool, pattern string, ok bool) {
	m := parenRule.FindStringSubmatch(strings.TrimSpace(rule))
	if m == nil {
		return kindNone, false, "", false
	}
	kind, alias = ruleFileKind(strings.TrimSpace(m[1]))
	if kind == kindNone {
		return kindNone, false, "", false
	}
	return kind, alias, m[2], true
}

// bareFamilyMatches reports whether a bare (no-argument) rule names this
// tool's family: Edit covers every edit tool and Read every read tool, as
// Claude Code's "Edit rules apply to all built-in tools that edit files".
// Any other bare rule (Write, Bash, ...) matches its own tool only.
func bareFamilyMatches(bare, tool string) bool {
	switch norm(bare) {
	case "edit":
		return editTools[norm(tool)]
	case "read":
		return readTools[norm(tool)]
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
	// bases are the directories segs is matched under: the anchor joined
	// with the pattern's leading literal segments, then the same directory
	// with symlinks resolved when that differs.
	bases []string
	segs  []string
	// fold compares case-insensitively (bases and segs already lowered).
	fold bool
}

// foldCase is set where the default filesystem ignores case (macOS,
// Windows): there ".ENV" opens .env, and filepath.EvalSymlinks does not
// canonicalize case, so a deny or ask rule must compare without it or it
// silently misses. Allow rules keep exact case, the narrower reading. A
// variable so a test can exercise both.
var foldCase = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

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
		r.bases = withReal(filepath.Join(anchor, p))
		r.segs = nil
		return r.folded(list), true
	}

	if r.rel && !anchored {
		switch {
		case len(segs) == 1 && !(explicitDot && list == listAllow):
			// A bare name matches at any depth. "./name" in an allow
			// rule stays at <cwd>/name: the narrower reading, since
			// Claude Code's docs give "./" no separate depth rule.
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
	r.bases = withReal(base)
	r.segs = segs
	return r.folded(list), true
}

// folded lower-cases a deny or ask rule where foldCase is set.
func (r pathRule) folded(list ruleList) pathRule {
	if !foldCase || list == listAllow {
		return r
	}
	r.fold = true
	bases := make([]string, len(r.bases))
	for i, b := range r.bases {
		bases[i] = strings.ToLower(b)
	}
	segs := make([]string, len(r.segs))
	for i, s := range r.segs {
		segs[i] = strings.ToLower(s)
	}
	r.bases, r.segs = bases, segs
	return r
}

func hasGlobMeta(s string) bool { return strings.ContainsAny(s, `*?[\`) }

// withReal is dir, plus dir with symlinks resolved when that differs.
func withReal(dir string) []string {
	dir = filepath.Clean(dir)
	if real := realPath(dir); real != dir {
		return []string{dir, real}
	}
	return []string{dir}
}

// realPath resolves every symlink in p. A path that does not exist yet (a
// file about to be written) resolves through its deepest existing
// ancestor. A path that cannot be resolved at all is returned as is.
func realPath(p string) string {
	p = filepath.Clean(p)
	var rest []string
	cur := p
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// matchesExactly reports whether the rule's pattern matches abs itself.
func (r pathRule) matchesExactly(abs string) bool {
	if r.fold {
		abs = strings.ToLower(abs)
	}
	for _, b := range r.bases {
		rel, err := filepath.Rel(b, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		var name []string
		if rel != "." {
			name = strings.Split(filepath.ToSlash(rel), "/")
		}
		if matchSegs(r.segs, name) {
			return true
		}
	}
	return false
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

// covers reports whether one rule (no carve-outs) matches abs or a
// directory above it.
func (r pathRule) covers(abs string) bool {
	for _, p := range prefixes(abs) {
		if r.matchesExactly(p) {
			return true
		}
	}
	return false
}

// groupKey groups the relative rules a "!" can carve out of: same source,
// same family.
type groupKey struct {
	src  RuleSource
	kind fileKind
}

// listBlocks applies deny/ask rules to abs with gitignore semantics:
// relative rules from one source are evaluated in order, so a later "!"
// rule carves out of earlier ones, and a directory that ends up blocked
// blocks everything under it (a carve-out cannot reopen it). Every other
// rule stands alone.
func listBlocks(rules []pathRule, abs string) bool {
	var order []groupKey
	groups := map[groupKey][]pathRule{}
	var single []pathRule
	for _, r := range rules {
		if !r.rel {
			single = append(single, r)
			continue
		}
		k := groupKey{r.src, r.kind}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	for _, r := range single {
		if r.covers(abs) {
			return true
		}
	}
	for _, p := range prefixes(abs) {
		for _, k := range order {
			state := false
			for _, r := range groups[k] {
				if r.matchesExactly(p) {
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

// compileList compiles the path rules in one list that apply to a call of
// the given family. For a deny list and an edit-family call, Read rules
// apply too (Claude Code: a Read deny also blocks Edit and Write, but not
// NotebookEdit). Alias rules (Write(...), Glob(...)) in an allow list are
// ignored.
func (c matchCtx) compileList(rules []string, from []RuleSource, list ruleList, tool string) []pathRule {
	callKind := toolFileKind(tool)
	var out []pathRule
	for i, rule := range rules {
		kind, alias, pattern, ok := splitFileRule(rule)
		if !ok {
			continue
		}
		if alias && list == listAllow {
			continue
		}
		applies := kind == callKind ||
			(list == listDeny && kind == kindRead && readDenyCoversEdit[norm(tool)])
		if !applies {
			continue
		}
		if r, ok := c.compilePathRule(kind, pattern, sourceAt(from, i), list); ok {
			out = append(out, r)
		}
	}
	return out
}

// sourceAt is the source of rule i, defaulting to a CLI/session rule (anchored
// at the primary working directory) when from does not cover it.
func sourceAt(from []RuleSource, i int) RuleSource {
	if i < len(from) {
		return from[i]
	}
	return RuleSource{}
}

// filePathVerdicts judges a file tool's path against the Read/Edit path
// rules. arg is the tool's path argument as sent; it is resolved the way
// the tools resolve it (execenv.ResolveToolPath) and then through
// symlinks.
func filePathVerdicts(p Permissions, c matchCtx, tool, arg string) (deny, ask, allow bool) {
	requested := execenv.ResolveToolPath(c.cwd, arg)
	resolved := realPath(requested)
	candidates := []string{requested}
	if resolved != requested {
		candidates = append(candidates, resolved)
	}

	blocks := func(rules []pathRule) bool {
		for _, cand := range candidates {
			if listBlocks(rules, cand) {
				return true
			}
		}
		return false
	}
	deny = blocks(c.compileList(p.Deny, p.DenyFrom, listDeny, tool))
	ask = blocks(c.compileList(p.Ask, p.AskFrom, listAsk, tool))

	allowRules := c.compileList(p.Allow, p.AllowFrom, listAllow, tool)
	allowed := func(cand string) bool {
		for _, r := range allowRules {
			if r.covers(cand) {
				return true
			}
		}
		return false
	}
	allow = len(allowRules) > 0
	for _, cand := range candidates {
		allow = allow && allowed(cand)
	}
	return deny, ask, allow
}

// FileRuleWarnings lists, for a startup warning, every path rule written
// for Write, MultiEdit, NotebookEdit or Glob, which Claude Code accepts but
// never consults. kiln honours the deny and ask ones as Edit/Read rules
// and ignores the allow ones; each message says which, and names the rule
// to write instead.
func FileRuleWarnings(p Permissions) []string {
	var out []string
	for _, l := range []struct {
		name  string
		rules []string
	}{{"allow", p.Allow}, {"deny", p.Deny}, {"ask", p.Ask}} {
		for _, rule := range l.rules {
			kind, alias, pattern, ok := splitFileRule(rule)
			if !ok || !alias {
				continue
			}
			want := "Edit"
			if kind == kindRead {
				want = "Read"
			}
			replacement := want + "(" + pattern + ")"
			effect := "kiln ignores it"
			if l.name != "allow" {
				effect = "kiln applies it as " + replacement
			}
			out = append(out, "Permission rule "+rule+" in "+l.name+" is not matched by file permission checks ("+effect+"); use "+replacement+" instead.")
		}
	}
	return out
}
