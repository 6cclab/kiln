//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stopPayloads reads the payload lines stop-block-once.sh appended.
func stopPayloads(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the hook never ran (%s): %v", path, err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestHooks_StopBlock_ContinuesTheTurn: as in Claude Code, a Stop hook
// that exits 2 keeps the turn going. The model gets one more turn with the
// hook's reason, the user sees the reason, and the hook's next call has
// stop_hook_active true, so a hook that checks it lets the turn end. kiln
// used to only report the block and end the turn.
func TestHooks_StopBlock_ContinuesTheTurn(t *testing.T) {
	addr, srv := startFaux(t, "model: faux-1\nsteps:\n  - text: \"first reply\"\n    end_turn: true\n  - text: \"second reply after the feedback\"\n    end_turn: true\n")
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	payloads := filepath.Join(t.TempDir(), "stop.jsonl")
	command := fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", payloads, hookScript(t, "stop-block-once.sh"))
	writeHookSettings(t, proj, "Stop", "", command)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hello", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("faux recorded %d requests, want 2: the block must give the model one more turn", len(reqs))
	}
	if !strings.Contains(string(reqs[1].Messages), "run the tests first") {
		t.Errorf("second request = %s, want the hook's reason in it", reqs[1].Messages)
	}
	// The exit-2 reason is prefixed with the command, as Claude Code's own
	// hook runner does - kiln used to send the hook's stderr alone.
	want := fmt.Sprintf("Stop hook asked to continue: [%s]: run the tests first", command)
	if !strings.Contains(res.Stderr, want) {
		t.Errorf("stderr = %q, want %q", res.Stderr, want)
	}
	if !strings.Contains(res.Stdout, "second reply after the feedback") {
		t.Errorf("stdout = %q, want the reply written after the feedback", res.Stdout)
	}
	lines := stopPayloads(t, payloads)
	if len(lines) != 2 || !strings.Contains(lines[0], `"stop_hook_active":false`) || !strings.Contains(lines[1], `"stop_hook_active":true`) {
		t.Fatalf("Stop payloads = %q, want two: stop_hook_active false, then true", lines)
	}
	if !strings.Contains(lines[0], `"last_assistant_message":"first reply"`) {
		t.Errorf("first payload = %s, want last_assistant_message", lines[0])
	}
}

// TestHooks_StopBlock_JSONDecisionContinues: {"decision": "block",
// "reason": ...} on stdout continues the turn like exit 2, and
// {"continue": false} ends it with stopReason shown.
func TestHooks_StopBlock_JSONDecisionContinues(t *testing.T) {
	t.Run("decision block", func(t *testing.T) {
		addr, srv := startFaux(t, "model: faux-1\nsteps:\n  - text: \"first reply\"\n    end_turn: true\n  - text: \"second reply\"\n    end_turn: true\n")
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		hook := filepath.Join(t.TempDir(), "block.sh")
		body := "#!/bin/bash\ninput=$(cat)\ncase \"$input\" in *'\"stop_hook_active\":true'*) exit 0;; esac\necho '{\"decision\":\"block\",\"reason\":\"coverage dropped\"}'\n"
		if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		writeHookSettings(t, proj, "Stop", "", hook)
		res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hello", "--output-format", "text")
		if res.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
		}
		if reqs := srv.Requests(); len(reqs) != 2 || !strings.Contains(string(reqs[1].Messages), "coverage dropped") {
			t.Fatalf("%d requests, want 2 with the reason in the second", len(reqs))
		}
	})
	t.Run("continue false", func(t *testing.T) {
		addr, srv := startFaux(t, "model: faux-1\nsteps:\n  - text: \"first reply\"\n    end_turn: true\n  - text: \"must not be requested\"\n    end_turn: true\n")
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		hook := filepath.Join(t.TempDir(), "stop.sh")
		body := "#!/bin/bash\ncat >/dev/null\necho '{\"continue\":false,\"stopReason\":\"build is green\"}'\n"
		if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		writeHookSettings(t, proj, "Stop", "", hook)
		res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hello", "--output-format", "text")
		if res.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
		}
		if n := len(srv.Requests()); n != 1 {
			t.Fatalf("%d requests, want 1: continue false ends the turn", n)
		}
		if !strings.Contains(res.Stderr, "Stop hook stopped the turn: build is green") {
			t.Errorf("stderr = %q, want the stopReason shown", res.Stderr)
		}
	})
}

// TestHooks_StopBlock_TwoHooksBothContributeTheirReason: two Stop hooks
// that each block with a different reason run together
// (RunHooksConcurrently), and both reasons reach the model and the user -
// not just the first one's, the way a sequential chain would only ever
// report.
func TestHooks_StopBlock_TwoHooksBothContributeTheirReason(t *testing.T) {
	addr, srv := startFaux(t, "model: faux-1\nsteps:\n  - text: \"first reply\"\n    end_turn: true\n  - text: \"second reply after the feedback\"\n    end_turn: true\n")
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	dir := t.TempDir()
	// Each hook blocks only once - checking stop_hook_active, exactly as
	// stop-block-once.sh does - so the continued turn's own Stop call lets
	// both hooks pass and the run ends, instead of looping forever.
	first := filepath.Join(dir, "first.sh")
	firstBody := "#!/bin/bash\ninput=$(cat)\ncase \"$input\" in *'\"stop_hook_active\":true'*) exit 0;; esac\necho \"lint failed\" >&2\nexit 2\n"
	if err := os.WriteFile(first, []byte(firstBody), 0o755); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "second.sh")
	secondBody := "#!/bin/bash\ninput=$(cat)\ncase \"$input\" in *'\"stop_hook_active\":true'*) exit 0;; esac\necho '{\"decision\":\"block\",\"reason\":\"coverage dropped\"}'\n"
	if err := os.WriteFile(second, []byte(secondBody), 0o755); err != nil {
		t.Fatal(err)
	}
	gapWriteHooks(t, proj, "Stop", "", first, second)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hello", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("faux recorded %d requests, want 2: both blocks must give the model one more turn, not one each", len(reqs))
	}
	secondRequest := string(reqs[1].Messages)
	if !strings.Contains(secondRequest, fmt.Sprintf("[%s]: lint failed", first)) {
		t.Errorf("second request = %s, want the first hook's reason in it", secondRequest)
	}
	if !strings.Contains(secondRequest, "coverage dropped") {
		t.Errorf("second request = %s, want the second hook's reason in it", secondRequest)
	}
	if !strings.Contains(res.Stderr, fmt.Sprintf("Stop hook asked to continue: [%s]: lint failed", first)) {
		t.Errorf("stderr = %q, want the first hook's reason shown to the user", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Stop hook asked to continue: coverage dropped") {
		t.Errorf("stderr = %q, want the second hook's reason shown to the user", res.Stderr)
	}
}

