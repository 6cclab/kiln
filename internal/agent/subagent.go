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
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/msg"
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
// "task" is stripped from available unless allowTask is true — recursive
// dispatch is depth-limited, not banned outright (see Dispatcher.Depth):
// two levels lets a subagent fan work out to others of its own without
// permitting unbounded recursion, which the caller enforces by only ever
// passing allowTask=true when its own Depth is under the limit and by
// never registering a `task` tool at all once it is not (a stripped name
// here would still leave the tool schema visible to the model with
// nothing behind it; not registering it is what actually closes the door).
func AllowedToolNames(requested []string, available []string, allowTask bool) []string {
	usable := make([]string, 0, len(available))
	for _, n := range available {
		if allowTask || !strings.EqualFold(n, "task") {
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

// DescribeRoles renders the configured model roles for the task tool's
// description, one "role: provider/model" line per role, sorted by role
// name. Clipped against the same per-tier limits as DescribeAgents: this
// text, like the agent catalog, is resident on every turn, so the same
// budget reasoning applies.
func DescribeRoles(roles map[string]string, tier budget.Tier) string {
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

// TaskParameters builds the task tool's JSON Schema: subagent_type
// (enumerated against defs, so a dispatch to a name that does not exist
// cannot even be produced), description, prompt, and, when roles is
// non-empty, an optional model role selector.
//
// Deviation from the phase brief: the brief names the first field "agent";
// subagent.ts calls it "subagent_type". Following the TS source (per the
// phase instructions) rather than the brief's paraphrase.
func TaskParameters(defs []agents.Definition, roles map[string]string) json.RawMessage {
	names := make([]string, len(defs))
	for i, a := range defs {
		names[i] = a.Name
	}
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
	// The schema gains a "model" property only when roles are actually
	// configured — with none, "inherit" is the only meaningful value, and
	// exposing a one-option enum would just be noise on every turn.
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
		// names/descriptions are plain strings; this cannot fail in
		// practice, but a tool with no schema is worse than panicking
		// loudly during development.
		panic(fmt.Sprintf("agent: encoding task tool schema: %v", err))
	}
	return raw
}

// TaskDescription builds the task tool's description text, embedding the
// agent catalog (DescribeAgents) and, when configured, the model role
// catalog (DescribeRoles), both budgeted against tier.
func TaskDescription(defs []agents.Definition, roles map[string]string, tier budget.Tier) string {
	catalog := DescribeAgents(defs, tier)
	parts := []string{
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
	}
	if roleCatalog := DescribeRoles(roles, tier); roleCatalog != "" {
		parts = append(parts, "", "Available model roles:", roleCatalog)
	}
	return strings.Join(parts, "\n")
}

// SubagentEventKind discriminates SubagentEvent.
type SubagentEventKind string

const (
	SubagentEventStart SubagentEventKind = "start"
	SubagentEventTool  SubagentEventKind = "tool"
	// SubagentEventUsage carries the subagent session's running token
	// totals, emitted once per model turn inside the dispatch. The design
	// (docs/kiln-design-handoff/Terminal.dc.html line 187-189) gives a
	// running subagent row a token figure, not only a finished one, so
	// the panel needs a usage signal before Done arrives.
	SubagentEventUsage SubagentEventKind = "usage"
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
	// ID is the dispatching tool call's id (DispatchRequest.ToolCallID),
	// so a UI that shows several concurrent dispatches (a Concurrent task
	// tool run, see internal/tool/tool.go's Tool.Concurrent) can tell
	// which start/tool/done/error events belong to the same call.
	ID string
	// ProviderID is the provider the resolved model actually runs on
	// (choice.ProviderID from agents.ResolveModel), alongside ModelID.
	ProviderID string
	// ModelKind is the string form of agents.ResolveKind explaining why
	// Dispatch landed on ModelID — "inherited", "role", "explicit",
	// "alias" or "fallback". Named ModelKind rather than the phase
	// brief's literal "Kind" because this struct already has a Kind field
	// for the event's own kind (start/tool/done/error); the two would
	// collide.
	ModelKind string
	Inherited bool
	// Depth is the nesting depth of the subagent this event describes: 1
	// for a subagent dispatched directly from the top-level session, 2 for
	// one dispatched from inside that subagent, and so on. It is always one
	// more than the Dispatcher.Depth of the Dispatcher that ran this
	// dispatch (see dispatch.go's Dispatch), so it also names the Depth a
	// child Dispatcher built for this subagent's own `task` tool would
	// carry, when one was built at all.
	Depth int

	// tool
	ToolName string

	// done
	ToolCalls int
	Chars     int
	// Usage is the subagent session's aggregate token/cost usage. It is
	// set on SubagentEventDone (final) and on SubagentEventUsage (the
	// running total after each of the subagent's own model turns).
	Usage msg.Usage

	// error
	Message string
}
