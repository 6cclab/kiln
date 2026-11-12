package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tools"
)

func TestAdaptDispatchPreservesWorkerBaseline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		baseline *WorkerBaseline
	}{
		{"clean artifact", nil},
		{"dirty snapshot", &WorkerBaseline{Tree: "tree-oid", State: "state-hash"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapted := adaptDispatch(func(_ context.Context, req DispatchRequest) (DispatchResult, error) {
				if req.Agent != "worker" {
					t.Fatalf("request agent = %q", req.Agent)
				}
				return DispatchResult{Artifact: &WorkerArtifact{BaseCommit: "head", PatchPath: "changes.patch", SHA256: "patch-hash", Bytes: 5, ChangedFiles: []string{"a.txt"}, Baseline: tc.baseline}}, nil
			})
			result, err := adapted(context.Background(), tools.TaskRequest{Agent: "worker"})
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(result.Artifact)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip WorkerArtifact
			if err := json.Unmarshal(data, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if !sameWorkerBaseline(roundtrip.Baseline, tc.baseline) {
				t.Fatalf("baseline lost in task result JSON: %s", data)
			}
		})
	}
}

func TestNestedIsolatedWorkerInheritsDirtySnapshot(t *testing.T) {
	script := `
model: faux-1
steps:
  - tool_call: {name: task, args: {subagent_type: general-purpose, prompt: "read parent"}, id: outer_task}
  - tool_call: {name: read, args: {path: "a.txt"}, id: nested_read}
  - on_tool_result: nested_read
    then:
      - text: "nested complete"
  - on_tool_result: outer_task
    then:
      - text: "outer complete"
`
	d, _, _ := newParentAndDispatcher(t, script, nil)
	repo := testRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("dirty parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.Env = execenv.New(repo)
	d.IsolatedWorkers = true
	d.SnapshotDirty = true
	var starts int
	d.OnEvent = func(ev SubagentEvent) {
		if ev.Kind == SubagentEventStart {
			starts++
		}
	}
	_, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "run nested"})
	if err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatalf("subagent starts = %d, want outer and nested", starts)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "dirty parent\n" {
		t.Fatalf("parent changed: %q", got)
	}
}
