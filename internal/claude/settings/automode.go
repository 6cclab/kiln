package settings

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/andrepato/harness/internal/claude/paths"
)

// AutoModeDefaults is the marker that splices the built-in entries into an
// autoMode list ("$defaults"), as Claude Code's docs describe it
// (code.claude.com/docs/en/auto-mode-config, "Override the block and allow
// rules"): a list without it replaces the built-in entries for that section.
const AutoModeDefaults = "$defaults"

// AutoModeConfig is settings.json's "autoMode" block: prose entries the auto
// mode classifier reads as part of its instructions. Each list is nil when
// no settings file set it, so the classifier uses its built-in entries.
type AutoModeConfig struct {
	// Environment describes trusted infrastructure: repositories, domains,
	// buckets, services.
	Environment []string
	// Allow are exceptions to SoftDeny.
	Allow []string
	// SoftDeny are risky actions a specific user request can clear.
	SoftDeny []string
	// HardDeny are actions no user request or allow entry clears.
	HardDeny []string
	// Ignored lists settings files that had an autoMode block kiln did not
	// read (project and local settings), for a startup warning.
	Ignored []string
}

type rawAutoMode struct {
	Environment []string `json:"environment"`
	Allow       []string `json:"allow"`
	SoftDeny    []string `json:"soft_deny"`
	HardDeny    []string `json:"hard_deny"`
}

// LoadAutoMode reads the "autoMode" block from the settings files the
// classifier trusts: user settings (~/.claude/settings.json, then kiln's
// ~/.kiln/settings.json) and the --settings file. It never reads project or
// local settings — .claude/settings.json, .claude/settings.local.json or
// .kiln/settings.local.json — because they live in the repository, and a
// cloned repo must not be able to tell the classifier what to allow. Claude
// Code draws the same line (code.claude.com/docs/en/auto-mode-config, "Where
// the classifier reads configuration"). A block found there is reported in
// Ignored.
//
// Entries from every trusted file are concatenated in that order. Kept apart
// from LoadSettings so the autoMode block's narrower source list cannot be
// confused with the merge every other key gets.
func LoadAutoMode(cwd string, opts LoadOptions) AutoModeConfig {
	var cfg AutoModeConfig
	read := func(path string) (*rawAutoMode, bool) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, false
		}
		var raw struct {
			AutoMode *rawAutoMode `json:"autoMode"`
		}
		if err := json.Unmarshal(data, &raw); err != nil || raw.AutoMode == nil {
			return nil, false
		}
		return raw.AutoMode, true
	}
	add := func(r *rawAutoMode) {
		cfg.Environment = appendSet(cfg.Environment, r.Environment)
		cfg.Allow = appendSet(cfg.Allow, r.Allow)
		cfg.SoftDeny = appendSet(cfg.SoftDeny, r.SoftDeny)
		cfg.HardDeny = appendSet(cfg.HardDeny, r.HardDeny)
	}
	for _, f := range paths.AllSettingsFiles(cwd) {
		r, ok := read(f.Path)
		if !ok {
			continue
		}
		if f.Scope != paths.ScopeUser {
			cfg.Ignored = append(cfg.Ignored, f.Path)
			continue
		}
		if wants(opts.Sources, f.Scope) {
			add(r)
		}
	}
	if opts.Extra != "" {
		if r, ok := read(opts.Extra); ok {
			add(r)
		}
	}
	return cfg
}

// appendSet appends b to a, keeping "set but empty" distinct from unset: an
// explicit [] in settings still replaces the defaults.
func appendSet(a, b []string) []string {
	if b == nil {
		return a
	}
	if a == nil {
		a = []string{}
	}
	return append(a, b...)
}

// SpliceAutoModeDefaults resolves one autoMode list against its built-in
// entries: nil (unset) gives the defaults; a list containing "$defaults"
// gets the defaults spliced in at its first occurrence (later occurrences
// dropped); any other list replaces the defaults.
func SpliceAutoModeDefaults(list, defaults []string) []string {
	if list == nil {
		return append([]string(nil), defaults...)
	}
	out := make([]string, 0, len(list)+len(defaults))
	spliced := false
	for _, e := range list {
		if e == AutoModeDefaults {
			if !spliced {
				out = append(out, defaults...)
				spliced = true
			}
			continue
		}
		out = append(out, e)
	}
	return out
}

// autoModeIgnoredName is the file name a warning shows for an ignored block.
func autoModeIgnoredName(cwd, path string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil {
		return rel
	}
	return path
}

// AutoModeIgnoredWarning is the startup warning for autoMode blocks kiln
// did not read, "" when there are none.
func AutoModeIgnoredWarning(cwd string, ignored []string) string {
	if len(ignored) == 0 {
		return ""
	}
	name := autoModeIgnoredName(cwd, ignored[0])
	if len(ignored) > 1 {
		name += " and other project settings"
	}
	return "autoMode in " + name + " is ignored: the auto mode classifier reads it only from ~/.claude/settings.json, ~/.kiln/settings.json or --settings, so a repository cannot change what it allows."
}
