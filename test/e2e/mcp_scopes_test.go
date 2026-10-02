//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMCP_AddListRemove_KilnScopes wires a server through `kiln mcp add`
// (local scope), lists it with a live connection check, reads it back, and
// removes it - all through kiln's own ~/.kiln/mcp.json, never Claude
// Code's ~/.claude.json. Then project scope, through <cwd>/.kiln/mcp.json
// instead of .mcp.json, which only starts in a trusted folder.
func TestMCP_AddListRemove_KilnScopes(t *testing.T) {
	fixture := mcpBuildFixture(t)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := map[string]string{"HOME": home, "HARNESS_SESSIONS_DIR": sessDir}

	res := runHarness(t, proj, env, "mcp", "add", "fixture", "--", fixture)
	if res.Code != 0 || !strings.Contains(res.Stdout, "Added stdio MCP server fixture (local)") {
		t.Fatalf("mcp add: exit %d\nstdout=%s\nstderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	// Local scope lands in ~/.kiln/mcp.json, under "projects" (mirroring
	// Claude Code's own ~/.claude.json shape) - never in ~/.claude.json.
	kilnUserMCP := filepath.Join(home, ".kiln", "mcp.json")
	if data, _ := os.ReadFile(kilnUserMCP); !strings.Contains(string(data), `"projects"`) {
		t.Errorf("local scope not written under projects in %s:\n%s", kilnUserMCP, data)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf("~/.claude.json should not exist after `kiln mcp add`, got err=%v", err)
	}
	res = runHarness(t, proj, env, "mcp", "list")
	if !strings.Contains(res.Stdout, "fixture (local, from "+kilnUserMCP+"): "+fixture+" - ✓ connected, 4 tools") {
		t.Errorf("mcp list:\n%s%s", res.Stdout, res.Stderr)
	}
	res = runHarness(t, proj, env, "mcp", "get", "fixture")
	if !strings.Contains(res.Stdout, "Scope: local") || !strings.Contains(res.Stdout, "Command: "+fixture) {
		t.Errorf("mcp get:\n%s%s", res.Stdout, res.Stderr)
	}
	res = runHarness(t, proj, env, "mcp", "remove", "fixture")
	if res.Code != 0 {
		t.Fatalf("mcp remove: exit %d %s", res.Code, res.Stderr)
	}
	if res = runHarness(t, proj, env, "mcp", "list"); !strings.Contains(res.Stdout, "No MCP servers configured") {
		t.Errorf("after remove, mcp list:\n%s", res.Stdout)
	}

	// Project scope: <cwd>/.kiln/mcp.json, started only once the folder is
	// trusted - never Claude Code's own .mcp.json.
	kilnProjectMCP := filepath.Join(proj, ".kiln", "mcp.json")
	if res = runHarness(t, proj, env, "mcp", "add", "-s", "project", "shared", "--", fixture); res.Code != 0 {
		t.Fatalf("mcp add -s project: %s", res.Stderr)
	}
	if _, err := os.Stat(kilnProjectMCP); err != nil {
		t.Fatalf(".kiln/mcp.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcp.json")); !os.IsNotExist(err) {
		t.Errorf(".mcp.json should not exist, got err=%v", err)
	}
	if res = runHarness(t, proj, env, "mcp", "list"); !strings.Contains(res.Stdout, "shared (project, from "+kilnProjectMCP+")") || !strings.Contains(res.Stdout, "not trusted") {
		t.Errorf("untrusted project server, mcp list:\n%s", res.Stdout)
	}
	trusted := map[string]string{"HOME": home, "HARNESS_SESSIONS_DIR": sessDir, "HARNESS_TRUST_ALL": "1"}
	if res = runHarness(t, proj, trusted, "mcp", "list"); !strings.Contains(res.Stdout, "shared (project, from "+kilnProjectMCP+"): "+fixture+" - ✓ connected") {
		t.Errorf("trusted project server, mcp list:\n%s", res.Stdout)
	}

	// A session in the untrusted folder says why the server did not start.
	addr, _ := startFaux(t, mcpPlainTextScript)
	res = runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "say hi")
	if !strings.Contains(res.Stderr, "not starting 1 MCP server from "+kilnProjectMCP) {
		t.Errorf("print mode in an untrusted folder, stderr:\n%s", res.Stderr)
	}
}

// TestMCP_Add_NeverTouchesClaudeCodeFiles: `kiln mcp add` in a scratch
// HOME with pre-existing Claude Code MCP config leaves ~/.claude.json and
// .mcp.json byte-identical, and the new server still shows up in
// `kiln mcp list` (read from kiln's own file, merged with Claude Code's).
func TestMCP_Add_NeverTouchesClaudeCodeFiles(t *testing.T) {
	fixture := mcpBuildFixture(t)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := map[string]string{"HOME": home, "HARNESS_SESSIONS_DIR": sessDir}

	ccJSON := `{"mcpServers":{"existing":{"command":"/nonexistent/existing"}}}`
	ccJSONPath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(ccJSONPath, []byte(ccJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	ccMCPPath := filepath.Join(proj, ".mcp.json")
	ccMCP := `{"mcpServers":{"existing-proj":{"command":"/nonexistent/existing-proj"}}}`
	if err := os.WriteFile(ccMCPPath, []byte(ccMCP), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runHarness(t, proj, env, "mcp", "add", "fixture", "--", fixture)
	if res.Code != 0 {
		t.Fatalf("mcp add: exit %d\nstdout=%s\nstderr=%s", res.Code, res.Stdout, res.Stderr)
	}

	if got, err := os.ReadFile(ccJSONPath); err != nil || string(got) != ccJSON {
		t.Errorf("~/.claude.json changed: err=%v, got=%s", err, got)
	}
	if got, err := os.ReadFile(ccMCPPath); err != nil || string(got) != ccMCP {
		t.Errorf(".mcp.json changed: err=%v, got=%s", err, got)
	}

	res = runHarness(t, proj, env, "mcp", "list")
	if !strings.Contains(res.Stdout, "fixture (local, from "+filepath.Join(home, ".kiln", "mcp.json")+")") {
		t.Errorf("mcp list missing the newly added server:\n%s%s", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "existing (user, from "+ccJSONPath+")") {
		t.Errorf("mcp list should still show Claude Code's own entry:\n%s", res.Stdout)
	}
}
