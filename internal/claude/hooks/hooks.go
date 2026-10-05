package hooks

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"sort"

	"github.com/andrepato/harness/internal/claude/paths"
)

// Event is a hook lifecycle event name. Claude Code itself recognizes more
// of these (PermissionRequest, PostCompact, PostToolUseFailure,
// StopFailure, SubagentStart, TeammateIdle, …) than kiln fires — see the
// nine constants below and UnsupportedEvents, which tells the two apart
// for a settings.json carrying hooks kiln will never run.
type Event string

const (
	PreToolUse       Event = "PreToolUse"
	PostToolUse      Event = "PostToolUse"
	UserPromptSubmit Event = "UserPromptSubmit"
	SessionStart     Event = "SessionStart"
	SessionEnd       Event = "SessionEnd"
	Stop             Event = "Stop"
	SubagentStop     Event = "SubagentStop"
	Notification     Event = "Notification"
	PreCompact       Event = "PreCompact"
)

// knownEvents is the nine constants above, as a set — kept here once so
// every caller asking "does kiln actually fire this event" (UnsupportedEvents,
// /doctor, `kiln doctor`) reads the same answer instead of each keeping its
// own copy of the const list that can drift.
var knownEvents = map[Event]bool{
	PreToolUse: true, PostToolUse: true, UserPromptSubmit: true,
	SessionStart: true, SessionEnd: true, Stop: true, SubagentStop: true,
	Notification: true, PreCompact: true,
}

// UnsupportedEvents returns, sorted, every event cfg registers at least one
// hook for that kiln does not fire at all — a settings.json hook event
// Claude Code defines (PermissionRequest, PostCompact, PostToolUseFailure,
// StopFailure, SubagentStart, TeammateIdle, …) that kiln silently dropped
// before this (qa/findings/20261004T205021Z-doctor-misses-sandbox-and-
// hook-events.json: 6 configured events went unreported by /doctor's hook
// count, with no note that they are simply unsupported).
func UnsupportedEvents(cfg Config) []Event {
	var out []Event
	for e, groups := range cfg {
		if knownEvents[e] {
			continue
		}
		n := 0
		for _, g := range groups {
			n += len(g.Hooks)
		}
		if n == 0 {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Command is one hook invocation: a shell one-liner run at a defined
// point, with an optional timeout in seconds.
type Command struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	// Timeout is in seconds. Claude Code's default is 60. Zero means unset.
	//
	// Deliberately not clamped to any ceiling: checked against Claude
	// Code's own hook runner, a configured timeout is used exactly as
	// given, with no maximum - only the unset (zero) case falls back to
	// an internal default. kiln matches that rather than inventing a cap
	// of its own: a hook that sets an hour is trusted to mean it, the same
	// way a long-running PreToolUse hook is trusted there.
	Timeout int `json:"timeout,omitempty"`
	// StatusMessage, when set, is what the busy row shows while this hook
	// runs in place of the default "running stop hook" (Claude Code's
	// statusMessage hook field). Only Stop and SubagentStop show it today.
	StatusMessage string `json:"statusMessage,omitempty"`
	// Env is extra environment variables set for this hook's process, on
	// top of the ambient environment and CLAUDE_HOOK/HARNESS_HOOK.
	// Unmarshaled from settings.json's own hooks (always nil there) and
	// set by internal/claude/plugins for a plugin's hooks, whose commands
	// get CLAUDE_PLUGIN_ROOT the same way Claude Code sets it.
	Env map[string]string `json:"-"`
}

// Matcher groups hook commands under a regex over the tool name. Matcher
// is empty (or "*") for events that have no tool.
type Matcher struct {
	MatcherPattern string    `json:"matcher,omitempty"`
	Hooks          []Command `json:"hooks"`
}

// Config maps each event to the matcher groups registered for it.
type Config map[Event][]Matcher

// DefaultTimeoutSeconds is Claude Code's default. A hook that hangs must
// not hang the session.
const DefaultTimeoutSeconds = 60

// MatchesHook reports whether this matcher applies to this tool.
//
// Anchored: "Bash" must not match "BashOutput". Case-insensitive because
// Claude Code writes Bash/Read while pi's tools are lowercase. A malformed
// regex must not take down every tool call, so it is treated as "does not
// match" — failing closed on a rewrite hook is the safe direction.
func MatchesHook(matcher string, toolName string, hasToolName bool) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	if !hasToolName {
		return false
	}
	re, err := regexp.Compile("(?i)^(?:" + matcher + ")$")
	if err != nil {
		return false
	}
	return re.MatchString(toolName)
}

// HooksFor returns every hook command registered for an event that
// applies to this tool. toolName being "" with hasToolName false models
// events with no tool.
func HooksFor(config Config, event Event, toolName string, hasToolName bool) []Command {
	var out []Command
	for _, group := range config[event] {
		if !MatchesHook(group.MatcherPattern, toolName, hasToolName) {
			continue
		}
		for _, h := range group.Hooks {
			if h.Type == "command" && h.Command != "" {
				out = append(out, h)
			}
		}
	}
	return out
}

type rawSettings struct {
	Hooks map[string][]Matcher `json:"hooks"`
}

// LoadHooks loads and merges hooks across settings scopes.
//
// Hooks accumulate rather than override, which is the opposite of how the
// `model` field merges and is deliberate: a project that defines a
// formatting hook should not silently disable the user's global audit
// hook. Both run.
func LoadHooks(cwd string) Config {
	return LoadHooksFrom(cwd, nil)
}

// LoadHooksFrom is LoadHooks restricted to the scopes in sources, as
// --setting-sources restricts which settings files are read at all (nil:
// every scope). `--setting-sources user` is how a -p run in a repository
// the person did not write keeps that repository's hooks from running,
// together with plugins.LoadPluginsFrom, which leaves out the plugins
// only the repository's settings enable (and so their hooks).
func LoadHooksFrom(cwd string, sources []paths.Scope) Config {
	merged := Config{}
	for _, f := range paths.SettingsFiles(cwd) {
		if sources != nil && !slices.Contains(sources, f.Scope) {
			continue
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			continue
		}
		var raw rawSettings
		if err := json.Unmarshal(data, &raw); err != nil {
			// Missing is the common case; malformed is the user's business
			// and is already reported by the settings loader reading the
			// same file.
			continue
		}
		for event, groups := range raw.Hooks {
			merged[Event(event)] = append(merged[Event(event)], groups...)
		}
	}
	return merged
}
