// Package plugins loads Claude Code plugins: the marketplace-installed
// extensions recorded in ~/.claude/plugins/installed_plugins.json and
// switched on per settings.json's "enabledPlugins" map
// (internal/claude/settings.LoadEnabledPlugins).
//
// LoadPlugins resolves the active set for a working directory - a
// project-scope install only applies when the cwd is inside its recorded
// projectPath, and a plugin is active only once its "<plugin>@<market
// place>" key merges to true across settings scopes. Skills, Commands,
// Agents, Hooks and MCPServers then read one active Plugin's own
// .claude-plugin/plugin.json manifest and its skills/, commands/,
// agents/, hooks/ and .mcp.json, namespacing items as "<plugin>:<item>"
// and expanding ${CLAUDE_PLUGIN_ROOT} to the plugin's install directory,
// matching Claude Code's own plugin runtime.
//
// A broken manifest or unreadable directory is skipped with a
// diag.L().Warn, never fatal: one bad plugin must not take a session
// down.
package plugins
