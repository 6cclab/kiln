//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrint_ReadMissingFile_DidYouMeanHint drives the real kiln binary
// (print mode, stream-json) through a faux model that calls the read tool
// on "notes final.txt" when the project only has a file spelled with
// U+00A0 NO-BREAK SPACE in that exact spot ("notes final.txt") —
// the same class of bug a real session hit on a Desktop screenshot named
// with U+202F NARROW NO-BREAK SPACE before "AM": the read failed with a
// bare "no such file or directory" and no hint, costing four turns before
// a human pointed out the real spelling.
//
// This asserts the fix end to end. tool_end stream-json events carry only
// isError, not the result text (internal/cli/print.go's StreamEvent), so
// what proves the model actually sees the hint is faux's second recorded
// request: the tool_result content kiln sends back for the read call, in
// the next turn's conversation history. The session's own stored tool
// result is checked the same way, as a second, independent witness.
func TestPrint_ReadMissingFile_DidYouMeanHint(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	real := "notes" + " " + "final.txt"
	if err := os.WriteFile(filepath.Join(proj, real), []byte("the real contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := `model: faux-1
steps:
  - tool_call: {name: read, args: {path: "notes final.txt"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "Got the result."
`
	addr, srv := startFaux(t, script)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "read the notes file",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	var sawReadToolEnd, readIsError bool
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse stream-json line %q: %v", line, err)
		}
		if ev["type"] == "tool_end" && ev["name"] == "read" {
			sawReadToolEnd = true
			readIsError, _ = ev["isError"].(bool)
		}
	}
	if !sawReadToolEnd {
		t.Fatalf("no tool_end{name:read} event in stream-json output:\n%s", res.Stdout)
	}
	if !readIsError {
		t.Fatalf("expected the read of the misspelled path to fail (isError:true)")
	}

	// Faux's second recorded request is the next turn, carrying the read
	// tool's result back to the model: this is what the model actually
	// reads, and it must contain the hint, not a bare OS error.
	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 recorded requests (initial + after tool_result), got %d", len(reqs))
	}
	secondTurn := string(reqs[1].Messages)
	if !strings.Contains(secondTurn, "Did you mean") {
		t.Errorf("second request's messages have no Did-you-mean hint:\n%s", secondTurn)
	}
	if !strings.Contains(secondTurn, "U+00A0 NO-BREAK SPACE") {
		t.Errorf("second request's messages do not call out U+00A0 specifically:\n%s", secondTurn)
	}

	// The session's own stored tool result (what a resumed session, or a
	// human reading the .jsonl, would see) carries the same hint.
	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Did you mean") {
		t.Errorf("session file has no Did-you-mean hint in its stored tool result:\n%s", raw)
	}
}

// TestPrint_ReadMissingFile_DidYouMeanHint_DeniedDirectory is the negative
// twin of the test above: the near-identical file this time sits in a
// directory outside the project root (and never added via --add-dir), so
// the permission gate would not let a plain read reach it without asking
// first (internal/claude/permission's WithinRoots/WithinReadOnlyRoots).
// internal/cli/chat.go wires Env.DidYouMeanDirAllowed to exactly that
// check, so the hint must stay silent here even though DidYouMeanHint's own
// directory scan would otherwise find the match - proving the hint cannot
// be used to enumerate a directory the tool itself couldn't read.
func TestPrint_ReadMissingFile_DidYouMeanHint_DeniedDirectory(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	outside := t.TempDir() // a sibling directory, never added as a root

	real := "secret" + " " + "notes.txt" // U+00A0 NO-BREAK SPACE, same mismatch as the positive test
	if err := os.WriteFile(filepath.Join(outside, real), []byte("outside contents"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := `model: faux-1
steps:
  - tool_call: {name: read, args: {path: "` + filepath.Join(outside, "secret notes.txt") + `"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "Got the result."
`
	addr, srv := startFaux(t, script)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "read the outside notes file",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 recorded requests (initial + after tool_result), got %d", len(reqs))
	}
	secondTurn := string(reqs[1].Messages)
	if strings.Contains(secondTurn, "Did you mean") {
		t.Errorf("second request's messages leaked a Did-you-mean hint for a directory outside the gate's roots:\n%s", secondTurn)
	}
}
