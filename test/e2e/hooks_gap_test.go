//go:build e2e

package e2e

// Hook behaviours not covered by hooks_test.go / hooks_events_test.go:
// PostToolUse's tool_response and additionalContext, the Notification
// event (internal/cli/tui.go's permission_prompt notice), Stop's
// documented non-reprompting block, and SubagentStop firing once per
// dispatched subagent at nesting depth. Ground truth read for this file:
// internal/claude/hooks/runner.go (Payload fields, interpret's JSON
// output handling) and internal/cli/chat.go's OnAfterTool/Stop/
// SubagentStop wiring (read directly, not from memory - see this file's
// test comments for the exact line-level findings).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
)

// gapWriteHooks writes a scratch .claude/settings.json wiring possibly
// several commands on one event (hooks_test.go's writeHookSettings only
// ever wires one), so PostToolUse can carry both a payload recorder and a
// context-injecting hook in the same chain (RunHooks runs a chain
// sequentially and composes their effects - internal/claude/hooks/
// runner.go's RunHooks doc comment).
func gapWriteHooks(t *testing.T, proj, event, matcher string, commands ...string) {
	t.Helper()
	hooks := make([]map[string]any, len(commands))
	for i, c := range commands {
		hooks[i] = map[string]any{"type": "command", "command": c}
	}
	settings := map[string]any{
		"hooks": map[string]any{
			event: []map[string]any{
				{"matcher": matcher, "hooks": hooks},
			},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(proj, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHooks_PostToolUse_ReceivesResultAndCanAddContext wires two
// PostToolUse hooks on the read call: record-payload.sh (proves
// tool_response reaches the hook) and context-json.sh (emits
// hookSpecificOutput.additionalContext - see testdata/hooks/context-json.sh).
//
// Fixed in internal/cli/chat.go: OnAfterTool now captures RunHooks'
// Outcome instead of discarding it. Read directly
// (internal/harness/turn.go's commitToolResult): the tool's own
// toolResult entry is already committed to the branch by the time
// invokeAfterTool runs, so additionalContext cannot be appended to the
// tool_result content itself; it is queued instead and threaded into the
// transcript via the transform_context hook (turn.go:183 calls
// invokeTransformContext right before every assistant request,
// including the one immediately following this tool result within the
// same operation). A PostToolUse block decision is reported to the user
// via the notice sink, since the tool has already run and cannot be
// undone - matching Claude Code's own PostToolUse semantics.
//
// Proved able to fail (for the half that does work): commenting out
// record-payload.sh's invocation (passing zero commands) turned this red
// with "PostToolUse never wrote ...: no such file or directory";
// reverted.
func TestHooks_PostToolUse_ReceivesResultAndCanAddContext(t *testing.T) {
	addr, srv := startFaux(t, readCallScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	payloadFile := filepath.Join(t.TempDir(), "posttooluse.json")
	gapWriteHooks(t, proj, "PostToolUse", "*",
		fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", payloadFile, hookScript(t, "record-payload.sh")),
		hookScript(t, "context-json.sh"),
	)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "read the file", "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	raw, err := os.ReadFile(payloadFile)
	if err != nil {
		t.Fatalf("PostToolUse never wrote %s: %v", payloadFile, err)
	}
	if !strings.Contains(string(raw), `"hook_event_name":"PostToolUse"`) {
		t.Errorf("payload = %s, want hook_event_name PostToolUse", raw)
	}
	if !strings.Contains(string(raw), `"tool_response"`) {
		t.Errorf("payload = %s, want a tool_response field (the tool's own result)", raw)
	}

	// context-json.sh's additionalContext ("extra context from hook") must
	// reach the model on a later request, threaded in via the
	// transform_context hook (see this test's doc comment for why it
	// cannot land inside the tool_result itself).
	var sawContext bool
	for _, r := range srv.Requests() {
		if strings.Contains(string(r.Messages), "extra context from hook") {
			sawContext = true
		}
	}
	if !sawContext {
		t.Errorf("no request carried PostToolUse's additionalContext (%q); want it threaded into a later request", "extra context from hook")
	}
}

// TestHooks_Notification_Payload drives the TUI (Notification only fires
// from RunInteractive - internal/cli/tui.go:~104-113, wired around
// deps.Gate.SetPrompter, firing right before the permission prompt
// paints) with a bash call in manual mode, and wires record-payload.sh on
// Notification. Asserts notification_type "permission_prompt" and a
// message naming the tool, matching tui.go's literal
// "Claude needs your permission to use " + req.ToolName.
//
// Proved able to fail: wiring the recorder on "SessionStart" instead of
// "Notification" turned this red ("Notification never wrote ...: no
// such file or directory"); reverted.
func TestHooks_Notification_Payload(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, permBashPromptScript)

	payloadFile := filepath.Join(t.TempDir(), "notification.json")
	writeHookSettings(t, proj, "Notification", "",
		fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", payloadFile, hookScript(t, "record-payload.sh")))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "manual")
	waitReady(t, s)
	s.Send("run a command")
	s.SendKey("enter")
	if err := s.WaitFor("Allow kiln to run this command", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(payloadFile)
	if err != nil {
		t.Fatalf("Notification never wrote %s: %v", payloadFile, err)
	}
	if !strings.Contains(string(raw), `"hook_event_name":"Notification"`) {
		t.Errorf("payload = %s, want hook_event_name Notification", raw)
	}
	if !strings.Contains(string(raw), `"notification_type":"permission_prompt"`) {
		t.Errorf("payload = %s, want notification_type permission_prompt", raw)
	}
	if !strings.Contains(string(raw), `"message":"Claude needs your permission to use bash"`) {
		t.Errorf("payload = %s, want message naming the bash tool", raw)
	}

	// Let the run finish cleanly rather than leaving the TUI stuck on the
	// prompt.
	s.SendKey("2")
	waitTurnSettled(t, s)
}

// TestHooks_StopBlock_ReportedOnceNotReprompted wires block-exit2.sh on
// Stop. Read directly: chat.go's EventRunEnd handler (~chat.go:679-695)
// runs the Stop hook once, and if it blocks, only calls
// notice("Stop hook asked to continue: " + reason) - the comment right
// above it says explicitly "a blocking Stop hook is reported to the
// user, not fed back into the model (this port does not re-prompt on
// Stop)". So this test asserts today's documented behavior: the hook
// runs exactly once (one payload write), no second model request is
// ever made because of the block, and the block reason reaches the
// user via stderr (print mode's notice sink - chat.go's
// hookNoticeSink(stderr)).
//
// Proved able to fail: asserting the reason must NOT appear on stderr
// (inverting the expectation) turned this red because it does appear;
// reverted.
func TestHooks_StopBlock_ReportedOnceNotReprompted(t *testing.T) {
	addr, srv := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	writeHookSettings(t, proj, "Stop", "", hookScript(t, "block-exit2.sh"))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hello", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	if !strings.Contains(res.Stderr, "not allowed here") {
		t.Errorf("stderr = %q, want the Stop hook's block reason reported to the user", res.Stderr)
	}

	if got := len(srv.Requests()); got != 1 {
		t.Errorf("faux recorded %d requests, want exactly 1 - a Stop block must not trigger a second model request", got)
	}
}

// gapNestedTaskScript is a three-model, two-hop dispatch: faux-1 (the
// parent) dispatches a task routed to the "fast" role (settings.json ->
// faux-2), and that depth-1 subagent itself dispatches a second task
// routed to "faster" (-> faux-3). internal/agent/dispatch.go's Dispatcher
// only wires a `task` tool into a subagent built at Depth < 2 (its own
// doc comment on the Depth field), so a depth-1 subagent (built by the
// Depth-0 Dispatcher) does still have one; its own dispatch builds a
// Depth-2 Dispatcher, which does not - two hops is exactly as deep as
// this can nest.
const gapNestedTaskScript = `models:
  faux-1:
    - tool_call: {name: task, args: {subagent_type: "general-purpose", description: "depth1", prompt: "go deeper", model: "fast"}, id: t1}
    - on_tool_result: t1
      then:
        - text: "parent done"
  faux-2:
    - tool_call: {name: task, args: {subagent_type: "general-purpose", description: "depth2", prompt: "go deepest", model: "faster"}, id: t2}
    - on_tool_result: t2
      then:
        - text: "depth1 done"
  faux-3:
    - text: "depth2 done"
`

// TestHooks_SubagentStop_PerDepth drives gapNestedTaskScript with
// gap-record-payload-append.sh (append, not truncate, since SubagentStop
// fires twice - once per dispatched subagent - and record-payload.sh's
// plain "cat >" would leave only the last one) wired on SubagentStop, and
// checks two distinct payload lines with two distinct transcript_path
// values, matching the two non-parent session files this run produces.
//
// Proved able to fail: temporarily forcing internal/agent/dispatch.go's
// allowTask to false unconditionally (no subagent ever gets a task tool)
// turned this red with "session files = [...2 entries...], want 3 (parent
// + 2 nested subagents)" - the depth-1 subagent could no longer dispatch
// its own depth-2 subagent, so only one nested session existed; reverted.
func TestHooks_SubagentStop_PerDepth(t *testing.T) {
	addr, _ := startFaux(t, gapNestedTaskScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeModelRolesSettings(t, proj, map[string]string{
		"fast":   fauxprovider.ProviderID + "/faux-2",
		"faster": fauxprovider.ProviderID + "/faux-3",
	})

	payloadFile := filepath.Join(t.TempDir(), "subagent-stop-depths.ndjson")
	mergeHookSettings(t, proj, "SubagentStop", "",
		fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", payloadFile, hookScript(t, "gap-record-payload-append.sh")))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "dispatch two levels deep", "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	files := sessionFiles(t, sessDir, proj)
	if len(files) != 3 {
		t.Fatalf("session files = %v, want 3 (parent + 2 nested subagents)", files)
	}

	f, err := os.Open(payloadFile)
	if err != nil {
		t.Fatalf("SubagentStop never wrote %s: %v", payloadFile, err)
	}
	defer f.Close()

	var payloads []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse payload line %q: %v", line, err)
		}
		payloads = append(payloads, m)
	}
	if len(payloads) != 2 {
		t.Fatalf("got %d SubagentStop payloads, want 2 (one per dispatched subagent): %v", len(payloads), payloads)
	}

	seen := map[string]bool{}
	for _, p := range payloads {
		if p["hook_event_name"] != "SubagentStop" {
			t.Errorf("payload hook_event_name = %v, want SubagentStop", p["hook_event_name"])
		}
		tp, _ := p["transcript_path"].(string)
		if tp == "" {
			t.Errorf("payload missing transcript_path: %v", p)
			continue
		}
		seen[tp] = true
	}
	if len(seen) != 2 {
		t.Errorf("want 2 distinct transcript_path values in SubagentStop payloads, got %d: %v", len(seen), seen)
	}
	for tp := range seen {
		var matched bool
		for _, f := range files {
			if f == tp {
				matched = true
			}
		}
		if !matched {
			t.Errorf("payload transcript_path %s does not match any known session file %v", tp, files)
		}
	}
}
