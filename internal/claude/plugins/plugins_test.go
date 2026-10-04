package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/paths"
)

// --setting-sources decides whose enabledPlugins count, as in Claude
// Code: a plugin only the project's settings enable stays off with
// `--setting-sources user`, and the user's own enabling still counts.
func TestLoadPluginsFrom_HonoursSettingSources(t *testing.T) {
	home, root := newHomeWithInstall(t, "user", "")
	writeManifest(t, root, "demo", "1.0.0", nil)
	proj := filepath.Join(home, "proj")
	writeJSON(t, filepath.Join(proj, ".claude", "settings.json"), map[string]any{
		"enabledPlugins": map[string]bool{"demo@market": true},
	})
	user := []paths.Scope{paths.ScopeUser}
	if got := LoadPluginsFrom(proj, user); len(got) != 0 {
		t.Errorf("--setting-sources user, project-enabled plugin: %+v", got)
	}
	if got := LoadPluginsFrom(proj, nil); len(got) != 1 {
		t.Errorf("every source: %+v, want the plugin", got)
	}
	enableInHome(t, home, "demo@market", true)
	if got := LoadPluginsFrom(proj, user); len(got) != 1 {
		t.Errorf("--setting-sources user, user-enabled plugin: %+v", got)
	}
}

// writeFile writes content to path, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
}

// installEntryFixture builds one installed_plugins.json entry.
func installEntryFixture(scope, installPath, version, projectPath string) map[string]any {
	m := map[string]any{
		"scope":       scope,
		"installPath": installPath,
		"version":     version,
	}
	if projectPath != "" {
		m["projectPath"] = projectPath
	}
	return m
}

func enableInHome(t *testing.T, home, key string, enabled bool) {
	t.Helper()
	writeJSON(t, filepath.Join(home, ".claude", "settings.json"), map[string]any{
		"enabledPlugins": map[string]bool{key: enabled},
	})
}

func writeManifest(t *testing.T, root, name, version string, extra map[string]any) {
	t.Helper()
	m := map[string]any{"name": name, "version": version}
	for k, v := range extra {
		m[k] = v
	}
	writeJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), m)
}

func newHomeWithInstall(t *testing.T, scope, projectPath string) (home, root string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	root = filepath.Join(home, "plugins", "repos", "demo")
	writeJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"demo@market": []map[string]any{installEntryFixture(scope, root, "1.0.0", projectPath)},
		},
	})
	return home, root
}

func TestLoadPlugins_EnabledUserScope(t *testing.T) {
	home, root := newHomeWithInstall(t, "user", "")
	writeManifest(t, root, "demo", "1.0.0", nil)
	enableInHome(t, home, "demo@market", true)

	got := LoadPlugins(filepath.Join(home, "proj"))
	if len(got) != 1 {
		t.Fatalf("LoadPlugins = %+v, want 1 plugin", got)
	}
	p := got[0]
	if p.Key != "demo@market" || p.Name != "demo" || p.Marketplace != "market" || p.Root != root || p.Version != "1.0.0" || p.Scope != "user" {
		t.Errorf("got %+v", p)
	}
}

func TestLoadPlugins_DisabledIsExcluded(t *testing.T) {
	home, root := newHomeWithInstall(t, "user", "")
	writeManifest(t, root, "demo", "1.0.0", nil)
	// No enabledPlugins entry at all: absent means inactive, not active.
	if got := LoadPlugins(filepath.Join(home, "proj")); len(got) != 0 {
		t.Fatalf("LoadPlugins = %+v, want none (absent key => disabled)", got)
	}

	enableInHome(t, home, "demo@market", false)
	if got := LoadPlugins(filepath.Join(home, "proj")); len(got) != 0 {
		t.Fatalf("LoadPlugins = %+v, want none (explicitly disabled)", got)
	}
}

