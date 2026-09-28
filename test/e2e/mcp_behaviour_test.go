//go:build e2e

// Package e2e: MCP behaviour regression suite (phase P2). Ownership: this
// file, testdata/behaviour/mcp/, and any testdata/faux fixtures prefixed
// "behaviour-" belong to this file alone; test/e2e/mcp_tui_test.go and the
// other shared e2e test files are read-only reference material here, never
// edited.
//
// Every test below cites the exact source line it depends on rather than
// assuming behaviour, per this task's evidence requirement. Two real
// discrepancies between the original plan and the shipped code were found
// during research and are documented at the top of the tests they affect
// (TestMCP_FirstTurnDoesNotWaitForConnect, TestMCP_PostureIndexSizing)
// rather than being quietly routed around.
package e2e

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/screen"
)

// mcpBuildFixture builds cmd/mcpfixture once per call site into a fresh
// temp dir, mirroring test/e2e/mcp_tui_test.go's own build step (that file
// is not touched here; the build step is duplicated deliberately, per this
// task's helpers rule: new helpers live in this file, not injected into
// shared ones).
func mcpBuildFixture(t *testing.T) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "mcpfixture")
	build := exec.Command("go", "build", "-o", fixture, "../../cmd/mcpfixture")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build mcpfixture: %v\n%s", err, out)
	}
	return fixture
}

