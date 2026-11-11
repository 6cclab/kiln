//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

func TestWorkerIsolationCLIProducesReviewablePatch(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Kiln Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", proj, "-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, output)
		}
	}
	config := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(config, []byte(`{"orchestration":{"isolatedWorkers":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: task, args: {subagent_type: "general-purpose", description: "write isolated note", prompt: "Write worker-note.txt containing reviewed"}, id: task1}
  - tool_call: {name: write, args: {path: "worker-note.txt", content: "reviewed\n"}, id: write1}
  - on_tool_result: write1
    then:
      - text: "Worker finished."
  - on_tool_result: task1
    then:
      - text: "Patch ready."
`)
	env := baseEnv(home, sessDir, addr)
	res := runHarness(t, proj, env, "-p", "Delegate the note", "--permission-mode", "bypassPermissions", "--settings", config)
	if res.Code != 0 {
		t.Fatalf("dispatch: %s", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(proj, "worker-note.txt")); !os.IsNotExist(err) {
		t.Fatal("worker mutated parent checkout")
	}
	store, err := jsonl.Open(sessionFile(t, sessDir, proj), nil)
	if err != nil {
		t.Fatal(err)
	}
	var artifact *agent.WorkerArtifact
	for _, entry := range store.ScanEntries(session.EntryScan{}) {
		if result, ok := entry.Message.(msg.ToolResultMessage); ok && result.ToolName == "task" {
			var details struct {
				Artifact *agent.WorkerArtifact `json:"artifact"`
			}
			if err := json.Unmarshal(result.Details, &details); err != nil {
				t.Fatalf("task details missing: %s (%v)", msg.TextOf(result.Content), err)
			}
			artifact = details.Artifact
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if artifact == nil || artifact.ManifestPath == "" {
		t.Fatal("missing worker artifact")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Dir(artifact.ManifestPath)); err != nil {
			t.Error(err)
		}
	})
	res = runHarness(t, proj, env, "worker", "inspect", artifact.ManifestPath)
	if res.Code != 0 || !strings.Contains(res.Stdout, "worker-note.txt") {
		t.Fatalf("inspect: %s %s", res.Stdout, res.Stderr)
	}
	res = runHarness(t, proj, env, "worker", "apply", artifact.ManifestPath)
	if res.Code != 0 {
		t.Fatalf("apply: %s", res.Stderr)
	}
	contents, err := os.ReadFile(filepath.Join(proj, "worker-note.txt"))
	if err != nil || string(contents) != "reviewed\n" {
		t.Fatalf("applied contents=%q err=%v", contents, err)
	}
	res = runHarness(t, proj, env, "worker", "apply", artifact.ManifestPath)
	if res.Code == 0 {
		t.Fatal("second apply should refuse dirty parent")
	}
}
