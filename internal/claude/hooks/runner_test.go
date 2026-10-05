package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(name string) string {
	abs, _ := filepath.Abs(filepath.Join("testdata", "hooks", name))
	return abs
}

// writeScript writes an executable shell script into dir and returns its
// path, for cases not covered by the checked-in fixtures.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/bin/bash\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func cfg(command string, timeout int) Config {
	return Config{
		PreToolUse: []Matcher{{MatcherPattern: "Bash", Hooks: []Command{{Type: "command", Command: command, Timeout: timeout}}}},
	}
}

func run(c Config, toolInput map[string]any, dir string) Outcome {
	if toolInput == nil {
		toolInput = map[string]any{"command": "git status"}
	}
	return RunHooks(RunOptions{
		Config:      c,
		Event:       PreToolUse,
		ToolName:    "bash",
		HasToolName: true,
		Payload:     Payload{SessionID: "t", Cwd: dir, ToolName: "bash", ToolInput: toolInput},
	})
}

func TestRunHooksPassesPayloadOnStdin(t *testing.T) {
	// The rtk hook reads .tool_input.command; if the payload shape is
	// wrong it exits silently and every rewrite stops happening.
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	dir := t.TempDir()
	path := writeScript(t, dir, "echo-cmd.sh", `jq -r '.tool_input.command'`)
	out := run(cfg(path, 0), nil, dir)
	if len(out.Context) != 1 || out.Context[0] != "git status" {
		t.Errorf("got %v", out.Context)
	}
}

func TestRunHooksAppliesUpdatedInput(t *testing.T) {
	dir := t.TempDir()
	out := run(cfg(fixture("rewrite-updated-input.sh"), 0), nil, dir)
	if out.UpdatedInput["command"] != "rtk git status" {
		t.Errorf("got %v", out.UpdatedInput)
	}
	if out.Blocked != nil {
		t.Errorf("expected no block, got %v", out.Blocked)
	}
}

func TestRunHooksTreatsNonJSONStdoutAsContext(t *testing.T) {
	dir := t.TempDir()
	out := run(cfg(fixture("context-plaintext.sh"), 0), nil, dir)
	if len(out.Context) != 1 || out.Context[0] != "you have 2 unread messages" {
		t.Errorf("got %v", out.Context)
	}
	if out.UpdatedInput != nil {
		t.Errorf("expected no updated input, got %v", out.UpdatedInput)
	}
}

func TestRunHooksAdditionalContextFromJSON(t *testing.T) {
	dir := t.TempDir()
	out := run(cfg(fixture("context-json.sh"), 0), nil, dir)
	if len(out.Context) != 1 || out.Context[0] != "extra context from hook" {
		t.Errorf("got %v", out.Context)
	}
}

func TestRunHooksBlocksOnExit2(t *testing.T) {
	dir := t.TempDir()
	out := run(cfg(fixture("block-exit2.sh"), 0), nil, dir)
	if out.Blocked == nil || out.Blocked.Reason != "not allowed here" {
		t.Errorf("got %v", out.Blocked)
	}
}

func TestRunHooksBlocksOnExplicitDenyDecision(t *testing.T) {
	dir := t.TempDir()
	out := run(cfg(fixture("deny-json.sh"), 0), nil, dir)
	if out.Blocked == nil || out.Blocked.Reason != "policy" {
		t.Errorf("got %v", out.Blocked)
	}
}

