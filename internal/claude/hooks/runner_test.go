package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
