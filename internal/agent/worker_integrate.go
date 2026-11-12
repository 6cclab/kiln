package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// WorkerBatchCompletion makes one decision after every tool result in an
// assistant response is durable. Intent is committed before applying any
// patch. A restarted process never repeats an intent's external effect.
func WorkerBatchCompletion(d *Dispatcher) harness.ToolBatchCompleteFunc {
	return func(ctx context.Context, storage session.Storage, operationID, assistantID string, calls []msg.ToolCall, results []msg.ToolResultMessage, recovered bool) (*harness.ToolBatchCompletion, error) {
		var artifacts []WorkerArtifact
		var manifests []string
		hasTask, failed := false, false
		for i, call := range calls {
			if results[i].IsError {
				failed = true
			}
			if call.Name != "task" {
				continue
			}
			hasTask = true
			var details struct {
				Dispatched bool            `json:"dispatched"`
				Failed     bool            `json:"dispatchFailed"`
				Artifact   *WorkerArtifact `json:"artifact"`
			}
			if len(results[i].Details) == 0 || json.Unmarshal(results[i].Details, &details) != nil || !details.Dispatched || details.Failed {
				failed = true
			}
			if details.Artifact != nil {
				artifacts = append(artifacts, *details.Artifact)
				manifests = append(manifests, details.Artifact.ManifestPath)
			}
		}
		if !hasTask {
			return nil, nil
		}
		label := strings.Join(manifests, ", ")
		if label == "" {
			label = "none"
		}
		priorRaw, _, exists := storage.GetValue(harness.WorkerIntegrationNamespace, assistantID)
		if exists {
			var prior harness.ToolBatchCompletion
			if err := json.Unmarshal(priorRaw, &prior); err != nil || prior.Status == "" {
				return nil, fmt.Errorf("worker integration: invalid durable decision for %s", assistantID)
			}
			if prior.Status == "intent" {
				retained := prior.Artifacts
				if len(retained) == 0 {
					retained = manifests
				}
				return &harness.ToolBatchCompletion{Status: "uncertain", Text: "Worker patch integration may have applied before interruption; inspect the parent checkout and retained artifacts before retrying: " + strings.Join(retained, ", ") + ".", Artifacts: retained}, nil
			}
			return &prior, nil
		}
		if recovered {
			return &harness.ToolBatchCompletion{Status: "blocked", Text: "Recovered worker batch was not automatically applied; inspect retained artifacts and apply explicitly: " + label + ".", Artifacts: manifests}, nil
		}
		if failed || ctx.Err() != nil {
			return &harness.ToolBatchCompletion{Status: "blocked", Text: "Worker batch was not integrated because a tool or worker failed; retained artifacts: " + label + ".", Artifacts: manifests}, nil
		}
		if len(artifacts) == 0 {
			return &harness.ToolBatchCompletion{Status: "unchanged", Text: "Worker batch completed with no file changes.", Artifacts: manifests}, nil
		}
		intent := harness.ToolBatchCompletion{Status: "intent", Text: "Worker patch integration in progress", Artifacts: manifests}
		raw, err := json.Marshal(intent)
		if err != nil {
			return nil, err
		}
		if _, err := storage.Commit([]session.Write{session.SetValueRaw(harness.WorkerIntegrationNamespace, assistantID, raw)}); err != nil {
			return nil, fmt.Errorf("persist worker integration intent: %w", err)
		}
		dispatchResults := make([]DispatchResult, len(artifacts))
		for i := range artifacts {
			artifact := artifacts[i]
			dispatchResults[i] = DispatchResult{Status: harness.StatusCompleted, Artifact: &artifact}
		}
		inspection, err := d.IntegrateWorkerResults(ctx, dispatchResults)
		if err != nil {
			return &harness.ToolBatchCompletion{Status: "conflict", Text: fmt.Sprintf("Worker batch was not integrated: %v. Retained artifacts: %s.", err, label), Artifacts: manifests}, nil
		}
		return &harness.ToolBatchCompletion{Status: "applied", Text: fmt.Sprintf("Worker batch integrated %d patches across %d files: %s.", inspection.Artifacts, len(inspection.Files), strings.Join(inspection.Files, ", ")), Artifacts: manifests}, nil
	}
}