func TestRunHooksDoesNotBlockOnOrdinaryNonZeroExit(t *testing.T) {
	// A hook exiting 1 (like `rtk rewrite` when there is nothing to
	// rewrite) must not be treated as a block.
	dir := t.TempDir()
	out := run(cfg(fixture("stderr-notice.sh"), 0), nil, dir)
	if out.Blocked != nil {
		t.Errorf("expected no block, got %v", out.Blocked)
	}
	found := false
	for _, n := range out.Notices {
		if n == "warning" || contains(n, "warning") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a notice mentioning 'warning', got %v", out.Notices)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRunHooksSurvivesEarlyExitWithoutReadingStdin(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "early-exit.sh", "exit 0")
	out := run(cfg(path, 0), nil, dir)
	if out.Blocked != nil {
		t.Errorf("expected no block, got %v", out.Blocked)
	}
	if len(out.Context) != 0 {
		t.Errorf("expected no context, got %v", out.Context)
	}
}

func TestRunHooksKillsHookExceedingTimeout(t *testing.T) {
	dir := t.TempDir()
	started := time.Now()
	out := run(cfg(fixture("sleep-forever.sh"), 1), nil, dir)
	if time.Since(started) > 10*time.Second {
		t.Errorf("did not kill the hook in time: took %v", time.Since(started))
	}
	found := false
	for _, n := range out.Notices {
		if contains(n, "timed out") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a timeout notice, got %v", out.Notices)
	}
	// A hung hook must not block the call - it decided nothing.
	if out.Blocked != nil {
		t.Errorf("expected no block, got %v", out.Blocked)
	}

	// The child `sleep` must actually be gone, not just detached from us.
	time.Sleep(300 * time.Millisecond)
	cmd := exec.Command("pgrep", "-f", "sleep-forever.sh")
	if err := cmd.Run(); err == nil {
		t.Error("sleep-forever.sh child process is still running after timeout")
	}
}

// TestRunHooksCtxCancelKillsHookPromptly asserts that cancelling the ctx
// passed in RunOptions kills a running hook immediately, process group and
// all, rather than waiting out its (here, deliberately long) timeout. Fails
// without the ctx/select wiring in runCommand: before that, cancelling ctx
// does nothing and the test times out waiting on the hook's own timeout.
func TestRunHooksCtxCancelKillsHookPromptly(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	out := RunHooks(RunOptions{
		// A timeout far longer than the cancel delay above: if this is what
		// actually stops the hook, the test takes ~30s and fails the
		// promptness check below, rather than passing for the wrong reason.
		Config:      cfg(fixture("sleep-forever.sh"), 30),
		Event:       PreToolUse,
		ToolName:    "bash",
		HasToolName: true,
		Payload:     Payload{SessionID: "t", Cwd: dir, ToolName: "bash", ToolInput: map[string]any{"command": "x"}},
		Ctx:         ctx,
	})
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("ctx cancellation did not kill the hook promptly: took %v", elapsed)
	}
	if !out.Cancelled {
		t.Errorf("expected Outcome.Cancelled, got %+v", out)
	}
	if out.Blocked != nil {
		t.Errorf("a cancelled hook must not be reported as Blocked: %v", out.Blocked)
	}

	// The child `sleep` must actually be gone, not just detached from us.
	time.Sleep(300 * time.Millisecond)
	cmd := exec.Command("pgrep", "-f", "sleep-forever.sh")
	if err := cmd.Run(); err == nil {
		t.Error("sleep-forever.sh child process is still running after ctx cancellation")
	}
}

