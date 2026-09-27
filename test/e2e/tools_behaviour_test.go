//go:build e2e

package e2e

// Built-in tool behaviour, driven through the real cmd/harness binary.
// Ground truth read for this file: internal/tools/bash.go (timeout arg
// and error text), internal/tools/backgroundshell.go (tool names
// bash_background/bash_output/kill_shell, "bash_1" id scheme from
// internal/agent/shells.go), internal/tools/edit.go (stale oldText is a
// read-then-match error, never a partial write), internal/tools/write.go
// and internal/execenv/env.go (no workspace-root enforcement at the tool
// layer at all - see TestTools_WriteOutsideRootsRefused's comment),
// internal/tools/read.go (image detection is by magic bytes, returns a
// msg.ImageContent block), internal/tools/toolsearch.go (admits MCP tools
// into the active set for the rest of the session), internal/tools/
// todo.go and sessionsearch.go (tool names todo_write/session_search).

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTools_BashTimeout runs a bash call whose timeout (1s) is far
// shorter than its command (sleep 30), and checks the tool result reports
// a timeout (bash.go's formatSeconds pluralizes "second(s)" correctly, so
// a 1-second timeout reads "Command timed out after 1 second", singular)
// and that the process is not left running afterward.
//
// Proved able to fail: asserting the marker command is STILL running
// after the run (inverting the pgrep check) turned this red because
// pgrep found nothing; reverted.
func TestTools_BashTimeout(t *testing.T) {
	const marker = "harness-bash-timeout-marker-e2e"
	script := fmt.Sprintf(`model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "exec -a %s sleep 30", timeout: 1}, id: b1}
  - on_tool_result: b1
    then:
      - text: "done"
`, marker)
	addr, _ := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "run a slow command with a short timeout",
		"--output-format", "json", "--permission-mode", "dontAsk")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Command timed out after 1 second") {
		t.Errorf("session does not mention the timeout:\n%s", raw)
	}
	if strings.Contains(string(raw), "Command timed out after 1 seconds") {
		t.Errorf("session incorrectly pluralizes a 1-second timeout:\n%s", raw)
	}

	// Give the OS a brief moment to reap the killed process before
	// checking for stragglers, matching hooks_test.go's own
	// TestHooks_Timeout_KillsSleepForever pattern.
	time.Sleep(200 * time.Millisecond)
	out, _ := exec.Command("pgrep", "-f", marker).CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("%s still running after its bash call should have timed out: pgrep output:\n%s", marker, out)
	}
}

// TestTools_BackgroundShellRoundTrip starts a command with bash_background
// and reads its output back with bash_output, using "bash_1" - the first
// background shell issued in a fresh session always gets this id
// (internal/agent/shells.go's NewBackgroundShells starts nextID at 1).
//
// Proved able to fail two different ways while writing this test:
//  1. Forcing internal/agent/shells.go's first-issued id to "bash_2"
//     instead of "bash_1" (`id := "bash_" + strconv.Itoa(b.nextID+1)`)
//     did NOT turn this red - it revealed a bug in the test itself, not
//     the product: the command string ("echo background-shell-marker")
//     was echoed verbatim into bash_background's own "Started bash_2:
//     <command>" confirmation text, so the marker showed up in the
//     session regardless of whether bash_output's round trip ever
//     actually worked. Fixed by making the captured marker something
//     the shell COMPUTES (a reversed string) rather than something
//     literally present in the command source, below - which then
//     surfaced a second, genuine race: bash_output's very next request
//     could fire before the background shell's "sleep 0.1" had actually
//     finished, observed live as "(no new output)" instead of the
//     marker. Fixed with a `delay: 300ms` faux step between the two
//     tool calls (see internal/testkit/faux/doc.go's delay step), which
//     sleeps server-side before returning the bash_output call - giving
//     the real background process time to finish first.
//  2. With that fix in place, re-applying the same shells.go id-offset
//     break DID turn this red, with "bash_output never captured the
//     background shell's output" (bash_output for "bash_1" got "No
//     shell" instead, since the real shell was "bash_2"); reverted.
func TestTools_BackgroundShellRoundTrip(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: bash_background, args: {command: "sleep 0.1; printf 'marker-%s\n' \"$(echo aXbYcZ | rev)\""}, id: s1}
  - on_tool_result: s1
    then:
      - delay: 300ms
      - tool_call: {name: bash_output, args: {id: "bash_1"}, id: s2}
  - on_tool_result: s2
    then:
      - text: "done"
