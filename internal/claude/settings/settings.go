package settings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/execenv"
)

// PermissionMode is one of the six modes Claude Code's permission system
// recognizes.
type PermissionMode string

const (
	ModeManual            PermissionMode = "manual"
	ModeAcceptEdits       PermissionMode = "acceptEdits"
	ModeAuto              PermissionMode = "auto"
	ModeDontAsk           PermissionMode = "dontAsk"
	ModeBypassPermissions PermissionMode = "bypassPermissions"
	ModePlan              PermissionMode = "plan"
)

// Permissions is the allow/deny/ask rule lists plus a default mode.
//
// AllowFrom, DenyFrom and AskFrom run parallel to Allow, Deny and Ask:
// entry i is where rule i came from, which decides where a Read/Edit "/path"
// rule is anchored (pathrules.go). A From slice shorter than its list (or
// nil) means the remaining rules came from CLI flags or this session, which
// Claude Code anchors at the primary working directory; so code that only
// appends rule strings keeps working, and code that removes one must remove
// the matching From entry (permission.Gate.RemoveRule does).
type Permissions struct {
	Allow       []string
	Deny        []string
	Ask         []string
	AllowFrom   []RuleSource
	DenyFrom    []RuleSource
	AskFrom     []RuleSource
	DefaultMode PermissionMode // "" means unset
}

// RuleSource is the settings file a permission rule was read from.
type RuleSource struct {
	// Scope is the settings scope; "" for CLI flags and session rules.
	Scope paths.Scope
	// File is the settings file's path; "" for CLI flags and session rules.
	// Two files are two sources, even in the same scope ("!" carve-outs
	// reach only rules from their own source).
	File string
	// Root is the directory a "/path" Read/Edit rule is anchored at: the
	// user's ~/.claude for user settings, the file's own directory for
	// --settings, and "" (the primary working directory) for project and
	// local settings, CLI flags and session rules.
	Root string
}

// Settings is the merged result of loading settings.json across scopes.
type Settings struct {
	Permissions Permissions
	Model       string
	EffortLevel string
	// ModelRoles maps a role name (e.g. "fast", "structured", "heavy") to
	// a "provider/model" string. Roles let a subagent or alias request a
	// class of model ("give me something fast") without naming a literal
	// provider/model, and without every definition on disk having to agree
	// on what "fast" means across machines - see
	// internal/claude/agents.ResolveModel, which consumes this map.
	ModelRoles map[string]string
	Env        map[string]string
	// StatusLine is the configured status line command, nil when unset.
	StatusLine *StatusLineConfig
	// LoadedFrom records which scopes actually contributed, for diagnostics.
	LoadedFrom []paths.Scope
}

// StatusLineConfig is Claude Code's settings.json "statusLine" object: a
// command whose stdout is rendered below the input box. Type is "command".
type StatusLineConfig struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Padding *int   `json:"padding,omitempty"`
}

type rawPermissions struct {
	Allow       []string       `json:"allow"`
	Deny        []string       `json:"deny"`
	Ask         []string       `json:"ask"`
	DefaultMode PermissionMode `json:"defaultMode"`
}

type rawSettings struct {
	Permissions *rawPermissions   `json:"permissions"`
	Model       string            `json:"model"`
	EffortLevel string            `json:"effortLevel"`
	ModelRoles  map[string]string `json:"modelRoles"`
	Env         map[string]string `json:"env"`
	StatusLine  *StatusLineConfig `json:"statusLine"`
}

// LoadOptions configures LoadSettings.
type LoadOptions struct {
	// Sources restricts which scopes are read, mirroring --setting-sources.
	// Nil means all three.
	Sources []paths.Scope
	// Extra is an additional file read last, from --settings. Highest
	// precedence: it is treated as an extra "local" scope entry.
	Extra string
}

func wants(sources []paths.Scope, scope paths.Scope) bool {
	if sources == nil {
		return true
	}
	for _, s := range sources {
		if s == scope {
			return true
		}
	}
	return false
}

