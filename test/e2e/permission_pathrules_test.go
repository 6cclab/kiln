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
	"regexp"
	"strings"
	"testing"
	"time"

	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
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

// TestPermission_DanglingLinkWriteBlocked (review HIGH 1): a project file
// that is a dangling link into ~/.ssh is not a way to write there. With a
// user Edit(~/.ssh/**) deny the gate blocks it; without one, acceptEdits
// still does not write it (outside the workspace, and the write tool
// refuses a symlink path).
func TestPermission_DanglingLinkWriteBlocked(t *testing.T) {
	for _, withDeny := range []bool{true, false} {
		t.Run(fmt.Sprintf("deny=%v", withDeny), func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			target := filepath.Join(home, ".ssh", "authorized_keys")
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(proj, "keys")); err != nil {
				t.Fatal(err)
			}
			if withDeny {
				pathRulesWriteFile(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"deny":["Edit(~/.ssh/**)"]}}`)
			}
			addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: write, args: {path: keys, content: "ssh-ed25519 AAAA attacker"}, id: w1}
  - on_tool_result: w1
    then:
      - text: "done"
`)
			res, _ := pathRulesRun(t, proj, home, sessDir, addr, "--permission-mode", "acceptEdits")
			if withDeny && !pathRulesBlocked(res, "write(keys)") {
				t.Errorf("write keys not blocked by Edit(~/.ssh/**); blocked=%v", res.Blocked)
			}
			if _, err := os.Stat(target); err == nil {
				t.Errorf("%s was created through the dangling link", target)
			}
		})
	}
}

// TestPermission_DotDotLinkWriteBlocked (verification HIGH 1): with
// sshl -> ~/.ssh and evil -> "sshl/..", the model's write to
// evil/.ssh/authorized_keys (which the kernel opens as
// ~/.ssh/authorized_keys) is blocked by a user Edit(~/.ssh/**) deny in
// acceptEdits, and the key file is not written.
func TestPermission_DotDotLinkWriteBlocked(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	target := filepath.Join(home, ".ssh", "authorized_keys")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(proj, "sshl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sshl/..", filepath.Join(proj, "evil")); err != nil {
		t.Fatal(err)
	}
	pathRulesWriteFile(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"deny":["Edit(~/.ssh/**)"]}}`)
	addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: write, args: {path: evil/.ssh/authorized_keys, content: "ssh-ed25519 AAAA attacker"}, id: w1}
  - on_tool_result: w1
    then:
      - tool_call: {name: bash, args: {command: "echo k >> evil/.ssh/authorized_keys"}, id: b1}
  - on_tool_result: b1
    then:
      - text: "done"
`)
	res, _ := pathRulesRun(t, proj, home, sessDir, addr, "--permission-mode", "acceptEdits", "--add-dir", home)
	if !pathRulesBlocked(res, "write(evil/.ssh/authorized_keys)") {
		t.Errorf("write not blocked by Edit(~/.ssh/**); blocked=%v", res.Blocked)
	}
	if _, err := os.Stat(target); err == nil {
		t.Errorf("%s was written through evil -> sshl/..", target)
	}
}

// TestPermission_BypassAskRefusedInPrint (verification LOW 10): an ask rule
// still applies in bypassPermissions (deny, then ask, then bypass); in a
// print run there is nobody to ask, so the call is refused, and nothing
// else is.
func TestPermission_BypassAskRefusedInPrint(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	pathRulesWriteFile(t, filepath.Join(proj, ".claude", "settings.json"), `{"permissions":{"ask":["Edit(src/**)"]}}`)
	addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: write, args: {path: src/new.js, content: "x"}, id: w1}
  - on_tool_result: w1
    then:
      - tool_call: {name: write, args: {path: notes.md, content: "x"}, id: w2}
  - on_tool_result: w2
    then:
      - text: "done"
`)
	res, _ := pathRulesRun(t, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	if !permBlockedFor(res.Blocked, "write(src/new.js)") {
		t.Errorf("write src/new.js was not refused under an ask rule; blocked=%v", res.Blocked)
	}
	if _, err := os.Stat(filepath.Join(proj, "src", "new.js")); err == nil {
		t.Error("src/new.js was written")
	}
	if _, err := os.Stat(filepath.Join(proj, "notes.md")); err != nil {
		t.Errorf("notes.md (no rule) was not written in bypassPermissions: %v", err)
	}
}

// pathRulesWarning is a substring of the startup warning for a
// Write(docs/**) rule.
const pathRulesWarning = "not matched by file permission checks"

// TestPermission_RuleWarningNeverReachesModel: the startup warning goes to
// stderr (print) or the TUI's notes, never into what the model is sent.
func TestPermission_RuleWarningNeverReachesModel(t *testing.T) {
	const script = `model: faux-1
steps:
  - text: "done"
`
	t.Run("print", func(t *testing.T) {
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		pathRulesWriteFile(t, filepath.Join(proj, ".claude", "settings.json"), `{"permissions":{"deny":["Write(docs/**)"]}}`)
		addr, srv := startFaux(t, script)
		_, run := pathRulesRun(t, proj, home, sessDir, addr)
		if !strings.Contains(run.Stderr, pathRulesWarning) {
			t.Fatalf("no warning on stderr: %s", run.Stderr)
		}
		pathRulesNotInRequests(t, srv.Requests())
	})
	t.Run("tui", func(t *testing.T) {
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		pathRulesWriteFile(t, filepath.Join(proj, ".claude", "settings.json"), `{"permissions":{"deny":["Write(docs/**)"]}}`)
		addr, srv := startFaux(t, script)
		s := startTUI(t, 220, 40, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
		defer s.Close()
		if err := s.WaitFor(pathRulesWarning, 10*time.Second); err != nil {
			t.Fatalf("warning not shown in the TUI: %v", err)
		}
		s.Send("hello\r")
		if err := s.WaitFor(regexp.MustCompile(`(?m)^\s*done\s*$`), 15*time.Second); err != nil {
			t.Fatal(err)
		}
		reqs := srv.Requests()
		if len(reqs) == 0 {
			t.Fatal("the model was never called")
		}
		pathRulesNotInRequests(t, reqs)
	})
}

func pathRulesNotInRequests(t *testing.T, reqs []tkfaux.Request) {
	t.Helper()
	for _, r := range reqs {
		if strings.Contains(string(r.Body), pathRulesWarning) || strings.Contains(string(r.Body), "use Edit(docs/**) instead") {
			t.Fatal("the permission-rule warning reached the model")
		}
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
