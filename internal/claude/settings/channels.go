package settings

import (
	"encoding/json"
	"os"

	"github.com/andrepato/harness/internal/claude/paths"
)

type rawChannels struct {
	Channels map[string]bool `json:"channels"`
}

// LoadChannels reads settings.json's "channels" map across the same three
// scopes LoadSettings reads (user, project, local), later scopes overriding
// per key. A key ("<plugin>@<marketplace>") subscribes the session to that
// plugin's declared channels only when the merged value is true — absent or
// false both mean unsubscribed, matching enabledPlugins: a channel is
// opt-in.
//
// Unlike Claude Code, which takes channels from the --channels flag, kiln
// drives them from settings so a channel can be turned on globally in the
// user scope (the design studio, say, used across every project) and still
// be overridden off for a single repo in its project/local settings — the
// per-key merge gives that for free.
//
// A separate loader rather than a field on Settings/LoadSettings, for the
// same reason as LoadEnabledPlugins: nothing else needs this map.
func LoadChannels(cwd string) map[string]bool {
	return LoadChannelsFrom(cwd, nil)
}

// LoadChannelsFrom is LoadChannels restricted to the scopes in sources (nil:
// every scope), as --setting-sources restricts which settings files are
// read: with `--setting-sources user`, a channel only the project's
// settings enable stays off, matching LoadEnabledPluginsFrom.
func LoadChannelsFrom(cwd string, sources []paths.Scope) map[string]bool {
	merged := map[string]bool{}
	for _, f := range paths.SettingsFiles(cwd) {
		if !wants(sources, f.Scope) {
			continue
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			continue
		}
		var raw rawChannels
		if err := json.Unmarshal(data, &raw); err != nil {
			// Malformed settings are already reported by LoadSettings
			// reading the same file; nothing more to do here.
			continue
		}
		for key, on := range raw.Channels {
			merged[key] = on
		}
	}
	return merged
}