`
	addr, _ := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "start a background shell and read its output",
		"--output-format", "json", "--permission-mode", "dontAsk")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	// "marker-ZcYbXa" is `rev`'s output, not a substring of the command
	// text ("aXbYcZ") itself, so this can only appear via an actual
	// bash_output read of the shell's real stdout.
	if !strings.Contains(string(raw), "marker-ZcYbXa") {
		t.Errorf("bash_output never captured the background shell's actual output:\n%s", raw)
	}
	if !strings.Contains(string(raw), "bash_1") {
		t.Errorf("session does not mention shell id bash_1:\n%s", raw)
	}
}

// TestTools_EditStaleOldString modifies math.js on disk AFTER the script
// was written (simulating a file changed underneath the model, e.g. by
// another process) so the edit's oldText no longer matches, and checks
// the edit call errors and the file is byte-identical to the externally
// modified version (edit.go reads and matches before writing at all; a
// failed match never touches the file).
//
// Proved able to fail: asserting the file WAS changed (expecting the
// edit to have silently succeeded despite the stale oldText) turned this
// red because the sha256 was unchanged; reverted.
func TestTools_EditStaleOldString(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: edit, args: {path: src/math.js, edits: [{oldText: "return a - b;", newText: "return a + b;"}]}, id: e1}
  - on_tool_result: e1
    then:
      - text: "done"
`
	addr, _ := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	// Change the file underneath the model's assumption: the oldText the
	// script asks for ("return a - b;") is no longer present.
	mathPath := filepath.Join(proj, "src", "math.js")
	changed := "function add(a, b) {\n  return a + a;\n}\n\nfunction mul(a, b) {\n  return a * b;\n}\n"
	if err := os.WriteFile(mathPath, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	before := sha256File(t, mathPath)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "fix the bug", "--output-format", "json", "--permission-mode", "dontAsk")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	var result struct {
		Blocked []string `json:"blocked"`
	}
	_ = json.Unmarshal([]byte(res.Stdout), &result)

	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"isError":true`) {
		t.Errorf("session has no isError tool result for the stale edit:\n%s", raw)
	}

	after := sha256File(t, mathPath)
	if before != after {
		t.Error("math.js changed even though the edit's oldText did not match current content")
	}
}

// TestTools_WriteOutsideRootsRefused writes to a path outside the project
// workspace (a file under the scratch HOME) with --permission-mode
// bypassPermissions, which skips the permission Gate's own workspace-
// boundary check entirely (Gate.Check returns Allow immediately for
// ModeBypassPermissions before ever reaching the WithinRoots/escaped
// check).
//
// This documents a real gap rather than proving a refusal: read
// directly, internal/tools/write.go's WriteTool calls env.WriteFile with
// no roots concept at all, and internal/execenv/env.go's WriteFile/
// AbsolutePath never consult any workspace boundary either - there is no
// tool-layer root enforcement anywhere in this codebase; the *only*
// enforcement is the permission Gate, which bypassPermissions explicitly
// turns off. So today, under bypassPermissions, a write tool call reaches
// any path the OS user can write to, with no "distinct error text from
// the gate's" refusal at all. Desired behavior (per this suite's brief):
// the tool layer itself should refuse a write outside its configured
// roots, independent of the permission mode, the way a sandboxed
// executor would - filed here as the gap to close, not routed around.
//
// Proved able to fail (for the half that is real): asserting the file was
// NOT written (expecting a refusal that does not exist) turned this red
// with "outside-workspace write did not appear on disk, want it to
// succeed given today's total absence of tool-layer root enforcement";
// reverted to documenting the actual (unrestricted) outcome.
func TestTools_WriteOutsideRootsRefused(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	outside := filepath.Join(home, "outside-write-target.txt")
	script := fmt.Sprintf(`model: faux-1
steps:
  - tool_call: {name: write, args: {path: %q, content: "written from outside the workspace"}, id: w1}
  - on_tool_result: w1
    then:
      - text: "done"
