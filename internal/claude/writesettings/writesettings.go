package writesettings

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/andrepato/harness/internal/claude/paths"
)

// RuleList selects which permission list AddRule/RemoveRule operate on.
type RuleList string

const (
	Allow RuleList = "allow"
	Deny  RuleList = "deny"
	Ask   RuleList = "ask"
)

// LocalSettingsPath returns <cwd>/.kiln/settings.local.json, the only
// project file kiln writes. Claude Code's .claude files are read, never
// written.
func LocalSettingsPath(cwd string) string {
	return paths.KilnLocalSettingsPath(cwd)
}

// UserSettingsPath is ~/.kiln/settings.json, the only user file kiln
// writes (the /model default).
func UserSettingsPath() string {
	return paths.KilnUserSettingsPath()
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

// write replaces path with settings atomically (a temp file in the same
// directory, renamed over it). A .kiln directory it creates gets a
// .gitignore of "*", so kiln's files never show up in git status.
func write(path string, settings localSettings) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if filepath.Base(dir) == paths.KilnDir {
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o644); err != nil {
				return err
			}
		}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
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

// AddRule adds rule to the given list in <cwd>/.kiln/settings.local.json,
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

// HasRule reports whether rule is in the given list of kiln's local file,
// the only file RemoveRule can take it out of.
func HasRule(cwd string, list RuleList, rule string) bool {
	for _, r := range stringList(permissionsOf(read(LocalSettingsPath(cwd)))[string(list)]) {
		if r == rule {
			return true
		}
	}
	return false
}

// RemoveRule removes rule from the given list of kiln's local file. A rule
// that lives in a Claude Code settings file is not in this file, so there
// is nothing to remove; that is reported by the caller (HasRule), not here.
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

// SetUserModel writes "model": "<provider/model>" into ~/.kiln/settings.json,
// the default for new sessions (/model's Enter path), preserving every other
// key. kiln reads that file after ~/.claude/settings.json, so its model wins
// there; Claude Code's own file is left as it is.
func SetUserModel(model string) error {
	path := UserSettingsPath()
	settings := read(path)
	settings["model"] = model
	return write(path, settings)
}

func toAnySlice(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
