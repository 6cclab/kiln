package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/learning"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/tools"
)

type outcomeObserver struct {
	store      *memory.Store
	project    string
	root       string
	workdir    string // slash-separated launch directory below root; "" at the root
	config     learning.ObservationConfig
	sessionID  string
	binaryHash string
	current    *runAggregate
}

type runAggregate struct {
	operationID   string
	started       time.Time
	models        map[string]bool
	modelOverflow bool
	outcome       learning.Outcome
	proofs        map[string]tools.VerificationEvidence
	proofBytes    int
}

const maxCollectedFactProofs = 16
const maxCollectedFactProofBytes = 64 << 10
const maxCollectedFactProofBytesEach = 8 << 10

func collectedProofSize(proof tools.VerificationEvidence) int {
	raw, _ := json.Marshal(proof)
	key, _ := json.Marshal(proof.Command)
	return len(raw) + len(key) + 2
}

func toolFailureCategory(name string, o *learning.Outcome) {
	switch name {
	case "bash", "Bash":
		o.BashFailure++
	case "read", "Read":
		o.ReadFailure++
	case "write", "Write", "edit", "Edit", "multi_edit", "MultiEdit":
		o.WriteFailure++
	case "search", "Search", "grep", "Grep", "glob", "Glob", "tool_search", "capability_search", "skill_search", "verification_status":
		o.SearchFailure++
	case "task", "Task", "todo_write", "task_state_write", "task_verify":
		o.TaskFailure++
	default:
		o.OtherFailure++
	}
}

func verifiedCommand(ev harness.Event) bool {
	if ev.ToolName != "bash" && ev.ToolName != "Bash" {
		return false
	}
	if ev.ToolResult == nil || ev.ToolResult.IsError || ev.ToolCallID == "" || ev.ToolResult.ToolCallID != ev.ToolCallID {
		return false
	}
	proof, valid := tools.VerificationFromResult(ev.ToolResult.Details)
	return valid && proof.CallID == ev.ToolCallID
}

func (o *outcomeObserver) observe(ctx context.Context, ev harness.Event) {
	if ev.Lane != agent.MainLane {
		return
	}
	switch ev.Type {
	case harness.EventRunResume:
		// Earlier segments of a resumed operation are unavailable here.
		o.current = nil
	case harness.EventRunStart:
		if ev.OperationID == "" {
			return
		}
		run := &runAggregate{operationID: ev.OperationID, started: time.Now(), models: map[string]bool{}, outcome: learning.Outcome{ID: learning.HashIdentity(o.sessionID + "\x00" + ev.OperationID), ModelHash: learning.HashIdentity(""), BinaryHash: o.binaryHash, SkillBudgetTokens: o.config.SourceSkillBudgetTokens, UsageScope: "main-lane model responses; worker and side usage excluded"}}
		if o.config.FactMemoryEnabled && o.root != "" {
			run.proofs = make(map[string]tools.VerificationEvidence)
		}
		o.current = run
	case harness.EventToolEnd:
		r := o.current
		if r == nil || r.operationID != ev.OperationID || ev.ToolResult == nil {
			return
		}
		if r.proofs != nil && (ev.ToolName == "bash" || ev.ToolName == "Bash") {
			attempt, attempted := tools.VerificationAttemptFromResult(ev.ToolResult.Details)
			if ev.ToolResult.IsError && (!attempted || attempt.CallID != ev.ToolCallID) {
				clear(r.proofs)
				r.proofBytes = 0
			}
			if attempted && attempt.CallID == ev.ToolCallID {
				if previous, ok := r.proofs[attempt.Command]; ok {
					r.proofBytes -= collectedProofSize(previous)
				}
				delete(r.proofs, attempt.Command)
			}
			if verifiedCommand(ev) {
				proof, _ := tools.VerificationFromResult(ev.ToolResult.Details)
				if previous, ok := r.proofs[proof.Command]; ok {
					r.proofBytes -= collectedProofSize(previous)
					delete(r.proofs, proof.Command)
				}
				size := collectedProofSize(proof)
				if (!attempted || attempt.Command == proof.Command) && size <= maxCollectedFactProofBytesEach && len(r.proofs) < maxCollectedFactProofs && r.proofBytes+size <= maxCollectedFactProofBytes {
					r.proofs[proof.Command] = proof
					r.proofBytes += size
				}
			}
		}
		if ev.ToolResult.IsError {
			r.outcome.ToolFailure++
			toolFailureCategory(ev.ToolName, &r.outcome)
		} else {
			r.outcome.ToolSuccess++
			if verifiedCommand(ev) {
				r.outcome.VerifiedCommands++
			}
		}
	case harness.EventUsage:
		r := o.current
		if r == nil || r.operationID != ev.OperationID || ev.UsageRow == nil {
			return
		}
		r.outcome.InputTokens += ev.UsageRow.Input
		r.outcome.OutputTokens += ev.UsageRow.Output
		r.outcome.CacheReadTokens += ev.UsageRow.CacheRead
		r.outcome.CacheWriteTokens += ev.UsageRow.CacheWrite
		if ev.UsageModel != nil {
			model := ev.UsageModel.Provider + "/" + ev.UsageModel.ModelID
			if !r.models[model] {
				if len(r.models) == 100 {
					r.modelOverflow = true
				} else {
					r.models[model] = true
				}
			}
		}
	case harness.EventRunEnd:
		r := o.current
		if r == nil || r.operationID != ev.OperationID {
			return
		}
		o.current = nil
		if (ev.Status != harness.StatusCompleted && ev.Status != harness.StatusFailed) || r.modelOverflow {
			return
		}
		r.outcome.Status = ev.Status
		if len(r.models) > 0 {
			models := make([]string, 0, len(r.models))
			for model := range r.models {
				models = append(models, model)
			}
			sort.Strings(models)
			r.outcome.ModelHash = learning.HashIdentity(strings.Join(models, "\x00"))
			r.outcome.ModelsUsed = len(models)
		}
		r.outcome.ObservedAt = time.Now().Unix()
		r.outcome.DurationMS = time.Since(r.started).Milliseconds()
		writeCtx := ctx
		if writeCtx.Err() != nil {
			writeCtx = context.Background()
		}
		boundedCtx, cancel := context.WithTimeout(writeCtx, 5*time.Second)
		defer cancel()
		if err := learning.RecordOutcomeAndPropose(boundedCtx, o.store, o.project, r.outcome, o.config); err != nil {
			diag.L().Warn("learning outcome unavailable", "error", err)
			return
		}
		if o.root != "" && o.config.FactMemoryEnabled && r.outcome.Status == "completed" {
			proofs := make([]tools.VerificationEvidence, 0, len(r.proofs))
			for _, proof := range r.proofs {
				proofs = append(proofs, proof)
			}
			sort.Slice(proofs, func(i, j int) bool { return proofs[i].Command < proofs[j].Command })
			if err := learning.RecordFactCandidatesIn(boundedCtx, o.store, o.project, o.root, o.workdir, r.outcome, proofs, o.config); err != nil {
				diag.L().Warn("learning fact candidates unavailable", "error", err)
			}
		}
	}
}

