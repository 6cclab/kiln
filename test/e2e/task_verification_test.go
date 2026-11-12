//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
)

func TestStructuredTaskVerificationIsWiredThroughCLI(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: task_state_write, args: {objective: "Repair addition", constraints: ["Preserve multiplication"], decisions: ["Run the existing tests"]}, id: state1}
  - on_tool_result: state1
    then:
      - tool_call: {name: bash, args: {command: "test -f src/math.js", verificationFiles: ["src/math.js"]}, id: verify1}
  - on_tool_result: verify1
    then:
      - text: "Recorded command evidence."
`)
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "Track the task and check the file", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d: %s", res.Code, res.Stderr)
	}
	requests := srv.Requests()
	if len(requests) < 3 {
		t.Fatalf("only %d requests", len(requests))
	}
	declared := false
	for _, def := range requests[0].Tools {
		if def.Name == "task_state_write" {
			declared = true
		}
	}
	if !declared {
		t.Fatal("task_state_write not offered")
	}
	raw, err := os.ReadFile(sessionFile(t, sessDir, proj))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Task state saved.") || !strings.Contains(string(raw), `"taskVerification"`) {
		t.Fatalf("missing durable task or command evidence: %s", raw)
	}
	last := string(requests[len(requests)-1].Messages)
	if !strings.Contains(last, "selected file snapshots (current, call toolu_verify1, files src/math.js)") {
		t.Fatalf("missing scoped fresh evidence: %s", last)
	}
}