// TestGuardToolCallCtxCancelDoesNotRunTheTool asserts GuardToolCall's
// contract for a PreToolUse hook killed by ctx cancellation: it returns an
// error (so Lane.invokeBeforeTool treats the call as refused, the same path
// an erroring hook already takes) and never reaches the permission gate.
func TestGuardToolCallCtxCancelDoesNotRunTheTool(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	gateChecked := false
	_, err := GuardToolCall(GuardOptions{
		Config:    cfg(fixture("sleep-forever.sh"), 30),
		ToolName:  "bash",
		Args:      map[string]any{"command": "git status"},
		SessionID: "t",
		Cwd:       dir,
		Ctx:       ctx,
		PrimaryArgOf: func(args map[string]any) (string, bool) {
			s, ok := args["command"].(string)
			return s, ok
		},
		Check: func(toolName, primaryArg string, hasPrimaryArg bool, args map[string]any, _ Decision, _ string) (*Blocked, error) {
			gateChecked = true
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("expected an error from a PreToolUse hook killed by ctx cancellation")
	}
	if gateChecked {
		t.Error("the permission gate ran after the hook was cancelled - the tool call must not reach it")
	}
}

// TestRunHooksCapsHookOutput asserts a hook's stdout is capped rather than
// retained in full. The hook itself is bounded with `head -c` to 2MB, not
// gigabytes, so a regression here (the cap not applying) fails the length
// assertion instead of making this test itself a memory bomb.
func TestRunHooksCapsHookOutput(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "big-output.sh", `cat >/dev/null; yes y | head -c 2000000`)
	const cap = 64
	out := RunHooks(RunOptions{
		Config:         cfg(path, 0),
		Event:          PreToolUse,
		ToolName:       "bash",
		HasToolName:    true,
		Payload:        Payload{SessionID: "t", Cwd: dir, ToolName: "bash", ToolInput: map[string]any{"command": "x"}},
		MaxOutputBytes: cap,
	})
	if len(out.Context) != 1 {
		t.Fatalf("expected the (non-JSON) output as context, got %v", out)
	}
	if len(out.Context[0]) > cap {
		t.Errorf("hook output was not capped: got %d bytes, want <= %d", len(out.Context[0]), cap)
	}
}

func TestRunHooksSurvivesNonexistentCommand(t *testing.T) {
	dir := t.TempDir()
	out := run(cfg("/nonexistent/hook.sh", 0), nil, dir)
	if out.Blocked != nil {
		t.Errorf("expected no block, got %v", out.Blocked)
	}
}

func TestRunHooksChainsRewrites(t *testing.T) {
	dir := t.TempDir()
	first := writeScript(t, dir, "first.sh", `cat >/dev/null; jq -n '{hookSpecificOutput:{updatedInput:{command:"one"}}}'`)
	second := writeScript(t, dir, "second.sh", `c=$(jq -r '.tool_input.command'); jq -n --arg c "$c" '{hookSpecificOutput:{updatedInput:{command:($c+"-two")}}}'`)
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available")
	}
	out := RunHooks(RunOptions{
		Config: Config{
			PreToolUse: []Matcher{
				{MatcherPattern: "Bash", Hooks: []Command{{Type: "command", Command: first}}},
				{MatcherPattern: "Bash", Hooks: []Command{{Type: "command", Command: second}}},
			},
		},
		Event:       PreToolUse,
		ToolName:    "bash",
		HasToolName: true,
		Payload:     Payload{SessionID: "t", Cwd: dir, ToolName: "bash", ToolInput: map[string]any{"command": "start"}},
	})
	if out.UpdatedInput["command"] != "one-two" {
		t.Errorf("got %v", out.UpdatedInput)
	}
}

func TestRunHooksStopsChainOnceBlocked(t *testing.T) {
	dir := t.TempDir()
	deny := writeScript(t, dir, "deny2.sh", `cat >/dev/null; echo no >&2; exit 2`)
	after := writeScript(t, dir, "after.sh", `cat >/dev/null; jq -n '{hookSpecificOutput:{updatedInput:{command:"ran"}}}'`)
	out := RunHooks(RunOptions{
		Config: Config{
			PreToolUse: []Matcher{
				{MatcherPattern: "Bash", Hooks: []Command{{Type: "command", Command: deny}}},
				{MatcherPattern: "Bash", Hooks: []Command{{Type: "command", Command: after}}},
			},
		},
		Event:       PreToolUse,
		ToolName:    "bash",
		HasToolName: true,
		Payload:     Payload{SessionID: "t", Cwd: dir, ToolName: "bash", ToolInput: map[string]any{}},
	})
	if out.Blocked == nil {
		t.Error("expected block")
	}
	if out.UpdatedInput != nil {
		t.Errorf("a later hook ran after a block: %v", out.UpdatedInput)
	}
}

func TestRunHooksDoesNothingWhenNoHookMatchesTool(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "never.sh", `cat >/dev/null; echo ran`)
	out := RunHooks(RunOptions{
		Config:      cfg(path, 0),
		Event:       PreToolUse,
		ToolName:    "read",
		HasToolName: true,
		Payload:     Payload{SessionID: "t", Cwd: dir, ToolName: "read", ToolInput: map[string]any{}},
	})
	if len(out.Context) != 0 {
		t.Errorf("expected no context, got %v", out.Context)
	}
}

