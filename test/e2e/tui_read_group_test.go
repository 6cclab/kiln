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

// TestTUI_EnterRunsFullyTypedArgument: "/posture ops" typed in full runs on
// one Enter. The argument popup used to take that Enter to accept "ops"
// again, leaving the command in the input, so the user's next message was
// typed onto its end ("/posture opsUsing the Grafana MCP…").
func TestTUI_EnterRunsFullyTypedArgument(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, _ := startFaux(t, mcpPlainTextScript)
	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	defer s.Close()
	waitReady(t, s)
	s.Send("/posture ops")
	if err := s.WaitFor("ops", 3*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("enter")
	if err := s.WaitFor(regexp.MustCompile(`(?i)posture[^\n]*ops`), 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor("describe a task", 3*time.Second); err != nil {
		t.Fatalf("input not cleared after one Enter:\n%s", strings.Join(s.Rows(), "\n"))
	}
}

// TestTUI_ReadGroupCommitsBeforeStreamedReply: a held read group commits as
// soon as the reply after it starts streaming, so the calls are on screen
// above the text arriving, and the busy line no longer names the finished
// tool.
func TestTUI_ReadGroupCommitsBeforeStreamedReply(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "cat src/math.js"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "The file exports one function that adds two numbers together and nothing else at all, which is the whole module."
        chunk_delay: 250ms
`)
	s := startTUI(t, 100, 40, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	defer s.Close()
	waitReady(t, s)
	s.Send("what is in math.js")
	s.SendKey("enter")
	if err := s.WaitFor("The file", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	rows := strings.Join(s.Rows(), "\n")
	if strings.Contains(rows, "the whole module.") {
		t.Fatalf("reply finished before the mid-stream check; slow the stream down:\n%s", rows)
	}
	if !regexp.MustCompile(`bash ─+`).MatchString(rows) || !strings.Contains(rows, "cat src/math.js") {
		t.Errorf("the bash call is not on screen while the reply after it streams:\n%s", rows)
	}
	if strings.Contains(rows, "Running cat") {
		t.Errorf("the busy line still names the finished command:\n%s", rows)
	}
	waitTurnSettled(t, s)
	if n := strings.Count(strings.Join(s.Rows(), "\n"), "cat src/math.js"); n != 1 {
		t.Errorf("the bash call is on screen %d times after the turn, want 1:\n%s", n, strings.Join(s.Rows(), "\n"))
	}
}