// LoadSettings reads and merges .claude/settings.json across scopes.
//
// Permission lists concatenate across scopes; defaultMode/model/effortLevel
// use last-non-empty-wins; env and modelRoles are shallow-merged with later
// scopes overriding per key (so a project can override just the "fast"
// role and still inherit "heavy" from the user's settings). Malformed JSON
// warns (to stderr) and continues; a missing file is silently skipped.
func LoadSettings(cwd string, opts LoadOptions) Settings {
	merged := Settings{
		Permissions: Permissions{Allow: []string{}, Deny: []string{}, Ask: []string{}},
	}

	type source struct {
		paths.SettingsFile
		root string // RuleSource.Root for this file's rules
	}
	files := []source{}
	for _, f := range paths.SettingsFiles(cwd) {
		if wants(opts.Sources, f.Scope) {
			root := ""
			if f.Scope == paths.ScopeUser {
				// "/path" in user settings is under ~/.claude, the
				// directory that holds the file (Claude Code's table).
				root = filepath.Dir(f.Path)
			}
			files = append(files, source{f, root})
		}
	}
	if opts.Extra != "" {
		// Applied last so it overrides the hierarchy, mirroring the TS
		// behaviour of pushing it onto the "local" scope. Its "/path"
		// rules anchor at the file's own directory.
		root := filepath.Dir(opts.Extra)
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		files = append(files, source{paths.SettingsFile{Scope: paths.ScopeLocal, Path: opts.Extra}, root})
	}

	for _, f := range files {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			// Absent is normal. Malformed is reported below (ReadFile only
			// errors on things like permission or absence, not parse).
			continue
		}
		var raw rawSettings
		if err := json.Unmarshal(data, &raw); err != nil {
			fmt.Fprintf(os.Stderr, "Ignoring unreadable settings at %s: %v\n", f.Path, err)
			continue
		}

		merged.LoadedFrom = append(merged.LoadedFrom, f.Scope)
		if raw.Permissions != nil {
			src := RuleSource{Scope: f.Scope, File: f.Path, Root: f.root}
			add := func(list *[]string, from *[]RuleSource, rules []string) {
				*list = append(*list, rules...)
				for range rules {
					*from = append(*from, src)
				}
			}
			add(&merged.Permissions.Allow, &merged.Permissions.AllowFrom, raw.Permissions.Allow)
			add(&merged.Permissions.Deny, &merged.Permissions.DenyFrom, raw.Permissions.Deny)
			add(&merged.Permissions.Ask, &merged.Permissions.AskFrom, raw.Permissions.Ask)
			if raw.Permissions.DefaultMode != "" {
				merged.Permissions.DefaultMode = raw.Permissions.DefaultMode
			}
		}
		if raw.Model != "" {
			merged.Model = raw.Model
		}
		if raw.EffortLevel != "" {
			merged.EffortLevel = raw.EffortLevel
		}
		if raw.ModelRoles != nil {
			if merged.ModelRoles == nil {
				merged.ModelRoles = map[string]string{}
			}
			for k, v := range raw.ModelRoles {
				if v != "" {
					merged.ModelRoles[k] = v
				}
			}
		}
		if raw.StatusLine != nil && raw.StatusLine.Command != "" {
			merged.StatusLine = raw.StatusLine
		}
		if raw.Env != nil {
			if merged.Env == nil {
				merged.Env = map[string]string{}
			}
			for k, v := range raw.Env {
				merged.Env[k] = v
			}
		}
	}

	return merged
}

var parenRule = regexp.MustCompile(`^([^(]+)\(([\s\S]*)\)$`)

// escapeRegexAndStar escapes every regex metacharacter except `*`, which is
// then turned into `.*`.
var regexMeta = regexp.MustCompile(`[.*+?^${}()|[\]\\]`)

