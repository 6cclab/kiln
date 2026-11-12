package tools

// The `task` tool: dispatch a task to a subagent with its own context
// window, and return only its final report. A Go port of
// harness/src/agent/subagent.ts's createTaskTool.
//
// Two deliberate deviations from the phase brief, both forced by the same
// constraint: internal/agent already imports internal/tools (session.go
// calls tools.Builtins), so internal/tools importing internal/agent back
// would be an import cycle.
//
//   - TaskTool does not take *agent.Dispatcher directly, unlike the
//     brief's literal `TaskTool(d *Dispatcher, agents, tier)`.
//     TaskDispatchFunc is the seam instead — a plain function value with
//     the same shape as (*agent.Dispatcher).Dispatch. Wiring code (cli, a
//     later phase) needs one line to bridge them: TaskDispatchResult and
//     agent.DispatchResult are distinct named types (field-for-field
//     identical, but Go does not treat that as interchangeable for a
//     function value's return type), so `d.Dispatch` itself cannot be
//     passed as a TaskDispatchFunc — see internal/tools/task_e2e_test.go
//     for the one-line adapter closure this requires.
//   - describeAgents/taskParameters/taskDescription below are small,
//     standalone duplicates of agent.DescribeAgents/TaskParameters/
//     TaskDescription (internal/agent/subagent.go), rather than calls into
//     that package. internal/claude/agents.Definition and budget.Tier are
//     shared safely (neither imports internal/tools); only the "agent"
//     package itself is off-limits here.
import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// TaskDispatchResult mirrors agent.DispatchResult's shape.
type TaskDispatchResult struct {
	UsageByModel map[string]msg.Usage `json:"usageByModel,omitempty"`
	Artifact     *TaskArtifact        `json:"artifact,omitempty"`
	Text         string
	ToolCalls    int
	Chars        int
	// Model is the provider/model the subagent actually ran on.
	Model string
	Usage msg.Usage
}

// TaskArtifact is the integration handle returned by an isolated worker.
type TaskArtifact struct {
	ManifestPath string        `json:"manifestPath,omitempty"`
	BaseCommit   string        `json:"baseCommit"`
	PatchPath    string        `json:"patchPath"`
	SHA256       string        `json:"sha256"`
	Bytes        int           `json:"bytes"`
	ChangedFiles []string      `json:"changedFiles,omitempty"`
	Baseline     *TaskBaseline `json:"baseline,omitempty"`
}

// TaskBaseline carries the captured dirty parent baseline through task results.
type TaskBaseline struct {
	Tree  string `json:"tree"`
	State string `json:"state"`
}

// TaskRequest mirrors agent.DispatchRequest's shape — what the task tool
// hands its dispatch function. ToolCallID comes from the Execute call's
// tool.Invocation, not from the model's arguments.
type TaskRequest struct {
	Agent       string
	Description string
	Prompt      string
	Model       string
	ToolCallID  string
}

// TaskDispatchFunc is satisfied by an adapter over (*agent.Dispatcher).Dispatch
// (see internal/tools/task_e2e_test.go for the one-line closure this
// requires, and this file's header comment for why an adapter rather than
// a direct reference is needed).
type TaskDispatchFunc func(ctx context.Context, req TaskRequest) (TaskDispatchResult, error)

type taskArgs struct {
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	Model        string `json:"model"`
}

// TaskTool builds the `task` tool. defs is the dispatchable agent catalog
// (its names populate the subagent_type enum, and its descriptions are
// embedded in the tool description, budgeted against tier); roles is
// settings.json's modelRoles map (nil or empty omits the `model` argument
// entirely, see taskParameters); dispatch actually runs one.
func TaskTool(dispatch TaskDispatchFunc, defs []agents.Definition, roles map[string]string, tier budget.Tier, autoIntegrate ...bool) *tool.Tool {
	automatic := len(autoIntegrate) > 0 && autoIntegrate[0]
	names := make([]string, len(defs))
	for i, a := range defs {
		names[i] = a.Name
	}

	return &tool.Tool{
		Name:        "task",
		Label:       "Task",
		Description: taskDescription(defs, roles, tier),
		Parameters:  taskParameters(names, roles),
		// A dispatched subagent gets its own session and its own storage,
		// so running several task calls from one assistant message in
		// parallel shares no mutable state with the parent session.
		Concurrent: true,
		Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, inv tool.Invocation) (tool.Result, error) {
			var a taskArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return tool.Result{}, fmt.Errorf("task: decoding arguments: %w", err)
				}
			}
			wanted := strings.TrimSpace(a.SubagentType)
			prompt := strings.TrimSpace(a.Prompt)

			found := false
			for _, n := range names {
				if n == wanted {
					found = true
					break
				}
			}
			if !found {
				// Naming what does exist turns a dead turn into a usable one.
				list := "none"
				if len(names) > 0 {
					list = strings.Join(names, ", ")
				}
				return tool.Text(fmt.Sprintf("No agent named %q. Available: %s.", wanted, list)), nil
			}
			if prompt == "" {
				return tool.Text("No prompt given. Call again with the task."), nil
			}

			description := strings.TrimSpace(a.Description)
			if description == "" {
				description = wanted
			}

			result, err := dispatch(ctx, TaskRequest{
				Agent:       wanted,
				Description: description,
				Prompt:      prompt,
				Model:       strings.TrimSpace(a.Model),
				ToolCallID:  inv.ToolCallID,
			})
			if err != nil {
				// A failed subagent is a failed tool call, not a failed
				// session. The parent can retry, dispatch elsewhere, or do
				// the work itself.
				out := tool.Text(fmt.Sprintf("Subagent %q failed: %s%s", wanted, err.Error(), taskArtifactText(result.Artifact, automatic)))
				out.Usage = &result.Usage
				out.UsageByModel = result.UsageByModel
				if details, encodeErr := taskArtifactDetails(result.Artifact, automatic, true); encodeErr == nil {
					out.Details = details
				}
				return out, nil
			}
			text := result.Text
			if text == "" {
				text = "(the subagent returned nothing)"
			}
			text += taskArtifactText(result.Artifact, automatic)
			out := tool.Text(text)
			out.Usage = &result.Usage
			out.UsageByModel = result.UsageByModel
			if details, encodeErr := taskArtifactDetails(result.Artifact, automatic, false); encodeErr == nil {
				out.Details = details
			}
			return out, nil
		},
	}
}