func TestRunHooksRecordsPayloadFields(t *testing.T) {
	dir := t.TempDir()
	payloadFile := filepath.Join(dir, "payload.json")
	t.Setenv("HARNESS_TEST_PAYLOAD_FILE", payloadFile)
	out := run(cfg(fixture("record-payload.sh"), 0), map[string]any{"command": "git status"}, dir)
	if out.Blocked != nil {
		t.Fatalf("unexpected block: %v", out.Blocked)
	}
	data, err := os.ReadFile(payloadFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{`"session_id":"t"`, fmt.Sprintf(`"cwd":%q`, dir), `"hook_event_name":"PreToolUse"`, `"tool_name":"bash"`, `"command":"git status"`} {
		if !contains(got, want) {
			t.Errorf("payload missing %q, got %s", want, got)
		}
	}
}

func TestGuardToolCallHooksThenGate(t *testing.T) {
	dir := t.TempDir()

	guard := func(hookPath string, denyMatching string) (GuardResult, []string) {
		var seen []string
		config := Config{}
		if hookPath != "" {
			config = Config{PreToolUse: []Matcher{{MatcherPattern: "Bash", Hooks: []Command{{Type: "command", Command: hookPath}}}}}
		}
		result, err := GuardToolCall(GuardOptions{
			Config:    config,
			ToolName:  "bash",
			Args:      map[string]any{"command": "git status"},
			SessionID: "t",
			Cwd:       dir,
			PrimaryArgOf: func(args map[string]any) (string, bool) {
				s, ok := args["command"].(string)
				return s, ok
			},
			Check: func(toolName, primaryArg string, hasPrimaryArg bool, args map[string]any, _ Decision, _ string) (*Blocked, error) {
				seen = append(seen, primaryArg)
				if denyMatching != "" && hasPrimaryArg && len(primaryArg) >= len(denyMatching) && primaryArg[:len(denyMatching)] == denyMatching {
					return &Blocked{Reason: "denied: " + primaryArg}, nil
				}
				return nil, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result, seen
	}

	t.Run("shows the gate the REWRITTEN command, not the original", func(t *testing.T) {
		hook := writeScript(t, dir, "rw.sh", `cat >/dev/null; jq -n '{hookSpecificOutput:{updatedInput:{command:"rtk git status"}}}'`)
		_, seen := guard(hook, "")
		if len(seen) != 1 || seen[0] != "rtk git status" {
			t.Errorf("got %v", seen)
		}
	})

	t.Run("blocks when a deny rule matches only the rewritten form", func(t *testing.T) {
		hook := writeScript(t, dir, "rw2.sh", `cat >/dev/null; jq -n '{hookSpecificOutput:{updatedInput:{command:"rtk git status"}}}'`)
		result, _ := guard(hook, "rtk")
		if result.Blocked == nil {
			t.Error("a rewrite escaped the deny rule")
		}
	})

	t.Run("does not consult the gate at all once a hook blocks", func(t *testing.T) {
		hook := writeScript(t, dir, "deny3.sh", `cat >/dev/null; echo nope >&2; exit 2`)
		result, seen := guard(hook, "")
		if result.Blocked == nil || result.Blocked.Reason != "nope" {
			t.Errorf("got %v", result.Blocked)
		}
		if len(seen) != 0 {
			t.Error("the gate ran after a hook block")
		}
		if !result.ByHook {
			t.Error("a hook block is not reported as one")
		}
	})

	t.Run("a gate block is not reported as a hook block", func(t *testing.T) {
		result, _ := guard("", "git")
		if result.Blocked == nil || result.ByHook {
			t.Errorf("got blocked=%v byHook=%v", result.Blocked, result.ByHook)
		}
	})

	t.Run("returns no args when nothing rewrote, so the call is untouched", func(t *testing.T) {
		result, _ := guard("", "")
		if result.Args != nil {
			t.Errorf("expected nil args, got %v", result.Args)
		}
		if result.Blocked != nil {
			t.Errorf("expected no block, got %v", result.Blocked)
		}
	})
}

func TestRunHooksSetsClaudeProjectDir(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "project-dir.sh", `printf '%s' "$CLAUDE_PROJECT_DIR"`)
	out := run(cfg(path, 0), nil, dir)
	if len(out.Context) != 1 || out.Context[0] != dir {
		t.Errorf("CLAUDE_PROJECT_DIR = %v, want %q", out.Context, dir)
	}
}

func runStop(t *testing.T, event Event, body string) (Outcome, string) {
	t.Helper()
	dir := t.TempDir()
	path := writeScript(t, dir, "stop.sh", body)
	c := Config{event: []Matcher{{Hooks: []Command{{Type: "command", Command: path}}}}}
	active := false
	return RunHooks(RunOptions{Config: c, Event: event, Payload: Payload{SessionID: "t", Cwd: dir, StopHookActive: &active}}), path
}

// Stop and SubagentStop give "decision": "block" its Claude Code meaning
// (keep working, reason to the model); exit 2 means the same, with the
// reason prefixed by the command, as Claude Code's own hook runner does.
// {"continue": false} is distinct: stop everything.
func TestRunHooksStopDecisions(t *testing.T) {
	for _, event := range []Event{Stop, SubagentStop} {
		t.Run(string(event), func(t *testing.T) {
			out, _ := runStop(t, event, `echo '{"decision":"block","reason":"tests are failing"}'`)
			if out.Blocked == nil || out.Blocked.Reason != "tests are failing" || out.Stopped {
				t.Errorf("decision block: %+v, want Blocked with the reason", out)
			}
			if got := out.BlockReasons; len(got) != 1 || got[0] != "tests are failing" {
				t.Errorf("decision block: BlockReasons = %v, want [tests are failing]", got)
			}
			out, path := runStop(t, event, `echo "lint first" >&2; exit 2`)
			want := fmt.Sprintf("[%s]: lint first", path)
			if out.Blocked == nil || out.Blocked.Reason != want || out.Stopped {
				t.Errorf("exit 2: %+v, want Blocked with %q", out, want)
			}
			out, path = runStop(t, event, `exit 2`)
			want = fmt.Sprintf("[%s]: No stderr output", path)
			if out.Blocked == nil || out.Blocked.Reason != want {
				t.Errorf("exit 2 with no stderr: %+v, want Blocked with %q", out, want)
			}
			out, _ = runStop(t, event, `echo '{"continue":false,"stopReason":"build is green"}'`)
			if !out.Stopped || out.StopReason != "build is green" {
				t.Errorf("continue false: %+v, want Stopped with stopReason", out)
			}
			out, _ = runStop(t, event, `echo '{"decision":"approve"}'`)
			if out.Blocked != nil || out.Stopped {
				t.Errorf("decision approve: %+v, want nothing", out)
			}
		})
	}
}

// Claude Code's docs describe a Stop/SubagentStop hook's
// hookSpecificOutput.additionalContext as adding context and continuing
// the conversation; the shipped hook runner has no case for either event
// name in the switch that would extract it, so it never reaches the
// model. kiln follows the shipped behaviour: Outcome.Context stays empty
// for these two events even when a hook sets additionalContext (it is
// still populated for every other event interpret handles - see
// TestRunHooksAdditionalContextFromJSON for PreToolUse).
func TestRunHooksStopAdditionalContextHasNoEffect(t *testing.T) {
	for _, event := range []Event{Stop, SubagentStop} {
		t.Run(string(event), func(t *testing.T) {
			out, _ := runStop(t, event, `echo '{"hookSpecificOutput":{"hookEventName":"`+string(event)+`","additionalContext":"build failed, retry"}}'`)
			if len(out.Context) != 0 {
				t.Errorf("Context = %v, want none: Claude Code's shipped hook runner ignores additionalContext for %s", out.Context, event)
			}
			if out.Blocked != nil || out.Stopped {
				t.Errorf("got %+v, want no block and no stop from additionalContext alone", out)
			}
		})
	}
}

// A Stop hook receives stop_hook_active and the last reply's text.
func TestRunHooksStopPayload(t *testing.T) {
	dir := t.TempDir()
	rec := filepath.Join(dir, "payload.json")
	path := writeScript(t, dir, "rec.sh", "cat > "+rec)
	active := true
	RunHooks(RunOptions{
		Config:  Config{Stop: []Matcher{{Hooks: []Command{{Type: "command", Command: path}}}}},
		Event:   Stop,
		Payload: Payload{SessionID: "t", Cwd: dir, StopHookActive: &active, LastAssistantMessage: "all done"},
	})
	raw, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"stop_hook_active":true`, `"last_assistant_message":"all done"`, `"hook_event_name":"Stop"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("payload %s lacks %s", raw, want)
		}
	}
}

// OnHookStart is told about each hook before it runs, with the whole chain,
// so the busy row can say which of several Stop hooks is running.
func TestRunHooksReportsEachHookStart(t *testing.T) {
	c := Config{Stop: []Matcher{{Hooks: []Command{
		{Type: "command", Command: "true"},
		{Type: "command", Command: "true", StatusMessage: "checking tests"},
	}}}}
	var starts []string
	RunHooks(RunOptions{
		Config:  c,
		Event:   Stop,
		Payload: Payload{Cwd: t.TempDir()},
		OnHookStart: func(i int, cmds []Command) {
			starts = append(starts, fmt.Sprintf("%d/%d", i, len(cmds)))
		},
	})
	if got := strings.Join(starts, ","); got != "0/2,1/2" {
		t.Fatalf("OnHookStart calls = %q, want 0/2,1/2", got)
	}
}

// TestRunHooksConcurrentlyRunsAtOnce: two ~1s hooks finish in well under
// the 2s a sequential RunHooks chain would take, since they start
// together rather than one after the other.
func TestRunHooksConcurrentlyRunsAtOnce(t *testing.T) {
	dir := t.TempDir()
	a := writeScript(t, dir, "a.sh", "cat >/dev/null; sleep 1")
	b := writeScript(t, dir, "b.sh", "cat >/dev/null; sleep 1")
	c := Config{Stop: []Matcher{{Hooks: []Command{
		{Type: "command", Command: a},
		{Type: "command", Command: b},
	}}}}
	started := time.Now()
	RunHooksConcurrently(RunOptions{Config: c, Event: Stop, Payload: Payload{Cwd: dir}})
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("took %v, want well under 2s if the hooks ran together", elapsed)
	}
}