// TestHooks_StopBlock_AdditionalContextNeverReachesTheModel: a Stop hook
// can combine a block with hookSpecificOutput.additionalContext in the
// same JSON reply. The public hooks docs describe additionalContext as
// continuing the conversation with extra context; in the shipped Claude
// Code it never reaches the model, docs notwithstanding - kiln follows
// what ships. Only the block's own reason must reach the model.
func TestHooks_StopBlock_AdditionalContextNeverReachesTheModel(t *testing.T) {
	addr, srv := startFaux(t, "model: faux-1\nsteps:\n  - text: \"first reply\"\n    end_turn: true\n  - text: \"second reply after the feedback\"\n    end_turn: true\n")
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	hook := filepath.Join(t.TempDir(), "block-with-context.sh")
	body := "#!/bin/bash\ninput=$(cat)\ncase \"$input\" in *'\"stop_hook_active\":true'*) exit 0;; esac\n" +
		`echo '{"decision":"block","reason":"coverage dropped","hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"internal build id 7f3a9c"}}'` + "\n"
	if err := os.WriteFile(hook, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHookSettings(t, proj, "Stop", "", hook)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hello", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("faux recorded %d requests, want 2", len(reqs))
	}
	secondRequest := string(reqs[1].Messages)
	if !strings.Contains(secondRequest, "coverage dropped") {
		t.Errorf("second request = %s, want the block's own reason in it", secondRequest)
	}
	if strings.Contains(secondRequest, "internal build id 7f3a9c") {
		t.Errorf("second request = %s, must not carry the hook's additionalContext", secondRequest)
	}
	if strings.Contains(res.Stderr, "internal build id 7f3a9c") {
		t.Errorf("stderr = %q, must not show the hook's additionalContext", res.Stderr)
	}
}

// TestHooks_StopBlock_AlwaysBlockingIsBoundedByMaxTurns: a Stop hook that
// blocks whatever stop_hook_active says would keep a run going for good,
// as it would in Claude Code. In -p, --max-turns bounds it: kiln counts
// each continued turn.
func TestHooks_StopBlock_AlwaysBlockingIsBoundedByMaxTurns(t *testing.T) {
	addr, srv := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeHookSettings(t, proj, "Stop", "", hookScript(t, "block-exit2.sh"))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hello", "--output-format", "text", "--max-turns", "3")
	if res.Code == 0 {
		t.Fatalf("exit code 0, want the --max-turns failure; stderr=%s", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "stopped after 3 turns (--max-turns)") {
		t.Errorf("stderr = %q, want the --max-turns stop", res.Stderr)
	}
	if n := len(srv.Requests()); n != 3 {
		t.Errorf("faux recorded %d requests, want 3", n)
	}
}

// TestHooks_SubagentStopBlock_ContinuesTheSubagent: a SubagentStop hook
// that exits 2 keeps the subagent working, as in Claude Code: the
// subagent gets one more turn with the reason, and the parent gets the
// report written after it.
func TestHooks_SubagentStopBlock_ContinuesTheSubagent(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call:
      name: task
      args: {subagent_type: "general-purpose", description: "look something up", prompt: "find X"}
      id: tc1
  - text: "draft report"
    end_turn: true
  - text: "checked report"
    end_turn: true
  - on_tool_result: tc1
    then:
      - text: "Done."
`
	addr, srv := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	payloads := filepath.Join(t.TempDir(), "subagent-stop.jsonl")
	writeHookSettings(t, proj, "SubagentStop", "", fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", payloads, hookScript(t, "stop-block-once.sh")))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "look something up for me", "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	if len(reqs) != 4 {
		t.Fatalf("faux recorded %d requests, want 4 (parent, subagent, subagent again, parent)", len(reqs))
	}
	if !strings.Contains(string(reqs[2].Messages), "run the tests first") {
		t.Errorf("the subagent's second request = %s, want the hook's reason", reqs[2].Messages)
	}
	if !strings.Contains(string(reqs[3].Messages), "checked report") || strings.Contains(string(reqs[3].Messages), "draft report") {
		t.Errorf("the parent's last request = %s, want the checked report as the task result", reqs[3].Messages)
	}
	lines := stopPayloads(t, payloads)
	if len(lines) != 2 || !strings.Contains(lines[0], `"stop_hook_active":false`) || !strings.Contains(lines[1], `"stop_hook_active":true`) {
		t.Fatalf("SubagentStop payloads = %q, want two: stop_hook_active false, then true", lines)
	}
}
