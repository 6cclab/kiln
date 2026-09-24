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
	"strings"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/tool"
)

// TaskDispatchResult mirrors agent.DispatchResult's shape.
type TaskDispatchResult struct {
	Text      string
	ToolCalls int
	Chars     int
}

// TaskDispatchFunc is satisfied by (*agent.Dispatcher).Dispatch.
type TaskDispatchFunc func(ctx context.Context, agentName, description, prompt string) (TaskDispatchResult, error)

type taskArgs struct {
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
}

// TaskTool builds the `task` tool. defs is the dispatchable agent catalog
// (its names populate the subagent_type enum, and its descriptions are
// embedded in the tool description, budgeted against tier); dispatch
// actually runs one.
func TaskTool(dispatch TaskDispatchFunc, defs []agents.Definition, tier budget.Tier) *tool.Tool {
	names := make([]string, len(defs))
	for i, a := range defs {
		names[i] = a.Name
	}

	return &tool.Tool{
		Name:        "task",
		Label:       "Task",
		Description: taskDescription(defs, tier),
		Parameters:  taskParameters(names),
		Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
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

			result, err := dispatch(ctx, wanted, description, prompt)
			if err != nil {
				// A failed subagent is a failed tool call, not a failed
				// session. The parent can retry, dispatch elsewhere, or do
				// the work itself.
				return tool.Text(fmt.Sprintf("Subagent %q failed: %s", wanted, err.Error())), nil
			}
			text := result.Text
			if text == "" {
				text = "(the subagent returned nothing)"
			}
			return tool.Text(text), nil
		},
	}
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

// taskDescription is a standalone duplicate of agent.TaskDescription.
func taskDescription(defs []agents.Definition, tier budget.Tier) string {
	catalog := describeAgents(defs, tier)
	return strings.Join([]string{
		"Dispatch a task to a subagent with its own context window. The subagent's tool calls and reasoning",
		"do not enter your context - you receive only its final report.",
		"",
		"Use it when answering would mean reading across many files, or for independent work that can run",
		"without your supervision. For a single lookup where you already know the file, read it yourself:",
		"dispatch costs a full round-trip.",
		"",
		"The subagent cannot ask you questions and does not see this conversation. Put everything it needs in",
		"the prompt.",
		"",
		"Available agents:",
		catalog,
	}, "\n")
}

// taskParameters is a standalone duplicate of agent.TaskParameters.
func taskParameters(names []string) json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
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
		},
		"required": []string{"subagent_type", "prompt"},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(fmt.Sprintf("tools: encoding task tool schema: %v", err))
	}
	return raw
}
