package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
)

// mcpScratch points HOME and the working directory at scratch dirs, and
// refuses to run if ~/.claude.json would still resolve to the real one.
func mcpScratch(t *testing.T) (home, proj string) {
	t.Helper()
	home, proj = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HARNESS_TRUST_ALL", "")
	t.Chdir(proj)
	if got := paths.ClaudeJSONPath(); !strings.HasPrefix(got, home) {
		t.Fatalf("~/.claude.json resolves to %s, outside the scratch HOME", got)
	}
	return home, proj
}

func runMCP(t *testing.T, argv ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = MCPCommand(context.Background(), argv, &out, &errb)
	return code, out.String(), errb.String()
}

// add, get, list and remove round-trip through kiln's own files
// (~/.kiln/mcp.json, <cwd>/.kiln/mcp.json), never Claude Code's
// ~/.claude.json or .mcp.json, and get never prints secret values.
func TestMCPCommand_RoundTrip(t *testing.T) {
	home, proj := mcpScratch(t)

	if code, out, errs := runMCP(t, "add", "-e", "TOKEN=secret-value", "tools", "--", "/nonexistent/kiln-mcp", "--flag"); code != 0 {
		t.Fatalf("add stdio: code %d, stderr %q", code, errs)
	} else if !strings.Contains(out, "Added stdio MCP server tools (local)") || !strings.Contains(out, filepath.Join(home, ".kiln", "mcp.json")) {
		t.Errorf("add stdout = %q", out)
	}
	if code, out, errs := runMCP(t, "add", "-s", "project", "-t", "http", "-H", "Authorization: Bearer abc", "web", "https://example.test/mcp"); code != 0 {
		t.Fatalf("add http: code %d, stderr %q", code, errs)
	} else if !strings.Contains(out, "once this folder is trusted") || !strings.Contains(out, filepath.Join(proj, ".kiln", "mcp.json")) {
		t.Errorf("project add should mention trust and the kiln project file: %q", out)
	}
	if code, _, errs := runMCP(t, "add-json", "-s", "user", "j", `{"command":"/nonexistent/j"}`); code != 0 {
		t.Fatalf("add-json: code %d, stderr %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiln", "mcp.json")); err != nil {
		t.Fatalf("local/user servers not written to ~/.kiln/mcp.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".kiln", "mcp.json")); err != nil {
		t.Fatalf("project server not written to .kiln/mcp.json: %v", err)
	}
	// Claude Code's own files are never created by `kiln mcp add`.
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf("~/.claude.json should not exist, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcp.json")); !os.IsNotExist(err) {
		t.Errorf(".mcp.json should not exist, got err=%v", err)
	}

	_, out, _ := runMCP(t, "get", "tools")
	for _, want := range []string{"Scope: local", "Type: stdio", "Command: /nonexistent/kiln-mcp", "Args: --flag", "Environment: TOKEN (values hidden)", "From: " + filepath.Join(home, ".kiln", "mcp.json")} {
		if !strings.Contains(out, want) {
			t.Errorf("get tools missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-value") {
		t.Errorf("get printed a secret value:\n%s", out)
	}
	_, out, _ = runMCP(t, "get", "web")
	if !strings.Contains(out, "URL: https://example.test/mcp") || !strings.Contains(out, "Headers: Authorization (values hidden)") {
		t.Errorf("get web:\n%s", out)
	}

	_, out, _ = runMCP(t, "list")
	if !strings.Contains(out, "tools (local, from "+filepath.Join(home, ".kiln", "mcp.json")+"): /nonexistent/kiln-mcp --flag - ✗") {
		t.Errorf("list should report the unstartable stdio server as failed:\n%s", out)
	}
	if !strings.Contains(out, "web (project, from "+filepath.Join(proj, ".kiln", "mcp.json")+"): https://example.test/mcp (http) - not started: this folder is not trusted yet") {
		t.Errorf("list should not start a project server in an untrusted folder:\n%s", out)
	}

	for _, name := range []string{"tools", "web", "j"} {
		if code, _, errs := runMCP(t, "remove", name); code != 0 {
			t.Errorf("remove %s: code %d, stderr %q", name, code, errs)
		}
	}
	if _, out, _ := runMCP(t, "list"); !strings.Contains(out, "No MCP servers configured") {
		t.Errorf("list after removing everything:\n%s", out)
	}
}

// TestMCPCommand_RemoveRefusesClaudeCodeOnly: `kiln mcp remove` never edits
// a Claude Code file; a name configured only there is refused, naming the
// file, rather than silently doing nothing or guessing.
func TestMCPCommand_RemoveRefusesClaudeCodeOnly(t *testing.T) {
	home, proj := mcpScratch(t)
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"cc-only":{"command":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}

	code, _, errs := runMCP(t, "remove", "cc-only")
	if code == 0 {
		t.Fatal("remove of a Claude-Code-only server should fail")
	}
	if !strings.Contains(errs, "cc-only") || !strings.Contains(errs, filepath.Join(home, ".claude.json")) || !strings.Contains(errs, "kiln doesn't edit Claude Code's config") {
		t.Errorf("stderr = %q", errs)
	}
	after, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil || string(after) != string(before) {
		t.Errorf("~/.claude.json changed: %v, %q", err, after)
	}

	// Still visible to `kiln mcp list`/`get`, just not removable.
	_, out, _ := runMCP(t, "get", "cc-only")
	if !strings.Contains(out, "From: "+filepath.Join(home, ".claude.json")) {
		t.Errorf("get cc-only should still show it: %q", out)
	}

	// Explicit -s also refuses.
	code, _, errs = runMCP(t, "remove", "-s", "user", "cc-only")
	if code == 0 || !strings.Contains(errs, "kiln doesn't edit Claude Code's config") {
		t.Errorf("remove -s user cc-only: code %d, stderr %q", code, errs)
	}
	_ = proj
}

func TestMCPCommand_Refusals(t *testing.T) {
	mcpScratch(t)
	runMCP(t, "add", "dup", "--", "/nonexistent/a")
	runMCP(t, "add", "-s", "user", "dup", "--", "/nonexistent/b")
	cases := []struct {
		argv []string
		want string
	}{
		{nil, "usage:"},
		{[]string{"frob"}, "usage:"},
		{[]string{"add", "only-a-name"}, "need a name and a command or URL"},
		{[]string{"add", "u", "https://example.test"}, "looks like a URL; add -t http"},
		{[]string{"add", "-t", "http", "u", "https://a", "https://b"}, "takes one URL"},
		{[]string{"add", "-e"}, "-e needs a value"},
		{[]string{"remove"}, "need the server's name"},
		{[]string{"remove", "ghost"}, "no MCP server named ghost"},
		{[]string{"remove", "dup"}, "configured in several scopes (local, user); pass -s"},
		{[]string{"remove", "-s", "project", "dup"}, "no MCP server named dup in project scope"},
		{[]string{"get"}, "need the server's name"},
		{[]string{"get", "ghost"}, "no MCP server named ghost"},
	}
	for _, c := range cases {
		code, _, errs := runMCP(t, c.argv...)
		if code == 0 || !strings.Contains(errs, c.want) {
			t.Errorf("mcp %v: code %d, stderr %q, want failure mentioning %q", c.argv, code, errs, c.want)
		}
	}
}
