package agent

// Running a subagent. A Go port of harness/src/agent/dispatch.ts.
//
// A subagent gets its own agent.Start (its own harness, its own JSONL
// session file) rather than a lane on the parent's harness: the system
// prompt is set per-harness, not per-lane, so a subagent whose system
// prompt is its parent's is not a subagent — it is the same agent with a
// different first message, which loses the specialization the definition
// exists to express.
//
// A separate session also means a separate JSONL log, which is what makes
// a subagent's work searchable afterwards instead of vanishing into a
// tool result the parent summarized.
//
// # The dispatch tree
//
// A Dispatcher's Depth (0 for the one process-level Dispatcher, built once
// by cli.go) names how many dispatches deep it sits. Dispatching a subagent
// at Depth < 2 gives that subagent its own `task` tool, wired to a fresh
// child Dispatcher at Depth+1 — so the tree can go root -> subagent ->
// subagent's-subagent before it stops offering `task` at all, rather than
// banning nested dispatch outright as the pre-P5a comment on
// AllowedToolNames did. Two levels covers "a subagent that needs to fan
// work out to others" without permitting unbounded recursion; a Dispatcher
// at Depth 2 simply never builds a `task` tool for what it starts, so the
// model at the bottom of the tree never even sees the option. Every
// Dispatcher in the tree shares the same Gate (one permission ledger, so an
// "allow always" granted three levels down is remembered everywhere) and
// the same Roles map (model names are configured once, at the top, not
// re-specified per level). A child Dispatcher's Parent is the subagent it
// was built for, not the tree's root, so "inherit" inside a nested dispatch
// resolves to the model the immediately enclosing subagent is running on.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/automode"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

// residentToolNames are the four built-in tools every session gets for
// free (tools.Builtins' names), hardcoded here rather than computed via
// tools.Builtins: internal/agent already imports internal/tools
// (session.go's Start), so this package importing internal/tools a second
// time here would be harmless in itself, but the allowlist only ever needs
// the name list, and hardcoding it matches dispatch.ts's own hardcoded
// availability list exactly.
var residentToolNames = []string{"bash", "read", "edit", "write"}

// DispatcherOptions has no separate type in this port; Dispatcher itself
// is constructed directly by its caller (cli wiring), matching the phase
// brief's Dispatcher{...} literal shape rather than dispatch.ts's
// DispatcherOptions + createDispatcher split — there is nothing here that
// benefits from the extra indirection in Go.

// Dispatcher runs subagents dispatched via the task tool.
type Dispatcher struct {
	Registry *provider.Registry
	// Parent is the session doing the dispatching. Its model is the
	// inheritance default for ResolveModel, and its cwd anchors the
	// subagent's own session (via Env.Cwd).
	Parent *Started
	// Gate is the parent's permission gate, installed unmodified (the SAME
	// instance, not a copy) on every subagent harness's before_tool hook.
	// An "allow always" granted inside a subagent is therefore remembered
	// by the parent and vice versa. Without this, dispatching would be a
	// way to run tools the user would otherwise have been asked about.
	// It also gates a cross-provider paid dispatch itself — see Dispatch.
	Gate *permission.Gate
	// Agents is the catalog Dispatch resolves subagent_type against.
	Agents []agents.Definition
	// Roles is settings.json's modelRoles map (role name ->
	// "provider/model"), threaded into agents.ResolveModel so a task's
	// `model` argument (or an agent definition's `model:` alias) can name
	// a role instead of a literal provider/model. Nil when none are
	// configured — ResolveModel treats that exactly like an empty map.
	Roles map[string]string
	// SessionsRoot is where the subagent's own JSONL session file is
	// created, alongside the parent's.
	SessionsRoot string
	// OnSubagentStop, if set, runs once when a dispatched subagent's run
	// has ended, however it ended. It carries the subagent's own session so
	// the SubagentStop hook can name its transcript, the dispatching tool
	// call's context (cancelled when the user interrupts the parent turn)
	// and the run's status (harness.StatusCompleted, StatusAborted, ...),
	// so a caller can run the hook only for a run that completed.
	OnSubagentStop func(ctx context.Context, agentName string, sub *Started, status string)
	// Env is the filesystem/shell context the subagent's built-in tools
	// run against; its Cwd anchors the subagent's session too.
	Env *execenv.Env
	// OnEvent reports dispatch progress, for the TUI. Never shown to the
	// model.
	OnEvent func(SubagentEvent)
	// UserHistory returns the root session's conversation, for auto mode's
	// classifier: in a subagent the user's own words are only there, and
	// the subagent's first message is a task the parent agent wrote. Child
	// dispatchers inherit it. Nil leaves the classifier only the task.
	UserHistory func(ctx context.Context) []msg.Message
	// Depth is how many dispatches deep this Dispatcher sits: 0 for the
	// top-level Dispatcher wired up once per process (cli.go's own
	// `dispatcher`), 1 for the Dispatcher a depth-0 dispatch builds for its
	// subagent's own `task` tool, 2 for the one that subagent's subagent
	// would get. Dispatch only ever builds a child at Depth < 2, so a
	// subagent three dispatches deep from the root has no `task` tool
	// registered at all — see Dispatch's doc comment for why two levels
	// rather than zero.
	Depth int
}

