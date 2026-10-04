// Package cli, this file: builds the slash-command registry, wiring every
// internal/commands source's Deps struct against this run's session,
// following cli.ts's registration order (src/cli.ts:381-533) — least
// specific first, so a later source's command of the same name wins.
//
// Deviation from cli.ts's literal registration order: internal/commands'
// InlineCommands bundles /posture, /bashes and /todos into ONE Source
// (cli.ts registers them at three separate points: posture early, bashes
// after sessionCommands, todos last, after manageCommands). Splitting that
// package to match the three positions exactly is out of this phase's
// scope (internal/commands is not owned by this agent). Registering
// InlineCommands last instead reproduces cli.ts's actual OUTCOME — its
// /todos still wins over accountCommands' /todos, the only collision that
// position affects — even though /posture and /bashes land later than
// cli.ts places them. See the phase report for this deviation.
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	claudeagents "github.com/andrepato/harness/internal/claude/agents"
	claudecommands "github.com/andrepato/harness/internal/claude/commands"
	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/permission"
	claudeplugins "github.com/andrepato/harness/internal/claude/plugins"
	claudeskills "github.com/andrepato/harness/internal/claude/skills"
	"github.com/andrepato/harness/internal/claude/writesettings"
	slashcommands "github.com/andrepato/harness/internal/commands"
	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session/jsonl"
)

// registryDeps is everything buildCommandRegistry needs from Run. Kept as
// one struct rather than a long parameter list, since most of its fields
// are handed unchanged to several of internal/commands' Deps structs.
type registryDeps struct {
	Cwd     string
	Started *agent.Started
	// Interactive enables commands that only make sense in the TUI, such
	// as /resume switching sessions by relaunching.
	Interactive bool

	Registry *provider.Registry
	Gate     *permission.Gate
	Hooks    claudehooks.Config
	Agents   []claudeagents.Definition
	Skills   []claudeskills.Skill
	// Plugins is the active (installed and enabled) plugin set for this
	// run, for /plugin's report.
	Plugins []claudeplugins.Plugin
	// PluginCommands is every active plugin's own commands/**/*.md,
	// already namespaced "<plugin>:<name>" (internal/claude/plugins.
	// Commands) — registered as their own Source so a plugin's commands
	// and its palette entries update independently of Claude's own
	// .claude/commands (see slashcommands.PluginCommandSource).
	PluginCommands []claudecommands.CommandFile
	MCP            *mcpSession
	Todos          *agent.TodoStore
	Shells         *agent.BackgroundShells

	SettingsLoadedFrom []string
	// SandboxLine/SandboxProblems are /doctor's sandbox section (cli's
	// sandboxReport, computed once at startup from the same cwd/settings
	// startSandbox used) — InspectDeps has no settings.Settings of its
	// own to build this from.
	SandboxLine     string
	SandboxProblems []string
	// ModelRoles is settings.json's modelRoles map, threaded through to
	// /model roles. Nil when none are configured.
	ModelRoles map[string]string
	// ModelLabel is a snapshot taken at construction time, matching cli.ts:
	// none of the report/manage commands re-read it after a /model switch
	// (cli.ts computes `modelLabel: \`${provider}/${modelId}\`` once, from
	// the session's initial resolve, and never updates it).
	ModelLabel string

	SessionRepo *jsonl.Repo
	SessionsDir string

	// ContextUsed reports the last usage event's total token count, for
	// /usage. Nil is treated as "no usage yet".
	ContextUsed func() (int, bool)
	// FileReadTokens reports the tokens attributable to file contents
	// read into this session, for /context's "Files read" segment
	// (docs/kiln-design-handoff/Terminal.dc.html line 227). Nil is
	// treated as "not tracked", and the segment reports zero.
	FileReadTokens func() (int, bool)
	// UsageByModel reports this session's accumulated usage keyed by
	// "provider/model", for /cost's by-model table. Nil when not tracked.
	UsageByModel func() map[string]msg.Usage
	// SessionStartedAt is when this session's Run began (captured right
	// around agent.Start in chat.go), for /cost's one-line design summary
	// ("... · 71s", Terminal.dc.html:320) via BuiltinDeps.SessionElapsed.
	// Zero is treated as "unknown" and the elapsed segment is omitted.
	SessionStartedAt time.Time
	// MCPConfigPath is the mcpServers file /mcp names in its section header.
	MCPConfigPath string
	// AutoMemoryStatus is /context's one-line report of Claude Code's
	// auto-memory index: whether it loaded, was trimmed, or was skipped,
	// and why (internal/claude/memory.AutoMemory.StatusLine).
	AutoMemoryStatus string
}