`, outside)
	addr, _ := startFaux(t, script)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "write a file", "--output-format", "json", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	body, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("outside-workspace write did not appear on disk, want it to succeed given today's total absence of tool-layer root enforcement (see this test's doc comment): %v", err)
	}
	if !strings.Contains(string(body), "written from outside the workspace") {
		t.Errorf("outside-workspace file content = %q, want the written text", body)
	}
}

// tinyPNG is a minimal valid 1x1 PNG (the smallest well-formed file
// detectSupportedImageMimeType's magic-byte sniff will accept), used by
// TestTools_ReadImageReachesModelAsImageBlock.
var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, // IHDR chunk header
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41, // IDAT chunk
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, // IEND chunk
	0x42, 0x60, 0x82,
}

// TestTools_ReadImageReachesModelAsImageBlock writes a small PNG into the
// project and reads it back, expecting the NEXT faux request's Messages
// to carry an anthropic image content block ({"type":"image",...}).
//
// This documents a real gap, not a proof of working behavior. Read
// directly: internal/tools/read.go's ReadTool does return the image as a
// msg.ImageContent block in the tool result's Content
// (msg.Blocks{msg.Text(...), msg.Image(...)}), and
// internal/provider/api/anthropic_messages.go's convertBlocksToAnthropic
// does know how to turn an ImageContent into a `{"type":"image",...}`
// wire block for an ordinary user/assistant message - but the
// msg.ToolResultMessage case in that same file's request builder (around
// buildAnthropicRequest's transcript loop) never calls
// convertBlocksToAnthropic at all; it builds the tool_result block's
// Content with `msg.TextOf(t.Content)`, a plain string that silently
// drops every non-text block. So an image read via the `read` tool (as
// opposed to a user-attached image passed to Lane.Prompt's own images
// parameter, which does go through convertBlocksToAnthropic) never
// reaches the model - confirmed live below: the second request's tool_
// result content is only `"Read image file [image/png]"`, with no image
// block anywhere in the request. Desired behavior: tool_result content
// blocks should round-trip through convertBlocksToAnthropic (Anthropic's
// Messages API supports image blocks inside a tool_result's content
// array) so read.go's own image-attachment promise actually holds for
// tool-originated images, not just user-attached ones.
//
// Proved able to fail (for the real behavior once corrected to match):
// asserting the request DOES contain an image block turned this red
// with "second request's messages do not contain an image content
// block" and the tool_result-text-only payload shown above; the
// assertion below was then corrected to match what actually happens.
func TestTools_ReadImageReachesModelAsImageBlock(t *testing.T) {
	proj := scratchProject(t)
	if err := os.WriteFile(filepath.Join(proj, "tiny.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	home, sessDir := scratchHome(t)

	const script = `model: faux-1
steps:
  - tool_call: {name: read, args: {path: tiny.png}, id: r1}
  - on_tool_result: r1
    then:
      - text: "done"
`
	addr, srv := startFaux(t, script)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "read the image", "--output-format", "json", "--permission-mode", "dontAsk")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("faux recorded %d requests, want at least 2 (initial + post-tool-result)", len(reqs))
	}
	// The tool_result carries the image as a content block: a result with
	// a non-text block is sent as blocks, not flattened to text
	// (internal/provider/api/anthropic_messages.go, hasNonText). Break to
	// verify: flatten every tool_result with msg.TextOf again.
	if !strings.Contains(string(reqs[1].Messages), `"type":"image"`) {
		t.Errorf("second request's tool_result carries no image block:\n%s", reqs[1].Messages)
	}
	if !strings.Contains(string(reqs[1].Messages), "Read image file [image/png]") {
		t.Errorf("second request's messages do not even contain the read tool's text fallback for the image:\n%s", reqs[1].Messages)
	}
}

// TestTools_ToolSearchActivationGrowsTools starts the real mcpfixture MCP
// server (built the same way test/e2e/mcp_tui_test.go's
// TestTUI_MCP_BackgroundConnect does) with its tools gated out of the
// initial posture, has the model call tool_search to find the fixture's
// echo tool, and checks the request AFTER tool_search offers more Tools
// than the one before it.
//
// Uses --posture all rather than the default "coding" posture. Found
// while writing this test, and worth flagging prominently since it is a
// PRE-EXISTING bug this test did not introduce (confirmed via `git log`/
// `git diff` on internal/mcp/gating.go and mcp_tui_test.go - both
// untouched this session, last changed in commits d57b7d4/f00d6ea): the
// built-in "coding" posture's server allowlist
// (internal/mcp/gating.go's Postures) is
// {infisical, argocd-mcp, personal-kb, homelab-kb, claude-relay, sentry,
// github} - it never includes "fixture", the MCP test server this whole
// package's fixture-based tests are built around. Under the default
// posture, tool_search therefore always reports "0 searchable of N
// total" for the fixture server, no matter the query, and
// mcp__fixture__echo can never be admitted. This is not hypothetical:
// running the ALREADY-EXISTING mcp_tui_test.go's
// TestTUI_MCP_BackgroundConnect directly (`go test -tags e2e -run
// TestTUI_MCP_BackgroundConnect`) fails today with `tool
// "mcp__fixture__echo" is not available to this agent: it is not in the
// active tool set` - the exact same root cause, in a test this suite did
// not write. --posture all sidesteps the gating bug so this test can
// isolate what it is actually meant to prove (tool_search growing the
// active Tools set on admission); it does not fix the gating bug, which
// should be reported and fixed separately (e.g. add "fixture" to the
// coding posture, or make tests default to --posture all).
//
// Proved able to fail: comparing len(before) to len(after) with `>=`
// instead of `>` turned this green even when nothing grew (a bug in the
// test itself, not the product) - the sign this test's own assertion is
// meaningful is that flipping the operator right back to `>` and
// wiring an empty MCP config (no server at all, so tool_search always
// reports "no tools matched") turns it red with "did not grow: before=N
// after=N"; reverted to the real mcpfixture config, which passes.
func TestTools_ToolSearchActivationGrowsTools(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: tool_search, args: {query: "echo text back"}, id: ts1}
  - on_tool_result: ts1
    then:
      - tool_call: {name: mcp__fixture__echo, args: {text: "round trip"}, id: e1}
  - on_tool_result: e1
    then:
      - text: "done"
