package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tools"
)

func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestFactLaunchDirSplitsRootAndSubdirectory(t *testing.T) {
	root := gitFixture(t)
	sub := filepath.Join(root, "internal", "foo")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if r, w := factLaunchDir(root); r != root || w != "" {
		t.Fatalf("root launch: %q %q", r, w)
	}
	if r, w := factLaunchDir(sub); r != root || w != "internal/foo" {
		t.Fatalf("subdirectory launch: %q %q", r, w)
	}
}

func TestOutcomeObserverPromotesSubdirectoryLaunch(t *testing.T) {
	ctx := context.Background()
	root := gitFixture(t)
	sub := filepath.Join(root, "internal", "foo")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("package foo\n")
	if err := os.WriteFile(filepath.Join(sub, "foo.go"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	launchRoot, workdir := factLaunchDir(sub)
	observer := &outcomeObserver{store: store, project: "project", root: launchRoot, workdir: workdir, config: learning.ObservationConfig{Enabled: true, FactMemoryEnabled: true, FactReadAllowed: func(string) bool { return true }, MaxOutcomes: 10, MinSamples: 5}, sessionID: "session", binaryHash: learning.HashIdentity("binary")}
	proof := tools.VerificationEvidence{CallID: "call", Command: "go test ./...", Files: []tools.VerificationFile{{Path: "foo.go", SHA256: hex.EncodeToString(sum[:]), Mode: 0600}}}
	details, err := json.Marshal(map[string]any{"taskVerification": proof, "taskVerificationAttempt": tools.VerificationAttempt{CallID: "call", Command: proof.Command}})
	if err != nil {
		t.Fatal(err)
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "one"})
	observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "one", ToolCallID: "call", ToolName: "bash", ToolResult: &msg.ToolResultMessage{ToolCallID: "call", Details: details}})
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "one", Status: harness.StatusCompleted})
	candidates, err := learning.ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "stored" || candidates[0].Workdir != "internal/foo" {
		t.Fatalf("subdirectory launch not promoted: %+v %v", candidates, err)
	}
	records, err := store.Search(ctx, "project", "Observed successful verification command", 5, root)
	if err != nil || len(records) != 1 || !strings.Contains(records[0].Text, "(in internal/foo): go test ./...") || records[0].Sources[0].Path != filepath.Join(root, "internal", "foo", "foo.go") {
		t.Fatalf("stored fact: %+v %v", records, err)
	}
	if err := os.WriteFile(filepath.Join(sub, "foo.go"), []byte("package foo // edited\n"), 0600); err != nil {
		t.Fatal(err)
	}
	records, err = store.Search(ctx, "project", "Observed successful verification command", 5, root)
	if err != nil || len(records) != 0 {
		t.Fatalf("edited source still retrieved: %+v %v", records, err)
	}
}