// TestRunHooksConcurrentlyCollectsEveryBlockReason: two Stop hooks that
// each block, one by exit 2, one by {"decision":"block"}, both
// contribute their reason - unlike RunHooks, where the first block ends
// the chain and the second hook never runs at all.
func TestRunHooksConcurrentlyCollectsEveryBlockReason(t *testing.T) {
	dir := t.TempDir()
	first := writeScript(t, dir, "first.sh", `echo "run the linter" >&2; exit 2`)
	second := writeScript(t, dir, "second.sh", `cat >/dev/null; echo '{"decision":"block","reason":"tests are red"}'`)
	c := Config{Stop: []Matcher{{Hooks: []Command{
		{Type: "command", Command: first},
		{Type: "command", Command: second},
	}}}}
	out := RunHooksConcurrently(RunOptions{Config: c, Event: Stop, Payload: Payload{Cwd: dir}})
	if out.Blocked == nil {
		t.Fatal("expected a block")
	}
	if len(out.BlockReasons) != 2 {
		t.Fatalf("BlockReasons = %v, want 2 entries (one per blocking hook)", out.BlockReasons)
	}
	wantFirst := fmt.Sprintf("[%s]: run the linter", first)
	sawFirst, sawSecond := false, false
	for _, r := range out.BlockReasons {
		if r == wantFirst {
			sawFirst = true
		}
		if r == "tests are red" {
			sawSecond = true
		}
	}
	if !sawFirst || !sawSecond {
		t.Errorf("BlockReasons = %v, want %q and %q", out.BlockReasons, wantFirst, "tests are red")
	}
}

