package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tools"
)

func TestLearningProposalCommandsInspectAndDismiss(t *testing.T) {
	ctx := context.Background()
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(cwd)
	store, err := memory.Open(filepath.Join(home, ".kiln", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := learning.ObservationConfig{Enabled: true, MaxOutcomes: 5, MinSamples: 3}
	for i := 0; i < 3; i++ {
		o := learning.Outcome{ID: learning.HashIdentity(fmt.Sprint(i)), ObservedAt: time.Now().Unix(), Status: "failed", ModelHash: learning.HashIdentity(""), BinaryHash: learning.HashIdentity("binary"), UsageScope: "main-lane model responses; worker and side usage excluded"}
		if err := learning.RecordOutcomeAndPropose(ctx, store, memoryScope(cwd), o, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := Learning(ctx, Args{Positional: []string{"proposals"}}, &out, &stderr); code != 0 {
		t.Fatalf("proposals exit %d: %s", code, stderr.String())
	}
	var proposals []learning.Proposal
	if err := json.Unmarshal(out.Bytes(), &proposals); err != nil || len(proposals) != 1 {
		t.Fatalf("proposals output: %s %v", out.String(), err)
	}
	out.Reset()
	stderr.Reset()
	if code := Learning(ctx, Args{Positional: []string{"dismiss-proposal", proposals[0].ID}}, &out, &stderr); code != 0 {
		t.Fatalf("dismiss exit %d: %s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := Learning(ctx, Args{Positional: []string{"proposals"}}, &out, &stderr); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("dismissed proposal still visible: exit %d output %s stderr %s", code, out.String(), stderr.String())
	}
}

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

func TestRun_LearningObserverRecordsEffectiveDiscoveryBudget(t *testing.T) {
	startFaux(t, `model: faux-1
steps:
  - text: "done"
    usage: {input: 40000, output: 10}
`)
	project := scratchProject(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(settingsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{"learningAutomation":{"enabled":true,"minSamples":3},"skillDiscovery":{"budgetTokens":512}}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	args.Print, args.PrintPrompt = true, "hello"
	var stdout, stderr strings.Builder
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("Run exit %d: %s", code, stderr.String())
	}
	store, err := memory.Open(filepath.Join(home, ".kiln", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	outcomes, err := learning.ListOutcomes(context.Background(), store, memoryScope(project))
	if err != nil || len(outcomes) != 1 || outcomes[0].SkillBudgetTokens != 512 {
		t.Fatalf("live observer source budget: %+v %v", outcomes, err)
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
	observer := &outcomeObserver{store: store, project: "project", config: learning.ObservationConfig{Enabled: true, MaxOutcomes: 10, MinSamples: 5, SourceSkillBudgetTokens: 512}, sessionID: "session secret", binaryHash: learning.HashIdentity("binary")}
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
	if o.Status != "failed" || o.ToolSuccess != 1 || o.ToolFailure != 1 || o.OtherFailure != 1 || o.VerifiedCommands != 1 || o.InputTokens != 130 || o.CacheReadTokens != 60 || o.CacheWriteTokens != 30 || o.SkillBudgetTokens != 512 || o.ModelsUsed != 2 || o.ModelHash == learning.HashIdentity("provider/a") || o.ModelHash == learning.HashIdentity("provider/b") {
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