func TestLoadPlugins_ProjectScopeOnlyAppliesInsideProjectPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "plugins", "repos", "demo")
	writeManifest(t, root, "demo", "1.0.0", nil)

	proj := filepath.Join(home, "proj")
	other := filepath.Join(home, "other")
	writeJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"demo@market": []map[string]any{installEntryFixture("project", root, "1.0.0", proj)},
		},
	})
	enableInHome(t, home, "demo@market", true)

	if got := LoadPlugins(other); len(got) != 0 {
		t.Fatalf("LoadPlugins(other) = %+v, want none: project scope must not leak outside its projectPath", got)
	}
	if got := LoadPlugins(proj); len(got) != 1 {
		t.Fatalf("LoadPlugins(proj) = %+v, want 1", got)
	}
	// A subdirectory of the project also counts.
	sub := filepath.Join(proj, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := LoadPlugins(sub); len(got) != 1 {
		t.Fatalf("LoadPlugins(sub) = %+v, want 1: cwd inside projectPath still applies", got)
	}
}

func TestLoadPlugins_BrokenManifestSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "plugins", "repos", "demo")
	writeFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), "{ not json")

	writeJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"demo@market": []map[string]any{installEntryFixture("user", root, "1.0.0", "")},
		},
	})
	enableInHome(t, home, "demo@market", true)

	if got := LoadPlugins(filepath.Join(home, "proj")); len(got) != 0 {
		t.Fatalf("LoadPlugins = %+v, want none: broken manifest must not crash or count as active", got)
	}
}

// writeMarketplaceFile writes a marketplace's own .claude-plugin/
// marketplace.json under home/.claude/plugins/marketplaces/<marketplace>,
// matching the real ~/.claude/plugins layout (verified against an actual
// installed marketplace's cache directory).
func writeMarketplaceFile(t *testing.T, home, marketplace string, plugins []map[string]any) {
	t.Helper()
	writeJSON(t, filepath.Join(home, ".claude", "plugins", "marketplaces", marketplace, ".claude-plugin", "marketplace.json"), map[string]any{
		"name":    marketplace,
		"plugins": plugins,
	})
}

// TestLoadPlugins_MarketplaceManifestFallback_NoOwnPluginJSON checks a
// plugin whose install directory has no .claude-plugin/plugin.json of its
// own (only LICENSE/README.md, e.g. gopls-lsp@claude-plugins-official)
// still loads and resolves its MCP servers from its marketplace's own
// .claude-plugin/marketplace.json entry — Claude Code's own fallback for
// such a plugin (pluginLoader.ts's finishLoadingPluginFromPath uses the
// marketplace entry as the manifest whenever the plugin itself has none,
// "strict" or not). Before this, readManifest only ever looked at the
// plugin's own directory, so this shape warned "plugins: no manifest" and
// was silently dropped (qa/findings/20261004T203042Z-marketplace-
// manifest-plugin-warns.json).
func TestLoadPlugins_MarketplaceManifestFallback_NoOwnPluginJSON(t *testing.T) {
	home, cwd, root := setupPluginTree(t)
	// No .claude-plugin/plugin.json under root at all — just LICENSE, as
	// the real gopls-lsp install dir has.
	writeFile(t, filepath.Join(root, "LICENSE"), "MIT")
	writeMarketplaceFile(t, home, "market", []map[string]any{
		{
			"name":    "demo",
			"version": "1.0.0",
			"strict":  false,
			"mcpServers": map[string]any{
				"tools": map[string]any{"command": "${CLAUDE_PLUGIN_ROOT}/bin/server"},
			},
		},
	})

	plugins := LoadPlugins(cwd)
	if len(plugins) != 1 {
		t.Fatalf("LoadPlugins = %+v, want one plugin (marketplace manifest fallback)", plugins)
	}
	servers := MCPServers(plugins[0])
	cfg, ok := servers["plugin_demo_tools"]
	if !ok {
		t.Fatalf("MCPServers = %+v, want key plugin_demo_tools from the marketplace entry", servers)
	}
	if cfg.Command != root+"/bin/server" {
		t.Errorf("Command = %q, want %s/bin/server", cfg.Command, root)
	}
}

// TestLoadPlugins_NoManifestAnywhereIsSkipped checks a plugin with
// neither its own plugin.json nor a matching marketplace entry is still
// skipped (not a crash, not silently "active" with nothing to act on).
func TestLoadPlugins_NoManifestAnywhereIsSkipped(t *testing.T) {
	_, cwd, root := setupPluginTree(t)
	writeFile(t, filepath.Join(root, "LICENSE"), "MIT")
	// No marketplace.json at all for "market".

	if got := LoadPlugins(cwd); len(got) != 0 {
		t.Fatalf("LoadPlugins = %+v, want none: no manifest anywhere", got)
	}
}

