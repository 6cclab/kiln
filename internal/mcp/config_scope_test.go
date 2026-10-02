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

// TestAddServer_WritesKilnFilesOnly: AddServer/RemoveServer never touch
// Claude Code's ~/.claude.json or .mcp.json - they write kiln's own
// ~/.kiln/mcp.json (user and local scope) and <cwd>/.kiln/mcp.json
// (project scope), preserving every other key already in those files,
// including large numbers.
func TestAddServer_WritesKilnFilesOnly(t *testing.T) {
	ccOrig := `{"numStartups": 1790554359302123, "tipsHistory": {"x": 3}, "projects": {"/other": {"allowedTools": ["Bash"], "history": [{"display": "é <b>"}]}}, "mcpServers": {"old": {"command": "old"}}}`
	home, proj := scopeFixture(t, ccOrig)
	ccPath := filepath.Join(home, ".claude.json")
	kilnUserPath := filepath.Join(home, ".kiln", "mcp.json")
	// Seed kiln's own user file with an unrelated key, to prove it survives.
	if err := os.MkdirAll(filepath.Dir(kilnUserPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kilnUserPath, []byte(`{"mcpServers":{"old-kiln":{"command":"old-kiln"}},"unrelatedKey":42}`), 0o600); err != nil {
		t.Fatal(err)
	}

	path, err := AddServer(ScopeLocal, proj, "inc", ServerConfig{Command: "/bin/inc", Args: []string{"-v"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != kilnUserPath {
		t.Errorf("wrote %s, want %s", path, kilnUserPath)
	}
	if _, err := AddServer(ScopeUser, proj, "u2", ServerConfig{URL: "https://example.invalid/mcp", Type: "http"}); err != nil {
		t.Fatal(err)
	}

	// ~/.claude.json is byte-identical: AddServer never touches it.
	if cc, _ := os.ReadFile(ccPath); string(cc) != ccOrig {
		t.Errorf("~/.claude.json changed:\n%s", cc)
	}

	data, _ := os.ReadFile(kilnUserPath)
	for _, keep := range []string{`"unrelatedKey": 42`, `"old-kiln"`} {
		if !strings.Contains(string(data), keep) {
			t.Errorf("lost %s from %s:\n%s", keep, kilnUserPath, data)
		}
	}
	r := Resolve(ResolveOptions{Cwd: proj})
	if r.Servers["inc"].Command != "/bin/inc" || r.Servers["inc"].Scope != ScopeLocal || r.Servers["inc"].Source != kilnUserPath {
		t.Errorf("inc = %+v", r.Servers["inc"])
	}
	if r.Servers["u2"].URL == "" || r.Servers["u2"].Scope != ScopeUser || r.Servers["u2"].Source != kilnUserPath {
		t.Errorf("u2 = %+v", r.Servers["u2"])
	}
	// Claude Code's "old" entry is still visible through Resolve (kiln
	// only stopped writing ~/.claude.json, it still reads it).
	if r.Servers["old"].Command != "old" {
		t.Errorf("old (Claude Code's own) = %+v, want still readable", r.Servers["old"])
	}

	if _, err := RemoveServer(ScopeLocal, proj, "inc"); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveServer(ScopeLocal, proj, "inc"); err != ErrNotFound {
		t.Errorf("second remove: %v, want ErrNotFound", err)
	}
	if KilnServerIn(ScopeLocal, proj, "inc") || !KilnServerIn(ScopeUser, proj, "u2") {
		t.Error("KilnServerIn disagrees with the kiln file")
	}
	if !ServerIn(ScopeUser, proj, "old") {
		t.Error("ServerIn should still see Claude Code's own 'old' entry")
	}
}

// TestAddServer_ProjectScopeWritesKilnFile: project scope writes
// <cwd>/.kiln/mcp.json, never <cwd>/.mcp.json; a kiln file that is not
// valid JSON is refused, never overwritten.
func TestAddServer_ProjectScopeWritesKilnFile(t *testing.T) {
	_, proj := scopeFixture(t, "")
	kilnProjectPath := filepath.Join(proj, ".kiln", "mcp.json")
	path, err := AddServer(ScopeProject, proj, "inc", ServerConfig{Command: "inc"})
	if err != nil {
		t.Fatal(err)
	}
	if path != kilnProjectPath {
		t.Errorf("wrote %s, want %s", path, kilnProjectPath)
	}
	if _, err := os.Stat(filepath.Join(proj, ".mcp.json")); !os.IsNotExist(err) {
		t.Error(".mcp.json should not have been created")
	}
	if got := Resolve(ResolveOptions{Cwd: proj}).Project["inc"].Command; got != "inc" {
		t.Errorf("project inc = %q", got)
	}

	broken := `{"mcpServers": {` // truncated
	if err := os.WriteFile(kilnProjectPath, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddServer(ScopeProject, proj, "x", ServerConfig{Command: "x"}); err == nil {
		t.Fatal("wrote over an unparseable .kiln/mcp.json")
	}
	if data, _ := os.ReadFile(kilnProjectPath); string(data) != broken {
		t.Errorf("broken file changed: %q", data)
	}
}

// TestKilnMCPFile_CommittableGitignore: the project .kiln directory's
// .gitignore carves mcp.json back out, so `kiln mcp add -s project`
// produces a file meant to be committed, like .mcp.json - not one buried
// under the blanket ".kiln/*" ignore settings.local.json relies on.
func TestKilnMCPFile_CommittableGitignore(t *testing.T) {
	_, proj := scopeFixture(t, "")
	if _, err := AddServer(ScopeProject, proj, "inc", ServerConfig{Command: "inc"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(proj, ".kiln", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "!mcp.json") {
		t.Errorf(".kiln/.gitignore = %q, want mcp.json carved out of the blanket ignore", data)
	}
}

// TestAddServer_RefusesSymlinkedFile: AddServer refuses to write through a
// symlinked kiln file, the same safety writesettings.WriteJSON applies
// everywhere else.
func TestAddServer_RefusesSymlinkedFile(t *testing.T) {
	_, proj := scopeFixture(t, "")
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.MkdirAll(filepath.Join(proj, ".kiln"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(proj, ".kiln", "mcp.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := AddServer(ScopeProject, proj, "inc", ServerConfig{Command: "inc"}); err == nil {
		t.Fatal("AddServer wrote through a symlinked .kiln/mcp.json")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("AddServer wrote to the symlink's target")
	}
}

// TestResolve_KilnEntryWinsOverClaudeCodeSameScope: a kiln entry with the
// same name as a Claude Code entry in the same scope wins.
func TestResolve_KilnEntryWinsOverClaudeCodeSameScope(t *testing.T) {
	home, proj := scopeFixture(t, `{"mcpServers":{"both":{"command":"cc-user"}}}`)
	kilnUserPath := filepath.Join(home, ".kiln", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(kilnUserPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kilnUserPath, []byte(`{"mcpServers":{"both":{"command":"kiln-user"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := Resolve(ResolveOptions{Cwd: proj})
	if got := r.Servers["both"].Command; got != "kiln-user" {
		t.Errorf("both = %q, want kiln-user (kiln wins in the same scope)", got)
	}
	if r.Servers["both"].Source != kilnUserPath {
		t.Errorf("Source = %q, want %q", r.Servers["both"].Source, kilnUserPath)
	}
}

// TestMCPRemove_KilnServerInVsServerIn: KilnServerIn only ever reports on
// kiln's own files; ServerIn only ever reports on Claude Code's. A name
// that exists only in a Claude Code file is invisible to KilnServerIn, so
// RemoveServer (which only edits kiln files) correctly reports ErrNotFound
// for it - the caller (internal/cli's mcpRemove) uses ServerIn to tell the
// user why.
func TestMCPRemove_KilnServerInVsServerIn(t *testing.T) {
	_, proj := scopeFixture(t, `{"mcpServers":{"cc-only":{"command":"x"}}}`)
	if KilnServerIn(ScopeUser, proj, "cc-only") {
		t.Error("KilnServerIn should not see a Claude-Code-only entry")
	}
	if !ServerIn(ScopeUser, proj, "cc-only") {
		t.Error("ServerIn should see it")
	}
	if _, err := RemoveServer(ScopeUser, proj, "cc-only"); err != ErrNotFound {
		t.Errorf("RemoveServer(cc-only) = %v, want ErrNotFound (kiln never edits Claude Code's file)", err)
	}
}
