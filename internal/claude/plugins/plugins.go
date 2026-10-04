package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	claudeagents "github.com/andrepato/harness/internal/claude/agents"
	claudecommands "github.com/andrepato/harness/internal/claude/commands"
	"github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/skills"
	"github.com/andrepato/harness/internal/diag"
	mcpcfg "github.com/andrepato/harness/internal/mcp"
)

// Plugin is one active Claude Code plugin: installed
// (~/.claude/plugins/installed_plugins.json) and enabled
// (settings.json's "enabledPlugins") for this working directory.
type Plugin struct {
	// Key is "<plugin>@<marketplace>", installed_plugins.json's own map
	// key and settings.json's enabledPlugins key.
	Key         string
	Name        string
	Marketplace string
	// Root is the plugin's install directory (installPath), the base
	// ${CLAUDE_PLUGIN_ROOT} expands to.
	Root    string
	Version string
	// Scope is "user" or "project", from the matching installed_plugins.json
	// entry.
	Scope string
}

type installEntry struct {
	Scope       string `json:"scope"`
	InstallPath string `json:"installPath"`
	Version     string `json:"version"`
	ProjectPath string `json:"projectPath"`
}

type installedFile struct {
	Version int                       `json:"version"`
	Plugins map[string][]installEntry `json:"plugins"`
}

// manifestRaw is .claude-plugin/plugin.json. MCPServers and Hooks are
// left raw because each may be either an inline object or a string path
// (relative to the plugin root) to a file holding the same shape.
type manifestRaw struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	MCPServers json.RawMessage `json:"mcpServers"`
	Hooks      json.RawMessage `json:"hooks"`
}

func installedPluginsPath(cwd string) string {
	// index 0 is always the user root (~/.claude), regardless of cwd.
	return filepath.Join(paths.ClaudeRoots(cwd)[0].Dir, "plugins", "installed_plugins.json")
}

// splitKey splits "<plugin>@<marketplace>" on the first '@'.
func splitKey(key string) (name, marketplace string, ok bool) {
	i := strings.IndexByte(key, '@')
	if i <= 0 || i == len(key)-1 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// withinProject reports whether cwd is projectPath or a descendant of it.
func withinProject(cwd, projectPath string) bool {
	if cwd == "" || projectPath == "" {
		return false
	}
	cwd = filepath.Clean(cwd)
	projectPath = filepath.Clean(projectPath)
	if cwd == projectPath {
		return true
	}
	return strings.HasPrefix(cwd, projectPath+string(filepath.Separator))
}

// readManifestFile reads just a plugin's own .claude-plugin/plugin.json,
// with no marketplace fallback. A missing file is not logged here — it is
// the expected, common case for a marketplace-manifest plugin (see
// pluginManifest, which is what call sites actually use); a file that
// exists but won't parse is.
func readManifestFile(root string) (manifestRaw, bool) {
	path := filepath.Join(root, ".claude-plugin", "plugin.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return manifestRaw{}, false
	}
	var mf manifestRaw
	if err := json.Unmarshal(data, &mf); err != nil {
		diag.L().Warn("plugins: broken manifest", "path", path, "error", err)
		return manifestRaw{}, false
	}
	return mf, true
}

// marketplaceEntry is one plugin's entry in a marketplace's own
// .claude-plugin/marketplace.json "plugins" array — only the fields kiln
// can act on. Claude Code's schema has several more (skills, commands,
// agents, lspServers, …): skills/commands/agents are read straight from
// the plugin's own directory regardless of which manifest source is in
// play, and kiln has no LSP runtime to read lspServers into at all.
type marketplaceEntry struct {
	Name       string          `json:"name"`
	Version    string          `json:"version"`
	MCPServers json.RawMessage `json:"mcpServers"`
	Hooks      json.RawMessage `json:"hooks"`
}

type marketplaceFile struct {
	Plugins []marketplaceEntry `json:"plugins"`
}

// marketplaceManifest reads name's entry from marketplace's own
// .claude-plugin/marketplace.json: Claude Code's fallback for a plugin
// with no .claude-plugin/plugin.json of its own (pluginLoader.ts's
// finishLoadingPluginFromPath: "If there's no plugin.json, use
// marketplace entry as manifest (regardless of strict mode)" — "strict"
// instead governs a *conflict* between the two when both exist, not
// whether this fallback applies at all, so kiln does not gate on it
// either).
func marketplaceManifest(name, marketplace string) (manifestRaw, bool) {
	if name == "" || marketplace == "" {
		return manifestRaw{}, false
	}
	data, err := os.ReadFile(filepath.Join(marketplaceInstallLocation(marketplace), ".claude-plugin", "marketplace.json"))
	if err != nil {
		return manifestRaw{}, false
	}
	var mf marketplaceFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return manifestRaw{}, false
	}
	for _, e := range mf.Plugins {
		if e.Name == name {
			return manifestRaw(e), true
		}
	}
	return manifestRaw{}, false
}

