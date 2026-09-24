//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHooks_Stop_FiresOnRunEnd wires record-payload.sh on Stop and checks
// the payload arrives once the run ends, with stop_hook_active false and
// the parent's transcript path.
func TestHooks_Stop_FiresOnRunEnd(t *testing.T) {
	addr, _ := startFaux(t, unreadScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	stopFile := filepath.Join(t.TempDir(), "stop.json")
	script := hookScript(t, "record-payload.sh")
	writeHookSettings(t, proj, "Stop", "", fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", stopFile, script))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "hello", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	raw, err := os.ReadFile(stopFile)
	if err != nil {
		t.Fatalf("Stop never wrote %s: %v", stopFile, err)
	}
	for _, want := range []string{`"hook_event_name":"Stop"`, `"stop_hook_active":false`, `"transcript_path":"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("stop payload = %s, want %s", raw, want)
		}
	}
}

// TestHooks_SubagentStop_FiresPerSubagent drives task.yaml with
// record-payload.sh on SubagentStop and checks one payload names the
// subagent's own session, not the parent's.
func TestHooks_SubagentStop_FiresPerSubagent(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task.yaml")
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := startFaux(t, string(script))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	stopFile := filepath.Join(t.TempDir(), "subagent-stop.json")
	hook := hookScript(t, "record-payload.sh")
	writeHookSettings(t, proj, "SubagentStop", "", fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", stopFile, hook))

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "look something up for me", "--output-format", "text", "--permission-mode", "dontAsk")
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	raw, err := os.ReadFile(stopFile)
	if err != nil {
		t.Fatalf("SubagentStop never wrote %s: %v", stopFile, err)
	}
	if !strings.Contains(string(raw), `"hook_event_name":"SubagentStop"`) {
		t.Fatalf("payload = %s, want SubagentStop", raw)
	}
	files := sessionFiles(t, sessDir, proj)
	if len(files) != 2 {
		t.Fatalf("session files = %v, want parent + subagent", files)
	}
	// The payload's transcript_path must be one of the two, and not the
	// parent's: the parent is the one that also contains the task call.
	named := 0
	for _, f := range files {
		if strings.Contains(string(raw), f) {
			named++
			body, _ := os.ReadFile(f)
			if strings.Contains(string(body), `"name":"task"`) {
				t.Errorf("SubagentStop named the parent transcript %s", f)
			}
		}
	}
	if named != 1 {
		t.Errorf("payload names %d of the session files, want exactly 1:\n%s", named, raw)
	}
}