func taskArtifactDetails(a *TaskArtifact, flags ...bool) (json.RawMessage, error) {
	var automatic, failed bool
	if len(flags) > 0 {
		automatic = flags[0]
	}
	if len(flags) > 1 {
		failed = flags[1]
	}
	if automatic {
		return json.Marshal(struct {
			Dispatched bool          `json:"dispatched"`
			Failed     bool          `json:"dispatchFailed"`
			Artifact   *TaskArtifact `json:"artifact,omitempty"`
		}{Dispatched: true, Failed: failed, Artifact: a})
	}
	if a == nil {
		return nil, nil
	}
	return json.Marshal(struct {
		Artifact *TaskArtifact `json:"artifact"`
	}{Artifact: a})
}

func taskArtifactText(a *TaskArtifact, automatic bool) string {
	if a == nil {
		return ""
	}
	if automatic {
		return fmt.Sprintf("\n\nWorker patch manifest: %s (%d bytes; %d files; base %s; SHA256 %s). Retained pending the batch integration result.", a.ManifestPath, a.Bytes, len(a.ChangedFiles), a.BaseCommit, a.SHA256)
	}
	return fmt.Sprintf("\n\nWorker patch manifest: %s (%d bytes; %d files; base %s; SHA256 %s). Apply explicitly after review.", a.ManifestPath, a.Bytes, len(a.ChangedFiles), a.BaseCommit, a.SHA256)
}

// describeAgents is a standalone duplicate of agent.DescribeAgents; see
// this file's header comment for why it is not a call into that package.
func describeAgents(defs []agents.Definition, tier budget.Tier) string {
	if len(defs) == 0 {
		return ""
	}
	var perAgent int
	unlimited := false
	switch tier.Name {
	case "small":
		perAgent = 120
	case "medium":
		perAgent = 300
	default:
		unlimited = true
	}
	lines := make([]string, 0, len(defs))
	for _, a := range defs {
		desc := a.Description
		if !unlimited && len(desc) > perAgent {
			desc = strings.TrimRight(desc[:perAgent], " \t\n") + "..."
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", a.Name, desc))
	}
	return strings.Join(lines, "\n")
}

// describeRoles is a standalone duplicate of agent.DescribeRoles; see this
// file's header comment for why it is not a call into that package.
func describeRoles(roles map[string]string, tier budget.Tier) string {
	if len(roles) == 0 {
		return ""
	}
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)

	var perRole int
	unlimited := false
	switch tier.Name {
	case "small":
		perRole = 120
	case "medium":
		perRole = 300
	default:
		unlimited = true
	}
	lines := make([]string, 0, len(names))
	for _, name := range names {
		value := roles[name]
		if !unlimited && len(value) > perRole {
			value = strings.TrimRight(value[:perRole], " \t\n") + "..."
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", name, value))
	}
	return strings.Join(lines, "\n")
}

// taskDescription is a standalone duplicate of agent.TaskDescription.
func taskDescription(defs []agents.Definition, roles map[string]string, tier budget.Tier) string {
	catalog := describeAgents(defs, tier)
	parts := []string{
		"Dispatch a task to a subagent with its own context window; you get back only its final report, not its tool calls or reasoning.",
		"",
		"Use it for work spanning many files, or independent work needing no supervision. Skip it for a single lookup where you already know the file - read it yourself; dispatch costs a full round-trip. Use it when the user asks for subagents or parallel agents.",
		"",
		"Call it several times in one message to run those tasks in parallel.",
		"",
		"The subagent sees none of this conversation and cannot ask you questions - put everything it needs in the prompt.",
		"",
		"Available agents:",
		catalog,
	}
	if roleCatalog := describeRoles(roles, tier); roleCatalog != "" {
		parts = append(parts, "", "Available model roles:", roleCatalog)
	}
	return strings.Join(parts, "\n")
}

// taskParameters is a standalone duplicate of agent.TaskParameters.
func taskParameters(names []string, roles map[string]string) json.RawMessage {
	properties := map[string]any{
		"subagent_type": map[string]any{
			"type":        "string",
			"enum":        names,
			"description": "Which agent to dispatch to.",
		},
		"description": map[string]any{
			"type":        "string",
			"description": "A 3-5 word label for this task, shown to the user.",
		},
		"prompt": map[string]any{
			"type":        "string",
			"description": "The task. Self-contained: the subagent sees none of this conversation.",
		},
	}
	if len(roles) > 0 {
		roleNames := make([]string, 0, len(roles))
		for name := range roles {
			roleNames = append(roleNames, name)
		}
		sort.Strings(roleNames)
		properties["model"] = map[string]any{
			"type":        "string",
			"enum":        append(roleNames, "inherit"),
			"description": "Role for this task: fast for scripts and lookups, structured for well-specified changes, heavy for open-ended work. Omit to inherit the current model.",
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   []string{"subagent_type", "prompt"},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("tools: encoding task tool schema: %v", err))
	}
	return raw
}
