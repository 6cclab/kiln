//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const fixBugScript = `model: faux-1
steps:
  - text: "I'll look at the file."
  - thinking: "the add function subtracts"
  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call:
          name: edit
          args:
            path: src/math.js
            edits:
              - oldText: "return a - b;"
                newText: "return a + b;"
          id: tc2
  - on_tool_result: tc2
    then:
      - text: "Fixed."
        usage: {input: 812, output: 34}
`

// goldenPath resolves a path under the repo root's testdata/golden,
// relative to this source file (test/e2e/print_test.go) rather than the
// process cwd (go test's cwd is the package directory, test/e2e, not the
// repo root testdata/golden lives under).
func goldenPath(name string) string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "golden", name)
}

// durationMSRe matches a result event's "duration_ms" field so
// assertGolden can normalize it to a fixed value: wall-clock duration is
// inherently non-deterministic between runs, so the golden file records
// "duration_ms":0 and the raw value is asserted separately (see e.g.
// TestPrint_ResultCarriesUsageAndTurns), never against the golden.
var durationMSRe = regexp.MustCompile(`"duration_ms":\d+`)

// assertGolden compares actual against the golden file at path, after
// normalizing any "duration_ms":<n> to "duration_ms":0 in actual (see
// durationMSRe). With UPDATE=1 it rewrites the golden file instead of
// comparing (used once, by hand, to (re)generate testdata/golden/*.ndjson).
func assertGolden(t *testing.T, path, actual string) {
	t.Helper()
	actual = durationMSRe.ReplaceAllString(actual, `"duration_ms":0`)
	if os.Getenv("UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(actual), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("UPDATE=1: wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `UPDATE=1 go test -tags e2e ./test/e2e/... -run %s` to create it)", path, err, t.Name())
	}
	if string(want) != actual {
		t.Errorf("output does not match golden %s\n--- want ---\n%s\n--- got ---\n%s", path, want, actual)
	}
}

// TestPrint_StreamJSON_FixBug drives the built harness binary through the
// canonical fix-bug scenario (testdata/faux/fix-bug.yaml, mirrored here as
// fixBugScript since faux.LoadScript reads the file directly and this test
// wants the same text as its own golden comparison target) with
// --output-format stream-json: the model reads src/math.js, edits it, and
// replies "Fixed." — the same run internal/cli/chat_test.go's
// TestRun_PrintStreamJSON_FixBug drives in-process, here driven through the
// real binary end to end.
func TestPrint_StreamJSON_FixBug(t *testing.T) {
	addr, srv := startFaux(t, fixBugScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "fix the bug",
		"--output-format", "stream-json",
		"--permission-mode", "acceptEdits",
		"--allowed-tools", "Read",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	assertGolden(t, goldenPath("print-fix-bug.ndjson"), res.Stdout)

	// The file on disk is fixed: add() adds, mul() is untouched.
	fixed, err := os.ReadFile(filepath.Join(proj, "src", "math.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fixed), "return a + b;") {
		t.Errorf("src/math.js not fixed, got:\n%s", fixed)
	}
	if !strings.Contains(string(fixed), "return a * b;") {
		t.Errorf("src/math.js: mul() no longer intact, got:\n%s", fixed)
	}

	// Exactly one session JSONL exists, and its transactions include a
	// toolResult for the edit call.
	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"toolName":"edit"`) || !strings.Contains(string(raw), `"role":"toolResult"`) {
		t.Errorf("session file has no edit toolResult entry:\n%s", raw)
	}

	// Faux's last recorded request carries both tools and the base system
	// prompt.
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	last := reqs[len(reqs)-1]
	if !strings.Contains(last.System, "You are a coding assistant") {
		t.Errorf("recorded system prompt missing base persona: %q", last.System)
	}
	var sawEdit, sawRead bool
	for _, tl := range last.Tools {
		if tl.Name == "edit" {
			sawEdit = true
		}
		if tl.Name == "read" {
			sawRead = true
		}
	}
	if !sawEdit || !sawRead {
		t.Errorf("recorded request tools missing edit/read: %+v (edit=%v read=%v)", last.Tools, sawEdit, sawRead)
	}
}

