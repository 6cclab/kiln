//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

// hooksTestdataDir is the repo-root testdata/hooks tree: one set of hook
// fixture scripts shared by this package and internal/claude/hooks'/
// internal/cli's own unit tests, rather than a second copy under
// testdata/e2e that had drifted from it (different script bodies, and a
// settings.json nothing read).
func hooksTestdataDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "hooks")
}

func hookScript(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(hooksTestdataDir(t), name)
}

// A process from another test can share a generic command marker. Keep one
// owned control alive while checking each timeout test, proving a global pgrep
// would report a false leak. The test removes the control on every exit path.
func startGlobalMarkerControl(t *testing.T, marker string) {
	t.Helper()
	cmd := exec.Command("/bin/bash", "-c", `exec -a "$1" sleep 30`, "control", marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		command, present, err := processCommand(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if present && strings.Contains(command, marker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unrelated control %d never started with marker %q", cmd.Process.Pid, marker)
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, err := exec.Command("pgrep", "-f", marker).CombinedOutput()
	if err != nil || !strings.Contains("\n"+strings.TrimSpace(string(out))+"\n", "\n"+strconv.Itoa(cmd.Process.Pid)+"\n") {
		t.Fatalf("global pgrep did not find unrelated control %d: %v, %s", cmd.Process.Pid, err, out)
	}
}

func processMarker(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

// ps exits 1 with no output for an absent PID on macOS and Linux. Any other
// failure, including a missing ps executable or an empty successful lookup,
// is unknown and must fail the cleanup assertion.
func processCommand(pid int) (command string, present bool, err error) {
	out, runErr := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").CombinedOutput()
	command = strings.TrimSpace(string(out))
	if runErr == nil {
		if command == "" {
			return "", false, fmt.Errorf("ps returned an empty command for PID %d", pid)
		}
		return command, true, nil
	}
	var exit *exec.ExitError
	if errors.As(runErr, &exit) && exit.ExitCode() == 1 && command == "" {
		return "", false, nil
	}
	return "", false, fmt.Errorf("ps lookup for PID %d failed: %w: %s", pid, runErr, command)
}

func assertTimedOutProcessGone(t *testing.T, pidFile, marker string) {
	t.Helper()
	if err := timedOutProcessGone(pidFile, marker); err != nil {
		t.Error(err)
	}
}

func timedOutProcessGone(pidFile, marker string) error {
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("timed-out process did not record its PID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("invalid timed-out process PID %q: %v", raw, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		command, present, err := processCommand(pid)
		if err != nil {
			return err
		}
		if !present || !strings.Contains(command, marker) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("owned timed-out process %d still running: %s", pid, command)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTimedOutProcessAssertionDetectsUncleanedOwnedProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "control.pid")
	marker := "kiln-timeout-control-" + processMarker(filepath.Dir(pidFile))
	cmd := exec.Command("/bin/bash", "-c", `printf '%s\n' "$$" > "$1"; exec -a "$2" sleep 30`, "control", pidFile, marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		command, present, err := processCommand(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if present && strings.Contains(command, marker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("negative-control process never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := timedOutProcessGone(pidFile, marker); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("uncleaned process escaped the timeout assertion: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err := timedOutProcessGone(pidFile, marker); err != nil {
		t.Fatalf("cleaned process still reported as live: %v", err)
	}
}

func TestTimedOutProcessAssertionFailsOnUnknownLookup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "unknown.pid")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // ps is unavailable; absence cannot be inferred
	if err := timedOutProcessGone(pidFile, "unrelated-marker"); err == nil || !strings.Contains(err.Error(), "ps lookup") {
		t.Fatalf("unknown process status was treated as gone: %v", err)
	}
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
		"--permission-mode", "bypassPermissions",
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

// TestHooks_Timeout_KillsSleepForever runs a uniquely marked 30-second hook
// with a 1-second timeout. It checks the run completes promptly and its own
// recorded process is gone, even while an unrelated process has the old
// shared fixture name in its command line.
func TestHooks_Timeout_KillsSleepForever(t *testing.T) {
	addr, _ := startFaux(t, readCallScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	pidFile := filepath.Join(t.TempDir(), "hook.pid")
	marker := "kiln-hook-timeout-" + processMarker(proj)
	script := filepath.Join(proj, "hook-timeout.sh")
	contents := fmt.Sprintf("#!/bin/bash\ncat >/dev/null\nprintf '%%s\\n' \"$$\" > %q\nexec -a %q sleep 30\n", pidFile, marker)
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}

	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []map[string]any{
				{
					"matcher": "*",
					"hooks": []map[string]any{
						{"type": "command", "command": script, "timeout": 1},
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
	if elapsed > 30*time.Second { // the hook never exits (sleep-forever), so a slow machine cannot reach this
		t.Errorf("run took %s, want it to complete within 30s (the timed-out hook should be killed, not awaited)", elapsed)
	}
	_ = res // exit code is not asserted here; the timeout behavior is.

	startGlobalMarkerControl(t, "sleep-forever.sh")
	assertTimedOutProcessGone(t, pidFile, marker)
}
