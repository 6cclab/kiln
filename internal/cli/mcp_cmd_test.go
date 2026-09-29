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

// add, get, list and remove round-trip through the same files Claude Code
// uses, and get never prints secret values.
func TestMCPCommand_RoundTrip(t *testing.T) {
	home, proj := mcpScratch(t)

	if code, out, errs := runMCP(t, "add", "-e", "TOKEN=secret-value", "tools", "--", "/nonexistent/kiln-mcp", "--flag"); code != 0 {
		t.Fatalf("add stdio: code %d, stderr %q", code, errs)
	} else if !strings.Contains(out, "Added stdio MCP server tools (local)") {
		t.Errorf("add stdout = %q", out)
	}
	if code, out, errs := runMCP(t, "add", "-s", "project", "-t", "http", "-H", "Authorization: Bearer abc", "web", "https://example.test/mcp"); code != 0 {
		t.Fatalf("add http: code %d, stderr %q", code, errs)
	} else if !strings.Contains(out, "once this folder is trusted") {
		t.Errorf("project add should mention trust: %q", out)
	}
	if code, _, errs := runMCP(t, "add-json", "-s", "user", "j", `{"command":"/nonexistent/j"}`); code != 0 {
		t.Fatalf("add-json: code %d, stderr %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Fatalf("local/user servers not written to the scratch ~/.claude.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcp.json")); err != nil {
		t.Fatalf("project server not written to .mcp.json: %v", err)
	}

	_, out, _ := runMCP(t, "get", "tools")
	for _, want := range []string{"Scope: local", "Type: stdio", "Command: /nonexistent/kiln-mcp", "Args: --flag", "Environment: TOKEN (values hidden)"} {
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
	if !strings.Contains(out, "tools (local): /nonexistent/kiln-mcp --flag - ✗") {
		t.Errorf("list should report the unstartable stdio server as failed:\n%s", out)
	}
	if !strings.Contains(out, "web (project): https://example.test/mcp (http) - not started: this folder is not trusted yet") {
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
