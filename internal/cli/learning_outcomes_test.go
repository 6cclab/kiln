package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tools"
)

func TestDisabledLearningObserverDoesNotOpenDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cleanup, err := attachLearningObserver(context.Background(), t.TempDir(), nil, learning.ObservationConfig{})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Join(home, ".kiln", "memory.db")); !os.IsNotExist(err) {
		t.Fatalf("disabled observer opened database: %v", err)
	}
}

func TestOutcomeObserverRedactsRawEventsAndCountsVerifiedProof(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	observer := &outcomeObserver{store: store, project: "project", config: learning.ObservationConfig{Enabled: true, MaxOutcomes: 10, MinSamples: 5}, sessionID: "session secret", binaryHash: learning.HashIdentity("binary")}
	modelA := &session.ModelRef{Provider: "provider", ModelID: "a"}
	modelB := &session.ModelRef{Provider: "provider", ModelID: "b"}
	observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "operation secret"})
	observer.observe(ctx, harness.Event{Type: harness.EventUsage, Lane: agent.MainLane, OperationID: "operation secret", UsageRow: &msg.Usage{Input: 100, Output: 20, CacheRead: 50, CacheWrite: 30}, UsageModel: modelB})
	observer.observe(ctx, harness.Event{Type: harness.EventUsage, Lane: agent.MainLane, OperationID: "operation secret", UsageRow: &msg.Usage{Input: 30, CacheRead: 10}, UsageModel: modelA})
	proof := tools.VerificationEvidence{CallID: "call", Command: "secret command", Files: []tools.VerificationFile{{Path: "secret-path.txt", SHA256: strings.Repeat("a", 64), Mode: 0600}}}
	details, err := json.Marshal(map[string]any{"taskVerification": proof})
	if err != nil {
		t.Fatal(err)
	}
	observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "operation secret", ToolCallID: "call", ToolName: "bash", ToolResult: &msg.ToolResultMessage{ToolCallID: "call", Details: details}})
	observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "operation secret", ToolCallID: "bad", ToolName: "untrusted secret tool", ToolResult: &msg.ToolResultMessage{ToolCallID: "bad", IsError: true, Content: msg.Blocks{msg.Text("secret error text")}}})
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "operation secret", Status: harness.StatusFailed})
	outcomes, err := learning.ListOutcomes(ctx, store, "project")
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("outcomes: %+v %v", outcomes, err)
	}
	o := outcomes[0]
	if o.Status != "failed" || o.ToolSuccess != 1 || o.ToolFailure != 1 || o.OtherFailure != 1 || o.VerifiedCommands != 1 || o.InputTokens != 130 || o.CacheReadTokens != 60 || o.CacheWriteTokens != 30 || o.ModelsUsed != 2 || o.ModelHash == learning.HashIdentity("provider/a") || o.ModelHash == learning.HashIdentity("provider/b") {
		t.Fatalf("wrong aggregate: %+v", o)
	}
	raw, err := store.Export(ctx, learning.OutcomeScope("project"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"session secret", "operation secret", "secret command", "secret-path", "secret error text", "untrusted secret tool"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("raw event data persisted: %s", secret)
		}
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunResume, Lane: agent.MainLane, OperationID: "resumed"})
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "resumed", Status: harness.StatusCompleted})
	outcomes, err = learning.ListOutcomes(ctx, store, "project")
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("partial resumed run persisted: %+v %v", outcomes, err)
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "no-model"})
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "no-model", Status: harness.StatusFailed})
	outcomes, err = learning.ListOutcomes(ctx, store, "project")
	if err != nil || len(outcomes) != 2 || outcomes[0].ModelsUsed != 0 || outcomes[0].ModelHash != learning.HashIdentity("") {
		t.Fatalf("no-model run claimed model usage: %+v %v", outcomes, err)
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "too-many-models"})
	for i := 0; i <= 100; i++ {
		observer.observe(ctx, harness.Event{Type: harness.EventUsage, Lane: agent.MainLane, OperationID: "too-many-models", UsageRow: &msg.Usage{Input: 1}, UsageModel: &session.ModelRef{Provider: "provider", ModelID: fmt.Sprint(i)}})
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "too-many-models", Status: harness.StatusCompleted})
	outcomes, err = learning.ListOutcomes(ctx, store, "project")
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("model overflow produced false identity: %+v %v", outcomes, err)
	}
}