// mcpWriteConfig writes a --mcp-config JSON file wiring one or more
// servers by name to {command, args}, and returns its path.
func mcpWriteConfig(t *testing.T, servers map[string][]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"mcpServers":{`)
	first := true
	for name, cmdArgs := range servers {
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, "%q:{\"command\":%q", name, cmdArgs[0])
		if len(cmdArgs) > 1 {
			b.WriteString(`,"args":[`)
			for i, a := range cmdArgs[1:] {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, "%q", a)
			}
			b.WriteString("]")
		}
		b.WriteString("}")
	}
	b.WriteString("}}")
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(cfg, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// mcpHookScript returns the absolute path to a hook script this file owns,
// under testdata/behaviour/mcp/ (as opposed to hooksTestdataDir's
// testdata/hooks, which belongs to test/e2e/hooks_test.go).
func mcpHookScript(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "testdata", "behaviour", "mcp", name)
}

// mcpLogTimeRe matches slog's default TextHandler time attribute at the
// start of a line, e.g. "time=2026-09-25T11:42:28.870-04:00 level=INFO
// msg=...". Verified empirically against internal/diag's actual output
// (slog.NewTextHandler with no ReplaceAttr) before writing this regexp,
// not assumed from slog's docs.
var mcpLogTimeRe = regexp.MustCompile(`^time=(\S+)`)

// mcpWaitLogLine polls dir (a scratch HARNESS_LOG_DIR) for a *.log file
// containing a line matching contains, up to timeout, and returns that
// line's parsed "time=" timestamp plus the full line. Polling (not a
// single read) because the harness process writes the line asynchronously
// relative to the test goroutine.
func mcpWaitLogLine(t *testing.T, dir, contains string, timeout time.Duration) (time.Time, string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
				continue
			}
			f, err := os.Open(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			scanner := bufio.NewScanner(f)
			scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, contains) {
					f.Close()
					m := mcpLogTimeRe.FindStringSubmatch(line)
					if m == nil {
						t.Fatalf("log line has no leading time= attribute: %q", line)
					}
					ts, err := time.Parse("2006-01-02T15:04:05.000Z07:00", m[1]) // Z07:00 accepts "Z" (UTC, as on CI) and offsets
					if err != nil {
						t.Fatalf("parse log time %q: %v", m[1], err)
					}
					return ts, line
				}
			}
			f.Close()
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line containing %q within %s under %s", contains, timeout, dir)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// mcpCountLogLines counts, across every *.log file under dir, the lines
// containing contains.
func mcpCountLogLines(t *testing.T, dir, contains string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, contains) {
				n++
			}
		}
	}
	return n
}

// ---------------------------------------------------------------------
// Test 5: TestMCP_FirstTurnDoesNotWaitForConnect
// ---------------------------------------------------------------------

// mcpPlainTextScript is a one-turn script that needs no tool call, so the
// turn can finish the instant the model replies -- nothing here depends on
// the MCP server having connected.
const mcpPlainTextScript = `model: faux-1
steps:
  - text: "hi there"
    usage: {input: 5, output: 2}
`

// TestMCP_FirstTurnDoesNotWaitForConnect proves the harness's first turn
// does not block on a slow MCP connect.
//
// This can only be shown in TUI/interactive mode: internal/cli/chat.go:402-407
// documents and implements the opposite for print mode --
//
//	"Print mode has exactly one prompt, so the MCP catalog must be
//	complete before it runs. Interactive mode connects after the TUI is
//	up ... and registers the tools when done."
//
// -- i.e. `connectMCP(mcpCtx)` is called synchronously, on the same
// goroutine, before the prompt ever runs, when args.Print is true
// (chat.go:402-407). Only the interactive path (internal/cli/tui.go:252-266)
// launches connect in its own goroutine ("go func() { ... deps.ConnectMCP(...) ... }()")
// while `program.Run()` (and so the first turn) proceeds independently.
// This is a real discrepancy against this task's general "prefer headless
// print mode" guidance: for this specific test, print mode would prove the
// opposite of what's being asserted, so this test uses the TUI, exactly as
// test/e2e/mcp_tui_test.go's TestTUI_MCP_BackgroundConnect does.
//
// cmd/mcpfixture's new -connect-delay flag (added for this test only, see
// its doc comment) sleeps 3s before the fixture process starts reading
// stdin at all, so the client's MCP initialize response is delayed exactly
// that long without touching protocol internals.
//
// Proof is by real timestamps, not just ordering: the faux server records
// a wall-clock Time on every request it receives (internal/testkit/faux/recording.go's
// Request.Time), and internal/diag's run log records a real "time="
// attribute per line (slog's default TextHandler; verified empirically --
// see mcpLogTimeRe's comment -- not assumed). The test asserts the first
// provider request's recorded Time is before the run log's "mcp connected"
// line's time, with the 3s connect delay as the margin.
func TestMCP_FirstTurnDoesNotWaitForConnect(t *testing.T) {
	fixture := mcpBuildFixture(t)
	const connectDelay = "3s"
	cfg := mcpWriteConfig(t, map[string][]string{
		"fixture": {fixture, "-connect-delay", connectDelay},
	})

	addr, srv := startFaux(t, mcpPlainTextScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	logDir := t.TempDir()

	tuiExtraOpts = []screen.Option{
		screen.WithEnv("HARNESS_LOG_DIR", logDir),
	}
	t.Cleanup(func() { tuiExtraOpts = nil })

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", cfg, "--permission-mode", "bypassPermissions")
	waitReady(t, s)

	s.Send("say hi")
	s.SendKey("enter")
	// 2.5s: comfortably above real turn latency, comfortably below the 3s
	// connect delay. If the turn actually waited on connect, this would
	// time out (a real, observable RED -- see the break/revert evidence in
	// this task's final report, not asserted here).
	if err := s.WaitFor(turnSummaryPattern, 2500*time.Millisecond); err != nil {
		t.Fatalf("turn did not finish before the MCP connect delay elapsed (so it must have waited on connect): %v", err)
	}

	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	firstReqTime := reqs[0].Time

	connectedTime, connectedLine := mcpWaitLogLine(t, logDir, `msg="mcp connected" server=fixture`, 6*time.Second)

	if !firstReqTime.Before(connectedTime) {
		t.Errorf("first provider request (%s) is not before MCP connect completion (%s):\nfirst request time from srv.Requests()[0].Time\nrun log line: %s",
			firstReqTime, connectedTime, connectedLine)
	}
	gap := connectedTime.Sub(firstReqTime)
	if gap < 1*time.Second {
		t.Errorf("first request landed only %s before MCP connect completed; want close to the %s connect delay, to be sure this isn't a coincidence of scheduling", gap, connectDelay)
	}
	t.Logf("first provider request at %s, MCP connect completed at %s, gap %s (connect delay was %s)", firstReqTime, connectedTime, gap, connectDelay)
}

// ---------------------------------------------------------------------
// Test 6: TestMCP_FailureReportedOnce
// ---------------------------------------------------------------------

// mcpThreeTurnScript drives three assistant turns (two tool round trips
// plus a final text reply) off the resident "bash" tool, entirely
// independent of any MCP server, so a broken MCP config's failure
// reporting can be checked across a multi-turn run without needing the
// MCP server to do anything.
const mcpThreeTurnScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo one"}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call: {name: bash, args: {command: "echo two"}, id: tc2}
  - on_tool_result: tc2
    then:
      - text: "done"
        usage: {input: 20, output: 4}
`

// TestMCP_FailureReportedOnce configures one MCP server whose command does
// not exist, drives a 3-turn (three assistant round trips: two tool_calls
// plus a final text reply, mcpThreeTurnScript above) run in a single `-p`
// process, and asserts the connect failure is reported exactly once on
// stderr and exactly once in the run log -- not once per turn.
//
// This is a real thing to regress: internal/cli/chat.go's connectMCP is
// called exactly once, before any turn runs (chat.go:397-408), and
// warnFailedServers (chat.go:407, mcp.go:79-82) iterates hub.Statuses()
// once, so there is exactly one call site for each message today. A
// regression that moved the warning inside the turn loop (e.g. "remind the
// user about unavailable MCP servers each turn") would multiply both
// lines; this test would catch it.
//
// Failure text sourced from internal/mcp/hub.go: describeConnectError
// (hub.go:73-77) returns "command not found: <path>" for exec.ErrNotFound,
// which record() (hub.go:144-155) logs once via
// diag.L().Warn("mcp failed", "server", ..., "reason", ...) and chat.go's
// warnFailedServers (mcp.go:79-82) writes once to stderr as
// "mcp: <name> unavailable — <error>\n".
func TestMCP_FailureReportedOnce(t *testing.T) {
	const badCommand = "/nonexistent/no-such-binary"
	cfg := mcpWriteConfig(t, map[string][]string{
		"badserver": {badCommand},
	})

	addr, _ := startFaux(t, mcpThreeTurnScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	logDir := t.TempDir()

	env := baseEnv(home, sessDir, addr)
	env["HARNESS_LOG_DIR"] = logDir

	res := runHarness(t, proj, env,
		"-p", "run two commands then report",
		"--output-format", "text",
		"--permission-mode", "bypassPermissions",
		"--strict-mcp-config", "--mcp-config", cfg,
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	const stderrWant = "mcp: badserver unavailable — command not found: " + badCommand
	stderrCount := strings.Count(res.Stderr, stderrWant)
	if stderrCount != 1 {
		t.Errorf("stderr contains %q %d times, want exactly 1:\n%s", stderrWant, stderrCount, res.Stderr)
	}

	logCount := mcpCountLogLines(t, logDir, `msg="mcp failed" server=badserver`)
	if logCount != 1 {
		t.Errorf(`run log contains a line matching msg="mcp failed" server=badserver %d times, want exactly 1`, logCount)
	}
}

// ---------------------------------------------------------------------
// Test 7: TestMCP_PostureIndexSizing
// ---------------------------------------------------------------------

// mcpEchoOnlyScript is a one-turn, no-tool-call script good enough to
// inspect only the first recorded request's Tools/System.
const mcpEchoOnlyScript = `models:
  faux-1:
    - text: "ok"
  faux-2:
    - text: "ok"
`

// TestMCP_PostureIndexSizing proves posture-index applies at every window
// size and that the posture, not the window, decides what gets indexed. The
// fixture is registered under the server name "grafana", which the default
// "coding" posture excludes: neither faux-1 (128k) nor faux-2 (32k) lists
// its tools in the system prompt, and both still gate every MCP tool behind
// tool_search (no MCP schema in the first request's Tools). Break to
// verify: restore the old window-based StrategyForWindow, or make
// IndexScope return the posture for every strategy.
func TestMCP_PostureIndexSizing(t *testing.T) {
	fixture := mcpBuildFixture(t)
	cfg := mcpWriteConfig(t, map[string][]string{"grafana": {fixture}})

	run := func(model string) ([]string, string) {
		addr, srv := startFaux(t, mcpEchoOnlyScript)
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		env := baseEnv(home, sessDir, addr)
		env["HARNESS_MODEL"] = model
		res := runHarness(t, proj, env, "-p", "hello", "--output-format", "text", "--permission-mode", "bypassPermissions",
			"--strict-mcp-config", "--mcp-config", cfg)
		if res.Code != 0 {
			t.Fatalf("model %s: exit code %d, stderr=%s", model, res.Code, res.Stderr)
		}
		reqs := srv.Requests()
		if len(reqs) == 0 {
			t.Fatalf("model %s: faux recorded no requests", model)
		}
		var tools []string
		for _, ts := range reqs[0].Tools {
			tools = append(tools, ts.Name)
		}
		return tools, reqs[0].System
	}
	tools1, sys1 := run("faux/faux-1")
	tools2, sys2 := run("faux/faux-2")

	for _, tc := range []struct {
		label string
		tools []string
	}{{"faux-1 (posture-index, 128k)", tools1}, {"faux-2 (posture-index, 32k)", tools2}} {
		sawSearch := false
		for _, name := range tc.tools {
			if strings.HasPrefix(name, "mcp__") {
				t.Errorf("%s: first request's Tools already offers %q; expected it gated behind tool_search on both tiers", tc.label, name)
			}
			if name == "tool_search" {
				sawSearch = true
			}
		}
		if !sawSearch {
			t.Errorf("%s: first request's Tools has no tool_search entry: %v", tc.label, tc.tools)
		}
	}
	if count := strings.Count(sys1, "mcp__grafana__"); count != 0 {
		t.Errorf("faux-1 (128k, posture-index) indexes %d grafana tools, want 0 (coding posture excludes grafana):\n%s", count, sys1)
	}
	if count := strings.Count(sys2, "mcp__grafana__"); count != 0 {
		t.Errorf("faux-2 (32k, posture-index) indexes %d grafana tools, want 0 (coding posture excludes grafana):\n%s", count, sys2)
	}
}

// ---------------------------------------------------------------------
// Test 8: TestMCP_ToolSearchThenCall
// ---------------------------------------------------------------------

// mcpToolSearchThenCallScript mirrors test/e2e/mcp_tui_test.go's
// mcpToolSearchScript (tool_search for "echo", then call
// mcp__fixture__echo), reproduced here rather than imported/shared because
// this file owns no dependency on that file's unexported const.
const mcpToolSearchThenCallScript = `model: faux-1
steps:
  - tool_call: {name: tool_search, args: {query: "echo text back"}, id: ts1}
  - on_tool_result: ts1
    then:
      - tool_call: {name: mcp__fixture__echo, args: {text: "round trip via headless"}, id: e1}
  - on_tool_result: e1
    then:
      - text: "Echoed."
        usage: {input: 20, output: 5}
`

// TestMCP_ToolSearchThenCall drives tool_search then a call to the
// admitted tool through headless print mode (`-p`), and checks two things
// against srv.Requests() -- what the faux server recorded of what the
// MODEL was offered and sent, which is the right vantage point regardless
// of MCP transport (in-process or stdio):
//
//  1. The Tools list size grows between the request before tool_search's
//     result and the request after it: internal/tools/toolsearch.go's
//     ToolSearchTool admits matches into state (toolsearch.go:127) and
//     calls onAdmit (toolsearch.go:129-131), which internal/cli/mcp.go's
//     mcpSession.regate (mcp.go:145-152) turns into
//     lane.SetActiveTools(...) before the harness's next request goes out.
//  2. The fixture (an out-of-process stdio server -- cmd/mcpfixture, not
//     an in-memory transport) actually executed the call: its echoed text
//     ("round trip via headless") appears in the tool_result content of
//     the request that follows the echo call, proving the round trip left
//     the test process.
//
// HARNESS_POSTURE=all is required here: internal/mcp/gating.go's Postures
// (gating.go:37-50) does not list "fixture" under the default "coding"
// posture, and internal/tools/toolsearch.go's ToolSearchTool scopes its
// searchable set to InPosture tools only (toolsearch.go:58-63) -- verified
// directly (a small throwaway test against mcpgate.InPosture) rather than
// assumed, since test/e2e/mcp_tui_test.go's own tool-search test passes
// without setting posture at all. That test's pass is not evidence the
// search succeeds under the default posture: its final assertion checks
// srv()'s recordedMessages (conversation history, which contains the
// tool_use block for any call the scripted model made, admitted or not),
// not the request's Tools field -- so it cannot tell gated-and-executed-
// anyway apart from actually-admitted. This test checks the real Tools
// field instead, so it needs the fixture actually in scope to search.
func TestMCP_ToolSearchThenCall(t *testing.T) {
	fixture := mcpBuildFixture(t)
	cfg := mcpWriteConfig(t, map[string][]string{"fixture": {fixture}})

	addr, srv := startFaux(t, mcpToolSearchThenCallScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_POSTURE"] = "all"

	res := runHarness(t, proj, env,
		"-p", "search for an echo tool and use it",
		"--output-format", "text",
		"--permission-mode", "bypassPermissions",
		"--strict-mcp-config", "--mcp-config", cfg,
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	reqs := srv.Requests()
	if len(reqs) < 3 {
		t.Fatalf("got %d requests, want at least 3 (initial, post-tool_search, post-echo): recorded models=%v", len(reqs), mcpReqModels(reqs))
	}

	beforeCount := len(reqs[0].Tools)
	afterCount := len(reqs[1].Tools)
	if afterCount <= beforeCount {
		t.Errorf("Tools list did not grow after tool_search admitted mcp__fixture__echo: before=%d after=%d", beforeCount, afterCount)
	}
	var afterHasEcho bool
	for _, ts := range reqs[1].Tools {
		if ts.Name == "mcp__fixture__echo" {
			afterHasEcho = true
		}
	}
	if !afterHasEcho {
		t.Errorf("request after tool_search's result does not offer mcp__fixture__echo; Tools=%v", mcpToolNames(reqs[1].Tools))
	}

	lastMessages := string(reqs[len(reqs)-1].Messages)
	if !strings.Contains(lastMessages, "round trip via headless") {
		t.Errorf("no request's message history shows the fixture's echoed content \"round trip via headless\" (the fixture process never responded, or its response never round-tripped back):\n%s", lastMessages)
	}
}

// mcpToolNames extracts the Name field of every recorded ToolSpec, for
// readable failure messages.
func mcpToolNames(ts []tkfaux.ToolSpec) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

// mcpReqModels extracts the Model field of every recorded request, for
// readable failure messages when a test expected more requests than
// arrived.
func mcpReqModels(reqs []tkfaux.Request) []string {
	out := make([]string, len(reqs))
	for i, r := range reqs {
		out[i] = r.Model
	}
	return out
}

// ---------------------------------------------------------------------
// Test 9: TestMCP_PreToolUseRewriteReachesFixture
// ---------------------------------------------------------------------

// mcpEchoOneShotScript calls mcp__fixture__echo once with an "original"
// text value that testdata/behaviour/mcp/rewrite-echo-text.sh's PreToolUse
// hook rewrites to "rewritten-by-hook" before the call reaches the
// fixture.
const mcpEchoOneShotScript = `model: faux-1
steps:
  # tool_search admits the fixture tool first: a gated tool is refused at
  # execution until admitted (internal/harness/turn.go's toolActive).
  - tool_call: {name: tool_search, args: {query: "echo"}, id: ts1}
  - on_tool_result: ts1
    then:
      - tool_call: {name: mcp__fixture__echo, args: {text: "original-value"}, id: e1}
  - on_tool_result: e1
    then:
      - text: "Echoed."
        usage: {input: 10, output: 3}
`

// TestMCP_PreToolUseRewriteReachesFixture wires
// testdata/behaviour/mcp/rewrite-echo-text.sh on PreToolUse for
// mcp__fixture__echo (mirroring testdata/hooks/rewrite-updated-input.sh's
// pattern from test/e2e/hooks_test.go's TestHooks_PreToolUse_Rewrites,
// reusing that file's writeHookSettings helper directly since it's shared
// e2e-package machinery, not something this task's ownership rule bars
// calling). The hook unconditionally rewrites the echo call's "text" to
// "rewritten-by-hook" (internal/claude/hooks/runner.go:48's UpdatedInput
// fully replaces the tool's args map, confirmed by reading runner.go and
// its own tests, not assumed).
//
// The fixture is the real cmd/mcpfixture stdio subprocess (not an
// in-memory transport), so the only way "rewritten-by-hook" can appear in
// the NEXT request's tool_result content is if the rewritten args actually
// crossed the process boundary to the fixture and its echo tool handler
// (internal/testkit/mcpfixture/server.go's echo handler, which returns
// args.Text verbatim) ran on them. The script admits the tool through
// tool_search first, because execution is gated by the lane's active tool
// set (internal/harness/turn.go's toolActive), and an unlisted MCP server
// such as the fixture is in scope for every posture (internal/mcp/gating.go
// InPosture).
func TestMCP_PreToolUseRewriteReachesFixture(t *testing.T) {
	fixture := mcpBuildFixture(t)
	cfg := mcpWriteConfig(t, map[string][]string{"fixture": {fixture}})

	addr, srv := startFaux(t, mcpEchoOneShotScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeHookSettings(t, proj, "PreToolUse", "mcp__fixture__echo", mcpHookScript(t, "rewrite-echo-text.sh"))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "echo the original value",
		"--output-format", "text",
		"--permission-mode", "bypassPermissions",
		"--strict-mcp-config", "--mcp-config", cfg,
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("got %d requests, want at least 2 (initial tool_call, then its tool_result)", len(reqs))
	}
	lastMessages := string(reqs[len(reqs)-1].Messages)
	// The assistant's own tool_use.input block still shows
	// "original-value" -- that is the model's stated call, not what the
	// fixture received, and rewriting it is not what PreToolUse claims to
	// do. What must change is the tool_result content, i.e. what the
	// fixture actually echoed back, so the check is scoped to that field
	// specifically rather than the whole message blob.
	const wantResult = `"tool_result","tool_use_id":"toolu_e1","content":"rewritten-by-hook"`
	const originalResult = `"tool_result","tool_use_id":"toolu_e1","content":"original-value"`
	if strings.Contains(lastMessages, originalResult) {
		t.Errorf("fixture's echoed tool_result still contains the original, unrewritten value \"original-value\"; the PreToolUse rewrite never reached the fixture process:\n%s", lastMessages)
	}
	if !strings.Contains(lastMessages, wantResult) {
		t.Errorf("no request's tool_result shows the fixture echoing the hook-rewritten value \"rewritten-by-hook\" (fixture never received or never returned it):\n%s", lastMessages)
	}
}
