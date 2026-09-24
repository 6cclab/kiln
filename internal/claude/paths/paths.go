// Package paths defines the `.claude` directory hierarchy: the roots,
// settings files and well-known file names every other claude subpackage
// reads from. Ported 1:1 from harness/src/claude/paths.ts.
package paths

import (
	"os"
	"path/filepath"
)

// Scope is a settings/commands/skills/memory source tier. Ordering is
// least-specific first throughout: a later tier shadows an earlier one.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeLocal   Scope = "local"
)

// ClaudeRoot is one `.claude` directory and the scope it belongs to.
type ClaudeRoot struct {
	Scope Scope
	// Dir is the absolute path to a `.claude` directory. It may not exist.
	Dir string
}

// homeDir returns the user's home directory, or "" if it cannot be
// determined. Deliberately silent: callers treat an empty home the same as
// any other root that doesn't exist.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// ClaudeRoots returns the `.claude` roots in precedence order, lowest
// first: the user's home directory, then the project's.
//
// "local" is the project's settings.local.json sibling space — gitignored,
// machine-specific. It shares the project directory rather than having one
// of its own, so only the settings file differs.
func ClaudeRoots(cwd string) []ClaudeRoot {
	return []ClaudeRoot{
		{Scope: ScopeUser, Dir: filepath.Join(homeDir(), ".claude")},
		{Scope: ScopeProject, Dir: filepath.Join(cwd, ".claude")},
	}
}

// CLAUDEMD is the memory file name.
const CLAUDEMD = "CLAUDE.md"

// SettingsFile is one settings.json path and the scope it belongs to.
type SettingsFile struct {
	Scope Scope
	Path  string
}

// SettingsFiles returns the three settings.json locations, in merge order
// (least- to most-specific).
func SettingsFiles(cwd string) []SettingsFile {
	return []SettingsFile{
		{Scope: ScopeUser, Path: filepath.Join(homeDir(), ".claude", "settings.json")},
		{Scope: ScopeProject, Path: filepath.Join(cwd, ".claude", "settings.json")},
		// Machine-local overrides, gitignored. Highest precedence.
		{Scope: ScopeLocal, Path: filepath.Join(cwd, ".claude", "settings.local.json")},
	}
}

// KeybindingsPath is ~/.claude/keybindings.json.
func KeybindingsPath() string {
	return filepath.Join(homeDir(), ".claude", "keybindings.json")
}

// ClaudeJSONPath is ~/.claude.json.
func ClaudeJSONPath() string {
	return filepath.Join(homeDir(), ".claude.json")
}
