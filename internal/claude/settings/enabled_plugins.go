package settings

import (
	"encoding/json"
	"os"

	"github.com/andrepato/harness/internal/claude/paths"
)

type rawEnabledPlugins struct {
	EnabledPlugins map[string]bool `json:"enabledPlugins"`
}

// LoadEnabledPlugins reads settings.json's "enabledPlugins" map across the
// same three scopes LoadSettings reads (user, project, local), later
// scopes overriding per key. A plugin's key ("<plugin>@<marketplace>") is
// active only when the merged value is true — absent or false both mean
// inactive, matching Claude Code: enabling a plugin is opt-in, not
// opt-out.
//
// A separate loader rather than a field on Settings/LoadSettings: nothing
// else needs this map, and folding it into the general merge would mean
// every LoadSettings call pays for parsing a key almost nothing reads.
func LoadEnabledPlugins(cwd string) map[string]bool {
	merged := map[string]bool{}
	for _, f := range paths.SettingsFiles(cwd) {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			continue
		}
		var raw rawEnabledPlugins
		if err := json.Unmarshal(data, &raw); err != nil {
			// Malformed settings are already reported by LoadSettings
			// reading the same file; nothing more to do here.
			continue
		}
		for key, enabled := range raw.EnabledPlugins {
			merged[key] = enabled
		}
	}
	return merged
}