// factLaunchDir splits the launch directory into the project (worktree) root
// and the slash-separated directory below it. Verification paths are relative
// to the launch directory, so promotion needs both. A launch directory outside
// its own worktree root yields no root, which disables fact extraction.
func factLaunchDir(cwd string) (root, workdir string) {
	root = memoryProjectRoot(cwd)
	canonical := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		canonical = resolved
	}
	rel, err := filepath.Rel(root, canonical)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ""
	}
	if rel == "." {
		return root, ""
	}
	return root, filepath.ToSlash(rel)
}

// attachLearningObserver installs opt-in sanitized main-run capture. A disabled
// configuration returns before opening a database or subscribing to events.
func attachLearningObserver(ctx context.Context, cwd string, started *agent.Started, cfg learning.ObservationConfig) (func(), error) {
	if !cfg.Enabled {
		return func() {}, nil
	}
	var err error
	cfg, err = learning.NormalizeObservationConfig(cfg)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	binaryHash, err := learning.FileHash(executable)
	if err != nil {
		return nil, err
	}
	store, err := memory.Open(filepath.Join(home, ".kiln", "memory.db"))
	if err != nil {
		return nil, err
	}
	root, workdir := factLaunchDir(cwd)
	observer := &outcomeObserver{store: store, project: memoryScope(cwd), root: root, workdir: workdir, config: cfg, sessionID: started.SessionID, binaryHash: binaryHash}
	events := started.Harness.Events()
	kinds := []harness.EventType{harness.EventRunStart, harness.EventRunResume, harness.EventToolEnd, harness.EventUsage, harness.EventRunEnd}
	unsubs := make([]func(), 0, len(kinds))
	for _, kind := range kinds {
		unsubs = append(unsubs, events.On(kind, func(ev harness.Event) { observer.observe(ctx, ev) }))
	}
	return func() {
		for _, unsub := range unsubs {
			unsub()
		}
		if err := store.Close(); err != nil {
			diag.L().Warn("learning outcome database close failed", "error", err)
		}
	}, nil
}
