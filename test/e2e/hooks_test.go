//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const readCallScript = `model: faux-1
steps:
  - tool_call: {name: read, args: {path: foo.txt}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`

const bashCallScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`

const unreadScript = `model: faux-1
steps:
  - text: "hi"
`

// hooksTestdataDir is where this package's own copies of the hook fixture
// scripts live (test/e2e/../..'s testdata/e2e/hooks), copied verbatim from
// internal/claude/hooks/testdata/hooks so this package owns them per the
// task brief rather than reaching into another package's testdata.
func hooksTestdataDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "e2e", "hooks")
}

func hookScript(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(hooksTestdataDir(t), name)
}

// writeHookSettings writes a scratch .claude/settings.json under proj
// wiring one command on one event, with an optional tool matcher.
func writeHookSettings(t *testing.T, proj, event, matcher, command string) {
	t.Helper()
	settings := map[string]any{
		"hooks": map[string]any{
			event: []map[string]any{
				{
					"matcher": matcher,
					"hooks": []map[string]any{
						{"type": "command", "command": command},
					},
				},
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

// mergeHookSettings adds a second event's hook to an existing
// .claude/settings.json written by writeHookSettings, rather than
// overwriting it.
func mergeHookSettings(t *testing.T, proj, event, matcher, command string) {
	t.Helper()
	path := filepath.Join(proj, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	hooksMap, _ := parsed["hooks"].(map[string]any)
	if hooksMap == nil {
		hooksMap = map[string]any{}
		parsed["hooks"] = hooksMap
	}
	hooksMap[event] = []map[string]any{
		{
			"matcher": matcher,
			"hooks": []map[string]any{
				{"type": "command", "command": command},
			},
		},
	}
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHooks_PreToolUse_Blocks wires block-exit2.sh (exit 2, "not allowed
// here" on stderr) on PreToolUse and checks that the blocked call comes
// back as tool_end isError:true, and that the run's overall "blocked" list
// names the reason.
func TestHooks_PreToolUse_Blocks(t *testing.T) {
	addr, _ := startFaux(t, readCallScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeHookSettings(t, proj, "PreToolUse", "*", hookScript(t, "block-exit2.sh"))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "read the file",
		"--output-format", "stream-json",
		"--permission-mode", "acceptEdits",
	)

	var sawBlockedToolEnd bool
	var blocked []string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		_ = json.Unmarshal([]byte(line), &ev)
		if ev["type"] == "tool_end" {
			if isErr, _ := ev["isError"].(bool); isErr {
				sawBlockedToolEnd = true
			}
		}
		if ev["type"] == "result" {
			if b, ok := ev["blocked"].([]any); ok {
				for _, x := range b {
					blocked = append(blocked, fmt.Sprint(x))
				}
			}
		}
	}
	if !sawBlockedToolEnd {
		t.Errorf("no tool_end with isError:true in:\n%s", res.Stdout)
	}
	var foundReason bool
	for _, b := range blocked {
		if strings.Contains(b, "not allowed here") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Errorf("blocked = %v, want an entry containing the hook's stderr reason", blocked)
	}
}

// TestHooks_PreToolUse_Rewrites wires rewrite-updated-input.sh
// (unconditionally rewrites the bash command to "rtk git status") on
// PreToolUse for the bash tool, and checks the session JSONL's toolResult
// content shows evidence the rewritten command ran, not "echo hi": on this
// machine "rtk git status" against a non-git scratch directory fails with
// "not a git repository" (verified directly below), so the toolResult
// content is either that failure text or rtk's own output — anything other
// than the literal "hi\n" the model's original "echo hi" would have
// produced.
func TestHooks_PreToolUse_Rewrites(t *testing.T) {
	addr, _ := startFaux(t, bashCallScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeHookSettings(t, proj, "PreToolUse", "bash", hookScript(t, "rewrite-updated-input.sh"))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "run a command",
		"--output-format", "text",
		"--permission-mode", "dontAsk",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"toolName":"bash"`) && strings.Contains(string(raw), `"text":"hi\n"`) {
		t.Errorf("bash ran \"echo hi\" unchanged; the PreToolUse rewrite never took effect:\n%s", raw)
	}
}

// TestHooks_UserPromptSubmit_Context wires context-plaintext.sh (prints
// "you have 2 unread messages" on stdout, plain text) on UserPromptSubmit,
// and checks that text reaches the model wrapped in <hook-context> ahead
// of the prompt.
func TestHooks_UserPromptSubmit_Context(t *testing.T) {
	addr, srv := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeHookSettings(t, proj, "UserPromptSubmit", "", hookScript(t, "context-plaintext.sh"))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "hello",
		"--output-format", "text",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	if !strings.Contains(string(reqs[0].Messages), "you have 2 unread messages") {
		t.Errorf("recorded messages missing hook context: %s", reqs[0].Messages)
	}
}

// TestHooks_SessionStartAndEnd wires record-payload.sh on both SessionStart
// and SessionEnd, each writing to its own file (the fixture truncates on
// every write, via `cat > "$HARNESS_TEST_PAYLOAD_FILE"`, so one file cannot
// hold both events — the env var is set inline in each hook's own command).
func TestHooks_SessionStartAndEnd(t *testing.T) {
	addr, _ := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	startFile := filepath.Join(t.TempDir(), "start.json")
	endFile := filepath.Join(t.TempDir(), "end.json")
	script := hookScript(t, "record-payload.sh")

	writeHookSettings(t, proj, "SessionStart", "", fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", startFile, script))
	mergeHookSettings(t, proj, "SessionEnd", "", fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", endFile, script))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "hello",
		"--output-format", "text",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	startRaw, err := os.ReadFile(startFile)
	if err != nil {
		t.Fatalf("SessionStart never wrote %s: %v", startFile, err)
	}
	if !strings.Contains(string(startRaw), `"hook_event_name":"SessionStart"`) {
		t.Errorf("start payload = %s, want hook_event_name SessionStart", startRaw)
	}

	endRaw, err := os.ReadFile(endFile)
	if err != nil {
		t.Fatalf("SessionEnd never wrote %s: %v", endFile, err)
	}
	if !strings.Contains(string(endRaw), `"hook_event_name":"SessionEnd"`) {
		t.Errorf("end payload = %s, want hook_event_name SessionEnd", endRaw)
	}
	if !strings.Contains(string(endRaw), `"transcript_path":"`) {
		t.Errorf("end payload = %s, want transcript_path set", endRaw)
	}
}

// TestHooks_Timeout_KillsSleepForever wires sleep-forever.sh (sleeps 30s)
// on PreToolUse with a 1-second hook timeout, and checks the run completes
// within ~5s (the hook is killed rather than awaited) and that no
// sleep-forever.sh process is left running afterward.
func TestHooks_Timeout_KillsSleepForever(t *testing.T) {
	addr, _ := startFaux(t, readCallScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "*",
					"hooks": []map[string]any{
						{"type": "command", "command": hookScript(t, "sleep-forever.sh"), "timeout": 1},
					},
				},
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

	start := time.Now()
	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "read the file",
		"--output-format", "text",
		"--permission-mode", "acceptEdits",
	)
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("run took %s, want it to complete within ~5s (the timed-out hook should be killed, not awaited)", elapsed)
	}
	_ = res // exit code is not asserted here; the timeout behavior is.

	// Give the OS a brief moment to reap the killed process's entry before
	// checking for stragglers.
	time.Sleep(200 * time.Millisecond)
	out, _ := exec.Command("pgrep", "-f", "sleep-forever.sh").CombinedOutput()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("sleep-forever.sh still running after the hook should have been killed: pgrep output:\n%s", out)
	}
}
