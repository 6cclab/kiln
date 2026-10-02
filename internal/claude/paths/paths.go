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
	// ScopePlugin marks an item (today, a skill) contributed by an active
	// Claude Code plugin rather than loaded from a .claude root directly.
	// See internal/claude/plugins.
	ScopePlugin Scope = "plugin"
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

// KilnDir is kiln's own directory beside a .claude one: kiln reads Claude
// Code's .claude files but never writes them, so everything it saves goes
// under .kiln instead (~/.kiln for the user, <cwd>/.kiln for a project).
const KilnDir = ".kiln"

// KilnUserSettingsPath is ~/.kiln/settings.json, where kiln saves a user
// setting (the /model default). Same shape as Claude Code's settings.json.
func KilnUserSettingsPath() string {
	return filepath.Join(homeDir(), KilnDir, "settings.json")
}

// KilnLocalSettingsPath is <cwd>/.kiln/settings.local.json, where kiln saves
// project rules ("don't ask again", /permissions).
func KilnLocalSettingsPath(cwd string) string {
	return filepath.Join(cwd, KilnDir, "settings.local.json")
}

// KilnUserMemoryPath is ~/.kiln/CLAUDE.md, where a "#" note goes when the
// project has no CLAUDE.md; read after ~/.claude/CLAUDE.md.
func KilnUserMemoryPath() string {
	return filepath.Join(homeDir(), KilnDir, CLAUDEMD)
}

// KilnUserMCPPath is ~/.kiln/mcp.json, where kiln saves MCP servers added
// with `kiln mcp add -s user` (top-level "mcpServers") and `-s local`
// ("projects"[<abs project dir>].mcpServers), mirroring how Claude Code
// shapes ~/.claude.json. kiln never writes ~/.claude.json.
func KilnUserMCPPath() string {
	return filepath.Join(homeDir(), KilnDir, "mcp.json")
}

// KilnProjectMCPPath is <cwd>/.kiln/mcp.json, where kiln saves MCP servers
// added with `kiln mcp add -s project` ("mcpServers"). Meant to be
// committed, like Claude Code's .mcp.json, which kiln never writes.
func KilnProjectMCPPath(cwd string) string {
	return filepath.Join(cwd, KilnDir, "mcp.json")
}

// SettingsSource is one settings file kiln reads; Kiln marks kiln's own.
type SettingsSource struct {
	SettingsFile
	Kiln bool
}

// AllSettingsFiles is every settings file kiln reads, in merge order: each
// of kiln's files right after Claude Code's file of the same scope, so it
// adds to that scope and wins over it for a single value (model).
func AllSettingsFiles(cwd string) []SettingsSource {
	cc := SettingsFiles(cwd)
	return []SettingsSource{
		{SettingsFile: cc[0]},
		{SettingsFile: SettingsFile{Scope: ScopeUser, Path: KilnUserSettingsPath()}, Kiln: true},
		{SettingsFile: cc[1]},
		{SettingsFile: cc[2]},
		{SettingsFile: SettingsFile{Scope: ScopeLocal, Path: KilnLocalSettingsPath(cwd)}, Kiln: true},
	}
}
