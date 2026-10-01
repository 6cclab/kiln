//go:build e2e

package e2e

// Read/Edit path rules (internal/claude/settings/pathrules.go) driven
// through the real binary: the rules are read from settings files in a
// scratch HOME and project, carry their source's anchor through the merge,
// and the gate judges the path the tool will actually open. Before path
// rules, each of these rules was compared as text with the raw path
// argument and never matched.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type pathRulesResult struct {
	OK      bool     `json:"ok"`
	Blocked []string `json:"blocked"`
}

func pathRulesRun(t *testing.T, proj, home, sessDir, addr string, extra ...string) (pathRulesResult, runResult) {
	t.Helper()
	args := append([]string{"-p", "go", "--output-format", "json"}, extra...)
	run := runHarness(t, proj, baseEnv(home, sessDir, addr), args...)
	var res pathRulesResult
	if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
		t.Fatalf("parse --output-format json: %v\nstdout=%s\nstderr=%s", err, run.Stdout, run.Stderr)
	}
	if run.Code != 0 {
		t.Fatalf("exit %d; stderr=%s", run.Code, run.Stderr)
	}
	return res, run
}

func pathRulesBlocked(res pathRulesResult, prefix string) bool {
	for _, b := range res.Blocked {
		if strings.HasPrefix(b, prefix) && strings.Contains(b, "blocked by permission rules") {
			return true
		}
	}
	return false
}

// TestPermission_UserEditDenyBlocksHomeWrite: Edit(~/notes/**) in the
// user's settings blocks the model's write to ~/notes/todo.md, spelt with
// ~ or absolute, even in bypassPermissions (only a deny rule can stop it
// there).
func TestPermission_UserEditDenyBlocksHomeWrite(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	target := filepath.Join(home, "notes", "todo.md")
	script := fmt.Sprintf(`model: faux-1
steps:
  - tool_call: {name: write, args: {path: "~/notes/todo.md", content: "pwned"}, id: w1}
  - on_tool_result: w1
    then:
      - tool_call: {name: write, args: {path: %q, content: "pwned"}, id: w2}
  - on_tool_result: w2
    then:
      - text: "done"
`, target)
	addr, _ := startFaux(t, script)
	pathRulesWriteFile(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"deny":["Edit(~/notes/**)"]}}`)

	res, _ := pathRulesRun(t, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	if !pathRulesBlocked(res, "write(~/notes/todo.md)") {
		t.Errorf("write ~/notes/todo.md not blocked by the rule; blocked=%v", res.Blocked)
	}
	if !pathRulesBlocked(res, "write("+target+")") {
		t.Errorf("write %s not blocked by the rule; blocked=%v", target, res.Blocked)
	}
	if _, err := os.Stat(target); err == nil {
		t.Errorf("%s was written despite the deny rule", target)
	}
}

// TestPermission_ProjectReadDenyBlocksNestedEnv: Read(.env) in project
// settings blocks a nested .env at any depth, in the default mode where
// reading otherwise never asks.
func TestPermission_ProjectReadDenyBlocksNestedEnv(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	pathRulesWriteFile(t, filepath.Join(proj, "pkg", "config", ".env"), "SECRET=hunter2\n")
	addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: read, args: {path: pkg/config/.env}, id: r1}
  - on_tool_result: r1
    then:
      - text: "done"
`)
	pathRulesWriteFile(t, filepath.Join(proj, ".claude", "settings.json"), `{"permissions":{"deny":["Read(.env)"]}}`)

	res, _ := pathRulesRun(t, proj, home, sessDir, addr)
	if !pathRulesBlocked(res, "read(pkg/config/.env)") {
		t.Errorf("read pkg/config/.env not blocked by Read(.env); blocked=%v", res.Blocked)
	}
	for _, req := range srv.Requests() {
		if strings.Contains(string(req.Body), "hunter2") {
			t.Fatal("the .env contents reached the model")
		}
	}
}

// TestPermission_WriteRuleWarnsAndDenies: a deny written as Write(docs/**)
// is warned about at startup, naming Edit(docs/**), and still blocks the
// write (kiln never treats it as weaker than an Edit rule).
func TestPermission_WriteRuleWarnsAndDenies(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: write, args: {path: docs/a.md, content: "x"}, id: w1}
  - on_tool_result: w1
    then:
      - text: "done"
`)
	pathRulesWriteFile(t, filepath.Join(proj, ".claude", "settings.json"), `{"permissions":{"deny":["Write(docs/**)"]}}`)

	res, run := pathRulesRun(t, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	if !strings.Contains(run.Stderr, "use Edit(docs/**) instead") {
		t.Errorf("no startup warning naming Edit(docs/**); stderr=%s", run.Stderr)
	}
	if !pathRulesBlocked(res, "write(docs/a.md)") {
		t.Errorf("write docs/a.md not blocked by Write(docs/**) deny; blocked=%v", res.Blocked)
	}
	if _, err := os.Stat(filepath.Join(proj, "docs", "a.md")); err == nil {
		t.Error("docs/a.md was written despite the deny rule")
	}
}

func pathRulesWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
