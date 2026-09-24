//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// assertGolden compares actual against the golden file at path. With
// UPDATE=1 it rewrites the golden file instead of comparing (used once, by
// hand, to (re)generate testdata/golden/print-fix-bug.ndjson).
func assertGolden(t *testing.T, path, actual string) {
	t.Helper()
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
func TestPrint_StreamJSON_ReadBlockedHeadless(t *testing.T) {
	addr, _ := startFaux(t, fixBugScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "fix the bug",
		"--output-format", "stream-json",
		"--permission-mode", "acceptEdits",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	var sawBlockedReadToolEnd bool
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
			if isErr, _ := ev["isError"].(bool); isErr {
				sawBlockedReadToolEnd = true
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
	if !sawBlockedReadToolEnd {
		t.Errorf("no tool_end{name:read, isError:true} in:\n%s", res.Stdout)
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
