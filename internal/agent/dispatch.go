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

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
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
	// inheritance default for ResolveAgentModel, and its cwd anchors the
	// subagent's own session (via Env.Cwd).
	Parent *Started
	// Gate is the parent's permission gate, installed unmodified (the SAME
	// instance, not a copy) on every subagent harness's before_tool hook.
	// An "allow always" granted inside a subagent is therefore remembered
	// by the parent and vice versa. Without this, dispatching would be a
	// way to run tools the user would otherwise have been asked about.
	Gate *permission.Gate
	// Agents is the catalog Dispatch resolves subagent_type against.
	Agents []agents.Definition
	// SessionsRoot is where the subagent's own JSONL session file is
	// created, alongside the parent's.
	SessionsRoot string
	// OnSubagentStop, if set, runs once when a dispatched subagent's run
	// has ended, however it ended. It carries the subagent's own session so
	// the SubagentStop hook can name its transcript.
	OnSubagentStop func(agentName string, sub *Started)
	// Env is the filesystem/shell context the subagent's built-in tools
	// run against; its Cwd anchors the subagent's session too.
	Env *execenv.Env
	// OnEvent reports dispatch progress, for the TUI. Never shown to the
	// model.
	OnEvent func(SubagentEvent)
}

// DispatchResult is what Dispatch returns to the task tool.
type DispatchResult struct {
	Text string
	// ToolCalls is reported to the user, never to the model.
	ToolCalls int
	Chars     int
}

// Dispatch resolves agentName against d.Agents, starts a fresh session for
// it (its own JSONL file under d.SessionsRoot), runs prompt to completion
// on it, and returns its final report.
//
// ctx is forwarded to both session creation and the run itself: cancelling
// the parent's context aborts the subagent's turn exactly as it would the
// parent's own.
func (d *Dispatcher) Dispatch(ctx context.Context, agentName, description, prompt string) (DispatchResult, error) {
	def, ok := findAgentDefinition(d.Agents, agentName)
	if !ok {
		return DispatchResult{}, fmt.Errorf("agent: no agent named %q", agentName)
	}

	parentModel := agents.ModelChoice{ProviderID: d.Parent.Model.Provider, ModelID: d.Parent.Model.ID}
	candidates, err := modelCandidates(ctx, d.Registry)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("agent: list model candidates: %w", err)
	}
	choice := agents.ResolveAgentModel(def.Model, parentModel, candidates)
	// def.Model == "" covers both "the field was absent" and "it was set
	// to an empty string"; agents.Definition does not distinguish them
	// (ParseAgent trims a missing key to ""). subagent.ts's Inherited flag
	// is `req.agent.model !== undefined`, which Go's plain string field
	// cannot reproduce exactly - this is the closest equivalent without
	// changing agents.Definition's shape.
	inherited := choice.ModelID == parentModel.ModelID && def.Model != ""

	resolved, err := d.Registry.Resolve(choice.ProviderID, choice.ModelID)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("agent: resolve model for subagent %q: %w", def.Name, err)
	}

	// The allowlist is applied to what this harness actually has, not to
	// what the definition imagined.
	activeToolNames := AllowedToolNames(def.Tools, residentToolNames)

	if d.OnEvent != nil {
		d.OnEvent(SubagentEvent{
			Kind:        SubagentEventStart,
			Agent:       def.Name,
			Description: description,
			ModelID:     choice.ModelID,
			Inherited:   inherited,
		})
	}

	started, err := Start(ctx, Options{
		Registry:     d.Registry,
		Resolved:     resolved,
		Cwd:          d.Env.Cwd,
		SessionsRoot: d.SessionsRoot,
		// The definition's body IS the system prompt. That is the whole
		// point of a subagent, and the reason for a separate harness.
		SystemPrompt:    def.Prompt,
		Env:             d.Env,
		ActiveToolNames: activeToolNames,
	})
	if err != nil {
		return DispatchResult{}, fmt.Errorf("agent: start subagent session for %q: %w", def.Name, err)
	}
	// Always closed, on every return path, including an aborted or failed
	// run.
	defer func() { _ = started.Harness.Close() }()

	// Installed before the first prompt, so no tool call can slip through
	// between session creation and the hook being attached.
	if d.Gate != nil {
		gate := d.Gate
		started.Harness.Hooks().OnBeforeTool(func(ctx context.Context, call msg.ToolCall) (harness.BeforeToolResult, error) {
			primaryArg, _ := permission.PrimaryArgOf(call.Arguments)
			blocked, err := gate.Check(ctx, permission.Request{
				ToolName:   call.Name,
				PrimaryArg: primaryArg,
				Args:       call.Arguments,
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

	unsubTool := started.Harness.Events().On(harness.EventToolStart, func(ev harness.Event) {
		toolCalls++
		if d.OnEvent != nil {
			d.OnEvent(SubagentEvent{Kind: SubagentEventTool, Agent: def.Name, ToolName: ev.ToolName})
		}
	})
	defer unsubTool()

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

	result, err := started.Lane.Prompt(ctx, prompt, nil)
	if d.OnSubagentStop != nil {
		d.OnSubagentStop(def.Name, started)
	}
	if err != nil {
		if d.OnEvent != nil {
			d.OnEvent(SubagentEvent{Kind: SubagentEventError, Agent: def.Name, Message: err.Error()})
		}
		return DispatchResult{}, err
	}

	if result.Status != harness.StatusCompleted {
		message := result.Status
		if result.Error != nil {
			message = result.Error.Error()
		}
		if d.OnEvent != nil {
			d.OnEvent(SubagentEvent{Kind: SubagentEventError, Agent: def.Name, Message: message})
		}
		// Returned rather than surfaced as an error: a partial answer plus
		// the failure is more use to the parent than the failure alone.
		if text == "" {
			text = fmt.Sprintf("The subagent failed: %s", message)
		}
		return DispatchResult{Text: text, ToolCalls: toolCalls, Chars: len(text)}, nil
	}

	if d.OnEvent != nil {
		d.OnEvent(SubagentEvent{Kind: SubagentEventDone, Agent: def.Name, ToolCalls: toolCalls, Chars: len(text)})
	}
	return DispatchResult{Text: text, ToolCalls: toolCalls, Chars: len(text)}, nil
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
