//go:build e2e

package e2e

// Phase 6: the `task` tool, driven through the real cmd/harness binary.
// Mirrors internal/cli/phase6_test.go's TestRun_Task_DispatchesToSubagent
// (same scenario, in-process) but here as a golden end-to-end run of the
// actual binary, per the phase brief.

import (
	"os"
	"strings"
	"testing"
)

// TestPrint_StreamJSON_Task drives testdata/faux/task.yaml through the real
// binary: the parent dispatches `task` to the built-in general-purpose
// agent, the subagent (served off the same sequential faux script — see
// task.yaml and internal/tools/task_e2e_test.go's own comment on this) hands
// back "subagent report", and the parent's final reply is "Done."
func TestPrint_StreamJSON_Task(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task.yaml")
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := startFaux(t, string(script))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "look something up for me",
		"--output-format", "stream-json",
		"--permission-mode", "dontAsk",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	assertGolden(t, goldenPath("print-task.ndjson"), res.Stdout)

	if !strings.Contains(res.Stdout, `"name":"task"`) {
		t.Errorf("stdout has no task tool event:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, `"isError":true`) {
		t.Errorf("task tool_end reported an error:\n%s", res.Stdout)
	}

	// Two session files: the parent's and the subagent's, both under the
	// same project bucket (the dispatcher shares SessionsRoot with the
	// parent — internal/cli/chat.go's dispatcher construction).
	if files := sessionFiles(t, sessDir, proj); len(files) != 2 {
		t.Errorf("session files = %v, want 2 (parent + subagent)", files)
	}
}