// mcpStatusesOf adapts mcp.ServerStatus onto slashcommands.ServerStatus, the
// shape internal/commands' Deps structs use since that package cannot
// import internal/mcp (see inspect_commands.go's own doc comment on this).
func mcpStatusesOf(hub *mcpgate.Hub) func() []slashcommands.ServerStatus {
	return func() []slashcommands.ServerStatus {
		statuses := hub.Statuses()
		out := make([]slashcommands.ServerStatus, len(statuses))
		for i, s := range statuses {
			out[i] = slashcommands.ServerStatus{Name: s.Name, Scope: s.Scope, OK: s.OK, ToolCount: s.ToolCount, Ms: s.Ms, Error: s.Error, Detail: s.Detail}
		}
		for _, s := range hub.Pending() {
			out = append(out, slashcommands.ServerStatus{Name: s.Name, Scope: s.Scope, Connecting: true})
		}
		return out
	}
}

// mcpToolsOf adapts mcp.McpTool onto slashcommands.MCPTool for /mcp's "t" (list
// tools) action.
func mcpToolsOf(hub *mcpgate.Hub) func() []slashcommands.MCPTool {
	return func() []slashcommands.MCPTool {
		tools := hub.Tools()
		out := make([]slashcommands.MCPTool, len(tools))
		for i, t := range tools {
			out[i] = slashcommands.MCPTool{Name: t.Name, Server: t.Server}
		}
		return out
	}
}

// isUnknownCommandResult reports whether res is registry.Execute's own
// "not a command"/"no such command" failure text (commands/registry.go's
// Execute: "Not a valid command: " / "Unknown command: "), as opposed to a
// real command that simply has nothing to print. Print mode exits 1 for
// the former and 0 for the latter.
func isUnknownCommandResult(res slashcommands.Result) bool {
	if res.Prompt != "" || len(res.Output) != 1 {
		return false
	}
	line := res.Output[0]
	return strings.HasPrefix(line, "Not a valid command: ") || strings.HasPrefix(line, "Unknown command: ")
}

// posturesOf adapts mcp.Postures onto slashcommands.Posture for InlineDeps.
func posturesOf() []slashcommands.Posture {
	out := make([]slashcommands.Posture, len(mcpgate.Postures))
	for i, p := range mcpgate.Postures {
		out[i] = slashcommands.Posture{Name: p.Name, Description: p.Description}
	}
	return out
}

