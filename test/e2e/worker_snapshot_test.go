//go:build e2e

package e2e

import (
	"bytes"
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

func TestWorkerIsolationCLIPreservesDirtyParent(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	git := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Dir = proj
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(proj, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(proj, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	write("draft.txt", "committed\n")
	write(".gitignore", "ignored.txt\n")
	git("init", "-q")
	git("add", "-A")
	git("-c", "user.name=Kiln Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	write("draft.txt", "staged\n")
	git("add", "draft.txt")
	write("draft.txt", "unstaged parent\n")
	write("untracked.txt", "parent untracked\n")
	write("ignored.txt", "private build output\n")
	indexBefore := read(".git/index")
	statusBefore := git("status", "--porcelain=v1", "--untracked-files=all")
	config := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(config, []byte(`{"orchestration":{"isolatedWorkers":true,"snapshotDirty":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: task, args: {subagent_type: "general-purpose", description: "update draft", prompt: "Read draft.txt and untracked.txt, then update draft.txt"}, id: task1}
  - tool_call: {name: read, args: {path: "draft.txt"}, id: read1}
  - on_tool_result: read1
    then:
      - tool_call: {name: read, args: {path: "untracked.txt"}, id: read2}
  - on_tool_result: read2
    then:
      - tool_call: {name: write, args: {path: "draft.txt", content: "worker revision\n"}, id: write1}
  - on_tool_result: write1
    then:
      - text: "Worker finished."
  - on_tool_result: task1
    then:
      - text: "Patch ready."
`)
	env := baseEnv(home, sessDir, addr)
	res := runHarness(t, proj, env, "-p", "Delegate the draft update", "--permission-mode", "bypassPermissions", "--settings", config)
	if res.Code != 0 {
		t.Fatalf("dispatch: %s %s", res.Stdout, res.Stderr)
	}
	artifact := workerSnapshotArtifact(t, sessionFile(t, sessDir, proj))
	if artifact.Baseline == nil || artifact.Baseline.Tree == "" || artifact.Baseline.State == "" {
		t.Fatal("worker result lost the captured baseline metadata")
	}
	if !bytes.Equal(indexBefore, read(".git/index")) || !bytes.Equal(statusBefore, git("status", "--porcelain=v1", "--untracked-files=all")) || string(read("draft.txt")) != "unstaged parent\n" {
		t.Fatal("snapshot/worker changed parent index or working tree")
	}
	var sawDraft, sawUntracked bool
	for _, request := range srv.Requests() {
		sawDraft = sawDraft || strings.Contains(string(request.Messages), "unstaged parent")
		sawUntracked = sawUntracked || strings.Contains(string(request.Messages), "parent untracked")
	}
	if !sawDraft || !sawUntracked {
		t.Fatal("worker did not see current parent files")
	}
	patch, err := os.ReadFile(artifact.PatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.ChangedFiles) != 1 || artifact.ChangedFiles[0] != "draft.txt" || !strings.Contains(string(patch), "-unstaged parent") || !strings.Contains(string(patch), "+worker revision") {
		t.Fatalf("patch did not isolate worker delta: %+v\n%s", artifact, patch)
	}
	res = runHarness(t, proj, env, "worker", "inspect", artifact.ManifestPath)
	if res.Code != 0 || !strings.Contains(res.Stdout, `"baseline"`) {
		t.Fatalf("inspect: %s %s", res.Stdout, res.Stderr)
	}
	write("untracked.txt", "changed since snapshot\n")
	res = runHarness(t, proj, env, "worker", "apply", artifact.ManifestPath)
	if res.Code == 0 || string(read("draft.txt")) != "unstaged parent\n" || !bytes.Equal(indexBefore, read(".git/index")) {
		t.Fatal("stale snapshot was accepted or mutated parent")
	}
	write("untracked.txt", "parent untracked\n")
	res = runHarness(t, proj, env, "worker", "apply", artifact.ManifestPath)
	if res.Code != 0 {
		t.Fatalf("apply: %s %s", res.Stdout, res.Stderr)
	}
	if string(read("draft.txt")) != "worker revision\n" || string(read("untracked.txt")) != "parent untracked\n" || string(read("ignored.txt")) != "private build output\n" || !bytes.Equal(indexBefore, read(".git/index")) {
		t.Fatal("apply lost parent state or changed the index")
	}
}

func workerSnapshotArtifact(t *testing.T, sessionPath string) agent.WorkerArtifact {
	t.Helper()
	store, err := jsonl.Open(sessionPath, nil)
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
				t.Errorf("task details: %s (%v)", msg.TextOf(result.Content), err)
			}
			artifact = details.Artifact
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if artifact == nil || artifact.ManifestPath == "" {
		t.Fatal("missing worker snapshot artifact")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(filepath.Dir(artifact.ManifestPath)); err != nil {
			t.Error(err)
		}
	})
	return *artifact
}