func TestLoadPlugins_BrokenInstalledPluginsJSONIsNotFatal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), "not json at all")

	got := LoadPlugins(filepath.Join(home, "proj")) // must not panic
	if len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestLoadPlugins_MissingInstalledPluginsJSONIsNormal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := LoadPlugins(filepath.Join(home, "proj")); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

func TestListInstalled_ReportsDisabledAndInapplicableToo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "plugins", "repos", "demo")
	writeManifest(t, root, "demo", "1.0.0", nil)
	proj := filepath.Join(home, "proj")
	other := filepath.Join(home, "other")
	writeJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"demo@market": []map[string]any{installEntryFixture("project", root, "1.0.0", proj)},
		},
	})
	// Not enabled.

	got := ListInstalled(other)
	if len(got) != 1 {
		t.Fatalf("ListInstalled = %+v, want 1 entry (even though disabled/inapplicable)", got)
	}
	if got[0].Enabled {
		t.Error("expected Enabled false (no enabledPlugins entry)")
	}
	if got[0].Applicable {
		t.Error("expected Applicable false (cwd outside projectPath)")
	}
}

func setupPluginTree(t *testing.T) (home, cwd, root string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	cwd = filepath.Join(home, "proj")
	root = filepath.Join(home, "plugins", "repos", "demo")
	writeJSON(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"demo@market": []map[string]any{installEntryFixture("user", root, "1.0.0", "")},
		},
	})
	enableInHome(t, home, "demo@market", true)
	return home, cwd, root
}

func TestSkills_NamespacedAndDisableModelInvocationHonored(t *testing.T) {
	_, cwd, root := setupPluginTree(t)
	writeManifest(t, root, "demo", "1.0.0", nil)
	writeFile(t, filepath.Join(root, "skills", "greet", "SKILL.md"), "---\nname: greet\ndescription: says hi\n---\nHello.")
	writeFile(t, filepath.Join(root, "skills", "silent", "SKILL.md"), "---\nname: silent\ndescription: not model-invocable\ndisable-model-invocation: true\n---\nBody.")

	plugins := LoadPlugins(cwd)
	if len(plugins) != 1 {
		t.Fatalf("LoadPlugins = %+v", plugins)
	}
	skills := Skills(plugins[0])
	byName := map[string]bool{}
	for _, s := range skills {
		byName[s.Name] = s.DisableModelInvocation
	}
	if disabled, ok := byName["demo:greet"]; !ok || disabled {
		t.Errorf("expected demo:greet present and model-invocable, got %+v", byName)
	}
	if disabled, ok := byName["demo:silent"]; !ok || !disabled {
		t.Errorf("expected demo:silent present and disabled for model invocation, got %+v", byName)
	}
}

// A plugin skill directory that is a symlink is loaded like a real one.
func TestSkills_FollowsSymlinkedSkillDir(t *testing.T) {
	_, cwd, root := setupPluginTree(t)
	writeManifest(t, root, "demo", "1.0.0", nil)
	target := filepath.Join(t.TempDir(), "greet")
	writeFile(t, filepath.Join(target, "SKILL.md"), "---\nname: greet\ndescription: says hi\n---\nHello.")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "skills", "greet")); err != nil {
		t.Fatal(err)
	}

	plugins := LoadPlugins(cwd)
	if len(plugins) != 1 {
		t.Fatalf("LoadPlugins = %+v", plugins)
	}
	got := Skills(plugins[0])
	if len(got) != 1 || got[0].Name != "demo:greet" {
		t.Fatalf("Skills = %+v, want the symlinked demo:greet", got)
	}
}

// A plugin commands subfolder that is a symlink is followed, keeping the
// folder in the command's name.
func TestCommands_FollowsSymlinkedFolder(t *testing.T) {
	_, cwd, root := setupPluginTree(t)
	writeManifest(t, root, "demo", "1.0.0", nil)
	target := filepath.Join(t.TempDir(), "git")
	writeFile(t, filepath.Join(target, "changelog.md"), "---\ndescription: Changelog\n---\nWrite one.")
	if err := os.MkdirAll(filepath.Join(root, "commands"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "commands", "git")); err != nil {
		t.Fatal(err)
	}

	plugins := LoadPlugins(cwd)
	if len(plugins) != 1 {
		t.Fatalf("LoadPlugins = %+v", plugins)
	}
	cmds := Commands(plugins[0])
	if len(cmds) != 1 {
		t.Fatalf("Commands = %+v, want the symlinked git/changelog", cmds)
	}
}

