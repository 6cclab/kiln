package agent

// Subagent dispatch — the `task` tool's data: the built-in agent
// definition, the allowlist rule, the catalog renderer and the schema/
// description the task tool wraps. This is a Go port of
// harness/src/agent/subagent.ts.
//
// A subagent is a fresh agent with its own context window, its own system
// prompt, and usually a narrower tool set. The parent sends a task and
// receives only the final answer — see dispatch.go for how that isolation
// is actually run.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
)

// GeneralPurpose is the built-in agent, so `task` is useful in a project
// with no .claude/agents/*.md definitions of its own. Text is a verbatim
// port of subagent.ts's GENERAL_PURPOSE.
var GeneralPurpose = agents.Definition{
	Name: "general-purpose",
	Description: "Researches a question or searches the codebase across many files and returns only the conclusion. " +
		"Use when answering would mean reading more than a couple of files.",
	Prompt: strings.Join([]string{
		"You are a research subagent. You have your own context window; the agent that dispatched you does not see",
		"your tool calls or intermediate reasoning, only your final message.",
		"",
		"Answer the task thoroughly, then report. Your final message IS the deliverable: state the conclusion and the",
		"evidence for it, with file paths and line numbers where they apply. Do not describe what you did.",
		"",
		"Do not ask follow-up questions - there is nobody to answer them. If the task is ambiguous, state the",
		"assumption you made and answer under it.",
	}, "\n"),
	Source: agents.Personal,
	Path:   "<built-in>",
}

// AllowedToolNames narrows the parent's tools to an agent's allowlist.
//
// Names are matched case-insensitively because definitions are written in
// Claude Code's casing (Read, Grep) while this harness's tools are
// lowercase.
//
// "task" is never included: recursive dispatch turns one runaway agent
// into a fork bomb, and nothing in a subagent's job needs it.
func AllowedToolNames(requested []string, available []string) []string {
	usable := make([]string, 0, len(available))
	for _, n := range available {
		if !strings.EqualFold(n, "task") {
			usable = append(usable, n)
		}
	}
	if requested == nil {
		return usable
	}

	wanted := make(map[string]bool, len(requested))
	for _, r := range requested {
		wanted[strings.ToLower(r)] = true
	}
	var matched []string
	for _, n := range usable {
		if wanted[strings.ToLower(n)] {
			matched = append(matched, n)
		}
	}
	// An allowlist that matches nothing is far more likely to be a casing
	// or naming mismatch than a genuine request for a tool-less agent, and
	// a tool-less agent cannot do research at all.
	if len(matched) > 0 {
		return matched
	}
	return usable
}

// DescribeAgents renders the agent catalog for the task tool's
// description.
//
// Budgeted, for the same reason the MCP catalog is: this text is resident
// on every turn. On the "small" tier a full listing with two-line
// descriptions would cost more than the entire tool budget, so
// descriptions are clipped there and at "medium". Derived from the tier
// name rather than hard-coded per tier so a new tier gets sensible
// behavior without editing this function.
func DescribeAgents(defs []agents.Definition, tier budget.Tier) string {
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

// TaskToolName is the task tool's name, matching Claude Code and
// subagent.ts.
const TaskToolName = "task"

// TaskParameters builds the task tool's JSON Schema: subagent_type
// (enumerated against defs, so a dispatch to a name that does not exist
// cannot even be produced), description and prompt.
//
// Deviation from the phase brief: the brief names the first field "agent";
// subagent.ts calls it "subagent_type". Following the TS source (per the
// phase instructions) rather than the brief's paraphrase.
func TaskParameters(defs []agents.Definition) json.RawMessage {
	names := make([]string, len(defs))
	for i, a := range defs {
		names[i] = a.Name
	}
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
		// names/descriptions are plain strings; this cannot fail in
		// practice, but a tool with no schema is worse than panicking
		// loudly during development.
		panic(fmt.Sprintf("agent: encoding task tool schema: %v", err))
	}
	return raw
}

// TaskDescription builds the task tool's description text, embedding the
// agent catalog (DescribeAgents) budgeted against tier.
func TaskDescription(defs []agents.Definition, tier budget.Tier) string {
	catalog := DescribeAgents(defs, tier)
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

// SubagentEventKind discriminates SubagentEvent.
type SubagentEventKind string

const (
	SubagentEventStart SubagentEventKind = "start"
	SubagentEventTool  SubagentEventKind = "tool"
	SubagentEventDone  SubagentEventKind = "done"
	SubagentEventError SubagentEventKind = "error"
)

// SubagentEvent is dispatch progress, for the TUI. Never shown to the
// model. Mirrors dispatch.ts's SubagentEvent union, flattened into one
// struct the way internal/harness's own Event is.
type SubagentEvent struct {
	Kind        SubagentEventKind
	Agent       string
	Description string
	ModelID     string
	Inherited   bool

	// tool
	ToolName string

	// done
	ToolCalls int
	Chars     int

	// error
	Message string
}
