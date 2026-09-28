//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMCP_AddListRemove_ClaudeCodeScopes wires a server the way a Claude
// Code user does (`mcp add <name> -- <command>`), lists it with a live
// connection check, reads it back, and removes it — local scope in
// ~/.claude.json, then project scope in .mcp.json, which only starts in a
// trusted folder.
func TestMCP_AddListRemove_ClaudeCodeScopes(t *testing.T) {
	fixture := mcpBuildFixture(t)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := map[string]string{"HOME": home, "HARNESS_SESSIONS_DIR": sessDir}

	res := runHarness(t, proj, env, "mcp", "add", "fixture", "--", fixture)
	if res.Code != 0 || !strings.Contains(res.Stdout, "Added stdio MCP server fixture (local)") {
		t.Fatalf("mcp add: exit %d\nstdout=%s\nstderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".claude.json")); !strings.Contains(string(data), `"projects"`) {
		t.Errorf("local scope not written under projects:\n%s", data)
	}
	res = runHarness(t, proj, env, "mcp", "list")
	if !strings.Contains(res.Stdout, "fixture (local): "+fixture+" - ✓ connected, 4 tools") {
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

	// Project scope: .mcp.json, started only once the folder is trusted.
	if res = runHarness(t, proj, env, "mcp", "add", "-s", "project", "shared", "--", fixture); res.Code != 0 {
		t.Fatalf("mcp add -s project: %s", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcp.json")); err != nil {
		t.Fatalf(".mcp.json not written: %v", err)
	}
	if res = runHarness(t, proj, env, "mcp", "list"); !strings.Contains(res.Stdout, "shared (project)") || !strings.Contains(res.Stdout, "not trusted") {
		t.Errorf("untrusted project server, mcp list:\n%s", res.Stdout)
	}
	trusted := map[string]string{"HOME": home, "HARNESS_SESSIONS_DIR": sessDir, "HARNESS_TRUST_ALL": "1"}
	if res = runHarness(t, proj, trusted, "mcp", "list"); !strings.Contains(res.Stdout, "shared (project): "+fixture+" - ✓ connected") {
		t.Errorf("trusted project server, mcp list:\n%s", res.Stdout)
	}

	// A session in the untrusted folder says why the server did not start.
	addr, _ := startFaux(t, mcpPlainTextScript)
	res = runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "say hi")
	if !strings.Contains(res.Stderr, "not starting 1 MCP server from "+filepath.Join(proj, ".mcp.json")) {
		t.Errorf("print mode in an untrusted folder, stderr:\n%s", res.Stderr)
	}
}