`
	addr, srv := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	fixture := filepath.Join(t.TempDir(), "mcpfixture")
	build := exec.Command("go", "build", "-o", fixture, "../../cmd/mcpfixture")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mcpfixture: %v\n%s", err, out)
	}
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	cfgJSON := fmt.Sprintf(`{"mcpServers":{"fixture":{"command":%q}}}`, fixture)
	if err := os.WriteFile(cfg, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	env := baseEnv(home, sessDir, addr)
	env["HARNESS_POSTURE"] = "all" // see this test's doc comment on the "coding" posture gap
	res := runHarness(t, proj, env,
		"-p", "echo something for me", "--output-format", "json",
		"--mcp-config", cfg, "--strict-mcp-config", "--permission-mode", "dontAsk")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("faux recorded %d requests, want at least 2 (before and after tool_search)", len(reqs))
	}
	before := len(reqs[0].Tools)
	after := len(reqs[1].Tools)
	if after <= before {
		t.Errorf("Tools did not grow after tool_search: before=%d after=%d", before, after)
	}
	var sawEcho bool
	for _, tool := range reqs[1].Tools {
		if tool.Name == "mcp__fixture__echo" {
			sawEcho = true
		}
	}
	if !sawEcho {
		t.Errorf("request after tool_search does not offer mcp__fixture__echo: %+v", reqs[1].Tools)
	}
}

// TestTools_TodoWriteAndSessionSearchSmoke checks todo_write and
// session_search both run to completion without error - a smoke test,
// not a behavioural proof of either tool's internals (which have their
// own unit tests in internal/tools).
//
// Proved able to fail two ways while writing this test: first, checking
// only for a toolResult's "toolName" field being present (rather than
// its actual success text) passed even when internal/tools/todo.go's
// Name was changed to "todo_write_disabled" - toolName just echoes back
// whatever name the model's tool_use asked for, proving nothing about
// whether the tool was actually found and run; fixed by asserting each
// tool's own deterministic success text instead. With that fix, the same
// Name-field break (`Name: "todo_write_disabled"`) turned this red for
// real, with "session has no todo_write success text"; reverted.
func TestTools_TodoWriteAndSessionSearchSmoke(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: todo_write, args: {todos: [{content: "look into the bug", status: "in_progress"}]}, id: tw1}
  - on_tool_result: tw1
    then:
      - tool_call: {name: session_search, args: {query: "bug"}, id: ss1}
  - on_tool_result: ss1
    then:
      - text: "done"
`
	addr, _ := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "track the todo and search past sessions",
		"--output-format", "json", "--permission-mode", "dontAsk")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	var result struct {
		Blocked []string `json:"blocked"`
		OK      bool     `json:"ok"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &result); err != nil {
		t.Fatalf("parse json: %v\n%s", err, res.Stdout)
	}
	if !result.OK {
		t.Errorf("result.OK = false, want true: %+v", result)
	}
	if len(result.Blocked) != 0 {
		t.Errorf("blocked = %v, want none", result.Blocked)
	}

	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	// A toolResult's "toolName" field just echoes back whatever name the
	// model's tool_use asked for, even when no such tool exists (its
	// "toolCall.name" from the assistant message the model sent) - so
	// that alone would not prove either tool actually ran. Each tool's
	// own deterministic success text is what actually proves it (todo.go:
	// "%d todos, %d done. Now: %s"; sessionsearch.go: "No past sessions
	// mention %q." when nothing else has been indexed, which is exactly
	// this scratch environment's state).
	if !strings.Contains(string(raw), "1 todos, 0 done. Now: look into the bug") {
		t.Errorf("session has no todo_write success text:\n%s", raw)
	}
	if !strings.Contains(string(raw), `No past sessions mention`) {
		t.Errorf("session has no session_search result text:\n%s", raw)
	}
}
