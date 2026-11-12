package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
)

// Each run is recorded with the shape of what it did, and a check that also
// writes files (a mutation check) counts as a check but not as a tool
// failure: its failure is the point.
func TestOutcomeObserverRecordsRunShapesAndMutationChecks(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	observer := &outcomeObserver{store: store, project: "project", config: learning.ObservationConfig{Enabled: true, MaxOutcomes: 10, MinSamples: 5}, sessionID: "session", binaryHash: learning.HashIdentity("binary")}
	type call struct {
		tool, command string
		failed        bool
	}
	runs := map[string][]call{
		"question":   {{tool: "read"}, {tool: "bash", command: "git status"}},
		"changed":    {{tool: "edit"}, {tool: "bash", command: "go test ./..."}},
		"unverified": {{tool: "write"}, {tool: "bash", command: "ls"}},
		"shell-edit": {{tool: "bash", command: "sed -i 's/a/b/' f.go"}},
		"mutation":   {{tool: "edit"}, {tool: "bash", command: "sed -i 's/a/b/' f.go && go test ./x", failed: true}},
	}
	for op, calls := range runs {
		observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: op})
		for i, c := range calls {
			id := op + string(rune('a'+i))
			args := map[string]any{}
			if c.command != "" {
				args["command"] = c.command
			}
			observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: op, ToolCallID: id, ToolName: c.tool, ToolArgs: args, ToolResult: &msg.ToolResultMessage{ToolCallID: id, IsError: c.failed}})
		}
		observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: op, Status: harness.StatusCompleted})
	}
	outcomes, err := learning.ListOutcomes(ctx, store, "project")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]learning.Outcome{}
	for _, o := range outcomes {
		byID[o.ID] = o
	}
	for op, want := range map[string]string{
		"question":   learning.RunShapeQuestion,
		"changed":    learning.RunShapeCodeChange,
		"unverified": learning.RunShapeCodeChangeUnverified,
		"shell-edit": learning.RunShapeCodeChangeUnverified,
		"mutation":   learning.RunShapeCodeChange,
	} {
		o, ok := byID[learning.HashIdentity("session\x00"+op)]
		if !ok {
			t.Fatalf("run %s not recorded", op)
		}
		if o.Shape != want {
			t.Errorf("run %s shape = %q, want %q", op, o.Shape, want)
		}
	}
	m := byID[learning.HashIdentity("session\x00mutation")]
	if m.MutationChecks != 1 || m.ToolFailure != 0 || m.BashFailure != 0 || m.ToolSuccess != 1 {
		t.Errorf("mutation run counters = checks %d failure %d bash %d success %d, want 1 0 0 1", m.MutationChecks, m.ToolFailure, m.BashFailure, m.ToolSuccess)
	}
}
