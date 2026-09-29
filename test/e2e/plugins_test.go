//go:build e2e

// Package e2e: Claude Code plugin loading, driven through the real
// binary. Builds a synthetic plugin under a scratch HOME (installed_plugins
// .json + settings.json's enabledPlugins, a skill, a command and an MCP
// server built from cmd/mcpfixture) and verifies three things kiln now
// reads alongside Claude Code's own .claude/commands, agents, hooks and
// mcpServers: the `/` palette shows the plugin's namespaced command, the
// `skill` tool's catalog and invocation both work for a plugin skill, and
// the plugin's MCP server connects and is offered to the model under its
// namespaced tool name.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeJSONFile marshals v and writes it to path, creating parent
// directories as needed. A small local helper: this file is the only one
// in the package building a synthetic plugin tree, so it owns its own
// fixture-writing helpers rather than adding to the shared ones.
func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTextFile(t, path, string(data))
}

func writeTextFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pluginSkillCallScript scripts a turn that calls the `skill` tool for the
// plugin's namespaced skill, then answers once it sees the result — the
// result itself is inspected from the faux server's recorded follow-up
// request, not from this script (faux scripts are not content-aware).
const pluginSkillCallScript = `model: faux-1
steps:
  - tool_call: {name: skill, args: {skill: "demo:greet"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`

// writePluginFixture builds a synthetic plugin ("demo@market") under
// home/plugins/repos/demo: a manifest naming an MCP server built from
// fixtureBin (cmd/mcpfixture), one command (commands/hello.md) and one
// skill (skills/greet/SKILL.md) with a distinctive body so its return
// value can't be confused with anything else. It is installed at user
// scope (applies regardless of cwd) and enabled in
// ~/.claude/settings.json.
func writePluginFixture(t *testing.T, home, fixtureBin string) (root string) {
	t.Helper()
	root = filepath.Join(home, "plugins", "repos", "demo")

	writeJSONFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), map[string]any{
		"name":    "demo",
		"version": "1.0.0",
		"mcpServers": map[string]any{
			"fixture": map[string]any{"command": fixtureBin},
		},
	})
	writeTextFile(t, filepath.Join(root, "commands", "hello.md"),
		"---\ndescription: Say hello from the plugin\n---\nHello from the plugin command.")
	writeTextFile(t, filepath.Join(root, "skills", "greet", "SKILL.md"),
		"---\nname: greet\ndescription: greets warmly, plugin-style\n---\nHello from the plugin skill, distinctive-marker-38f1.")

	writeJSONFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"demo@market": []map[string]any{
				{"scope": "user", "installPath": root, "version": "1.0.0"},
			},
		},
	})
	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"), map[string]any{
		"enabledPlugins": map[string]bool{"demo@market": true},
	})
	return root
}

func TestPlugins_PaletteShowsNamespacedCommand(t *testing.T) {
	fixtureBin := mcpBuildFixture(t)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writePluginFixture(t, home, fixtureBin)

	// A faux server that is never called: this test only exercises the
	// slash palette, which is resolved locally and never reaches the
	// model.
	addr, _ := startFaux(t, unreadScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)

	s.Send("/demo")
	if err := s.WaitFor("/demo:hello", 2*time.Second); err != nil {
		t.Fatalf("palette did not show /demo:hello for /demo: %v", err)
	}
}

func TestPlugins_SkillToolListsAndInvokesPluginSkill(t *testing.T) {
	fixtureBin := mcpBuildFixture(t)
	addr, srv := startFaux(t, pluginSkillCallScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writePluginFixture(t, home, fixtureBin)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "greet me")
	if res.Code != 0 {
		t.Fatalf("exit %d\nstdout=%s\nstderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "done") {
		t.Fatalf("stdout = %q, want the scripted final reply", res.Stdout)
	}

	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}

	// The `skill` tool's own catalog (its description, sent on the first
	// request) lists the plugin skill under its namespaced name.
	firstBody := string(reqs[0].Body)
	if !strings.Contains(firstBody, "demo:greet") {
		t.Errorf("first request's tool catalog did not mention demo:greet:\n%s", firstBody)
	}

	// The plugin's MCP server connected and is offered under its
	// namespaced tool name (mcp__plugin_<plugin>_<server>__<tool>) — not
	// in the request's resident Tools list directly (MCP tools are gated
	// behind tool_search, see internal/mcp/gating.go), but in the posture
	// index text folded into the system prompt.
	if !strings.Contains(reqs[0].System, "mcp__plugin_demo_fixture__echo") {
		t.Errorf("system prompt's MCP index missing mcp__plugin_demo_fixture__echo:\n%s", reqs[0].System)
	}

	// The follow-up request (after the skill tool ran) carries the
	// SKILL.md body back to the model, with its base directory prefixed.
	last := reqs[len(reqs)-1]
	lastMessages := string(last.Messages)
	if !strings.Contains(lastMessages, "Base directory for this skill:") {
		t.Errorf("tool result missing base directory line:\n%s", lastMessages)
	}
	if !strings.Contains(lastMessages, "distinctive-marker-38f1") {
		t.Errorf("tool result missing the plugin skill's body:\n%s", lastMessages)
	}
}

func TestPlugins_ReportCommand(t *testing.T) {
	fixtureBin := mcpBuildFixture(t)
	addr, _ := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writePluginFixture(t, home, fixtureBin)

	// /plugin is resolved locally and never reaches the model, but print
	// mode still needs a resolvable model configured — the faux server
	// itself is never called here.
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "/plugin")
	if res.Code != 0 {
		t.Fatalf("exit %d\nstdout=%s\nstderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "demo@market") {
		t.Errorf("/plugin output missing the active plugin:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "1 skill · 1 command") || !strings.Contains(res.Stdout, "1 MCP server") || strings.Contains(res.Stdout, "(s)") {
		t.Errorf("/plugin output missing expected contribution counts:\n%s", res.Stdout)
	}
}