// DispatchRequest is what the task tool hands Dispatch. Model is the
// task's own `model` argument (a role name, "inherit", or empty); it is
// consulted ahead of the agent definition's own `model:` field. ToolCallID
// identifies the dispatching tool call, carried through to SubagentEvent.ID
// so a UI running several dispatches concurrently (Tool.Concurrent) can
// tell them apart.
type DispatchRequest struct {
	Agent       string
	Description string
	Prompt      string
	Model       string
	ToolCallID  string
}

// DispatchResult is what Dispatch returns to the task tool.
type DispatchResult struct {
	Text string
	// ToolCalls is reported to the user, never to the model.
	ToolCalls int
	Chars     int
	// Model is the provider/model the subagent actually ran on (empty
	// when resolution never got that far, e.g. an unknown agent).
	Model string
	// Usage is the subagent session's aggregate token/cost usage.
	Usage msg.Usage
}

// Dispatch resolves agentName against d.Agents, starts a fresh session for
// it (its own JSONL file under d.SessionsRoot), runs prompt to completion
// on it, and returns its final report.
//
// ctx is forwarded to both session creation and the run itself: cancelling
// the parent's context aborts the subagent's turn exactly as it would the
// parent's own.
func (d *Dispatcher) Dispatch(ctx context.Context, req DispatchRequest) (DispatchResult, error) {
	def, ok := findAgentDefinition(d.Agents, req.Agent)
	if !ok {
		return DispatchResult{}, fmt.Errorf("agent: no agent named %q", req.Agent)
	}

	parentModel := agents.ModelChoice{ProviderID: d.Parent.Model.Provider, ModelID: d.Parent.Model.ID}
	candidates, err := modelCandidates(ctx, d.Registry)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("agent: list model candidates: %w", err)
	}

	// The task call's own `model` argument wins over the agent
	// definition's `model:` field — a caller picking a role for this one
	// task knows more about what this task needs than the definition's
	// static default.
	requested := req.Model
	if requested == "" || requested == "inherit" {
		requested = def.Model
	}
	choice, resolveKind := agents.ResolveModel(requested, d.Roles, parentModel, candidates)
	// Logged, not surfaced: a fallback is visible in the transcript line,
	// but the reason (which providers had usable auth and models at that
	// moment) is only recoverable from the run log.
	diag.L().Info("subagent model resolved",
		"agent", def.Name, "requested", requested, "kind", string(resolveKind),
		"provider", choice.ProviderID, "model", choice.ModelID,
		"roles", len(d.Roles), "candidates", len(candidates), "providers", candidateProviders(candidates))
	// Kept for compatibility with callers that only care whether the
	// request landed on the parent's model rather than exactly why.
	inherited := resolveKind == agents.ResolveInherited || resolveKind == agents.ResolveFallback

	resolved, err := d.Registry.Resolve(choice.ProviderID, choice.ModelID)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("agent: resolve model for subagent %q: %w", def.Name, err)
	}

	// A dispatch that crosses onto a different, metered provider is a
	// spending decision, not just a routing one - the user configured
	// modelRoles.heavy to mean "the good model", not "put it on my card
	// without asking". Same-provider switches (faux-1 -> faux-2, or any
	// local Ollama tier change) and free models never hit this: it is
	// scoped to exactly the case that can cost real money on a provider
	// the user did not already choose for this turn.
	if d.Gate != nil && choice.ProviderID != parentModel.ProviderID && resolved.Model.Cost.ModelCostRates.Input > 0 {
		blocked, err := d.Gate.Check(ctx, permission.Request{
			ToolName:   "task",
			PrimaryArg: "role:" + requested,
		})
		if err != nil {
			return DispatchResult{}, fmt.Errorf("agent: permission check for subagent %q: %w", def.Name, err)
		}
		if blocked != nil {
			return DispatchResult{
				Text: fmt.Sprintf("Dispatch to %s/%s was not allowed: %s", choice.ProviderID, choice.ModelID, blocked.Reason),
			}, nil
		}
	}

	// allowTask gates both this subagent's own `task` tool (built below)
	// and, through AllowedToolNames, whether "task" can even survive an
	// explicit allowlist in def.Tools. A subagent built by a Dispatcher at
	// Depth 0 or 1 gets one, wired to a child Dispatcher one level deeper;
	// at Depth 2 nothing is registered at all, so the model never sees the
	// tool and cannot even attempt it — see Dispatcher.Depth's doc comment.
	allowTask := d.Depth < 2
	available := residentToolNames
	if allowTask {
		available = append(append([]string(nil), residentToolNames...), TaskToolName)
	}
	// The allowlist is applied to what this harness actually has, not to
	// what the definition imagined.
	activeToolNames := AllowedToolNames(def.Tools, available, allowTask)

	// Depth on the event is the depth of the subagent THIS Dispatch call is
	// starting, i.e. one past this Dispatcher's own — it lines up with the
	// child Dispatcher built just below, when one is built at all.
	depth := d.Depth + 1

	if d.OnEvent != nil {
		d.OnEvent(SubagentEvent{
			Kind:        SubagentEventStart,
			Agent:       def.Name,
			Description: req.Description,
			ModelID:     choice.ModelID,
			ID:          req.ToolCallID,
			ProviderID:  choice.ProviderID,
			ModelKind:   string(resolveKind),
			Inherited:   inherited,
			Depth:       depth,
		})
	}

	// child is this subagent's own dispatcher for its `task` tool, one
	// level deeper than d. Its Parent is set once Start returns below (the
	// subagent's own *Started), not d.Parent — nested "inherit" must land
	// on the model the subagent it is nested inside actually ran on, the
	// same way chat.go's top-level dispatcher.Parent is only ever readable
	// once its own Start has returned. Nothing reads child.Parent before
	// then: the task tool's Execute cannot run until started.Lane.Prompt
	// below actually reaches a tool call.
	var child *Dispatcher
	var extraTools []*tool.Tool
	if allowTask {
		child = &Dispatcher{
			Registry:       d.Registry,
			Gate:           d.Gate,
			Agents:         d.Agents,
			Roles:          d.Roles,
			SessionsRoot:   d.SessionsRoot,
			Env:            d.Env,
			OnEvent:        d.OnEvent,
			OnSubagentStop: d.OnSubagentStop,
			UserHistory:    d.UserHistory,
			Depth:          depth,
		}
		extraTools = append(extraTools, tools.TaskTool(adaptDispatch(child.Dispatch), d.Agents, d.Roles, resolved.Tier))
	}

	started, err := Start(ctx, Options{
		Registry:     d.Registry,
		Resolved:     resolved,
		Cwd:          d.Env.Cwd,
		SessionsRoot: d.SessionsRoot,
		// The definition's body IS the system prompt. That is the whole
		// point of a subagent, and the reason for a separate harness.
		SystemPrompt: def.Prompt,
		Env:          d.Env,
		// d.Parent is the session dispatching this one — set so the new
		// session's own header records it as a subagent's, not a
		// top-level one (internal/cli/tui.go's buildRecentSessionRows
		// filters the banner's recent-sessions list on exactly this
		// field). d.Parent is always non-nil here: Dispatch is only ever
		// reached through a Dispatcher a caller built with one (cli.go's
		// process-level dispatcher, or a child Dispatcher this same
		// function assigns Parent to just below, once this call
		// succeeds).
		ParentSessionID: d.Parent.SessionID,
		ExtraTools:      extraTools,
		ActiveToolNames: activeToolNames,
	})
	if err != nil {
		return DispatchResult{}, fmt.Errorf("agent: start subagent session for %q: %w", def.Name, err)
	}
	// Always closed, on every return path, including an aborted or failed
	// run.
	defer func() { _ = started.Harness.Close() }()
	if child != nil {
		child.Parent = started
	}

	// Installed before the first prompt, so no tool call can slip through
	// between session creation and the hook being attached.
	if d.Gate != nil {
		gate := d.Gate
		var userHistory func() []msg.Message
		if d.UserHistory != nil {
			userHistory = func() []msg.Message { return d.UserHistory(ctx) }
		}
		started.Harness.Hooks().OnBeforeTool(func(ctx context.Context, call msg.ToolCall) (harness.BeforeToolResult, error) {
			primaryArg, _ := permission.PrimaryArgOf(call.Arguments)
			blocked, err := gate.Check(ctx, permission.Request{
				ToolName:   call.Name,
				PrimaryArg: primaryArg,
				Args:       call.Arguments,
				// In auto mode the classifier judges a subagent's call
				// against the subagent's own conversation, whose user
				// messages are the delegated task (Delegated: data, not
				// the user's words), and the root session's typed lines.
				CallID:      call.ID,
				History:     func() []msg.Message { return automode.BranchMessages(ctx, started.Lane) },
				Delegated:   true,
				UserHistory: userHistory,
			})
			if err != nil {
				return harness.BeforeToolResult{}, err
			}
			if blocked != nil {
				return harness.BeforeToolResult{Block: &harness.ToolBlock{Reason: blocked.Reason}}, nil
			}
			return harness.BeforeToolResult{}, nil
		})
	}

	var toolCalls int
	var text string

	unsubToolStart := started.Harness.Events().On(harness.EventToolStart, func(ev harness.Event) {
		toolCalls++
	})
	defer unsubToolStart()

	// The panel's "last action" line (internal/tui/subagents.go) wants a
	// finished call's primary argument and a short result summary, not
	// just the tool's name — neither is known until the call ends, so this
	// forwards EventToolEnd instead of EventToolStart (which the "tool"
	// SubagentEvent used to fire on, before the panel needed anything more
	// than the bare tool name).
	unsubToolEnd := started.Harness.Events().On(harness.EventToolEnd, func(ev harness.Event) {
		if d.OnEvent != nil {
			d.OnEvent(SubagentEvent{
				Kind:       SubagentEventTool,
				Agent:      def.Name,
				ID:         req.ToolCallID,
				ToolName:   ev.ToolName,
				ToolArgs:   ev.ToolArgs,
				ToolResult: ev.ToolResult,
				Depth:      depth,
			})
		}
	})
	defer unsubToolEnd()

	// The subagent's own harness emits EventUsage once per model turn with
	// the session's running totals (internal/harness/turn.go). Forwarding
	// it lets the panel show a live token figure for a running row, which
	// the design requires; without it a row's tokens column stays empty
	// until Done.
	unsubUsage := started.Harness.Events().On(harness.EventUsage, func(ev harness.Event) {
		if ev.UsageTotals == nil || d.OnEvent == nil {
			return
		}
		d.OnEvent(SubagentEvent{
			Kind:  SubagentEventUsage,
			Agent: def.Name,
			ID:    req.ToolCallID,
			Depth: depth,
			Usage: *ev.UsageTotals,
		})
	})
	defer unsubUsage()

	unsubMsg := started.Harness.Events().On(harness.EventMessageEnd, func(ev harness.Event) {
		if ev.Message == nil {
			return
		}
		// Replaced, not accumulated: the parent wants the subagent's final
		// report, not a transcript of every intermediate thought it had on
		// the way there. Accumulating would reintroduce exactly the
		// context cost the isolation exists to avoid.
		chunk := msg.TextOf(ev.Message.Content)
		if strings.TrimSpace(chunk) != "" {
			text = chunk
		}
	})
	defer unsubMsg()

	result, err := started.Lane.Prompt(ctx, req.Prompt, nil)
	if d.OnSubagentStop != nil {
		status := result.Status
		if err != nil && status == "" {
			status = harness.StatusFailed
		}
		d.OnSubagentStop(ctx, def.Name, started, status)
	}
	if err != nil {
		if d.OnEvent != nil {
			d.OnEvent(SubagentEvent{Kind: SubagentEventError, Agent: def.Name, ID: req.ToolCallID, Message: err.Error(), Depth: depth})
		}
		return DispatchResult{}, err
	}

	modelLabel := choice.ProviderID + "/" + choice.ModelID
	usage := started.Harness.Stats().Usage

	if result.Status != harness.StatusCompleted {
		message := result.Status
		if result.Error != nil {
			message = result.Error.Error()
		}
		if d.OnEvent != nil {
			d.OnEvent(SubagentEvent{Kind: SubagentEventError, Agent: def.Name, ID: req.ToolCallID, Message: message, Depth: depth})
		}
		// Returned rather than surfaced as an error: a partial answer plus
		// the failure is more use to the parent than the failure alone.
		if text == "" {
			text = fmt.Sprintf("The subagent failed: %s", message)
		}
		return DispatchResult{Text: text, ToolCalls: toolCalls, Chars: len(text), Model: modelLabel, Usage: usage}, nil
	}

	if d.OnEvent != nil {
		d.OnEvent(SubagentEvent{
			Kind:      SubagentEventDone,
			Agent:     def.Name,
			ID:        req.ToolCallID,
			ModelID:   choice.ModelID,
			ToolCalls: toolCalls,
			Chars:     len(text),
			Usage:     usage,
			Depth:     depth,
			Text:      text,
		})
	}
	return DispatchResult{Text: text, ToolCalls: toolCalls, Chars: len(text), Model: modelLabel, Usage: usage}, nil
}

