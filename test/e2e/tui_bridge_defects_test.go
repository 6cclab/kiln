//go:build e2e

package e2e

// Regression coverage for four transcript-output defects the qa campaign
// found in bridge.go and its subagent-panel/command-result plumbing:
//
//  1. qa/findings/20260927T000350Z-subagent-error-triplicated.json: a
//     failed subagent's error message printed three times — a bare
//     top-level "error" block (bridge.go's SubagentSink itself,
//     committing a RenderError block for every SubagentEventError, not
//     the EventFault path the finding's own design_ref guessed at — see
//     SubagentSink's doc comment for how this was actually confirmed),
//     the subagents panel row, and the parent's own "task" tool-call
//     result text. Fixed by dropping SubagentSink's own commit: the panel
//     row and the task result already say everything a failed dispatch
//     needs to say.
//  2. qa/findings/20260927T000638Z-subagent-row-no-action.json: a
//     subagent row showed only "✓ finished" once done, and nothing but
//     "starting…" while running, no matter how many tools it called.
//     Fixed by forwarding a finished tool call's primary argument and
//     result summary (agent/dispatch.go's SubagentEventTool, now reported
//     on EventToolEnd instead of EventToolStart) and the subagent's final
//     answer (SubagentEventDone.Text) to the row's own action line
//     (subagents.go's formatSubagentAction/renderSubagentRow).
//  3. qa/findings/20260927T000712Z-command-output-elbow-misaligned.json:
//     a multi-line slash-command result rendered through a "⎿ " prefix
//     that only ever touched the first row, so a command's own aligned
//     key/value columns (e.g. /status) zig-zagged. Fixed by
//     CommitCommandResult/RenderCommandResult rendering a labelled block
//     named after the command instead, every row indented uniformly.
//  4. qa/findings/20260927T000543Z-snake-case-tool-names-not-title-cased.json:
//     MapToolName/titleCase capitalized only the tool name's first rune,
//     leaving underscores in place ("bash_background" ->
//     "Bash_background"). Fixed by splitting on "_" and joining with a
//     single space before capitalizing the first letter.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestTUI_SubagentError_ReportsOnce drives testdata/faux/qa/tools-
// subagent-error.yaml (the qa campaign's own reproduction: a subagent
// dispatch whose model turn returns a non-retryable 400) and checks the
// failure appears exactly where the design wants it — the subagents
// panel's row — and nowhere else. Before the fix this screen also carried a third, contextless
// "error ───" block with the identical text right under the user's
// message; TestTUI_SubagentError_ReportsOnce would have failed against
// the pre-fix bridge.go (verified manually: reverting SubagentSink to its
// old body reintroduces the extra "error ───" row and this test's
// wantErrorBlocks==0 check fails).
func TestTUI_SubagentError_ReportsOnce(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/qa/tools-subagent-error.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))
	writeModelRolesSettings(t, proj, map[string]string{"fast": "faux/faux-2"})

	s := startTUI(t, 120, 40, proj, home, sessDir, addr,
		"--permission-mode", "acceptEdits",
	)
	waitReady(t, s)

	s.Send("check the deploy config with a subagent")
	s.SendKey("enter")

	if err := s.WaitFor(regexp.MustCompile(`subagents? finished`), 20*time.Second); err != nil {
		t.Fatalf("turn never reached a finished subagents panel: %v", err)
	}
	if err := s.WaitFor(turnSummaryPattern, 20*time.Second); err != nil {
		t.Fatal(err)
	}

	rows := s.Rows()
	joined := strings.Join(append(append([]string(nil), s.Scrollback()...), rows...), "\n")

	// Exactly one "error ───" label rule must appear (RenderError's own
	// shape) — the design has no bare top-level error block for a
	// subagent failure; a genuine parent-turn fault would still render
	// one, but nothing in this script produces one.
	errorBlocks := regexp.MustCompile(`(?m)^error ─+`).FindAllString(joined, -1)
	if len(errorBlocks) != 0 {
		t.Errorf("expected no bare top-level \"error\" block for a subagent failure, found %d:\n%s", len(errorBlocks), joined)
	}
	// The panel row still reports the failure inline ("✕ <message>").
	if !regexp.MustCompile(`✕ .*boom, subagent misbehaved`).MatchString(joined) {
		t.Errorf("subagents panel row missing its failure message:\n%s", joined)
	}
	// And only there: the task call the panel row stands for gets no
	// block of its own repeating the failure.
	if regexp.MustCompile(`(?m)^\s*task ─+`).MatchString(joined) || strings.Contains(joined, `Subagent "general-purpose" failed`) {
		t.Errorf("the failure is repeated in a task block under the panel:\n%s", joined)
	}
}

