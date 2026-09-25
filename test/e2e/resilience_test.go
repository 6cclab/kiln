//go:build e2e

// Package e2e: resilience behaviour tests -- retries, mid-stream
// disconnects, malformed tool args, unknown tool names and empty assistant
// turns -- against the real kiln binary. Each test's doc comment records
// the break used to confirm it can fail.
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resilienceRunLogDir points HARNESS_LOG_DIR at a fresh temp dir, distinct
// from the scratch HOME's default (~/.harness/logs), so a test's own run
// log is trivially found without scanning for the newest file.
func resilienceRunLogDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// resilienceReadLog concatenates every harness-*.log file under dir (there
// should be exactly one, from the one runHarness call the test made) and
// fails the test if none exist.
func resilienceReadLog(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir %s: %v", dir, err)
	}
	var out strings.Builder
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "harness-") && strings.HasSuffix(e.Name(), ".log") {
			found = true
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out.Write(data)
		}
	}
	if !found {
		t.Fatalf("no harness-*.log found under %s", dir)
	}
	return out.String()
}

// TestResilience_OverloadedRetriesThenSucceeds scripts a 529 overloaded
// error for the first turn, then a normal text reply. internal/harness's
// retry policy (internal/harness/retry.go: isRetriable treats HTTP
// 429/529/5xx as retriable, DefaultRetryPolicy allows up to 4 attempts)
// should retry once and the run should succeed with the scripted text,
// and internal/cli/chat.go's logHarnessEvents should have mirrored the
// EventRetryScheduled into the run log as "retry_scheduled".
//
// Proved able to fail: temporarily changed the wanted log line from
// "retry_scheduled" to "retry_never_scheduled" -- went red (log genuinely
// contains "retry_scheduled", not the changed string, with the full
// harness-*.log dumped in the failure) -- then reverted.
func TestResilience_OverloadedRetriesThenSucceeds(t *testing.T) {
	const script = `model: faux-1
steps:
  - error: {status: 529, type: overloaded_error, message: "Overloaded"}
  - text: "recovered after overload"
`
	addr, _ := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	logDir := resilienceRunLogDir(t)

	env := baseEnv(home, sessDir, addr)
	env["HARNESS_LOG_DIR"] = logDir
	res := runHarness(t, proj, env, "-p", "hello", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "recovered after overload") {
		t.Errorf("stdout missing final text, got: %q", res.Stdout)
	}

	logText := resilienceReadLog(t, logDir)
	if !strings.Contains(logText, "retry_scheduled") {
		t.Errorf("run log missing a retry_scheduled line:\n%s", logText)
	}
}