// knownMarketplace is one entry of ~/.claude/plugins/known_marketplaces.json:
// only InstallLocation matters here (where the marketplace's own files,
// including marketplace.json, were cloned/cached to).
type knownMarketplace struct {
	InstallLocation string `json:"installLocation"`
}

// marketplaceInstallLocation resolves where marketplace's files live:
// known_marketplaces.json's recorded installLocation when present (it can
// differ from the default, e.g. a relocated or legacy cache), else the
// default cache path every marketplace kiln has seen so far actually
// uses, <plugins dir>/marketplaces/<name>.
func marketplaceInstallLocation(marketplace string) string {
	pluginsDir := filepath.Join(paths.ClaudeRoots("")[0].Dir, "plugins")
	if data, err := os.ReadFile(filepath.Join(pluginsDir, "known_marketplaces.json")); err == nil {
		var known map[string]knownMarketplace
		if json.Unmarshal(data, &known) == nil {
			if km, ok := known[marketplace]; ok && km.InstallLocation != "" {
				return km.InstallLocation
			}
		}
	}
	return filepath.Join(pluginsDir, "marketplaces", marketplace)
}

// pluginManifest resolves name's manifest: its own .claude-plugin/
// plugin.json if it has one, else name's entry in marketplace's own
// .claude-plugin/marketplace.json (marketplaceManifest). Only warns when
// neither source has it — a marketplace-manifest plugin (e.g.
// gopls-lsp@claude-plugins-official, "strict": false, lspServers only,
// nothing under its own install dir but LICENSE/README.md) is the
// expected shape for "no plugin.json of its own", not a problem to warn
// about (qa/findings/20261004T203042Z-marketplace-manifest-plugin-
// warns.json: kiln logged "plugins: no manifest" and dropped it).
func pluginManifest(root, name, marketplace string) (manifestRaw, bool) {
	if mf, ok := readManifestFile(root); ok {
		return mf, true
	}
	if mf, ok := marketplaceManifest(name, marketplace); ok {
		return mf, true
	}
	diag.L().Warn("plugins: no manifest", "path", filepath.Join(root, ".claude-plugin", "plugin.json"), "marketplace", marketplace)
	return manifestRaw{}, false
}

// LoadPlugins returns every plugin that is both installed and enabled for
// cwd: installed_plugins.json lists it, its "<plugin>@<marketplace>" key
// merges to true across settings.json scopes
// (settings.LoadEnabledPlugins), and — for a project-scope install — cwd
// is inside its recorded projectPath. A broken manifest is skipped (with
// a diag warning); a missing installed_plugins.json means no plugins are
// installed, which is the normal case, not an error.
func LoadPlugins(cwd string) []Plugin {
	return LoadPluginsFrom(cwd, nil)
}

