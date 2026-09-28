//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestTUI_ConsecutiveReadsCommitAsOneBlock: two file reads in a row commit
// as one "read" block, one row per file with its size, with the paths
// relative to the project even though the model passed absolute ones.
// ctrl+o expands to each call's own block; toggling back renders the
// group again, identically (the replay path groups the same way).
func TestTUI_ConsecutiveReadsCommitAsOneBlock(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	abs := filepath.Join(proj, "src", "math.js")
	addr, _ := startFaux(t, fmt.Sprintf(`model: faux-1
steps:
  - tool_call: {name: read, args: {path: %q}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call: {name: read, args: {path: %q}, id: tc2}
  - on_tool_result: tc2
    then:
      - text: "Read both."
`, abs, abs))

	s := startTUI(t, 100, 40, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	defer s.Close()
	waitReady(t, s)
	s.Send("read it twice")
	s.SendKey("enter")
	if err := s.WaitFor("Read both.", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, s)

	collapsed := strings.Join(s.Rows(), "\n")
	if !regexp.MustCompile(`read ─+\s+2 files`).MatchString(collapsed) {
		t.Errorf("no grouped read block with \"2 files\":\n%s", collapsed)
	}
	if n := strings.Count(collapsed, "src/math.js · 7 lines"); n != 2 {
		t.Errorf("want 2 rows \"src/math.js · 7 lines\", got %d:\n%s", n, collapsed)
	}
	if strings.Contains(collapsed, proj) {
		t.Errorf("collapsed view shows the absolute project path:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "0.0s") {
		t.Errorf("collapsed view shows a 0.0s duration:\n%s", collapsed)
	}

	s.SendKey("ctrl+o")
	if err := s.WaitFor("Showing detailed transcript · ctrl+o to toggle", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if verbose := strings.Join(s.Rows(), "\n"); strings.Count(verbose, "→") < 2 {
		t.Errorf("verbose view does not show each read's own output:\n%s", verbose)
	}
	s.SendKey("ctrl+o")
	if err := s.WaitFor(regexp.MustCompile(`read ─+\s+2 files`), 3*time.Second); err != nil {
		t.Fatalf("toggling back lost the grouped block: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
}

// TestTUI_BashRowDropsCdIntoProject: a committed bash block shows the
// command without its leading "cd <project> &&" — the command runs there
// anyway — and ctrl+o shows it exactly as run.
func TestTUI_BashRowDropsCdIntoProject(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, _ := startFaux(t, fmt.Sprintf(`model: faux-1
steps:
  - tool_call: {name: bash, args: {command: %q}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "Ran it."
`, "cd "+proj+" && echo hi"))

	s := startTUI(t, 100, 40, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	defer s.Close()
	waitReady(t, s)
	s.Send("run it")
	s.SendKey("enter")
	if err := s.WaitFor("Ran it.", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	waitTurnSettled(t, s)
	collapsed := strings.Join(s.Rows(), "\n")
	if !regexp.MustCompile(`bash ─+[^\n]*\n\s*echo hi\s*\n`).MatchString(collapsed) {
		t.Errorf("bash row is not just \"echo hi\":\n%s", collapsed)
	}
	s.SendKey("ctrl+o")
	if err := s.WaitFor("Showing detailed transcript · ctrl+o to toggle", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if verbose := strings.Join(s.Rows(), "\n"); !strings.Contains(verbose, "&& echo hi") {
		t.Errorf("verbose view lost the command as run:\n%s", verbose)
	}
}
