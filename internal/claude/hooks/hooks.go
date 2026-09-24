package hooks

import (
	"encoding/json"
	"os"
	"regexp"

	"github.com/andrepato/harness/internal/claude/paths"
)

// Event is one of the nine hook lifecycle events Claude Code recognizes.
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

// Command is one hook invocation: a shell one-liner run at a defined
// point, with an optional timeout in seconds.
type Command struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	// Timeout is in seconds. Claude Code's default is 60. Zero means unset.
	Timeout int `json:"timeout,omitempty"`
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
	merged := Config{}
	for _, f := range paths.SettingsFiles(cwd) {
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