func TestCommands_Namespaced(t *testing.T) {
	_, cwd, root := setupPluginTree(t)
	writeManifest(t, root, "demo", "1.0.0", nil)
	writeFile(t, filepath.Join(root, "commands", "review.md"), "---\ndescription: Review code\n---\nReview $ARGUMENTS.")
	writeFile(t, filepath.Join(root, "commands", "git", "changelog.md"), "---\ndescription: Changelog\n---\nWrite one.")

	plugins := LoadPlugins(cwd)
	cmds := Commands(plugins[0])
	names := map[string]bool{}
	for _, c := range cmds {
		names[c.Namespace+":"+c.Name] = true
	}
	if !names["demo:review"] {
		t.Errorf("expected demo:review, got %+v", names)
	}
	if !names["demo:git:changelog"] {
		t.Errorf("expected demo:git:changelog, got %+v", names)
	}
}

func TestAgents_Namespaced(t *testing.T) {
	_, cwd, root := setupPluginTree(t)
	writeManifest(t, root, "demo", "1.0.0", nil)
	writeFile(t, filepath.Join(root, "agents", "reviewer.md"), "---\nname: reviewer\ndescription: reviews things\n---\nYou review.")

	plugins := LoadPlugins(cwd)
	agents := Agents(plugins[0])
	if len(agents) != 1 || agents[0].Name != "demo:reviewer" {
		t.Fatalf("got %+v", agents)
	}
}

func TestHooks_ExpandsPluginRootAndSetsEnv(t *testing.T) {
	_, cwd, root := setupPluginTree(t)
	writeManifest(t, root, "demo", "1.0.0", nil)
	writeJSON(t, filepath.Join(root, "hooks", "hooks.json"), map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "Bash",
					"hooks": []map[string]any{
						{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/check.sh"},
					},
				},
			},
		},
	})

	plugins := LoadPlugins(cwd)
	cfg := Hooks(plugins[0])
	groups := cfg[hooks.PreToolUse]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("got %+v", cfg)
	}
	cmd := groups[0].Hooks[0]
	want := root + "/check.sh"
	if cmd.Command != want {
		t.Errorf("Command = %q, want %q", cmd.Command, want)
	}
	if cmd.Env["CLAUDE_PLUGIN_ROOT"] != root {
		t.Errorf("Env[CLAUDE_PLUGIN_ROOT] = %q, want %q", cmd.Env["CLAUDE_PLUGIN_ROOT"], root)
	}
}

func TestMCPServers_NamespacedAndRootExpanded(t *testing.T) {
	_, cwd, root := setupPluginTree(t)
	writeManifest(t, root, "demo", "1.0.0", map[string]any{
		"mcpServers": map[string]any{
			"tools": map[string]any{
				"command": "${CLAUDE_PLUGIN_ROOT}/bin/server",
				"args":    []string{"--root", "${CLAUDE_PLUGIN_ROOT}"},
			},
		},
	})

	plugins := LoadPlugins(cwd)
	servers := MCPServers(plugins[0])
	cfg, ok := servers["plugin_demo_tools"]
	if !ok {
		t.Fatalf("got %+v, want key plugin_demo_tools", servers)
	}
	if cfg.Command != root+"/bin/server" {
		t.Errorf("Command = %q", cfg.Command)
	}
	if len(cfg.Args) != 2 || cfg.Args[1] != root {
		t.Errorf("Args = %+v", cfg.Args)
	}
}

func TestSplitKey(t *testing.T) {
	tests := []struct {
		key               string
		name, marketplace string
		ok                bool
	}{
		{"demo@market", "demo", "market", true},
		{"no-at-sign", "", "", false},
		{"@leading", "", "", false},
		{"trailing@", "", "", false},
	}
	for _, tc := range tests {
		name, marketplace, ok := splitKey(tc.key)
		if name != tc.name || marketplace != tc.marketplace || ok != tc.ok {
			t.Errorf("splitKey(%q) = %q, %q, %v; want %q, %q, %v", tc.key, name, marketplace, ok, tc.name, tc.marketplace, tc.ok)
		}
	}
}
