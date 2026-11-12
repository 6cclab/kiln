package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tools"
)

func TestOutcomeObserverExtractsOnlyFreshCompletedCommand(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	data := []byte("package main\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
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
	observer := &outcomeObserver{store: store, project: "project", root: root, config: learning.ObservationConfig{Enabled: true, FactMemoryEnabled: true, FactReadAllowed: func(string) bool { return true }, MaxOutcomes: 10, MinSamples: 5}, sessionID: "session", binaryHash: learning.HashIdentity("binary")}
	proof := tools.VerificationEvidence{CallID: "call", Command: "go test ./...", Files: []tools.VerificationFile{{Path: "main.go", SHA256: hex.EncodeToString(sum[:]), Mode: 0600}}}
	details, err := json.Marshal(map[string]any{"taskVerification": proof, "taskVerificationAttempt": tools.VerificationAttempt{CallID: "call", Command: proof.Command}})
	if err != nil {
		t.Fatal(err)
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "one"})
	observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "one", ToolCallID: "call", ToolName: "bash", ToolResult: &msg.ToolResultMessage{ToolCallID: "call", Details: details}})
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "one", Status: harness.StatusCompleted})
	candidates, err := learning.ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "stored" {
		t.Fatalf("observer failed to extract fact: %+v %v", candidates, err)
	}
	// The same command failing later in a run supersedes its earlier success.
	observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "two"})
	observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "two", ToolCallID: "call", ToolName: "bash", ToolResult: &msg.ToolResultMessage{ToolCallID: "call", Details: details}})
	failedDetails, err := json.Marshal(map[string]any{"taskVerificationAttempt": tools.VerificationAttempt{CallID: "retry", Command: proof.Command}})
	if err != nil {
		t.Fatal(err)
	}
	observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "two", ToolCallID: "retry", ToolName: "bash", ToolResult: &msg.ToolResultMessage{ToolCallID: "retry", IsError: true, Details: failedDetails}})
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "two", Status: harness.StatusCompleted})
	candidates, err = learning.ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("failed retry produced fact: %+v %v", candidates, err)
	}
}

func TestOutcomeObserverBoundsProofCollectionAndInvalidatesFailedCommand(t *testing.T) {
	observer := &outcomeObserver{root: t.TempDir(), config: learning.ObservationConfig{Enabled: true, FactMemoryEnabled: true}, sessionID: "session", binaryHash: learning.HashIdentity("binary")}
	observer.observe(context.Background(), harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "long"})
	first := ""
	for i := 0; i < 100; i++ {
		command := fmt.Sprintf("go test -run=Test%03d ./...", i)
		if i == 0 {
			first = command
		}
		proof := tools.VerificationEvidence{CallID: fmt.Sprintf("call-%d", i), Command: command, Files: []tools.VerificationFile{{Path: strings.Repeat("a", 2000), SHA256: strings.Repeat("a", 64), Mode: 0600}}}
		details, err := json.Marshal(map[string]any{"taskVerification": proof, "taskVerificationAttempt": tools.VerificationAttempt{CallID: proof.CallID, Command: command}})
		if err != nil {
			t.Fatal(err)
		}
		observer.observe(context.Background(), harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "long", ToolName: "bash", ToolCallID: proof.CallID, ToolResult: &msg.ToolResultMessage{ToolCallID: proof.CallID, Details: details}})
	}
	if len(observer.current.proofs) > 16 {
		t.Fatalf("retained %d raw proofs", len(observer.current.proofs))
	}
	raw, err := json.Marshal(observer.current.proofs)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 64<<10 {
		t.Fatalf("retained %d proof bytes", len(raw))
	}
	if _, ok := observer.current.proofs[first]; !ok {
		t.Fatal("oldest retained proof missing before failure")
	}
	failed, err := json.Marshal(map[string]any{"taskVerificationAttempt": tools.VerificationAttempt{CallID: "failed", Command: first}})
	if err != nil {
		t.Fatal(err)
	}
	observer.observe(context.Background(), harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "long", ToolName: "bash", ToolCallID: "failed", ToolResult: &msg.ToolResultMessage{ToolCallID: "failed", IsError: true, Details: failed}})
	if _, ok := observer.current.proofs[first]; ok {
		t.Fatal("failed command kept earlier proof")
	}
}

