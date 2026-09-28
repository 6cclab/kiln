package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/andrepato/harness/internal/claude/paths"
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
type Permissions struct {
	Allow       []string
	Deny        []string
	Ask         []string
	DefaultMode PermissionMode // "" means unset
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

	files := []paths.SettingsFile{}
	for _, f := range paths.SettingsFiles(cwd) {
		if wants(opts.Sources, f.Scope) {
			files = append(files, f)
		}
	}
	if opts.Extra != "" {
		// Applied last so it overrides the hierarchy, mirroring the TS
		// behaviour of pushing it onto the "local" scope.
		files = append(files, paths.SettingsFile{Scope: paths.ScopeLocal, Path: opts.Extra})
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
			merged.Permissions.Allow = append(merged.Permissions.Allow, raw.Permissions.Allow...)
			merged.Permissions.Deny = append(merged.Permissions.Deny, raw.Permissions.Deny...)
			merged.Permissions.Ask = append(merged.Permissions.Ask, raw.Permissions.Ask...)
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
//
// Only `*` is a wildcard; every other regex metacharacter is escaped.
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
		return bare == tool
	}
	if strings.ToLower(m[1]) != tool {
		return false
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

	re, err := regexp.Compile("^" + pattern + "$")
	if err != nil {
		return false
	}
	return re.MatchString(strings.TrimSpace(primaryArg))
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
// deny is checked first and is absolute: an explicit denial must not be
// overridable by a broader allow rule elsewhere in the hierarchy.
func Decide(permissions Permissions, toolName, primaryArg string, mode PermissionMode) Decision {
	hits := func(rules []string) bool {
		for _, r := range rules {
			if MatchesRule(r, toolName, primaryArg) {
				return true
			}
		}
		return false
	}

	denyHit, askHit, allowHit := hits(permissions.Deny), hits(permissions.Ask), hits(permissions.Allow)
	if strings.EqualFold(toolName, "bash") {
		// A command line is several commands; judge each one
		// (bashRuleVerdicts).
		denyHit, askHit, allowHit = bashRuleVerdicts(permissions, toolName, primaryArg)
	}

	if denyHit {
		return Deny
	}
	if mode == ModeBypassPermissions {
		return Allow
	}
	if allowHit {
		return Allow
	}
	if askHit {
		return Ask
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
		return Allow
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
