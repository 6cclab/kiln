package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
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
	// AutoMemoryEnabled mirrors Claude Code's autoMemoryEnabled setting:
	// nil means unset (auto memory defaults to on), a set value from a
	// later scope overrides an earlier one, so a project can turn off what
	// the user's settings turned on. kiln only reads this - it never
	// writes auto memory itself, but honours the same switch Claude Code
	// does so a project that opted out stays opted out here too.
	AutoMemoryEnabled *bool
	// AutoMemoryDirectory mirrors Claude Code's autoMemoryDirectory
	// setting: an absolute path or one starting with "~/", overriding
	// where the auto-memory directory is resolved to. "" means unset.
	AutoMemoryDirectory string
	// LoadedFrom records which files actually contributed, for
	// diagnostics — Claude Code's own settings.json and kiln's own (same
	// scope, different file: ~/.claude/settings.json and
	// ~/.kiln/settings.json are both ScopeUser) are kept separate so a
	// report naming only the scope can't conflate them
	// (qa/findings/20261004T205021Z-doctor-misses-sandbox-and-hook-
	// events.json: /doctor printed "user, user, project", indistinguishable).
	LoadedFrom []LoadedSettingsFile
	// HeldAllow are the allow rules of the settings files held back
	// because the folder is not trusted (LoadOptions.Trusted): the
	// project's .claude/settings.json, and a local file that may have come
	// with the repository. HeldFrom is each one's source. A caller adds
	// them once the folder is trusted. Until then only a held file's deny
	// and ask rules apply, since they can only restrict.
	HeldAllow []string
	HeldFrom  []RuleSource
	// IgnoredModes are the files whose permissions.defaultMode was auto or
	// bypassPermissions in project or local settings, which Claude Code
	// never honours from those files, trusted or not: the session starts
	// in the built-in default (manual) instead.
	IgnoredModes []string
	// HeldFiles are the held files that exist, in load order.
	HeldFiles []string
	// Sandbox is the merged "sandbox" object (sandbox.go), and
	// SandboxWarnings the entries in it that were skipped.
	Sandbox         Sandbox
	SandboxWarnings []string
	// Unreadable are settings files that exist but could not be read or
	// parsed, so contributed nothing. A reload mid-session keeps the rules
	// it had rather than lose a file's deny rules to a half-written save.
	Unreadable []string
}

// LoadedSettingsFile is one settings file that actually contributed to a
// merge (Settings.LoadedFrom): its scope, its path, and whether it is
// kiln's own file (~/.kiln/settings.json, <cwd>/.kiln/settings.local.json)
// rather than Claude Code's (~/.claude/settings.json, <cwd>/.claude/
// settings.json, <cwd>/.claude/settings.local.json).
type LoadedSettingsFile struct {
	Scope paths.Scope
	Path  string
	Kiln  bool
}

// Label is this file's short, human name for a diagnostics report ("/doctor",
// "kiln doctor", /config): the owning directory, "~/" abbreviated for the
// user scope — "~/.claude", "~/.kiln", ".claude" (project and local scopes
// are not distinguished here; both read as the project's own directory).
func (f LoadedSettingsFile) Label() string {
	dir := ".claude"
	if f.Kiln {
		dir = ".kiln"
	}
	if f.Scope == paths.ScopeUser {
		return "~/" + dir
	}
	return dir
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
	Permissions         *rawPermissions   `json:"permissions"`
	Model               string            `json:"model"`
	EffortLevel         string            `json:"effortLevel"`
	ModelRoles          map[string]string `json:"modelRoles"`
	Env                 map[string]string `json:"env"`
	StatusLine          *StatusLineConfig `json:"statusLine"`
	AutoMemoryEnabled   *bool             `json:"autoMemoryEnabled"`
	AutoMemoryDirectory string            `json:"autoMemoryDirectory"`
}