// adaptDispatch bridges a *Dispatcher.Dispatch method value to
// tools.TaskDispatchFunc, the same one-line adapter cli.go's own top-level
// wiring uses (tools.TaskRequest/TaskDispatchResult are distinct named
// types from DispatchRequest/DispatchResult — see internal/tools/task.go's
// header comment for why). Used here to give a subagent's own `task` tool
// a dispatch function without internal/tools importing internal/agent.
func adaptDispatch(dispatch func(context.Context, DispatchRequest) (DispatchResult, error)) tools.TaskDispatchFunc {
	return func(ctx context.Context, req tools.TaskRequest) (tools.TaskDispatchResult, error) {
		r, err := dispatch(ctx, DispatchRequest{
			Agent:       req.Agent,
			Description: req.Description,
			Prompt:      req.Prompt,
			Model:       req.Model,
			ToolCallID:  req.ToolCallID,
		})
		return tools.TaskDispatchResult{Text: r.Text, ToolCalls: r.ToolCalls, Chars: r.Chars, Model: r.Model, Usage: r.Usage}, err
	}
}

func findAgentDefinition(defs []agents.Definition, name string) (agents.Definition, bool) {
	for _, d := range defs {
		if d.Name == name {
			return d, true
		}
	}
	return agents.Definition{}, false
}

// modelCandidates projects the registry's currently-available providers
// and their models onto agents.Candidate, matching
// createDispatcher's `(await opts.registry.available()).map(...)`.
func modelCandidates(ctx context.Context, reg *provider.Registry) ([]agents.Candidate, error) {
	providers, err := reg.Available(ctx)
	if err != nil {
		return nil, err
	}
	var out []agents.Candidate
	for _, p := range providers {
		for _, m := range p.Models() {
			out = append(out, agents.Candidate{ID: m.ID, Provider: p.ID()})
		}
	}
	return out, nil
}

// candidateProviders lists the distinct providers among candidates, for
// the resolution log line.
func candidateProviders(candidates []agents.Candidate) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		if !seen[c.Provider] {
			seen[c.Provider] = true
			out = append(out, c.Provider)
		}
	}
	sort.Strings(out)
	return out
}
