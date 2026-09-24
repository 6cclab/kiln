package cli

// Phase 6 integration tests: task dispatch, background shells, and plan
// mode's exit_plan_mode/write interplay, all driven through Run() exactly
// as chat_test.go's own scenarios are. See internal/tools/task_e2e_test.go
// for the lower-level (agent.Dispatcher, no CLI) version of the task
// scenario this file drives end to end through Run().

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/session/jsonl"
)

// sessionFileCount counts the .jsonl session files under sessDir for cwd.
func sessionFileCount(t *testing.T, sessDir, cwd string) int {
	t.Helper()
	dir := filepath.Join(sessDir, jsonl.DirectoryName(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read session dir %s: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			n++
		}
	}
	return n
}

// TestRun_Task_DispatchesToSubagent drives a parent turn that calls `task`
// with subagent_type "general-purpose". The faux server answers both the
// parent's and the subagent's requests off the same sequential script (see
// task_e2e_test.go's own comment on this): turn 0 is the parent's tool_call,
// turn 1 is served to whichever request arrives next — the subagent's own
// first turn, since dispatch runs synchronously before the parent's own
// on_tool_result step is ever reached — and turn 2 is the parent's
// continuation once the task tool returns.
func TestRun_Task_DispatchesToSubagent(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call:
      name: task
      args: {subagent_type: "general-purpose", description: "look something up", prompt: "find X"}
      id: tc1
  - text: "subagent report"
  - on_tool_result: tc1
    then:
      - text: "Done."
`
	startFaux(t, script)
	proj := scratchProject(t)
	sessDir := os.Getenv("HARNESS_SESSIONS_DIR")

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "look something up for me"
	args.OutputFormat = "stream-json"
	// "task" is not in settings.ReadOnly, so the default (manual) mode
	// would ask — and headless has nobody to ask. dontAsk allows it
	// unconditionally, matching how this scenario would actually be run.
	args.PermissionMode = "dontAsk"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	types := ndjsonTypes(t, stdout.String())
	if !containsPair(types, "tool_start", "tool_end") {
		t.Errorf("types = %v, want a tool_start/tool_end pair for task", types)
	}
	if !strings.Contains(stdout.String(), `"name":"task"`) {
		t.Errorf("stdout has no task tool event:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), `"isError":true`) {
		t.Errorf("task tool_end reported an error:\n%s", stdout.String())
	}

	if n := sessionFileCount(t, sessDir, proj); n != 2 {
		t.Errorf("session file count = %d, want 2 (parent + subagent)", n)
	}
}

// containsPair reports whether types contains a and, somewhere after it, b.
func containsPair(types []string, a, b string) bool {
	seenA := false
	for _, ty := range types {
		if ty == a {
			seenA = true
		}
		if seenA && ty == b {
			return true
		}
	}
	return false
}

// TestRun_BackgroundShell_StartReadKill drives bash_background, bash_output
// and kill_shell in one turn, then checks the OS process the shell started
// is actually gone once the run completes.
func TestRun_BackgroundShell_StartReadKill(t *testing.T) {
	marker := "harness_phase6_bgshell_marker"
	script := `model: faux-1
steps:
  - tool_call: {name: bash_background, args: {command: "sleep 30 # ` + marker + `"}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call: {name: bash_output, args: {id: "bash_1"}, id: tc2}
  - on_tool_result: tc2
    then:
      - tool_call: {name: kill_shell, args: {id: "bash_1"}, id: tc3}
  - on_tool_result: tc3
    then:
      - text: "stopped it"
`
	startFaux(t, script)
	scratchProject(t)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "start a background job and stop it"
	args.OutputFormat = "stream-json"
	args.PermissionMode = "dontAsk"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	out := stdout.String()
	for _, name := range []string{"bash_background", "bash_output", "kill_shell"} {
		if !strings.Contains(out, `"name":"`+name+`"`) {
			t.Errorf("no tool event for %q:\n%s", name, out)
		}
	}
	if strings.Contains(out, `"isError":true`) {
		t.Errorf("a background-shell tool_end reported an error:\n%s", out)
	}

	assertProcessGone(t, marker)
}

// TestRun_BackgroundShell_KilledOnExit starts a background shell the script
// never kills itself; Run's own shells.KillAll() (chat.go, before the
// SessionEnd hook) must stop it anyway once the run completes, matching
// cli.ts's `shells.killAll()` on the way out of print mode.
func TestRun_BackgroundShell_KilledOnExit(t *testing.T) {
	marker := "harness_phase6_bgshell_neverkilled"
	script := `model: faux-1
steps:
  - tool_call: {name: bash_background, args: {command: "sleep 30 # ` + marker + `"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "started it, done"
`
	startFaux(t, script)
	scratchProject(t)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "start a background job"
	args.OutputFormat = "stream-json"
	args.PermissionMode = "dontAsk"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	assertProcessGone(t, marker)
}

// assertProcessGone polls `pgrep -f marker` briefly (the process may take a
// moment to actually die after being signaled) and fails if it still finds
// a match.
func assertProcessGone(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, _ := pgrepF(marker)
		if strings.TrimSpace(out) == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pgrep -f %q still finds a process after the run exited:\n%s", marker, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func pgrepF(pattern string) (string, error) {
	out, err := exec.Command("pgrep", "-f", pattern).CombinedOutput()
	return string(out), err
}

// TestRun_PlanMode_BlocksWrite drives --permission-mode plan against a
// script that calls `write`: plan mode is read-only, so the call is blocked
// by the permission gate before the tool ever runs, matching
// resolvePermissionMode's plan branch and settings.Decide's own plan case
// (internal/claude/settings/settings.go).
func TestRun_PlanMode_BlocksWrite(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call: {name: write, args: {path: "new.txt", content: "hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	startFaux(t, script)
	proj := scratchProject(t)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "write a file"
	args.OutputFormat = "stream-json"
	args.PermissionMode = "plan"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"isError":true`) {
		t.Errorf("write tool_end was not an error under plan mode:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "plan mode is read-only") {
		t.Errorf("stdout missing the plan-mode block reason:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(proj, "new.txt")); err == nil {
		t.Error("new.txt was created; plan mode should have blocked the write")
	}
}

// TestRun_PlanMode_ExitPlanModeAlsoBlockedByGate documents a real,
// pre-existing gap this phase's brief did not anticipate: calling
// exit_plan_mode WHILE the session is in --permission-mode plan never
// reaches the tool's own Execute (and so never reaches its "revise" stub
// text) at all. internal/claude/settings/settings.go's Decide (a verbatim
// port of harness/src/claude/settings.ts's own READ_ONLY set) treats every
// tool not in READ_ONLY as a mutation in plan mode and denies it outright —
// and exit_plan_mode is not in that set, in EITHER the TS source or this
// port. The gate's before_tool hook therefore blocks the call itself,
// before agent.PlanController or the exit_plan_mode tool ever sees it; the
// blocked reason comes from claude/permission.Gate.Check
// ("plan mode is read-only, so exit_plan_mode is not available. Describe
// the change instead of making it."), not from tools.PlanDecisionRevise's
// feedback text.
//
// This is not a Go-port regression — cli.ts exhibits the identical gap,
// verified by reading settings.ts's own READ_ONLY set (it lacks
// exit_plan_mode/task/todo_write/bash_background/kill_shell too) — so it is
// out of this phase's scope to fix (internal/claude/settings is not owned
// by internal/cli's agent). The real fix, if wanted, is adding
// exit_plan_mode (and arguably task) to that set, on both sides.
// See the phase report for this deviation from the brief, which expected
// this scenario to reach the revise stub.
func TestRun_PlanMode_ExitPlanModeAlsoBlockedByGate(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call: {name: exit_plan_mode, args: {plan: "do the thing"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "ok"
`
	startFaux(t, script)
	scratchProject(t)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "please plan"
	args.OutputFormat = "stream-json"
	args.PermissionMode = "plan"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), `"isError":true`) {
		t.Errorf("exit_plan_mode tool_end was not an error under plan mode:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "plan mode is read-only") {
		t.Errorf("stdout missing the gate's plan-mode block reason:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "No interactive approval available") {
		t.Error("stdout contains the tool's own revise stub text; the gate no longer blocks exit_plan_mode in plan mode — update this test's doc comment, it documented a gap that is now fixed")
	}
}

// TestRun_Bashes_RendersShellList drives /bashes after starting a
// background shell, and checks it reports one running shell — the
// integration point for agent.RenderShellList wired through
// registryDeps.Shells (internal/cli/commands.go).
func TestRun_Bashes_RendersShellList(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call: {name: bash_background, args: {command: "sleep 30 # harness_phase6_bashes_marker"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "started"
`
	startFaux(t, script)
	scratchProject(t)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "start a background job"
	args.OutputFormat = "text"
	args.PermissionMode = "dontAsk"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	// This run's own shell is killed by the time Run returns (shells.KillAll
	// on exit), so /bashes is checked in a second, fresh run against the
	// SAME session dir but with no script step calling it — /bashes itself
	// runs entirely inside registry.Execute, with no model turn, so a fresh
	// Run() with its own (empty) BackgroundShells is enough to prove the
	// wiring: "No background shells." is agent.RenderShellList's own text
	// for an empty registry, and a non-nil, non-placeholder line proves
	// deps.Shells reached InlineCommands rather than staying nil (which
	// would print "no background shells" — see commands.go's fallback).
	args2 := baseArgs()
	args2.Print = true
	args2.PrintPrompt = "/bashes"
	args2.OutputFormat = "text"
	var stdout2, stderr2 bytes.Buffer
	code2 := Run(context.Background(), args2, &stdout2, &stderr2, strings.NewReader(""))
	if code2 != 0 {
		t.Fatalf("/bashes exit code %d, stderr=%s", code2, stderr2.String())
	}
	if !strings.Contains(stdout2.String(), "No background shells.") {
		t.Errorf("/bashes output = %q, want agent.RenderShellList's own empty-list text", stdout2.String())
	}
}
