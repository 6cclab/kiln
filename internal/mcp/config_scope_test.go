package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scopeFixture points HOME at a scratch dir holding claudeJSON and returns
// a project dir (a git root) inside it.
func scopeFixture(t *testing.T, claudeJSON string) (home, proj string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	if claudeJSON != "" {
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	proj = filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, proj
}

// TestResolve_ReadsEveryClaudeCodeScope: user, local (projects[cwd]) and
// .mcp.json all load, with local > project > user on a name clash, and
// project entries held apart until the folder is trusted.
func TestResolve_ReadsEveryClaudeCodeScope(t *testing.T) {
	home, proj := scopeFixture(t, "")
	doc := map[string]any{
		"mcpServers": map[string]any{
			"u":      map[string]any{"command": "user-u"},
			"shared": map[string]any{"command": "user-shared"},
			"both":   map[string]any{"command": "user-both"},
		},
		"projects": map[string]any{
			proj: map[string]any{"mcpServers": map[string]any{
				"l":    map[string]any{"command": "local-l"},
				"both": map[string]any{"command": "local-both"},
			}},
		},
	}
	raw, _ := json.Marshal(doc)
	os.WriteFile(filepath.Join(home, ".claude.json"), raw, 0o600)
	os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(`{"mcpServers":{"p":{"command":"proj-p"},"shared":{"command":"proj-shared"},"both":{"command":"proj-both"}}}`), 0o644)

	r := Resolve(ResolveOptions{Cwd: proj})
	want := map[string]string{"u": "user-u", "l": "local-l", "both": "local-both"}
	for name, cmd := range want {
		if got := r.Servers[name].Command; got != cmd {
			t.Errorf("Servers[%s] = %q, want %q", name, got, cmd)
		}
	}
	if _, ok := r.Servers["shared"]; ok {
		t.Error("user 'shared' should be shadowed by the project's")
	}
	if got := r.Project["shared"].Command; got != "proj-shared" {
		t.Errorf("Project[shared] = %q, want proj-shared", got)
	}
	if _, ok := r.Project["both"]; ok {
		t.Error("project 'both' should be shadowed by the local one")
	}
	if r.Project["p"].Scope != ScopeProject || r.Servers["l"].Scope != ScopeLocal || r.Servers["u"].Scope != ScopeUser {
		t.Errorf("scopes not recorded: %+v %+v", r.Project["p"], r.Servers["l"])
	}
	if r.ProjectFile != filepath.Join(proj, ".mcp.json") {
		t.Errorf("ProjectFile = %q", r.ProjectFile)
	}

	// A subdirectory of the repo finds the root's .mcp.json.
	sub := filepath.Join(proj, "api")
	os.MkdirAll(sub, 0o755)
	if got := Resolve(ResolveOptions{Cwd: sub}).Project["p"].Command; got != "proj-p" {
		t.Errorf("from a subdirectory, Project[p] = %q", got)
	}

	// --strict-mcp-config reads only the given file.
	flag := filepath.Join(home, "flag.json")
	os.WriteFile(flag, []byte(`{"mcpServers":{"f":{"command":"flag-f"}}}`), 0o644)
	strict := Resolve(ResolveOptions{Cwd: proj, Path: flag, Strict: true})
	if len(strict.Servers) != 1 || strict.Servers["f"].Command != "flag-f" || len(strict.Project) != 0 {
		t.Errorf("strict = %+v", strict)
	}
}

// TestResolve_ExpandsEnvReferences: ${VAR} and ${VAR:-default}, as in
// Claude Code's .mcp.json.
func TestResolve_ExpandsEnvReferences(t *testing.T) {
	_, proj := scopeFixture(t, "")
	t.Setenv("KILN_T_TOKEN", "sekrit")
	os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte(`{"mcpServers":{"api":{"url":"${KILN_T_BASE:-https://example.invalid}/mcp","headers":{"Authorization":"Bearer ${KILN_T_TOKEN}"}},"cli":{"command":"${KILN_T_BIN:-tool}","args":["--key=${KILN_T_TOKEN}"]}}}`), 0o644)
	p := Resolve(ResolveOptions{Cwd: proj}).Project
	if p["api"].URL != "https://example.invalid/mcp" || p["api"].Headers["Authorization"] != "Bearer sekrit" {
		t.Errorf("api = %+v", p["api"])
	}
	if p["cli"].Command != "tool" || p["cli"].Args[0] != "--key=sekrit" {
		t.Errorf("cli = %+v", p["cli"])
	}
}

// TestAddServer_PreservesClaudeJSON: ~/.claude.json is Claude Code's state
// file; adding a server must leave every other value byte-identical,
// including large numbers.
func TestAddServer_PreservesClaudeJSON(t *testing.T) {
	orig := `{"numStartups": 1790554359302123, "tipsHistory": {"x": 3}, "projects": {"/other": {"allowedTools": ["Bash"], "history": [{"display": "é <b>"}]}}, "mcpServers": {"old": {"command": "old"}}}`
	home, proj := scopeFixture(t, orig)
	path, err := AddServer(ScopeLocal, proj, "inc", ServerConfig{Command: "/bin/inc", Args: []string{"-v"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".claude.json") {
		t.Errorf("wrote %s", path)
	}
	if _, err := AddServer(ScopeUser, proj, "u2", ServerConfig{URL: "https://example.invalid/mcp", Type: "http"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, keep := range []string{`1790554359302123`, `"tipsHistory"`, `"allowedTools"`, `é <b>`, `"old"`} {
		if !strings.Contains(string(data), keep) {
			t.Errorf("lost %s:\n%s", keep, data)
		}
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 kept", st.Mode().Perm())
	}
	r := Resolve(ResolveOptions{Cwd: proj})
	if r.Servers["inc"].Command != "/bin/inc" || r.Servers["inc"].Scope != ScopeLocal {
		t.Errorf("inc = %+v", r.Servers["inc"])
	}
	if r.Servers["u2"].URL == "" || r.Servers["u2"].Scope != ScopeUser {
		t.Errorf("u2 = %+v", r.Servers["u2"])
	}

	if _, err := RemoveServer(ScopeLocal, proj, "inc"); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveServer(ScopeLocal, proj, "inc"); err != ErrNotFound {
		t.Errorf("second remove: %v, want ErrNotFound", err)
	}
	if ServerIn(ScopeLocal, proj, "inc") || !ServerIn(ScopeUser, proj, "u2") {
		t.Error("ServerIn disagrees with the file")
	}
}

// TestAddServer_ProjectScopeAndBrokenFile: project scope writes .mcp.json;
// a file that is not valid JSON is refused, never overwritten.
func TestAddServer_ProjectScopeAndBrokenFile(t *testing.T) {
	home, proj := scopeFixture(t, "")
	if _, err := AddServer(ScopeProject, proj, "inc", ServerConfig{Command: "inc"}); err != nil {
		t.Fatal(err)
	}
	if got := Resolve(ResolveOptions{Cwd: proj}).Project["inc"].Command; got != "inc" {
		t.Errorf("project inc = %q", got)
	}
	broken := `{"mcpServers": {` // truncated
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(broken), 0o600)
	if _, err := AddServer(ScopeUser, proj, "x", ServerConfig{Command: "x"}); err == nil {
		t.Fatal("wrote over an unparseable ~/.claude.json")
	}
	if data, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(data) != broken {
		t.Errorf("broken file changed: %q", data)
	}
}