// LoadOptions configures LoadSettings.
type LoadOptions struct {
	// Sources restricts which scopes are read, mirroring --setting-sources.
	// Nil means all three.
	Sources []paths.Scope
	// Extra is an additional file read last, from --settings. Highest
	// precedence: it is treated as an extra "local" scope entry.
	Extra string
	// Trusted is set when the folder is trusted (the trust dialog was
	// accepted for it or an ancestor). Otherwise the settings files a
	// repository can supply are held (Settings.HeldAllow): their allow
	// rules wait for trust, as in Claude Code, which
	// applies a project's permissions.allow only after its workspace
	// trust dialog is accepted. Deny and ask rules apply regardless.
	Trusted bool
	// Headless is a -p run, which never shows the trust dialog. Claude
	// Code then checks with git whether .claude/settings.local.json came
	// with the repository (tracked, or .claude a symlink) and holds it
	// only if so. Interactively, before trust, it does not run git in the
	// folder at all and holds every local file like the project's; the
	// dialog comes before any prompt, so nothing is lost by waiting.
	Headless bool
	// Quiet leaves the "Ignoring unreadable settings" warning off stderr
	// (a reload mid-session, under the TUI); Settings.Unreadable still
	// names the file.
	Quiet bool
}

// settingsFile is one file LoadSettings reads, with how its rules apply.
type settingsFile struct {
	paths.SettingsFile
	root string // RuleSource.Root for this file's rules
	held bool   // only deny and ask rules apply; allow is held
	// heldAll holds every other key too: kiln's own .kiln file, which a
	// repository should never supply at all.
	heldAll bool
	cli     bool // --settings
	kiln    bool // kiln's own file, not Claude Code's (LoadedSettingsFile.Kiln)
}

