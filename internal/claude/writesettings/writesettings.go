package writesettings

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// RuleList selects which permission list AddRule/RemoveRule operate on.
type RuleList string

const (
	Allow RuleList = "allow"
	Deny  RuleList = "deny"
	Ask   RuleList = "ask"
)

// LocalSettingsPath returns <cwd>/.claude/settings.local.json.
func LocalSettingsPath(cwd string) string {
	return filepath.Join(cwd, ".claude", "settings.local.json")
}

type localSettings map[string]any

func read(path string) localSettings {
	data, err := os.ReadFile(path)
	if err != nil {
		return localSettings{}
	}
	var parsed localSettings
	if err := json.Unmarshal(data, &parsed); err != nil {
		// Missing is the normal case. Malformed is not silently
		// overwritten by returning {}: that would come from throwing
		// here and losing one rule, which is worse than rewriting and
		// losing the whole file. Matching the TS behaviour exactly:
		// malformed content is treated the same as absent.
		return localSettings{}
	}
	if parsed == nil {
		return localSettings{}
	}
	return parsed
}

func write(path string, settings localSettings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func permissionsOf(settings localSettings) map[string]any {
	if p, ok := settings["permissions"].(map[string]any); ok {
		return p
	}
	return map[string]any{}
}

func stringList(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// AddRule adds rule to the given list in <cwd>/.claude/settings.local.json,
// reading, modifying and rewriting the file so anything else in it
// survives. Deduplicates on add.
func AddRule(cwd string, list RuleList, rule string) error {
	path := LocalSettingsPath(cwd)
	settings := read(path)
	permissions := permissionsOf(settings)
	existing := stringList(permissions[string(list)])

	found := false
	for _, r := range existing {
		if r == rule {
			found = true
			break
		}
	}
	if !found {
		existing = append(existing, rule)
	}
	permissions[string(list)] = toAnySlice(existing)
	settings["permissions"] = permissions
	return write(path, settings)
}

// RemoveRule removes rule from the given list. A rule that lives in
// settings.json or the user scope is not in this file, so there is
// nothing to remove; that is reported by the caller, not here.
func RemoveRule(cwd string, list RuleList, rule string) error {
	path := LocalSettingsPath(cwd)
	settings := read(path)
	permissions := permissionsOf(settings)
	existingRaw, ok := permissions[string(list)]
	if !ok {
		return nil
	}
	existing := stringList(existingRaw)
	out := make([]string, 0, len(existing))
	for _, r := range existing {
		if r != rule {
			out = append(out, r)
		}
	}
	permissions[string(list)] = toAnySlice(out)
	settings["permissions"] = permissions
	return write(path, settings)
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