// subagentToolActionScript has a single subagent dispatch, routed to its
// own "fast"-role model (faux-2, via writeModelRolesSettings below — a
// dispatch with no "model" field inherits the PARENT's model instead,
// which would have the subagent share the top-level conversation's own
// faux-1 script queue and garble both), whose own model calls "read" once
// before answering, so its panel row has a real finished tool call to
// show a "last action" line for (finding 2), and a real final answer to
// show once done.
const subagentToolActionScript = `models:
  faux-1:
    - tool_call:
        name: task
        args: {subagent_type: "general-purpose", description: "find the bug", prompt: "read math.js and report the bug", model: "fast"}
        id: tc1
    - on_tool_result: tc1
      then:
        - text: "Fixed, thanks to the subagent."
          usage: {input: 100, output: 20}
  faux-2:
    - tool_call:
        name: read
        args: {path: "src/math.js"}
        id: r1
    - on_tool_result: r1
      then:
        # The delay gives a PTY-driven test a real window to observe the
        # panel row's "running" state (its action line already updated
        # from the just-finished read, but the subagent's own final
        # answer — and so the row's Done transition — still pending)
        # before this reply lands.
        - text: "add() subtracts instead of adding."
          usage: {input: 50, output: 10}
          delay: 600ms
`

// TestTUI_SubagentPanel_ShowsToolActionThenFinalAnswer checks the running
// row's action line names the tool it just finished plus its primary
// argument and a result summary (not a bare "→ starting…" the whole
// time), and the done row's action line is the subagent's own final
// answer, not "✓ finished".
// *qa/findings/20260927T000638Z-subagent-row-no-action.json*.
func TestTUI_SubagentPanel_ShowsToolActionThenFinalAnswer(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, subagentToolActionScript)
	writeModelRolesSettings(t, proj, map[string]string{"fast": "faux/faux-2"})

	s := startTUI(t, 120, 40, proj, home, sessDir, addr,
		"--permission-mode", "acceptEdits",
	)
	waitReady(t, s)

	s.Send("find the bug with a subagent")
	s.SendKey("enter")

	if err := s.WaitFor(regexp.MustCompile(`Read "src/math\.js"`), 5*time.Second); err != nil {
		t.Fatalf("subagent row never showed its finished tool call:\n%s", strings.Join(s.Rows(), "\n"))
	}

	if err := s.WaitFor(turnSummaryPattern, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(append(append([]string(nil), s.Scrollback()...), s.Rows()...), "\n")
	if !strings.Contains(joined, "add() subtracts instead of adding.") {
		t.Errorf("done row missing the subagent's final answer as its action line:\n%s", joined)
	}
	if regexp.MustCompile(`✓\s+finished\b`).MatchString(joined) {
		t.Errorf("done row still shows the generic \"finished\" instead of the final answer:\n%s", joined)
	}
}

// faultAfterToolScript runs one bash call, then fails the follow-up
// request with a hard 400 — the shape of the live Opus 4.8 failure, where
// the second request of a turn was rejected after a tool had already run.
const faultAfterToolScript = `model: faux-1
steps:
  - tool_call:
      name: bash
      args: {command: "echo ran-before-the-fault"}
      id: b1
  - on_tool_result: b1
    then:
      - error: {status: 400, type: invalid_request_error, message: "rejected after the tool ran"}
`