// settingsFiles lists the files LoadSettings reads for opts, in order.
func settingsFiles(cwd string, opts LoadOptions) []settingsFile {
	files := []settingsFile{}
	for _, f := range paths.AllSettingsFiles(cwd) {
		if wants(opts.Sources, f.Scope) {
			root := ""
			if f.Scope == paths.ScopeUser {
				// "/path" in user settings is under ~/.claude (~/.kiln for
				// kiln's), the directory that holds the file (Claude Code's
				// table).
				root = filepath.Dir(f.Path)
			}
			held, heldAll := false, false
			if !opts.Trusted {
				held, heldAll = holdUntrusted(cwd, f, opts.Headless)
			}
			files = append(files, settingsFile{f.SettingsFile, root, held, heldAll, false, f.Kiln})
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
		files = append(files, settingsFile{paths.SettingsFile{Scope: paths.ScopeLocal, Path: opts.Extra}, root, false, false, true, false})
	}
	return files
}

// SettingsFiles is every settings file LoadSettings reads for opts, read
// or not, in order: what a watcher for changes to them watches.
func SettingsFiles(cwd string, opts LoadOptions) []string {
	var out []string
	for _, f := range settingsFiles(cwd, opts) {
		out = append(out, f.Path)
	}
	return out
}

// repoScopedMode reports a defaultMode that a project or local settings
// file may not set: auto and bypassPermissions take effect only from user
// settings and --settings, as in Claude Code.
func repoScopedMode(f settingsFile, m PermissionMode) bool {
	if f.cli || (f.Scope != paths.ScopeProject && f.Scope != paths.ScopeLocal) {
		return false
	}
	return m == ModeAuto || m == ModeBypassPermissions
}

// holdUntrusted decides whether f, read in a folder that is not trusted,
// is held (its allow rules wait for trust) and whether every other key of
// it waits too. User settings are the person's own; the project's
// .claude/settings.json comes with the repository. A local file is held
// when it may have come with it: always before an interactive trust
// dialog (no git is run in an untrusted folder), and under -p unless git
// shows it is untracked and it is reached through no symlink
// (repoSupplied). kiln's own .kiln/settings.local.json is held whole when
// it may have come with the repository, since kiln never puts one there.
func holdUntrusted(cwd string, f paths.SettingsSource, headless bool) (held, heldAll bool) {
	switch f.Scope {
	case paths.ScopeProject:
		return true, false
	case paths.ScopeLocal:
		if _, err := os.Lstat(f.Path); err != nil {
			return false, false
		}
		if !headless || repoSupplied(cwd, f.Path) {
			return true, f.Kiln
		}
	}
	return false, false
}

// repoSupplied reports a local settings file that may have come with the
// repository rather than from this machine. It is local only when that is
// shown: neither it nor its directory (.claude, .kiln) is a symlink or a
// repository of its own, and git either says the folder is in no
// repository or lists neither the file nor a submodule at its directory,
// compared case-insensitively (a case-insensitive filesystem opens
// .Claude/Settings.local.json as the same file). Anything else, a git
// error or no git at all, counts as supplied.
//
// git ls-files reads only the index: it runs no filter, hook or
// fsmonitor, and the last two are switched off besides, so asking cannot
// run a command the repository configures.
func repoSupplied(cwd, path string) bool {
	dir := filepath.Dir(path)
	for _, p := range []string{path, dir} {
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true // a repository (or submodule checkout) of its own
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return true
	}
	rel = filepath.ToSlash(rel)
	relDir := filepath.ToSlash(filepath.Dir(rel))
	cmd := exec.Command("git", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath="+os.DevNull,
		"ls-files", "--stage", "-z", "--", ":(icase,literal)"+rel, ":(icase,literal)"+relDir)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANGUAGE=") // the message read below
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		// In no repository at all: the file is the person's own.
		return !(errors.As(err, &exit) && exit.ExitCode() == 128 && strings.Contains(stderr.String(), "not a git repository"))
	}
	for _, entry := range strings.Split(string(out), "\x00") {
		// "<mode> <object> <stage>\t<path>"
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		if strings.EqualFold(name, rel) || (strings.EqualFold(name, relDir) && strings.HasPrefix(meta, "160000 ")) {
			return true
		}
	}
	return false
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

// LoadSettings reads and merges .claude/settings.json across scopes, and
// kiln's own files (paths.AllSettingsFiles): ~/.kiln/settings.json right
// after the user's ~/.claude/settings.json, and <cwd>/.kiln/settings.local.json
// right after .claude/settings.local.json, each in its scope.
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

	for _, f := range settingsFiles(cwd, opts) {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			// Absent is normal. Any other failure (permissions, a
			// directory) is recorded; malformed JSON is reported below.
			if !errors.Is(err, fs.ErrNotExist) {
				merged.Unreadable = append(merged.Unreadable, f.Path)
			}
			continue
		}
		var raw rawSettings
		if err := json.Unmarshal(data, &raw); err != nil {
			merged.Unreadable = append(merged.Unreadable, f.Path)
			if !opts.Quiet {
				fmt.Fprintf(os.Stderr, "Ignoring unreadable settings at %s: %v\n", f.Path, err)
			}
			continue
		}

		merged.LoadedFrom = append(merged.LoadedFrom, LoadedSettingsFile{Scope: f.Scope, Path: f.Path, Kiln: f.kiln})
		// The sandbox has its own rules for what a repository's file may
		// set (sandbox.go). A -p run in a folder never trusted also takes
		// only the entries that narrow it from a held file: turning it on
		// would auto-allow bash (autoAllowBashIfSandboxed), and its write
		// and network lists widen it. Claude Code honours them there; kiln
		// is stricter. Interactively nothing runs before the dialog, and
		// trusting it makes them the person's to apply.
		mergeSandbox(&merged, data, sandboxSourceFor(cwd, f.Path, f.Scope, f.cli, f.heldAll || (f.held && opts.Headless)))
		if f.held {
			merged.HeldFiles = append(merged.HeldFiles, f.Path)
			if raw.Permissions != nil {
				src := RuleSource{Scope: f.Scope, File: f.Path, Root: f.root}
				merged.HeldAllow = append(merged.HeldAllow, raw.Permissions.Allow...)
				for range raw.Permissions.Allow {
					merged.HeldFrom = append(merged.HeldFrom, src)
				}
				raw.Permissions.Allow = nil
			}
			if f.heldAll {
				// Only the restrictions of kiln's own file, when it came
				// with the repository.
				if p := raw.Permissions; p != nil {
					raw = rawSettings{Permissions: &rawPermissions{Deny: p.Deny, Ask: p.Ask}}
				} else {
					continue
				}
			}
		}
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
			if m := raw.Permissions.DefaultMode; m != "" {
				merged.Permissions.DefaultMode = m
				if repoScopedMode(f, m) {
					merged.Permissions.DefaultMode = ""
					merged.IgnoredModes = append(merged.IgnoredModes, f.Path)
				}
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
		if raw.AutoMemoryEnabled != nil {
			merged.AutoMemoryEnabled = raw.AutoMemoryEnabled
		}
		if raw.AutoMemoryDirectory != "" {
			merged.AutoMemoryDirectory = raw.AutoMemoryDirectory
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
//	mcp__homelab     every tool of server homelab, and no other server
//	mcp__homelab__*  the same
//	mcp__homelab__x  that one MCP tool (MCP names match case-sensitively)
//	Bash(find:*)     colon form: commands beginning with `find`
//	Bash(git *)      glob form, as documented by `claude --help`
//	Read(src/**)     a Read/Edit path rule, gitignore-style (pathrules.go)
//
// A bare Edit rule covers every edit tool (edit, write, ...) and a bare Read
// rule every read tool, as in Claude Code. For the non-path shapes only `*`
// is a wildcard; every other regex metacharacter is escaped.
// Matching is case-insensitive because Claude Code writes Read/Bash/Edit
// while pi's tools are read/bash/edit; MCP names, which both write the
// same way, match case-sensitively, as in Claude Code.
func MatchesRule(rule, toolName, primaryArg string) bool {
	tool := strings.ToLower(toolName)

	m := parenRule.FindStringSubmatch(rule)
	if m == nil {
		if raw := strings.TrimSpace(rule); strings.HasPrefix(raw, "mcp__") || strings.HasPrefix(toolName, "mcp__") {
			return mcpRuleMatches(raw, toolName)
		}
		bare := strings.ToLower(strings.TrimSpace(rule))
		return sameTool(bare, tool) || bareFamilyMatches(bare, tool)
	}
	if name := strings.TrimSpace(m[1]); strings.HasPrefix(name, "mcp__") || strings.HasPrefix(toolName, "mcp__") {
		// An MCP tool's name is compared as written, case included:
		// sameTool's underscore and case folding would make mcp__a_b__c
		// and mcp__ab__c, or mcp__Srv__x and mcp__srv__x, one tool.
		if name != toolName {
			return false
		}
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

	// (?s): "*" matches newlines too, as Claude Code compiles its patterns
	// with the dotAll flag. Without it a quoted newline in an argument
	// ended every match: an allow rule missed a multi-line commit message,
	// and a deny rule such as Bash(rm *) missed rm -rf "a<newline>b".
	re := compiledPattern("(?s)^" + pattern + "$")
	if re == nil {
		return false
	}
	return re.MatchString(strings.TrimSpace(primaryArg))
}

// mcpRuleMatches matches a bare rule against a tool when either is an MCP
// name ("mcp__server" or "mcp__server__tool"), as Claude Code's
// toolMatchesRule does: the same name, or a rule naming only the server
// (or the server and "*") and the tool's server parsing as the same one,
// case-sensitively. A rule is never a string prefix: "mcp__homelab" says
// nothing about server "homelab-kb". The split is not exact for a server
// name holding "__": as in Claude Code, a name splits at its first "__",
// so "mcp__a" also covers the tools of a server named "a__b".
func mcpRuleMatches(rule, tool string) bool {
	if rule == tool {
		return true
	}
	ruleServer, ruleTool, ok := mcpParts(rule)
	if !ok || (ruleTool != "" && ruleTool != "*") {
		return false
	}
	toolServer, _, ok := mcpParts(tool)
	return ok && ruleServer == toolServer
}

// mcpParts splits "mcp__server__tool" into its server and tool; tool is ""
// for "mcp__server". Not ok for a name that is not an MCP one.
func mcpParts(name string) (server, tool string, ok bool) {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return "", "", false
	}
	server, tool, _ = strings.Cut(rest, "__")
	return server, tool, server != ""
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
	// ReadOnly is set for a bash command that only reads (IsReadOnlyCommand,
	// with the working directory known, so "cd <cwd> && git log" is a no-op
	// cd and still read-only).
	ReadOnly bool
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
	case isBashTool(toolName):
		// A command line is several commands; judge each one, nested ones
		// included (bash_parse.go, bashRuleVerdicts), and the files they
		// name when a Read/Edit rule could cover one (bashFileVerdict).
		guarded := compiledFor(permissions, c).guarded
		a := analyzeBash(primaryArg, c.cwd, c.home, guarded)
		h.Deny, h.Ask, h.Allow = bashRuleVerdicts(permissions, a, toolName, primaryArg)
		fileDeny, fileAsk, unsure := bashFileVerdict(permissions, c, a)
		h.Deny = h.Deny || fileDeny
		h.Ask = h.Ask || fileAsk
		h.Unsure = unsure
		// A line kiln cannot parse, or one running something it cannot
		// name, is asked about rather than allowed past a deny or ask rule
		// that might have covered it.
		if (!a.parsed || a.unknown) && (guarded || bashGuarded(permissions, toolName)) {
			h.Unsure = true
		}
		if !a.parsed {
			// Deny and ask rules still see what can be made out: each piece
			// between separators that parses on its own.
			for _, seg := range roughSegments(primaryArg) {
				s := analyzeBash(seg, c.cwd, c.home, guarded)
				if !s.parsed {
					continue
				}
				d, k, _ := bashRuleVerdicts(permissions, s, toolName, seg)
				fd, fk, _ := bashFileVerdict(permissions, c, s)
				h.Deny = h.Deny || d || fd
				h.Ask = h.Ask || k || fk
			}
		}
		h.ReadOnly = a.readOnly()
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

// Plan mode, as Claude Code's docs describe it: "edits stay blocked until
// you approve the plan", whatever allow or ask rules say; read-only shell
// commands run without prompting, and "any other shell command goes
// through the regular permission flow while you are still planning"
// (permissions, "Sandboxing" section): deny, ask, allow, and with no rule
// a prompt, never a refusal.

// PlanOverridesAllow reports a call that no allow rule, mode or session
// "don't ask again" grant approves while planning: an edit.
func PlanOverridesAllow(toolName, _ string) bool {
	return toolFileKind(toolName) == kindEdit
}

func decideRules(h Hits, toolName, primaryArg string, mode PermissionMode) Decision {
	if h.Deny {
		return Deny
	}
	if mode == ModePlan && PlanOverridesAllow(toolName, primaryArg) {
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
		// Edits were refused above. A bash command that provably only
		// reads (IsReadOnlyCommand) is allowed, so planning can look around
		// the way the read tool does; any other one asks, as in manual
		// mode. Any other tool no rule allowed is refused outright.
		if ReadOnly[toolName] {
			return Allow
		}
		if isBashTool(toolName) {
			if h.ReadOnly {
				return Allow
			}
			return Ask
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
		// Allow as far as rules go, still subject to deny and ask rules
		// above. The gate then applies the workspace boundary and sends
		// what is left past the auto mode classifier
		// (internal/claude/permission/classifier.go).
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