// TestPrint_StreamJSON_ReadBlockedHeadless runs the same scenario WITHOUT
// --allowed-tools Read: under acceptEdits, only edit/write auto-allow (see
// internal/claude/settings/settings.go's Decide), so read falls to "ask",
// and print mode has nobody to ask (internal/claude/permission/
// permission.go's Check: "requires confirmation and no prompt is
// available."). This documents that headless-ask refusal: the read
// tool_end comes back isError:true and the run's blocked list is non-empty,
// even though the run itself still completes (the model gets the error as
// tool content and gets to respond to it, same as any other tool error).
// TestPrint_StreamJSON_EditBlockedHeadless: with no way to prompt (print
// mode), a tool the gate would ask about is refused ("headless ask =
// refusal"). Read-only tools (read/glob/grep) are auto-allowed to match
// Claude Code, which never prompts for them, so the fix-bug script's read
// proceeds; its edit, which asks in manual mode, is the one refused.
func TestPrint_StreamJSON_EditBlockedHeadless(t *testing.T) {
	addr, _ := startFaux(t, fixBugScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "fix the bug",
		"--output-format", "stream-json",
		"--permission-mode", "manual",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	var sawReadOK, sawBlockedEditToolEnd bool
	var blocked []string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse stream-json line %q: %v", line, err)
		}
		if ev["type"] == "tool_end" && ev["name"] == "read" {
			if isErr, _ := ev["isError"].(bool); !isErr {
				sawReadOK = true
			}
		}
		if ev["type"] == "tool_end" && ev["name"] == "edit" {
			if isErr, _ := ev["isError"].(bool); isErr {
				sawBlockedEditToolEnd = true
			}
		}
		if ev["type"] == "result" {
			if b, ok := ev["blocked"].([]any); ok {
				for _, x := range b {
					blocked = append(blocked, x.(string))
				}
			}
		}
	}
	if !sawReadOK {
		t.Errorf("read (a read-only tool) was not auto-allowed in:\n%s", res.Stdout)
	}
	if !sawBlockedEditToolEnd {
		t.Errorf("no tool_end{name:edit, isError:true} in:\n%s", res.Stdout)
	}
	if len(blocked) == 0 {
		t.Error("result.blocked is empty, want the headless-ask refusal reason")
	}
	var foundReason bool
	for _, b := range blocked {
		if strings.Contains(b, "requires confirmation and no prompt is available") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Errorf("blocked = %v, want an entry mentioning the headless-ask refusal", blocked)
	}
}