// LoadPluginsFrom is LoadPlugins with enabledPlugins read only from the
// settings scopes in sources (nil: every scope), as --setting-sources
// restricts which settings files are read.
func LoadPluginsFrom(cwd string, sources []paths.Scope) []Plugin {
	seen := map[string]bool{}
	var out []Plugin
	for _, ins := range listInstalled(cwd, sources) {
		if !ins.Enabled || !ins.Applicable || seen[ins.Key] {
			continue
		}
		// An unreadable manifest already fell back to the key-derived
		// name/version in ListInstalled (for a full report to still show
		// something); a plugin that active session state is built from
		// needs a manifest that actually parsed, since Skills/Commands/
		// Agents/Hooks/MCPServers all read it again for mcpServers/hooks.
		if _, ok := pluginManifest(ins.Root, ins.Name, ins.Marketplace); !ok {
			continue
		}
		seen[ins.Key] = true
		out = append(out, Plugin{
			Key:         ins.Key,
			Name:        ins.Name,
			Marketplace: ins.Marketplace,
			Root:        ins.Root,
			Version:     ins.Version,
			Scope:       ins.Scope,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Installed is one installed_plugins.json entry, active or not — the
// basis for /plugin's full report (internal/commands' plugin_commands.go)
// and for LoadPlugins, which is ListInstalled filtered to
// Enabled && Applicable.
type Installed struct {
	Key         string
	Name        string
	Marketplace string
	Root        string
	Version     string
	Scope       string
	// Enabled is settings.json's merged enabledPlugins[Key].
	Enabled bool
	// Applicable is false for a project-scope install whose recorded
	// projectPath does not cover cwd: invisible here regardless of
	// Enabled, the same way LoadPlugins would skip it.
	Applicable bool
}

// ListInstalled returns every installed_plugins.json entry, active or
// not, for cwd. A broken manifest yields a Name/Version falling back to
// the key and the install record's own version (with a diag warning); a
// missing installed_plugins.json means no plugins are installed, the
// normal case, not an error.
func ListInstalled(cwd string) []Installed {
	return listInstalled(cwd, nil)
}

func listInstalled(cwd string, sources []paths.Scope) []Installed {
	data, err := os.ReadFile(installedPluginsPath(cwd))
	if err != nil {
		return nil
	}
	var installed installedFile
	if err := json.Unmarshal(data, &installed); err != nil {
		diag.L().Warn("plugins: broken installed_plugins.json", "error", err)
		return nil
	}

	enabled := settings.LoadEnabledPluginsFrom(cwd, sources)

	var out []Installed
	for key, entries := range installed.Plugins {
		name, marketplace, ok := splitKey(key)
		if !ok {
			continue
		}
		for _, e := range entries {
			applicable := true
			if e.Scope == "project" && !withinProject(cwd, e.ProjectPath) {
				applicable = false
			}
			pluginName, version := name, e.Version
			if mf, ok := pluginManifest(e.InstallPath, name, marketplace); ok {
				if mf.Name != "" {
					pluginName = mf.Name
				}
				if mf.Version != "" {
					version = mf.Version
				}
			}
			out = append(out, Installed{
				Key:         key,
				Name:        pluginName,
				Marketplace: marketplace,
				Root:        e.InstallPath,
				Version:     version,
				Scope:       e.Scope,
				Enabled:     enabled[key],
				Applicable:  applicable,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

// expandRoot replaces ${CLAUDE_PLUGIN_ROOT} with the plugin's install
// directory, matching Claude Code's own expansion in commands/args/env/
// hook commands.
func expandRoot(s, root string) string {
	return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", root)
}

// Skills returns a plugin's skills/<name>/SKILL.md definitions, namespaced
// "<plugin>:<name>" and scoped paths.ScopePlugin.
func Skills(p Plugin) []skills.Skill {
	dir := filepath.Join(p.Root, "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []skills.Skill
	for _, entry := range entries {
		if !skills.IsDirEntry(dir, entry) {
			continue
		}
		skillPath := filepath.Join(dir, entry.Name(), "SKILL.md")
		data, err := os.ReadFile(skillPath)
		if err != nil {
			continue
		}
		sk, ok := skills.ParseSkill(string(data), skillPath, paths.ScopePlugin)
		if !ok {
			diag.L().Warn("plugins: broken skill", "path", skillPath)
			continue
		}
		sk.Name = p.Name + ":" + sk.Name
		out = append(out, sk)
	}
	return out
}

// Commands returns a plugin's commands/**/*.md definitions. The top-level
// namespace is the plugin's name; a command nested under a subdirectory
// keeps that subdirectory as part of its own name, so
// commands/git/changelog.md becomes "<plugin>:git:changelog".
func Commands(p Plugin) []claudecommands.CommandFile {
	dir := filepath.Join(p.Root, "commands")
	files := claudecommands.WalkMarkdown(dir)

	var out []claudecommands.CommandFile
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			rel = filepath.Base(file)
		}
		rel = strings.TrimSuffix(rel, ".md")
		segments := strings.Split(rel, string(filepath.Separator))
		name := segments[len(segments)-1]
		if len(segments) > 1 {
			name = strings.Join(segments[:len(segments)-1], ":") + ":" + name
		}
		out = append(out, claudecommands.ParseCommandFile(string(data), name, p.Name, claudecommands.Plugin, file))
	}
	return out
}

// Agents returns a plugin's agents/*.md definitions, namespaced
// "<plugin>:<name>".
func Agents(p Plugin) []claudeagents.Definition {
	dir := filepath.Join(p.Root, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []claudeagents.Definition
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		def, ok := claudeagents.ParseAgent(string(data), path, claudeagents.Plugin)
		if !ok {
			diag.L().Warn("plugins: broken agent", "path", path)
			continue
		}
		def.Name = p.Name + ":" + def.Name
		out = append(out, def)
	}
	return out
}

// hooksShape is hooks/hooks.json's (and a plugin.json "hooks" file's)
// wrapped shape.
type hooksShape struct {
	Hooks map[string][]hooks.Matcher `json:"hooks"`
}

// parseHooksBlob accepts either the wrapped {"hooks": {...}} shape
// (hooks/hooks.json) or the bare {"<Event>": [...]} shape (plugin.json's
// inline "hooks" field, already nested under its own "hooks" key so it is
// not wrapped again). Whichever parses to a non-empty map wins; malformed
// input yields nil.
func parseHooksBlob(data []byte) map[string][]hooks.Matcher {
	var wrapped hooksShape
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.Hooks) > 0 {
		return wrapped.Hooks
	}
	var bare map[string][]hooks.Matcher
	if err := json.Unmarshal(data, &bare); err == nil && len(bare) > 0 {
		return bare
	}
	return nil
}

// Hooks returns a plugin's hook commands: hooks/hooks.json plus
// plugin.json's own "hooks" field (inline object or a string path,
// relative to the plugin root, to a file in the same shape). Every
// command's ${CLAUDE_PLUGIN_ROOT} is expanded and its Env carries
// CLAUDE_PLUGIN_ROOT, matching Claude Code's plugin hook runtime.
func Hooks(p Plugin) hooks.Config {
	merged := map[string][]hooks.Matcher{}
	add := func(m map[string][]hooks.Matcher) {
		for event, groups := range m {
			merged[event] = append(merged[event], groups...)
		}
	}

	if data, err := os.ReadFile(filepath.Join(p.Root, "hooks", "hooks.json")); err == nil {
		add(parseHooksBlob(data))
	}

	if mf, ok := pluginManifest(p.Root, p.Name, p.Marketplace); ok && len(mf.Hooks) > 0 {
		var asString string
		if err := json.Unmarshal(mf.Hooks, &asString); err == nil && asString != "" {
			path := asString
			if !filepath.IsAbs(path) {
				path = filepath.Join(p.Root, path)
			}
			if data, err := os.ReadFile(path); err == nil {
				add(parseHooksBlob(data))
			} else {
				diag.L().Warn("plugins: hooks path unreadable", "plugin", p.Key, "path", path, "error", err)
			}
		} else {
			add(parseHooksBlob(mf.Hooks))
		}
	}

	out := hooks.Config{}
	for event, groups := range merged {
		expanded := make([]hooks.Matcher, len(groups))
		for i, g := range groups {
			cmds := make([]hooks.Command, len(g.Hooks))
			for j, c := range g.Hooks {
				c.Command = expandRoot(c.Command, p.Root)
				if c.Env == nil {
					c.Env = map[string]string{}
				}
				c.Env["CLAUDE_PLUGIN_ROOT"] = p.Root
				cmds[j] = c
			}
			expanded[i] = hooks.Matcher{MatcherPattern: g.MatcherPattern, Hooks: cmds}
		}
		out[hooks.Event(event)] = expanded
	}
	return out
}

// MCPServers returns a plugin's MCP servers: .mcp.json plus plugin.json's
// own "mcpServers" field (inline object or a string path, relative to the
// plugin root, to a file holding {"mcpServers": {...}}), overlaid in that
// order so plugin.json wins a name clash. Each server is named
// "plugin_<plugin>_<server>" (so its tools read
// mcp__plugin_<plugin>_<server>__<tool>, matching Claude Code), has
// ${CLAUDE_PLUGIN_ROOT} expanded, and is otherwise expanded exactly as
// Resolve expands every other server (mcp.ExpandConfig).
func MCPServers(p Plugin) map[string]mcpcfg.ServerConfig {
	merged := map[string]mcpcfg.ServerConfig{}
	add := func(servers map[string]mcpcfg.ServerConfig) {
		for name, cfg := range servers {
			merged[name] = cfg
		}
	}

	if data, err := os.ReadFile(filepath.Join(p.Root, ".mcp.json")); err == nil {
		var doc struct {
			MCPServers map[string]mcpcfg.ServerConfig `json:"mcpServers"`
		}
		if json.Unmarshal(data, &doc) == nil {
			add(doc.MCPServers)
		}
	}

	if mf, ok := pluginManifest(p.Root, p.Name, p.Marketplace); ok && len(mf.MCPServers) > 0 {
		var asString string
		if err := json.Unmarshal(mf.MCPServers, &asString); err == nil && asString != "" {
			path := asString
			if !filepath.IsAbs(path) {
				path = filepath.Join(p.Root, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				diag.L().Warn("plugins: mcpServers path unreadable", "plugin", p.Key, "path", path, "error", err)
			} else {
				var doc struct {
					MCPServers map[string]mcpcfg.ServerConfig `json:"mcpServers"`
				}
				if json.Unmarshal(data, &doc) == nil {
					add(doc.MCPServers)
				} else {
					diag.L().Warn("plugins: broken mcpServers file", "plugin", p.Key, "path", path)
				}
			}
		} else {
			var inline map[string]mcpcfg.ServerConfig
			if json.Unmarshal(mf.MCPServers, &inline) == nil {
				add(inline)
			} else {
				diag.L().Warn("plugins: broken inline mcpServers", "plugin", p.Key)
			}
		}
	}

	out := make(map[string]mcpcfg.ServerConfig, len(merged))
	for name, cfg := range merged {
		cfg.Command = expandRoot(cfg.Command, p.Root)
		cfg.URL = expandRoot(cfg.URL, p.Root)
		for i, a := range cfg.Args {
			cfg.Args[i] = expandRoot(a, p.Root)
		}
		if cfg.Env != nil {
			expanded := make(map[string]string, len(cfg.Env))
			for k, v := range cfg.Env {
				expanded[k] = expandRoot(v, p.Root)
			}
			cfg.Env = expanded
		}
		cfg.Scope = mcpcfg.ScopePlugin
		qualified := fmt.Sprintf("plugin_%s_%s", p.Name, name)
		out[qualified] = mcpcfg.ExpandConfig(cfg)
	}
	return out
}
