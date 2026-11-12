package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/tool"
)

// TaskCheckpoint is a model-authored task note. It contains no verification claims.
type TaskCheckpoint struct {
	Version     int      `json:"taskVersion"`
	Objective   string   `json:"objective"`
	Constraints []string `json:"constraints,omitempty"`
	Decisions   []string `json:"decisions,omitempty"`
}

// TaskStateTool saves bounded, structured task notes in a successful result.
func TaskStateTool() *tool.Tool {
	return &tool.Tool{
		Name: "task_state_write", Label: "Update task state",
		Description: "Save the current objective, constraints, and decisions for recovery after context compaction. These are task notes, not verified outcomes. Replaces the previous state.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"objective":{"type":"string"},"constraints":{"type":"array","items":{"type":"string"}},"decisions":{"type":"array","items":{"type":"string"}}},"required":["objective"]}`),
		Execute: func(_ context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var in TaskCheckpoint
			if err := json.Unmarshal(raw, &in); err != nil {
				return tool.Errorf("task_state_write: invalid arguments: %v", err), nil
			}
			in.Objective = strings.TrimSpace(in.Objective)
			if in.Objective == "" || len(in.Objective) > 4096 || len(in.Constraints) > 16 || len(in.Decisions) > 16 {
				return tool.Errorf("task_state_write: objective or list size is invalid"), nil
			}
			for _, s := range append(append([]string{}, in.Constraints...), in.Decisions...) {
				if strings.TrimSpace(s) == "" || len(s) > 1024 {
					return tool.Errorf("task_state_write: empty or oversized note"), nil
				}
			}
			in.Version = 1
			details, err := json.Marshal(in)
			if err != nil {
				return tool.Result{}, fmt.Errorf("task_state_write: marshal: %w", err)
			}
			result := tool.Text("Task state saved.")
			result.Details = details
			return result, nil
		},
	}
}