// buildCommandRegistry wires every internal/commands source against this
// run's session and returns the assembled registry, ready for
// registry.Execute / registry.List.
func buildCommandRegistry(deps registryDeps, hub *mcpgate.Hub) *slashcommands.Registry {
	started := deps.Started
	reg := deps.Registry
	gate := deps.Gate
	mcpSess := deps.MCP

	registry := slashcommands.NewRegistry()

	builtinSource := slashcommands.BuiltinCommands(slashcommands.BuiltinDeps{
		Lane:       started.Lane,
		Registry:   reg,
		ModelRoles: deps.ModelRoles,
		CurrentModel: func() (string, string) {
			return started.Model.Provider, started.Model.ID
		},
		CurrentTier:    func() budget.Tier { return started.Tier },
		ModelLabel:     func() string { return deps.ModelLabel },
		ContextUsed:    deps.ContextUsed,
		FileReadTokens: deps.FileReadTokens,
		// The tier's SystemPromptTokens is a fixed ceiling, not what this
		// session's system prompt actually assembled to — measure the
		// real one instead so /context's "System prompt" segment (and
		// the header built from it, buildContextBreakdown) reports the
		// real figure rather than a budget that can be far from it.
		SystemPromptTokens: func() (int, bool) {
			sp := started.Harness.SystemPrompt()
			if sp == "" {
				return 0, false
			}
			return (len(sp) + 3) / 4, true
		},
		// Measured from the lane's own active tool schemas rather than
		// budget.ToolStrategyCost, which is a planning ceiling per
		// strategy: in a real session that estimate overshot the whole
		// measured context and drove /context's Conversation segment to
		// zero.
		ToolSchemaTokens: func() (int, bool) {
			if started.Lane == nil {
				return 0, false
			}
			n, err := started.Lane.ToolSchemaTokens()
			if err != nil || n <= 0 {
				return 0, false
			}
			return n, true
		},
		SwitchModel: func(ctx context.Context, providerID, modelID string) (budget.Tier, error) {
			return switchModel(ctx, reg, started, mcpSess, providerID, modelID)
		},
		Agents:           deps.Agents,
		SessionsDir:      deps.SessionsDir,
		UsageByModel:     deps.UsageByModel,
		AutoMemoryStatus: func() string { return deps.AutoMemoryStatus },
		SessionElapsed: func() time.Duration {
			if deps.SessionStartedAt.IsZero() {
				return 0
			}
			return time.Since(deps.SessionStartedAt)
		},
		// /clear moves the lane's branch tip back to the root, so the next
		// turn starts from an empty conversation; the session log keeps
		// the earlier turns (reachable again through /rewind).
		OnClear: func(ctx context.Context) error {
			if started.Lane == nil {
				return nil
			}
			return started.Lane.NavigateTree(ctx, nil)
		},
		OnExit: func() {},
	})
	registry.Add(slashcommands.BindHelp(builtinSource, registry.List))

	registry.Add(slashcommands.SkillSource(deps.Skills))

	registry.Add(slashcommands.SessionCommands(slashcommands.SessionCommandDeps{
		Lane:        started.Lane,
		Repo:        deps.SessionRepo,
		Gate:        gate,
		Cwd:         deps.Cwd,
		SessionsDir: deps.SessionsDir,
		CurrentID:   started.SessionID,
		Relaunch:    relaunchFor(deps.Interactive),
	}))

	registry.Add(slashcommands.AccountCommands(slashcommands.AccountDeps{
		Registry:    reg,
		Todos:       deps.Todos,
		Tier:        started.Tier,
		ModelLabel:  deps.ModelLabel,
		ContextUsed: deps.ContextUsed,
	}))

	registry.Add(slashcommands.InspectCommands(slashcommands.InspectDeps{
		MCPStatuses:        mcpStatusesOf(hub),
		Gate:               gate,
		Hooks:              deps.Hooks,
		Agents:             deps.Agents,
		Tier:               started.Tier,
		Cwd:                deps.Cwd,
		ModelLabel:         deps.ModelLabel,
		ActiveTools:        func() ([]string, error) { return started.Lane.GetActiveTools() },
		SettingsLoadedFrom: deps.SettingsLoadedFrom,
		SandboxLine:        deps.SandboxLine,
		SandboxProblems:    deps.SandboxProblems,
	}))

	registry.Add(slashcommands.ManageCommands(slashcommands.ManageDeps{
		Gate:               gate,
		MCPConfigPath:      deps.MCPConfigPath,
		MCPStatuses:        mcpStatusesOf(hub),
		MCPTools:           mcpToolsOf(hub),
		Hooks:              deps.Hooks,
		Agents:             deps.Agents,
		Cwd:                deps.Cwd,
		ModelLabel:         deps.ModelLabel,
		SettingsLoadedFrom: deps.SettingsLoadedFrom,
		SaveRule: func(list writesettings.RuleList, rule string) error {
			gate.AddRule(permission.RuleList(list), rule)
			return writesettings.AddRule(deps.Cwd, list, rule)
		},
		RemoveRule: func(list writesettings.RuleList, rule string) error {
			return removeKilnRule(gate, deps.Cwd, list, rule)
		},
	}))

	registry.Add(slashcommands.InlineCommands(slashcommands.InlineDeps{
		Postures:      posturesOf(),
		ActivePosture: func() string { return mcpSess.posture.Name },
		PostureToolCount: func(name string) int {
			p, ok := mcpgate.PostureByName(name)
			if !ok {
				return 0
			}
			return len(scopedMCPTools(mcpSess.tools, p))
		},
		SwitchPosture: func(ctx context.Context, name string) error { return switchPosture(mcpSess, name) },
		RenderShellList: func() []string {
			if deps.Shells == nil {
				return nil
			}
			return strings.Split(agent.RenderShellList(deps.Shells.List()), "\n")
		},
		Todos: deps.Todos,
	}))

	for _, s := range slashcommands.ClaudeCommandSources(deps.Cwd) {
		registry.Add(s)
	}
	registry.Add(slashcommands.PluginCommandSource(deps.PluginCommands))

	registry.Add(slashcommands.PluginReportCommand(deps.Cwd, deps.Plugins))

	return registry
}

// relaunchFor is /resume's Relaunch hook: nil in print mode, which runs one
// prompt and exits rather than switching sessions.
func relaunchFor(interactive bool) func(string) {
	if !interactive {
		return nil
	}
	return func(id string) { pendingRelaunch = id }
}

// removeKilnRule is /permissions' delete: kiln edits only its own
// .kiln/settings.local.json, so a rule that comes from Claude Code's
// settings (or the command line) is refused, with where it lives.
func removeKilnRule(gate *permission.Gate, cwd string, list writesettings.RuleList, rule string) error {
	if !writesettings.HasRule(cwd, list, rule) {
		return fmt.Errorf("%s is not kiln's to delete: it comes from %s, which kiln reads but never edits", rule, gate.RuleOrigin(permission.RuleList(list), rule))
	}
	gate.RemoveRule(permission.RuleList(list), rule)
	return writesettings.RemoveRule(cwd, list, rule)
}