func TestOutcomeObserverMemoryOptOutKeepsCountsWithoutRawProof(t *testing.T) {
	observer := &outcomeObserver{root: t.TempDir(), config: learning.ObservationConfig{Enabled: true, FactMemoryEnabled: false}, sessionID: "session", binaryHash: learning.HashIdentity("binary")}
	observer.observe(context.Background(), harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "opted-out"})
	proof := tools.VerificationEvidence{CallID: "call", Command: "go test ./...", Files: []tools.VerificationFile{{Path: "main.go", SHA256: strings.Repeat("a", 64), Mode: 0600}}}
	details, err := json.Marshal(map[string]any{"taskVerification": proof})
	if err != nil {
		t.Fatal(err)
	}
	observer.observe(context.Background(), harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "opted-out", ToolName: "bash", ToolCallID: "call", ToolResult: &msg.ToolResultMessage{ToolCallID: "call", Details: details}})
	if observer.current.outcome.VerifiedCommands != 1 || len(observer.current.proofs) != 0 || observer.current.proofs != nil {
		t.Fatalf("opt-out retained proof or lost count: %+v", observer.current)
	}
}

func TestOutcomeObserverReadRevocationBetweenToolAndRunEnd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data := []byte("package main\n")
	if err := os.WriteFile(filepath.Join(root, "main.go"), data, 0600); err != nil {
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
	allowed := true
	observer := &outcomeObserver{store: store, project: "project", root: root, config: learning.ObservationConfig{Enabled: true, FactMemoryEnabled: true, FactReadAllowed: func(string) bool { return allowed }, MaxOutcomes: 10, MinSamples: 5}, sessionID: "session", binaryHash: learning.HashIdentity("binary")}
	proof := tools.VerificationEvidence{CallID: "call", Command: "go test ./...", Files: []tools.VerificationFile{{Path: "main.go", SHA256: hex.EncodeToString(sum[:]), Mode: 0600}}}
	details, err := json.Marshal(map[string]any{"taskVerification": proof, "taskVerificationAttempt": tools.VerificationAttempt{CallID: "call", Command: proof.Command}})
	if err != nil {
		t.Fatal(err)
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "run"})
	observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "run", ToolName: "bash", ToolCallID: "call", ToolResult: &msg.ToolResultMessage{ToolCallID: "call", Details: details}})
	allowed = false
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "run", Status: harness.StatusCompleted})
	outcomes, err := learning.ListOutcomes(ctx, store, "project")
	if err != nil || len(outcomes) != 1 || outcomes[0].VerifiedCommands != 1 {
		t.Fatalf("aggregate lost after revocation: %+v %v", outcomes, err)
	}
	candidates, err := learning.ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" || !strings.Contains(candidates[0].Reason, "Read permission denied") {
		t.Fatalf("revoked Read promoted: %+v %v", candidates, err)
	}
	records, err := store.Search(ctx, "project", "Observed successful verification command", 5, root)
	if err != nil || len(records) != 0 {
		t.Fatalf("revoked Read stored fact: %+v %v", records, err)
	}
}

func TestOutcomeObserverUsesLiveGateReadRuleAtRunEnd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data := []byte("package main\n")
	if err := os.WriteFile(filepath.Join(root, "main.go"), data, 0600); err != nil {
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
	gate := permission.NewGate(permission.GateOptions{Roots: []string{root}, Mode: claudesettings.ModeManual})
	readAllowed := func(path string) bool {
		return claudesettings.DecideIn(gate.Permissions(), root, "read", filepath.Join(root, path), gate.Mode()) == claudesettings.Allow
	}
	observer := &outcomeObserver{store: store, project: "project", root: root, config: learning.ObservationConfig{Enabled: true, FactMemoryEnabled: true, FactReadAllowed: readAllowed, MaxOutcomes: 10, MinSamples: 5}, sessionID: "session", binaryHash: learning.HashIdentity("binary")}
	proof := tools.VerificationEvidence{CallID: "call", Command: "go test ./...", Files: []tools.VerificationFile{{Path: "main.go", SHA256: hex.EncodeToString(sum[:]), Mode: 0600}}}
	details, err := json.Marshal(map[string]any{"taskVerification": proof, "taskVerificationAttempt": tools.VerificationAttempt{CallID: "call", Command: proof.Command}})
	if err != nil {
		t.Fatal(err)
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunStart, Lane: agent.MainLane, OperationID: "run"})
	observer.observe(ctx, harness.Event{Type: harness.EventToolEnd, Lane: agent.MainLane, OperationID: "run", ToolName: "bash", ToolCallID: "call", ToolResult: &msg.ToolResultMessage{ToolCallID: "call", Details: details}})
	gate.AddRule(permission.RuleDeny, "Read(/main.go)")
	if readAllowed("main.go") {
		t.Fatal("live deny rule did not take effect")
	}
	observer.observe(ctx, harness.Event{Type: harness.EventRunEnd, Lane: agent.MainLane, OperationID: "run", Status: harness.StatusCompleted})
	candidates, err := learning.ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" || !strings.Contains(candidates[0].Reason, "Read permission denied") {
		t.Fatalf("live gate denial ignored: %+v %v", candidates, err)
	}
}