// TestRunHooksConcurrentlyContinueFalseWinsOverABlock: one hook blocks,
// another answers {"continue": false} - the stop wins, as in Claude
// Code, regardless of which hook the caller happens to read first.
func TestRunHooksConcurrentlyContinueFalseWinsOverABlock(t *testing.T) {
	dir := t.TempDir()
	blocker := writeScript(t, dir, "blocker.sh", `echo "no" >&2; exit 2`)
	stopper := writeScript(t, dir, "stopper.sh", `cat >/dev/null; echo '{"continue":false,"stopReason":"build is green"}'`)
	c := Config{Stop: []Matcher{{Hooks: []Command{
		{Type: "command", Command: blocker},
		{Type: "command", Command: stopper},
	}}}}
	out := RunHooksConcurrently(RunOptions{Config: c, Event: Stop, Payload: Payload{Cwd: dir}})
	if !out.Stopped || out.StopReason != "build is green" {
		t.Errorf("got %+v, want Stopped with stopReason \"build is green\"", out)
	}
}

// TestRunHooksConcurrentlyCtxCancelKillsEveryHookPromptly asserts that
// cancelling ctx kills every running hook at once, not just the one a
// sequential chain happens to be on, and that none of them survive as
// orphaned processes.
func TestRunHooksConcurrentlyCtxCancelKillsEveryHookPromptly(t *testing.T) {
	dir := t.TempDir()
	c := Config{Stop: []Matcher{{Hooks: []Command{
		{Type: "command", Command: fixture("sleep-forever.sh"), Timeout: 30},
		{Type: "command", Command: fixture("sleep-forever.sh"), Timeout: 30},
	}}}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	out := RunHooksConcurrently(RunOptions{Config: c, Event: Stop, Payload: Payload{Cwd: dir}, Ctx: ctx})
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("ctx cancellation did not kill the hooks promptly: took %v", elapsed)
	}
	if !out.Cancelled {
		t.Errorf("expected Outcome.Cancelled, got %+v", out)
	}
	if out.Blocked != nil || len(out.BlockReasons) != 0 {
		t.Errorf("a cancelled chain must not be reported as Blocked: %+v", out)
	}

	time.Sleep(300 * time.Millisecond)
	cmd := exec.Command("pgrep", "-f", "sleep-forever.sh")
	if err := cmd.Run(); err == nil {
		t.Error("a sleep-forever.sh child process is still running after ctx cancellation")
	}
}