// MatchesRule matches a tool invocation against one permission rule string.
//
// Shapes:
//
//	Read             whole tool, by name
//	mcp__homelab     PREFIX - every tool from that MCP server
//	Bash(find:*)     colon form: commands beginning with `find`
//	Bash(git *)      glob form, as documented by `claude --help`
//	Read(src/**)     a Read/Edit path rule, gitignore-style (pathrules.go)
//
// A bare Edit rule covers every edit tool (edit, write, ...) and a bare Read
// rule every read tool, as in Claude Code. For the non-path shapes only `*`
// is a wildcard; every other regex metacharacter is escaped.
// Matching is case-insensitive because Claude Code writes Read/Bash/Edit
// while pi's tools are read/bash/edit.
func MatchesRule(rule, toolName, primaryArg string) bool {
	tool := strings.ToLower(toolName)

	m := parenRule.FindStringSubmatch(rule)
	if m == nil {
		bare := strings.ToLower(rule)
		if strings.HasPrefix(bare, "mcp__") {
			return strings.HasPrefix(tool, bare)
		}
		return sameTool(bare, tool) || bareFamilyMatches(bare, tool)
	}
	if f, ok := splitFileRule(rule); ok {
		// A Read/Edit path rule (pathrules.go), judged here as a deny
		// rule from the command line: anchored at the current directory,
		// matching the requested path or its symlink target. Decide does
		// not come through here; it knows each rule's list and source.
		if f.empty() {
			return MatchesRule(f.tool, toolName, "")
		}
		if toolFileKind(toolName) != f.kind {
			return false
		}
		c := newMatchCtx("")
		r, ok := c.compilePathRule(f.kind, f.pattern, RuleSource{}, listDeny)
		if !ok {
			return false
		}
		requested := execenv.ResolveToolPath(c.cwd, primaryArg)
		return listBlocks([]pathRule{r}, requested) || listBlocks([]pathRule{r}, realPath(requested))
	}
	if !sameTool(strings.ToLower(m[1]), tool) {
		return false
	}

	// Claude Code's WebFetch rules name a host: `WebFetch(domain:x.com)`.
	// kiln's web_fetch argument is the whole URL, so compare its host.
	if d, ok := strings.CutPrefix(strings.TrimSpace(m[2]), "domain:"); ok && sameTool(tool, "webfetch") {
		return strings.EqualFold(urlHost(primaryArg), d)
	}

	// `find:*` means "the find command, any arguments". Normalize the
	// colon form to the glob form before compiling.
	glob := m[2]
	if strings.HasSuffix(glob, ":*") {
		glob = glob[:len(glob)-2] + " *"
	}
	glob = strings.TrimSpace(glob)

	escaped := regexMeta.ReplaceAllStringFunc(glob, func(s string) string {
		if s == "*" {
			return "\x00STAR\x00"
		}
		return "\\" + s
	})
	pattern := strings.ReplaceAll(escaped, "\x00STAR\x00", ".*")
	// A trailing " .*" should also match the bare command with no
	// arguments, so `Bash(ls:*)` permits plain `ls`.
	if strings.HasSuffix(pattern, " .*") {
		pattern = pattern[:len(pattern)-len(" .*")] + "(\\s.*)?"
	}

	re := compiledPattern("^" + pattern + "$")
	if re == nil {
		return false
	}
	return re.MatchString(strings.TrimSpace(primaryArg))
}

// patternCache holds MatchesRule's compiled patterns, keyed by the pattern
// (so bounded by the rules there are, not the calls made); nil records
// one that does not compile. A bash call is judged against every rule for
// every segment, and compiling each time cost more than the rest of the
// decision.
var patternCache sync.Map // string -> *regexp.Regexp

func compiledPattern(p string) *regexp.Regexp {
	if v, ok := patternCache.Load(p); ok {
		return v.(*regexp.Regexp)
	}
	re, err := regexp.Compile(p)
	if err != nil {
		re = nil
	}
	patternCache.Store(p, re)
	return re
}

// sameTool compares a rule's tool name with a tool's, both lower-cased,
// ignoring underscores: Claude Code spells tools WebFetch and TodoWrite
// where kiln's are web_fetch and todo_write.
func sameTool(a, b string) bool {
	return strings.ReplaceAll(a, "_", "") == strings.ReplaceAll(b, "_", "")
}

