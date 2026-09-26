//go:build e2e

package e2e

// Phase 6: the `task` tool, driven through the real cmd/harness binary.
// Mirrors internal/cli/phase6_test.go's TestRun_Task_DispatchesToSubagent
// (same scenario, in-process) but here as a golden end-to-end run of the
// actual binary, per the phase brief.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/session/jsonl"
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
	files := sessionFiles(t, sessDir, proj)
	if len(files) != 2 {
		t.Fatalf("session files = %v, want 2 (parent + subagent)", files)
	}
	// Exactly one file (the parent's) has an empty ParentSessionID; the
	// other (the subagent's) has it set — internal/agent/dispatch.go now
	// passes the dispatching session's own SessionID through
	// agent.Options.ParentSessionID, so a subagent's session file is
	// distinguishable from a top-level one by its own header alone
	// (internal/cli/tui.go's buildRecentSessionRows filters the banner's
	// recent-sessions list on exactly this field).
	var topLevel, withParent int
	for _, f := range files {
		st, err := jsonl.Open(f, nil)
		if err != nil {
			t.Fatalf("jsonl.Open(%s): %v", f, err)
		}
		hdr := st.Header()
		st.Close()
		if hdr.ParentSessionID == "" {
			topLevel++
		} else {
			withParent++
		}
	}
	if topLevel != 1 || withParent != 1 {
		t.Errorf("session files with empty/set ParentSessionID = %d/%d, want 1/1 (parent/subagent) among %v", topLevel, withParent, files)
	}
}

// writeModelRolesSettings writes a scratch .claude/settings.json with the
// given modelRoles map, matching writeHookSettings's own
// mkdir+MarshalIndent+WriteFile pattern (hooks_test.go).
func writeModelRolesSettings(t *testing.T, proj string, roles map[string]string) {
	t.Helper()
	settings := map[string]any{"modelRoles": roles}
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

// TestPrint_StreamJSON_TaskConcurrent drives testdata/faux/task_concurrent.yaml
// through the real binary: the parent dispatches two `task` calls in one
// assistant message (internal/harness now runs concurrent task tool calls
// in parallel), one inheriting the parent's own model and one routed to
// the "fast" role (settings.json's modelRoles -> faux-2), and the parent's
// final reply, gated on both results, is "Both done." See
// task_concurrent.yaml's own comments for why this ordering is
// deterministic rather than merely likely, and
// internal/tools/task_e2e_test.go for the in-process counterpart this
// mirrors as a golden run of the actual binary.
//
// Note on what stream-json can prove: print.ts's stream-json shape
// (internal/cli/print.go) carries no tool-call id at all — {"type":
// "tool_start","name":...,"arg":...} only, and "arg" is populated solely
// from a "command" or "path" argument, neither of which the task tool
// has. Two tool_start events with the same name is therefore the
// strongest signal available from this output format that two distinct
// dispatches happened; the requests recorded by the faux server (below)
// are what actually distinguish them by model.
func TestPrint_StreamJSON_TaskConcurrent(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task_concurrent.yaml")
	if err != nil {
		t.Fatal(err)
	}
	addr, srv := startFaux(t, string(script))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeModelRolesSettings(t, proj, map[string]string{"fast": fauxprovider.ProviderID + "/" + fauxprovider.ModelID2})

	res := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "dispatch two tasks",
		"--output-format", "stream-json",
		"--permission-mode", "dontAsk",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	assertGolden(t, goldenPath("print-task-concurrent.ndjson"), res.Stdout)

	if n := strings.Count(res.Stdout, `"type":"tool_start","name":"task"`); n != 2 {
		t.Errorf("stdout has %d task tool_start events, want 2:\n%s", n, res.Stdout)
	}
	if strings.Contains(res.Stdout, `"isError":true`) {
		t.Errorf("a task tool_end reported an error:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, `"text":"Both done."`) {
		t.Errorf("final assistant text was not \"Both done.\":\n%s", res.Stdout)
	}

	// Three session files: the parent's, the inherited subagent's (faux-1)
	// and the fast-routed subagent's (faux-2).
	if files := sessionFiles(t, sessDir, proj); len(files) != 3 {
		t.Errorf("session files = %v, want 3 (parent + 2 subagents)", files)
	}

	// The faux server's own request log is what actually proves the "fast"
	// role reached faux-2 — the stream-json output above cannot, since it
	// carries no model information at all.
	var sawFaux2 bool
	for _, r := range srv.Requests() {
		if r.Model == fauxprovider.ModelID2 {
			sawFaux2 = true
			break
		}
	}
	if !sawFaux2 {
		t.Errorf("no request reached faux-2 (the fast role)")
	}
}