// TestRunHooksConcurrentlyReportsProgress: OnHookDone is called once up
// front with 0 (so a caller can show the total before anything
// finishes), then once per hook as it finishes, counting up to the
// total - the parallel counterpart of OnHookStart's per-hook index.
func TestRunHooksConcurrentlyReportsProgress(t *testing.T) {
	dir := t.TempDir()
	a := writeScript(t, dir, "a.sh", "cat >/dev/null; true")
	b := writeScript(t, dir, "b.sh", "cat >/dev/null; true")
	c := Config{Stop: []Matcher{{Hooks: []Command{
		{Type: "command", Command: a},
		{Type: "command", Command: b},
	}}}}
	var mu sync.Mutex
	var done []int
	RunHooksConcurrently(RunOptions{
		Config:  c,
		Event:   Stop,
		Payload: Payload{Cwd: dir},
		OnHookDone: func(n int, cmds []Command) {
			if len(cmds) != 2 {
				t.Errorf("OnHookDone commands = %d, want 2", len(cmds))
			}
			mu.Lock()
			done = append(done, n)
			mu.Unlock()
		},
	})
	if len(done) != 3 {
		t.Fatalf("OnHookDone called %d times, want 3 (0, then once per hook)", len(done))
	}
	if done[0] != 0 {
		t.Errorf("first OnHookDone call = %d, want 0", done[0])
	}
	if done[len(done)-1] != 2 {
		t.Errorf("last OnHookDone call = %d, want 2 (both hooks done)", done[len(done)-1])
	}
}