// urlHost is the host of a URL argument, or "" when it has none.
func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ReadOnly is the set of tools that cannot change anything.
//
// "bash" is deliberately absent: bash(ls) is read-only and bash(rm -rf) is
// not, and the difference is in an argument this cannot inspect safely.
// Erring toward asking is the only correct default there.
var ReadOnly = map[string]bool{
	"read":           true,
	"glob":           true,
	"grep":           true,
	"session_search": true,
	"tool_search":    true,
	"bash_output":    true,
	// exit_plan_mode changes nothing on disk and is the only way out of plan
	// mode; the TS READ_ONLY set omitted it, which made plan mode a dead end.
	// todo_write only edits the in-memory todo list.
	"exit_plan_mode": true,
	"todo_write":     true,
	// skill only reads a SKILL.md's own content; it changes nothing on
	// disk, so plan mode (and manual mode) allow it the same way Read
	// does.
	"skill": true,
	// ask_user_question only asks the user a multiple-choice question; it
	// changes nothing on disk and, like exit_plan_mode, must stay
	// available from inside plan mode or the model has no way to resolve
	// an ambiguity without leaving it.
	"ask_user_question": true,
}

// Hits is which rule lists match a call, before any mode or precedence is
// applied (DecideFromHits orders them).
type Hits struct {
	Deny, Ask, Allow bool
	// Unsure is set for a bash command naming a file kiln cannot resolve
	// (a variable, a glob it could not expand, a substitution as an
	// operand) while some Read/Edit path rule is in deny or ask
	// (bash_paths.go). It is not a rule match: DecideFromHits only lets it
	// turn an Allow into an Ask, never weaken a Deny.
	Unsure bool
}

// RuleHits reports which rule lists match a call. The permission gate uses
// it to let deny and ask rules win over a session "don't ask again" grant.
//
// For bash, a file the command names that a Read/Edit deny rule covers is
// a deny hit and one an ask rule covers an ask hit.
func RuleHits(permissions Permissions, cwd, toolName, primaryArg string) Hits {
	hits := func(rules []string) bool {
		for _, r := range rules {
			if _, isPath := splitFileRule(r); isPath {
				continue // judged with its source by filePathVerdicts
			}
			if MatchesRule(r, toolName, primaryArg) {
				return true
			}
		}
		return false
	}

	c := newMatchCtx(cwd)
	h := Hits{Deny: hits(permissions.Deny), Ask: hits(permissions.Ask), Allow: hits(permissions.Allow)}
	switch {
	case strings.EqualFold(toolName, "bash"), strings.EqualFold(toolName, "bash_background"):
		if strings.EqualFold(toolName, "bash") {
			// A command line is several commands; judge each one
			// (bashRuleVerdicts).
			h.Deny, h.Ask, h.Allow = bashRuleVerdicts(permissions, toolName, primaryArg)
		}
		fileDeny, fileAsk, unsure := bashFileVerdict(permissions, c, primaryArg)
		h.Deny = h.Deny || fileDeny
		h.Ask = h.Ask || fileAsk
		h.Unsure = unsure
	case IsFileTool(toolName):
		d, a, al := filePathVerdicts(permissions, c, toolName, primaryArg)
		h.Deny, h.Ask, h.Allow = h.Deny || d, h.Ask || a, h.Allow || al
	}
	return h
}

// Decision is the outcome of Decide.
type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
	Ask   Decision = "ask"
)

// Decide whether a call may proceed.
//
// Rules are evaluated as Claude Code evaluates them: deny, then ask, then
// allow, the first match deciding, whatever the rules' specificity. deny
// is absolute: an explicit denial must not be overridable by a broader
// allow rule elsewhere in the hierarchy; and a matching ask rule prompts
// even when an allow rule also matches. bypassPermissions comes after both
// (Claude Code's permission-modes docs: deny rules block in every mode,
// and no mode auto-approves a call an explicit ask rule matches), so in
// bypassPermissions a deny rule still blocks and an ask rule still asks; a
// print run, having nobody to ask, refuses it (the gate). Allow rules
// change nothing in bypassPermissions, which allows everything else.
//
// Relative paths, and Read/Edit rules anchored at the current directory,
// resolve against the process's working directory; DecideIn names it.
func Decide(permissions Permissions, toolName, primaryArg string, mode PermissionMode) Decision {
	return DecideIn(permissions, "", toolName, primaryArg, mode)
}

