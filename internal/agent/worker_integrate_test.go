package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

func integrationStorage(t *testing.T, cwd string) session.Storage {
	t.Helper()
	repo, err := jsonl.NewRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, _, err := repo.Create(jsonl.CreateOptions{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func integrationTask(a *WorkerArtifact, id string) (msg.ToolCall, msg.ToolResultMessage) {
	details, _ := json.Marshal(struct {
		Dispatched bool            `json:"dispatched"`
		Artifact   *WorkerArtifact `json:"artifact,omitempty"`
	}{Dispatched: true, Artifact: a})
	return msg.ToolCall{Name: "task", ID: id}, msg.ToolResultMessage{ToolName: "task", ToolCallID: id, Details: details}
}

func TestWorkerBatchCompletionAppliesDisjointOnlyAfterFullResultSet(t *testing.T) {
	repo := testRepo(t)
	a := batchArtifact(t, repo, "one.txt", "one\n")
	b := batchArtifact(t, repo, "two.txt", "two\n")
	c1, r1 := integrationTask(&a, "one")
	c2, r2 := integrationTask(&b, "two")
	store := integrationStorage(t, repo)
	d := &Dispatcher{IsolatedWorkers: true, AutoIntegrate: true, Env: execenv.New(repo)}
	complete, err := WorkerBatchCompletion(d)(context.Background(), store, "op", "assistant", []msg.ToolCall{c1, c2}, []msg.ToolResultMessage{r1, r2}, false)
	if err != nil || complete.Status != "applied" || !strings.Contains(complete.Text, "2 patches") {
		t.Fatalf("completion=%+v err=%v", complete, err)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	if raw, _, ok := store.GetValue(harness.WorkerIntegrationNamespace, "assistant"); !ok || !strings.Contains(string(raw), `"intent"`) {
		t.Fatalf("missing durable pre-apply intent: %s", raw)
	}
}

func TestWorkerBatchCompletionBlocksMixedFailureAndMovedBaseline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
		other  msg.ToolResultMessage
	}{
		{"later tool failed", nil, msg.ToolResultMessage{ToolName: "write", ToolCallID: "later", IsError: true}},
		{"baseline moved", func(t *testing.T, repo string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("moved\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, msg.ToolResultMessage{ToolName: "read", ToolCallID: "later"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			a := batchArtifact(t, repo, "one.txt", "one\n")
			if tc.mutate != nil {
				tc.mutate(t, repo)
			}
			call, result := integrationTask(&a, "worker")
			store := integrationStorage(t, repo)
			d := &Dispatcher{IsolatedWorkers: true, AutoIntegrate: true, Env: execenv.New(repo)}
			outcome, err := WorkerBatchCompletion(d)(context.Background(), store, "op", "assistant", []msg.ToolCall{call, {Name: tc.other.ToolName, ID: "later"}}, []msg.ToolResultMessage{result, tc.other}, false)
			if err != nil || outcome.Status == "applied" {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
			if _, err := os.Stat(filepath.Join(repo, "one.txt")); !os.IsNotExist(err) {
				t.Fatal("worker patch applied despite mixed failure or baseline change")
			}
		})
	}
}

func TestWorkerBatchCompletionRestartWindowsNeverReplayPatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		intent bool
		apply  bool
		want   string
	}{
		{"before intent", false, false, "blocked"},
		{"after intent before apply", true, false, "uncertain"},
		{"after apply before report", true, true, "uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			a := batchArtifact(t, repo, "one.txt", "one\n")
			store := integrationStorage(t, repo)
			if tc.intent {
				raw, _ := json.Marshal(harness.ToolBatchCompletion{Status: "intent", Artifacts: []string{a.ManifestPath}})
				if _, err := store.Commit([]session.Write{session.SetValueRaw(harness.WorkerIntegrationNamespace, "assistant", raw)}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.apply {
				if err := ApplyWorkerArtifact(context.Background(), repo, a); err != nil {
					t.Fatal(err)
				}
			}
			durable := store.(*jsonl.Storage)
			path := durable.Path()
			if err := durable.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := jsonl.Open(path, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			store = reopened
			call, result := integrationTask(&a, "worker")
			d := &Dispatcher{IsolatedWorkers: true, AutoIntegrate: true, Env: execenv.New(repo)}
			outcome, err := WorkerBatchCompletion(d)(context.Background(), store, "op", "assistant", []msg.ToolCall{call}, []msg.ToolResultMessage{result}, true)
			if err != nil || outcome.Status != tc.want || !strings.Contains(outcome.Text, a.ManifestPath) {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
			_, statErr := os.Stat(filepath.Join(repo, "one.txt"))
			if tc.apply && statErr != nil {
				t.Fatal(statErr)
			}
			if !tc.apply && !os.IsNotExist(statErr) {
				t.Fatal("recovery applied patch")
			}
		})
	}
}