// TestPrint_JSONFormat and TestPrint_TextFormat check the "json" and "text"
// output-format shapes against the same fix-bug scenario.
func TestPrint_JSONFormat(t *testing.T) {
	addr, _ := startFaux(t, fixBugScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "fix the bug",
		"--output-format", "json",
		"--permission-mode", "acceptEdits",
		"--allowed-tools", "Read",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	var parsed struct {
		OK        bool             `json:"ok"`
		Text      string           `json:"text"`
		ToolCalls []map[string]any `json:"toolCalls"`
		Blocked   []string         `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("parse json output: %v\n%s", err, res.Stdout)
	}
	if !parsed.OK {
		t.Error("ok = false")
	}
	if !strings.Contains(parsed.Text, "Fixed.") {
		t.Errorf("text = %q, want it to contain %q", parsed.Text, "Fixed.")
	}
	if len(parsed.ToolCalls) < 2 {
		t.Errorf("toolCalls = %v, want at least 2", parsed.ToolCalls)
	}
}

func TestPrint_TextFormat(t *testing.T) {
	addr, _ := startFaux(t, fixBugScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "fix the bug",
		"--output-format", "text",
		"--permission-mode", "acceptEdits",
		"--allowed-tools", "Read",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "Fixed.") {
		t.Errorf("stdout = %q, want it to contain %q", res.Stdout, "Fixed.")
	}
}

// TestPrint_ManualMode_BlockedBashStillExitsZero drives
// testdata/faux/bash-echo.yaml under --permission-mode manual: the bash
// call has nobody to ask, so it comes back blocked, but the script's
// on_tool_result step fires regardless (a tool error is content, not a
// wire failure) and the run completes normally — exit code 0, with the
// refusal recorded in the JSON result's "blocked" list.
func TestPrint_ManualMode_BlockedBashStillExitsZero(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "testdata", "faux", "bash-echo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := startFaux(t, string(script))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "run a command",
		"--output-format", "json",
		"--permission-mode", "manual",
	)
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s stdout=%s", res.Code, res.Stderr, res.Stdout)
	}

	var parsed struct {
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("parse json output: %v\n%s", err, res.Stdout)
	}
	found := false
	for _, b := range parsed.Blocked {
		if strings.Contains(b, "requires confirmation and no prompt is available") {
			found = true
		}
	}
	if !found {
		t.Errorf("blocked = %v, want an entry mentioning \"requires confirmation and no prompt is available\"", parsed.Blocked)
	}
}

// TestPrint_EmptyPrompt_ExitsOne is print mode's other exit code: with no
// prompt text at all (no -p value, nothing on stdin), runPrintMode refuses
// with a usage message and exit code 1 before ever talking to a model.
func TestPrint_EmptyPrompt_ExitsOne(t *testing.T) {
	addr, _ := startFaux(t, fixBugScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p")
	if res.Code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s stderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "usage:") {
		t.Errorf("stderr = %q, want a usage message", res.Stderr)
	}
}

// usageTurnsScript drives two turns, each with an explicit usage step, so
// the aggregate usage the result event reports is a known sum rather than
// faux's per-turn default (100 input / 50 output — see
// internal/testkit/faux/anthropic.go's resolveUsage). Turn 1 ends in a
// tool_call (a turn boundary); turn 2, gated on that tool's result, ends
// with plain text and no further tool call, so the run completes after
// exactly two assistant turns.
const usageTurnsScript = `model: faux-1
steps:
  - text: "Looking around."
    usage: {input: 300, output: 40}
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "All done."
        usage: {input: 512, output: 60}
`

// TestPrint_ResultCarriesUsageAndTurns checks the P1 enrichment of the
// print-mode result: usage totals sum every scripted turn's usage step
// (usageTurnsScript's 300+512 input, 40+60 output), num_turns counts the
// two assistant turns (one tool-calling, one final), duration_ms is
// positive (wall-clock, so only checked for sign — the golden comparison
// itself normalizes it, see durationMSRe), and total_cost_usd is exactly 0
// because faux-1 carries no pricing (internal/provider/faux/faux.go's
// provider.ModelCost{}).
func TestPrint_ResultCarriesUsageAndTurns(t *testing.T) {
	addr, _ := startFaux(t, usageTurnsScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "look around then report",
		"--output-format", "json",
		"--permission-mode", "dontAsk",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	var parsed struct {
		OK    bool `json:"ok"`
		Usage struct {
			Input      int `json:"input"`
			Output     int `json:"output"`
			CacheRead  int `json:"cache_read"`
			CacheWrite int `json:"cache_write"`
		} `json:"usage"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		DurationMS   int64   `json:"duration_ms"`
		NumTurns     int     `json:"num_turns"`
		NumToolCalls int     `json:"num_tool_calls"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("parse json output: %v\n%s", err, res.Stdout)
	}
	if !parsed.OK {
		t.Errorf("ok = false, stdout=%s", res.Stdout)
	}
	if parsed.Usage.Input != 812 {
		t.Errorf("usage.input = %d, want 812 (300+512)", parsed.Usage.Input)
	}
	if parsed.Usage.Output != 100 {
		t.Errorf("usage.output = %d, want 100 (40+60)", parsed.Usage.Output)
	}
	if parsed.NumTurns != 2 {
		t.Errorf("num_turns = %d, want 2", parsed.NumTurns)
	}
	if parsed.NumToolCalls != 1 {
		t.Errorf("num_tool_calls = %d, want 1", parsed.NumToolCalls)
	}
	if parsed.TotalCostUSD != 0 {
		t.Errorf("total_cost_usd = %v, want 0 (faux-1 has no pricing)", parsed.TotalCostUSD)
	}
	if parsed.DurationMS <= 0 {
		t.Errorf("duration_ms = %d, want > 0", parsed.DurationMS)
	}
}

// maxTurnsScript would run four tool-calling turns followed by a fifth,
// final, text-only turn if allowed to run to completion. --max-turns 2
// should stop it after the second tool-calling turn, before the harness
// ever requests a third.
const maxTurnsScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo 1"}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call: {name: bash, args: {command: "echo 2"}, id: tc2}
      - on_tool_result: tc2
        then:
          - tool_call: {name: bash, args: {command: "echo 3"}, id: tc3}
          - on_tool_result: tc3
            then:
              - tool_call: {name: bash, args: {command: "echo 4"}, id: tc4}
              - on_tool_result: tc4
                then:
                  - text: "Done after 4."
`

// TestPrint_MaxTurns_StopsRun checks --max-turns's cancel-on-next-turn-start
// behavior (see chat.go's runPrintMode doc comment on the EventTurnEnd /
// EventTurnStart handoff): with --max-turns 2 against maxTurnsScript (which
// wants 4 tool-calling turns plus a final text-only turn), the run is
// cancelled once the harness is about to start turn 3, exits 1, and the
// JSON result reports ok:false and reason:"max-turns-exceeded". The session
// file is the independent check that the harness itself really stopped
// after 2 turns rather than merely truncating the printed result: it holds
// exactly 2 assistant message entries.
func TestPrint_MaxTurns_StopsRun(t *testing.T) {
	addr, _ := startFaux(t, maxTurnsScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "run several commands",
		"--output-format", "json",
		"--permission-mode", "dontAsk",
		"--max-turns", "2",
	)
	if res.Code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%s stderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "stopped after 2 turns") {
		t.Errorf("stderr = %q, want a --max-turns stop message", res.Stderr)
	}

	var parsed struct {
		OK       bool   `json:"ok"`
		Reason   string `json:"reason"`
		NumTurns int    `json:"num_turns"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("parse json output: %v\n%s", err, res.Stdout)
	}
	if parsed.OK {
		t.Errorf("ok = true, want false")
	}
	if parsed.Reason != "max-turns-exceeded" {
		t.Errorf("reason = %q, want max-turns-exceeded", parsed.Reason)
	}

	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), `"role":"assistant"`); n > 2 {
		t.Errorf("session file has %d assistant messages, want at most 2 (--max-turns 2 stopped the run):\n%s", n, raw)
	}
}