// TestTUI_FaultCommitsAfterPrecedingToolCall: the error block used to land
// ABOVE the bash block for the call that ran before it. The tool block goes
// through the app's Update loop while the fault was committed straight to
// the output queue, so the fault overtook it
// (qa/findings *error-block-above-preceding-tool). Both now go through
// Update, in order.
func TestTUI_FaultCommitsAfterPrecedingToolCall(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, faultAfterToolScript)
	s := startTUI(t, 120, 40, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	waitReady(t, s)

	s.Send("run it")
	s.SendKey("enter")
	if err := s.WaitFor(regexp.MustCompile(`rejected after the tool\s+ran`), 20*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor(turnSummaryPattern, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(append(append([]string(nil), s.Scrollback()...), s.Rows()...), "\n")
	tool := strings.Index(joined, "ran-before-the-fault")
	fault := strings.Index(joined, "Bad Request (400)")
	if tool < 0 || fault < 0 || tool > fault {
		t.Fatalf("want the bash block (at %d) above the error (at %d):\n%s", tool, fault, joined)
	}
}

// TestTUI_PromptDigitDoesNotLeakIntoEditor: answering a permission prompt
// with a digit must not also type that digit into the input box. A live
// run ended with "› 2" in the editor right after "2" answered an
// outside-workspace read prompt.
func TestTUI_PromptDigitDoesNotLeakIntoEditor(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "model: faux-1\nsteps:\n  - tool_call: {name: read, args: {path: \"" + outside + "\"}, id: r1}\n  - on_tool_result: r1\n    then:\n      - text: \"read it\"\n"
	proj, home, sessDir, addr, _ := tuiFixture(t, script)
	s := startTUI(t, 120, 40, proj, home, sessDir, addr)
	waitReady(t, s)
	s.Send("read the outside file")
	s.SendKey("enter")
	if err := s.WaitFor("outside the workspace", 20*time.Second); err != nil {
		t.Fatal(err)
	}
	s.Send("2")
	if err := s.WaitFor("read it", 20*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor(turnSummaryPattern, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, row := range s.Rows() {
		if strings.Contains(row, "›") && strings.Contains(row, "2") && !strings.Contains(row, "describe a task") {
			t.Fatalf("the prompt's answer leaked into the editor: %q\n%s", row, strings.Join(s.Rows(), "\n"))
		}
	}
}

// TestTUI_BadModelRoleShowsAsNote: a model role that does not resolve is
// reported inside the TUI, under the banner. It used to go to stderr before
// the fullscreen TUI started, where it stayed hidden until exit.
func TestTUI_BadModelRoleShowsAsNote(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, "model: faux-1\nsteps:\n  - text: \"ok\"\n")
	writeModelRolesSettings(t, proj, map[string]string{"heavy": "faux/faux-9"})
	s := startTUI(t, 120, 40, proj, home, sessDir, addr)
	waitReady(t, s)
	if err := s.WaitFor(regexp.MustCompile(`Model role heavy: faux/faux-9 is not among the available models; subagents asking for it run on the current model\.`), 5*time.Second); err != nil {
		t.Fatalf("no startup note for the bad role:\n%s", strings.Join(s.Rows(), "\n"))
	}
}

// TestPrint_BadModelRoleWarnsOnStderr: print mode has no TUI, so the same
// warning still goes to stderr.
func TestPrint_BadModelRoleWarnsOnStderr(t *testing.T) {
	addr, _ := startFaux(t, "model: faux-1\nsteps:\n  - text: \"ok\"\n")
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeModelRolesSettings(t, proj, map[string]string{"heavy": "faux/faux-9"})
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hi")
	if !strings.Contains(res.Stderr, "kiln: Model role heavy: faux/faux-9") {
		t.Errorf("stderr = %q, want the role warning", res.Stderr)
	}
}