// DecideIn is Decide with the primary working directory given: cwd is where
// a relative path argument, a "path" or "./path" Read/Edit rule, and a
// "/path" rule from project/local settings or the command line resolve.
// "" means the process's working directory. For a file tool (read, edit,
// write, ...) primaryArg is its path argument.
func DecideIn(permissions Permissions, cwd, toolName, primaryArg string, mode PermissionMode) Decision {
	return DecideFromHits(RuleHits(permissions, cwd, toolName, primaryArg), toolName, primaryArg, mode)
}

// DecideFromHits is DecideIn for a caller that already has RuleHits'
// result (the permission gate, which also needs the hits themselves).
//
// Hits.Unsure is applied last, to the verdict rules and mode reached: it
// lifts an Allow to an Ask (an unknowable file operand while path rules
// exist, in every mode, bypassPermissions included, since a deny rule is
// meant to hold in every mode) and leaves Deny and Ask as they are, so it
// can never turn plan mode's refusal into a prompt.
func DecideFromHits(h Hits, toolName, primaryArg string, mode PermissionMode) Decision {
	d := decideRules(h, toolName, primaryArg, mode)
	if d == Allow && h.Unsure {
		return Ask
	}
	return d
}

func decideRules(h Hits, toolName, primaryArg string, mode PermissionMode) Decision {
	if h.Deny {
		return Deny
	}
	if h.Ask {
		return Ask
	}
	if mode == ModeBypassPermissions || h.Allow {
		return Allow
	}

	switch mode {
	case ModePlan:
		// Read-only: anything that could mutate is refused outright rather
		// than prompted, which is what makes plan mode trustworthy. A bash
		// command that provably only reads (IsReadOnlyCommand) is allowed,
		// so planning can look around the way the read tool does.
		if ReadOnly[toolName] || (toolName == "bash" && IsReadOnlyCommand(primaryArg)) {
			return Allow
		}
		// Fetching a page changes nothing locally and is how a plan gets
		// researched, but the URL can carry data out, so it asks rather
		// than being refused or allowed.
		if toolName == "web_fetch" {
			return Ask
		}
		return Deny
	case ModeAcceptEdits:
		// task is also auto-approved: dispatching a subagent is not itself
		// a mutation, and the subagent inherits the parent's permission
		// mode, so its own tool calls are still gated normally. Without
		// this, a parallel task dispatch prompts for approval even though
		// the user already chose to auto-approve edits.
		if toolName == "edit" || toolName == "write" || toolName == "task" || ReadOnly[toolName] {
			return Allow
		}
		return Ask
	case ModeDontAsk:
		// Claude Code's dontAsk (docs: permission-modes, "Allow only
		// pre-approved tools with dontAsk mode"): what runs without asking
		// in manual mode still runs, allow rules still apply (above), and
		// everything that would prompt is denied instead. Decide reports
		// that as Ask, as manual does; the gate turns it into a denial,
		// after its own read-only bash check, so the two modes cannot
		// drift apart on what "would prompt" means.
		if ReadOnly[toolName] {
			return Allow
		}
		return Ask
	case ModeAuto:
		// Blanket allow, still subject to deny rules above and to the
		// workspace boundary enforced separately by the gate.
		return Allow
	case ModeManual:
		// Read-only tools never prompt, as Claude Code's Read/Glob/Grep
		// never do (docs/claude-code-reference.md §3: read-only calls are
		// grouped into "Read N files" without a permission step). The gate's
		// workspace boundary still asks about paths outside the roots.
		if ReadOnly[toolName] {
			return Allow
		}
		return Ask
	default:
		return Ask
	}
}