// TestResilience_StreamCutMidResponse scripts a disconnect_after fault
// (mid-response TCP cut, see internal/testkit/faux's doc.go on
// disconnect_after) followed by a normal text step, bounded by runHarness's
// own 60s timeout (t.Fatal on expiry) so a hang would be reported as a
// failure rather than left to run forever.
//
// Fixed behaviour (previously this test documented a real gap: the run did
// NOT retry a mid-stream disconnect, exiting 1 with stderr "kiln:
// unexpected EOF" even though an overloaded/5xx response IS retried by
// TestResilience_OverloadedRetriesThenSucceeds). The gap was that a plain
// "unexpected EOF" read error never reached internal/harness/retry.go's
// isRetriable as a recognized shape: it isn't a net.Error, and its message
// doesn't match isRetriable's string-sniffing needles ("connection reset",
// "429", "529", "overloaded", "timeout"). The fix: internal/provider/api's
// streaming clients (anthropic_messages.go and the other Stream
// implementations) now wrap a read error that happens before the stream's
// terminal event in provider.StreamInterrupted (internal/provider/types.go),
// and retry.go's isRetriable recognizes that type explicitly and treats it
// as retriable, the same as a 429/529/5xx status. See
// internal/provider/api/conformance_test.go's
// TestAnthropicClientDisconnectMidStream and
// internal/harness/retry_test.go's TestIsRetriable_StreamInterrupted for
// the fix verified in isolation at the provider/retry-policy layer.
//
// Proved able to fail (both directions): temporarily reverted retry.go's
// isRetriable to not special-case provider.StreamInterrupted -- went red
// with exit code 1 and stderr "kiln: unexpected EOF" (today's old,
// documented-broken behaviour) -- then restored the fix, which turns this
// test green again.
func TestResilience_StreamCutMidResponse(t *testing.T) {
	const script = `model: faux-1
steps:
  - text: "this reply gets cut short"
    disconnect_after: 20
  - text: "recovered after disconnect"
`
	addr, _ := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	logDir := resilienceRunLogDir(t)

	env := baseEnv(home, sessDir, addr)
	env["HARNESS_LOG_DIR"] = logDir
	res := runHarness(t, proj, env, "-p", "hello", "--output-format", "text")

	if res.Code != 0 {
		t.Fatalf("exit code %d, want 0 (a mid-stream disconnect should retry, same as a 529); stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "recovered after disconnect") {
		t.Errorf("stdout missing the recovery text after the retried disconnect, got: %q", res.Stdout)
	}

	logText := resilienceReadLog(t, logDir)
	if !strings.Contains(logText, "retry_scheduled") {
		t.Errorf("run log missing a retry_scheduled line for the mid-stream disconnect:\n%s", logText)
	}
}

// TestResilience_MalformedToolArgs feeds a tool call whose arguments are
// not valid JSON (faux raw_args). The provider flags it (ToolCall.InvalidArgs)
// and the harness refuses to run the tool, returning an is_error tool_result
// that names the parse failure.
func TestResilience_MalformedToolArgs(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: bash, raw_args: '{"command": ', id: tc1}
  - on_tool_result: tc1
    then:
      - text: "handled the bad args"
`
	addr, srv := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "run something",
		"--output-format", "text",
		"--permission-mode", "bypassPermissions",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "handled the bad args") {
		t.Errorf("run did not reach the scripted final text after the malformed tool call, stdout=%q", res.Stdout)
	}

	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 recorded requests (initial + after tool result), got %d", len(reqs))
	}
	msgs := string(reqs[1].Messages)
	if !strings.Contains(msgs, "tc1") {
		t.Fatalf("second request has no reference to tc1's tool result: %s", msgs)
	}
	var parsed []map[string]any
	if err := json.Unmarshal(reqs[1].Messages, &parsed); err != nil {
		t.Fatalf("parse second request messages: %v\n%s", err, reqs[1].Messages)
	}
	// This documents the gap: today's tool_result is NOT an error.
	// The harness refuses the call (internal/harness/turn.go beginTool,
	// call.InvalidArgs) and the model receives an is_error tool_result naming
	// the parse failure instead of a "(no output)" from bash run with empty
	// arguments. Break to verify: drop the InvalidArgs branch in beginTool.
	if !resilienceFindErrorToolResult(parsed, "tc1") {
		t.Errorf("no is_error tool_result for tc1: the malformed call executed instead of being refused; messages=%s", msgs)
	}
	if !strings.Contains(msgs, "arguments were not valid JSON") {
		t.Errorf("tool_result for tc1 does not name the JSON parse failure; messages=%s", msgs)
	}
}

// resilienceFindErrorToolResult walks an Anthropic-shaped messages array
// looking for a tool_result content block referencing toolUseID (matched
// by suffix, since the harness's Anthropic encoding prefixes the script's
// bare id with "toolu_", e.g. script id "tc1" becomes "toolu_tc1") that is
// marked as an error (is_error: true).
func resilienceFindErrorToolResult(messages []map[string]any, toolUseID string) bool {
	for _, m := range messages {
		content, _ := m["content"].([]any)
		for _, c := range content {
			block, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] != "tool_result" {
				continue
			}
			id, _ := block["tool_use_id"].(string)
			if id != toolUseID && !strings.HasSuffix(id, "_"+toolUseID) {
				continue
			}
			if isErr, _ := block["is_error"].(bool); isErr {
				return true
			}
		}
	}
	return false
}

// TestResilience_UnknownToolName scripts a tool_call for a name the
// harness has no such tool for ("this_tool_does_not_exist"). turn.go's
// beginTool path (internal/harness/turn.go:560) returns
// `tool.Errorf("unknown tool %q", call.Name)` as the tool's result rather
// than failing the run, so the next request should carry an error
// tool_result and the run should still reach the scripted final text.
//
// Proved able to fail: temporarily inverted the error-tool-result check to
// `if resilienceFindErrorToolResult(parsed, "tc1")` (requiring it be
// absent, the opposite of what's expected here) -- went red as expected
// (an is_error tool_result naming this_tool_does_not_exist is present) --
// then reverted.
func TestResilience_UnknownToolName(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: this_tool_does_not_exist, args: {}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "handled the unknown tool"
`
	addr, srv := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "call a bogus tool",
		"--output-format", "text",
		"--permission-mode", "bypassPermissions",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "handled the unknown tool") {
		t.Errorf("run did not reach the scripted final text after the unknown tool call, stdout=%q", res.Stdout)
	}

	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 recorded requests, got %d", len(reqs))
	}
	var parsed []map[string]any
	if err := json.Unmarshal(reqs[1].Messages, &parsed); err != nil {
		t.Fatalf("parse second request messages: %v\n%s", err, reqs[1].Messages)
	}
	if !resilienceFindErrorToolResult(parsed, "tc1") {
		t.Errorf("expected an is_error tool_result for tc1 (unknown tool) in the second request; messages=%s", reqs[1].Messages)
	}
	// The error text names the unknown tool, matching turn.go's
	// `unknown tool %q` format.
	if !strings.Contains(string(reqs[1].Messages), "this_tool_does_not_exist") {
		t.Errorf("expected the tool_result error to name the unknown tool; messages=%s", reqs[1].Messages)
	}
}

// TestResilience_EmptyAssistantMessage scripts a turn whose text is "" and
// which makes no tool calls: an end-of-turn reply with nothing in it. This
// documents today's behaviour (no crash) and records the actual exit code
// and stdout, since the plan does not prescribe one.
//
// Proved able to fail: temporarily required result.text to equal a
// non-empty sentinel string instead of "" -- went red with `result.text = ,
// want empty string` -- then reverted.
func TestResilience_EmptyAssistantMessage(t *testing.T) {
	const script = `model: faux-1
steps:
  - text: ""
`
	addr, _ := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "say nothing",
		"--output-format", "json",
	)
	// Documented current behaviour: the run completes cleanly with exit 0
	// even though the assistant's reply was empty text and no tool calls.
	if res.Code != 0 {
		t.Fatalf("exit code %d, want 0 (documenting today's no-crash behaviour; if this changed intentionally, update the expectation), stderr=%s", res.Code, res.Stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &result); err != nil {
		t.Fatalf("parse --output-format json stdout: %v\nstdout=%q", err, res.Stdout)
	}
	if result["text"] != "" {
		t.Errorf("result.text = %v, want empty string for an empty assistant reply", result["text"])
	}
}