func TestRun_RepoLearningSettingsCannotEnableFactCapture(t *testing.T) {
	startFaux(t, "model: faux-1\nsteps:\n  - text: done\n")
	project := scratchProject(t)
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(`{"learningAutomation":{"enabled":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	args.Print, args.PrintPrompt = true, "hello"
	var stdout, stderr strings.Builder
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("Run exit %d: %s", code, stderr.String())
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(filepath.Join(home, ".kiln", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	outcomes, err := learning.ListOutcomes(context.Background(), store, memoryScope(project))
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("repository settings enabled capture: %+v %v", outcomes, err)
	}
}

func TestRun_PersonalMemoryOptOutBlocksFactsButKeepsOutcomes(t *testing.T) {
	startFaux(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "go test ./...", verificationFiles: ["main.go"]}, id: verify1}
  - on_tool_result: verify1
    then:
      - text: done
`)
	project := scratchProject(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"go.mod": "module example.test/facts\n\ngo 1.26\n", "main.go": "package main\n"} {
		if err := os.WriteFile(filepath.Join(project, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".kiln"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kiln", "settings.json"), []byte(`{"learningAutomation":{"enabled":true},"personalMemoryEnabled":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(`{"personalMemoryEnabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	args.Print, args.PrintPrompt, args.PermissionMode = true, "verify", "bypassPermissions"
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
	if err != nil || len(outcomes) != 1 || outcomes[0].VerifiedCommands != 1 {
		t.Fatalf("aggregate not captured: %+v %v", outcomes, err)
	}
	candidates, err := learning.ListFactCandidates(context.Background(), store, memoryScope(project), project)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("memory opt-out captured candidates: %+v %v", candidates, err)
	}
	records, err := store.Search(context.Background(), memoryScope(project), "Observed successful verification command", 5, project)
	if err != nil || len(records) != 0 {
		t.Fatalf("memory opt-out stored facts: %+v %v", records, err)
	}
}

func TestLearningFactCandidatesInspectAndDismiss(t *testing.T) {
	ctx := context.Background()
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(root)
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(resolved, "main.go")
	data := []byte("package main\n")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	proof := tools.VerificationEvidence{CallID: "call", Command: "go test ./...", Files: []tools.VerificationFile{{Path: "main.go", SHA256: hex.EncodeToString(sum[:]), Mode: 0600}}}
	store, err := memory.Open(filepath.Join(home, ".kiln", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	outcome := learning.Outcome{ID: learning.HashIdentity("run"), ObservedAt: time.Now().Unix(), Status: "completed", ModelHash: learning.HashIdentity(""), BinaryHash: learning.HashIdentity("binary"), UsageScope: "main-lane model responses; worker and side usage excluded"}
	if err := learning.RecordFactCandidates(ctx, store, memoryScope(resolved), resolved, outcome, []tools.VerificationEvidence{proof}, learning.ObservationConfig{Enabled: true, FactMemoryEnabled: true, FactReadAllowed: func(string) bool { return true }}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := Learning(ctx, Args{Positional: []string{"fact-candidates"}}, &out, &stderr); code != 0 {
		t.Fatalf("list exit %d: %s", code, stderr.String())
	}
	var candidates []learning.FactCandidate
	if err := json.Unmarshal(out.Bytes(), &candidates); err != nil || len(candidates) != 1 {
		t.Fatalf("list: %s %v", out.String(), err)
	}
	out.Reset()
	stderr.Reset()
	if code := Learning(ctx, Args{Positional: []string{"dismiss-fact", candidates[0].ID}}, &out, &stderr); code != 0 {
		t.Fatalf("dismiss exit %d: %s", code, stderr.String())
	}
	out.Reset()
	stderr.Reset()
	if code := Learning(ctx, Args{Positional: []string{"fact-candidates"}}, &out, &stderr); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("dismissed candidate visible: %d %s %s", code, out.String(), stderr.String())
	}
}

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
