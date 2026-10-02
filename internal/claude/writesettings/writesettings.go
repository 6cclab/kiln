package writesettings

import (
	"encoding/json"
	"fmt"
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

// kilnGitignore is written into a freshly created .kiln directory. Most
// kiln files (settings.local.json, mcp.json's local/user entries when that
// directory is ~/.kiln) are machine-local and must never be committed, so
// the default is "ignore everything" - except <repo>/.kiln/mcp.json, which
// is meant to be committed like Claude Code's own .mcp.json, so it (and
// the .gitignore file itself) is carved back out.
const kilnGitignore = "*\n!mcp.json\n!.gitignore\n"

// write replaces path with settings atomically (a temp file in the same
// directory, renamed over it). A .kiln directory it creates gets a
// .gitignore (kilnGitignore) so kiln's machine-local files never show up
// in git status, while mcp.json stays committable. Refuses to write
// through a symlinked file or a symlinked .kiln directory: planting one is
// a way to redirect kiln's write outside the location the user expects.
func write(path string, settings localSettings) error {
	return WriteJSON(path, settings)
}

// RefuseSymlink reports an error if path, or the directory that holds it,
// is itself a symlink. Called before every kiln write so a planted symlink
// cannot redirect the write outside the location the user expects.
func RefuseSymlink(path string) error {
	for _, p := range []string{path, filepath.Dir(path)} {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink; refusing to write through it", p)
		}
	}
	return nil
}

// WriteJSON atomically replaces path with v, marshaled as indented JSON
// with a trailing newline (a temp file in the same directory, renamed over
// it). A .kiln directory it creates gets kilnGitignore. Every kiln writer
// (settings, MCP config) shares this so file safety lives in one place;
// see RefuseSymlink for the write-time symlink check callers should run
// first.
func WriteJSON(path string, v any) error {
	if err := RefuseSymlink(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if filepath.Base(dir) == paths.KilnDir {
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(kilnGitignore), 0o644); err != nil {
				return err
			}
		}
	}
	data, err := json.MarshalIndent(v, "", "  ")
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
