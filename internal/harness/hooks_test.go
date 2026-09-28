package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
)

// TestBeforeToolBlocksWithoutExecuting asserts an OnBeforeTool handler
// that returns a Block produces an isError toolResult carrying its reason,
// and that the tool never actually executes: the bash command would create
// a marker file if it ran, and it must not exist afterward.
func TestBeforeToolBlocksWithoutExecuting(t *testing.T) {
	rig := newTestRig(t, "", []string{"bash"}) // script set below, after we know the marker path
	marker := filepath.Join(t.TempDir(), "marker")
	script := `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "touch ` + marker + `"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	if err := rig.Faux.LoadScriptYAML(script); err != nil {
		t.Fatal(err)
	}
	lane := rig.mustLane("main")

	rig.H.Hooks().OnBeforeTool(func(ctx context.Context, call msg.ToolCall) (BeforeToolResult, error) {
		return BeforeToolResult{Block: &ToolBlock{Reason: "blocked for test"}}, nil
	})

	result, err := lane.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, err = %v", result.Status, result.Error)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker file exists: blocked tool call executed anyway")
	}

	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		tr, ok := e.Message.(msg.ToolResultMessage)
		if !ok {
			continue
		}
		found = true
		if !tr.IsError {
			t.Fatalf("toolResult.IsError = false, want true")
		}
		if got := msg.TextOf(tr.Content); got != "blocked for test" {
			t.Fatalf("toolResult content = %q, want %q", got, "blocked for test")
		}
	}
	if !found {
		t.Fatal("no toolResult entry found on branch")
	}
}

// TestAbortMidStream aborts a lane while its provider request is in
// flight (a scripted delay gives us a window) and asserts the run ends
// with status "aborted" and a clean pi.result (no dangling pi.op.* keys).
func TestAbortMidStream(t *testing.T) {
	script := `
model: faux-1
steps:
  - delay: 300ms
  - text: "too late"
`
	rig := newTestRig(t, script, []string{"bash"})
	lane := rig.mustLane("main")

	resultCh := make(chan RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := lane.Prompt(context.Background(), "go", nil)
		resultCh <- r
		errCh <- err
	}()

	// Give Prompt time to reach the in-flight request, then abort.
	time.Sleep(50 * time.Millisecond)
	if err := lane.Abort(); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	select {
	case r := <-resultCh:
		if r.Status != StatusAborted {
			t.Fatalf("status = %q, want aborted", r.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt did not return after Abort")
	}
	<-errCh

	// A clean pi.result: no pi.op.meta/pi.op.state left behind for this
	// operation, and pi.lane.state is idle.
	st, err := lane.laneState()
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentOperationID != nil {
		t.Fatalf("lane state still has a current operation: %v", *st.CurrentOperationID)
	}
	if st.LastOperationID == nil {
		t.Fatal("lane state has no lastOperationId after abort")
	}
	if _, _, ok := rig.Storage.GetValue("pi.op.meta", *st.LastOperationID); ok {
		t.Fatal("pi.op.meta still present after abort")
	}
	if _, _, ok := rig.Storage.GetValue("pi.op.state", *st.LastOperationID); ok {
		t.Fatal("pi.op.state still present after abort")
	}
}

// TestSetActiveToolsAffectsRequest asserts SetActiveTools changes the tool
// set the harness actually sends to the provider, verified against the
// faux server's own request log.
func TestSetActiveToolsAffectsRequest(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "ok"
`, []string{"bash", "read", "edit", "write"})
	lane := rig.mustLane("main")

	if err := lane.SetActiveTools([]string{"read"}); err != nil {
		t.Fatalf("SetActiveTools: %v", err)
	}

	if _, err := lane.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	reqs := rig.Faux.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	last := reqs[len(reqs)-1]
	if len(last.Tools) != 1 || last.Tools[0].Name != "read" {
		t.Fatalf("recorded tools = %+v, want exactly [read]", last.Tools)
	}
}

// TestUnknownToolNameReportsUnknownTool asserts that calling a tool name
// that was never registered anywhere produces the "unknown tool %q"
// message, not the "not in the active tool set" restriction message -
// the two read very differently to a user (a typo/hallucinated name vs.
// an access restriction), and only the registry lookup (not the active-set
// check) can tell them apart.
func TestUnknownToolNameReportsUnknownTool(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - tool_call: {name: frobnicate, args: {}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`, []string{"bash", "read", "edit", "write"})
	lane := rig.mustLane("main")

	result, err := lane.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, err = %v", result.Status, result.Error)
	}

	tr := findToolResult(t, lane, "tc1")
	if !tr.IsError {
		t.Fatalf("toolResult.IsError = false, want true")
	}
	got := msg.TextOf(tr.Content)
	want := `There is no tool named frobnicate, so the call did not run.`
	if got != want {
		t.Fatalf("toolResult content = %q, want %q", got, want)
	}
}

// TestInactiveRegisteredToolReportsRestriction asserts that a tool which
// IS registered but not in the lane's active set still gets the
// restriction message, not "unknown tool" - the registry-existence check
// added for the unknown-tool case above must not swallow this branch.
func TestInactiveRegisteredToolReportsRestriction(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "true"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`, []string{"read"}) // bash is registered (it's a built-in) but not active

	lane := rig.mustLane("main")

	result, err := lane.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, err = %v", result.Status, result.Error)
	}

	tr := findToolResult(t, lane, "tc1")
	if !tr.IsError {
		t.Fatalf("toolResult.IsError = false, want true")
	}
	got := msg.TextOf(tr.Content)
	want := `tool "bash" is not available to this agent: it is not in the active tool set`
	if got != want {
		t.Fatalf("toolResult content = %q, want %q", got, want)
	}
}

// TestRefusedToolSkipsBeforeToolHooks: a call to an unknown or inactive
// tool is refused before the before-tool hooks run. Those hooks include
// the permission gate, and asking the user to approve a call that is then
// refused anyway wastes their answer.
func TestRefusedToolSkipsBeforeToolHooks(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "true"}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call: {name: frobnicate, args: {}, id: tc2}
  - on_tool_result: tc2
    then:
      - text: "done"
`, []string{"read"})
	var consulted []string
	rig.H.Hooks().OnBeforeTool(func(ctx context.Context, call msg.ToolCall) (BeforeToolResult, error) {
		consulted = append(consulted, call.Name)
		return BeforeToolResult{}, nil
	})
	lane := rig.mustLane("main")
	if _, err := lane.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(consulted) != 0 {
		t.Errorf("before-tool hooks consulted for refused calls: %v", consulted)
	}
	for _, id := range []string{"tc1", "tc2"} {
		if tr := findToolResult(t, lane, id); !tr.IsError {
			t.Errorf("%s: IsError = false, want the refusal", id)
		}
	}
}

// findToolResult locates the toolResult entry for toolCallID on lane's
// branch, failing the test if it is not present.
func findToolResult(t *testing.T, lane *Lane, toolCallID string) msg.ToolResultMessage {
	t.Helper()
	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		tr, ok := e.Message.(msg.ToolResultMessage)
		if !ok || !strings.HasSuffix(tr.ToolCallID, toolCallID) {
			continue
		}
		return tr
	}
	t.Fatalf("no toolResult entry found for tool call %q", toolCallID)
	return msg.ToolResultMessage{}
}
