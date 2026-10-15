//go:build e2e

// Package e2e: Claude Code mods. testdata/mods/probe is a mod built from
// the same parts squad and savvy-progress use: a command, a tool answered
// by tool.call, agent.spawn, a pane and the band above the prompt. Each
// test's doc comment records the break used to confirm it can fail.
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
)

// installProbeMod copies testdata/mods/probe into the scratch HOME as an
// installed, enabled user plugin.
func installProbeMod(t *testing.T, home string) {
	t.Helper()
	root := filepath.Join(home, "plugins", "repos", "probe")
	src := filepath.Join(repoRootDir(t), "testdata", "mods", "probe")
	if err := os.CopyFS(root, os.DirFS(src)); err != nil {
		t.Fatalf("copy probe mod: %v", err)
	}
	writeJSONFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"), map[string]any{
		"version": 2,
		"plugins": map[string]any{
			"probe@market": []map[string]any{{"scope": "user", "installPath": root, "version": "1.0.0"}},
		},
	})
	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"), map[string]any{
		"enabledPlugins": map[string]bool{"probe@market": true},
	})
}

const modsScript = `models:
  faux-1:
    - tool_call: {name: bash, args: {command: "echo hi"}, id: b1}
    - on_tool_result: b1
      then:
        - tool_call: {name: task, args: {subagent_type: "general-purpose", description: "probe sub", prompt: "go", model: "fast"}, id: t1}
        - on_tool_result: t1
          then:
            - tool_call: {name: mcp__probe__step, args: {note: "main"}, id: s1}
            - on_tool_result: s1
              then:
                - text: "parent done"
  faux-2:
    - tool_call: {name: mcp__probe__step, args: {note: "sub"}, id: s2}
    - on_tool_result: s2
      then:
        - text: "sub done"
`

// TestMods_ToolCallSpawnAndOwnTool runs the probe mod in print mode, in
// acceptEdits (task runs; an MCP-style tool would still need a prompt,
// which print mode refuses):
//   - the mod's own tool runs without a permission prompt;
//   - tool.call sees kiln's tools by Claude Code's names (bash -> Bash,
//     task -> Agent);
//   - agent.spawn sees the subagent and rewrites its prompt;
//   - the subagent gets the mod's tool and the call carries its agentId.
//
// Break: drop modsRT.attach / dispatcher.Hooks / gate.SetModTools in
// chat.go -> fails.
func TestMods_ToolCallSpawnAndOwnTool(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	installProbeMod(t, home)
	writeModelRolesSettings(t, proj, map[string]string{"fast": fauxprovider.ProviderID + "/faux-2"})
	addr, srv := startFaux(t, modsScript)
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "go", "--permission-mode", "acceptEdits")
	if res.Code != 0 {
		t.Fatalf("exit %d: %s", res.Code, res.Stderr)
	}
	var all strings.Builder
	for _, r := range srv.Requests() {
		all.Write(r.Messages)
	}
	got := all.String()
	for _, want := range []string{
		"(probe: report with mcp__probe__step)", // agent.spawn rewrote the subagent's prompt
		"probe saw [Bash,Agent,spawn:general-purpose] from sub",
		"probe saw [Bash,Agent,spawn:general-purpose] from main",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("requests never carried %q\n%s\nstderr: %s", want, got, res.Stderr)
		}
	}
	if strings.Contains(got, "did not run") || strings.Contains(got, "not available") {
		t.Errorf("a mod tool call was refused:\n%s\nstderr: %s", got, res.Stderr)
	}
}

// TestTUI_Mods_CommandPaneAndBand: /probe (a mod's command) opens the
// mod's pane, drawn in kiln's live region, and the mod's band shows above
// the prompt.
// Break: drop cfg.Mods in tui.go -> fails.
func TestTUI_Mods_CommandPaneAndBand(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, unreadScript)
	installProbeMod(t, home)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)
	if err := s.WaitFor("probe band · 0 notes", 5*time.Second); err != nil {
		t.Fatalf("the mod's band never showed: %v", err)
	}
	s.Send("/probe")
	s.SendKey("enter")
	if err := s.WaitFor("Probe pane opened.", 5*time.Second); err != nil {
		t.Fatalf("/probe did not run the mod's command: %v", err)
	}
	if err := s.WaitFor("probe notes: none", 5*time.Second); err != nil {
		t.Fatalf("the mod's pane never drew: %v", err)
	}
}
