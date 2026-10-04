package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/plural"
)

// ServerStatus is one MCP server's connection outcome. It stands in for a
// type internal/mcp would otherwise own; that package does not exist yet
// (built by another agent in this phase). The integrator maps the real
// mcp.ServerStatus onto this shape when wiring InspectDeps.MCPStatuses --
// see doc.go and the phase report for this deviation.
type ServerStatus struct {
	Name string
	// Scope is where the server is configured: user, local, project or
	// flag (--mcp-config); "" when unknown.
	Scope string
	// Connecting is true while the server's first connect attempt is still
	// running; OK and Error are unset until it finishes.
	Connecting bool
	OK         bool
	ToolCount  int
	Ms         int64
	Error      string
	// Detail is the failure's full underlying text (mcp.ServerStatus.Detail),
	// shown dim in /mcp's per-server detail view. Empty unless the
	// integrator's mcpStatusesOf adapter (internal/cli/commands.go) maps
	// it through — see the /mcp dialog's handback report for whether that
	// wiring landed.
	Detail string
}

// InspectGate is the subset of permission.Gate that /permissions and
// /doctor need.
type InspectGate interface {
	Mode() settings.PermissionMode
	Permissions() settings.Permissions
	SessionGrants() []string
	Roots() []string
}

// hookEventOrder is the fixed display order for /hooks and /doctor's count.
var hookEventOrder = []hooks.Event{
	hooks.PreToolUse,
	hooks.PostToolUse,
	hooks.UserPromptSubmit,
	hooks.SessionStart,
	hooks.SessionEnd,
	hooks.Stop,
	hooks.SubagentStop,
	hooks.Notification,
	hooks.PreCompact,
}

// InspectDeps is everything inspectCommands binds against.
type InspectDeps struct {
	MCPStatuses func() []ServerStatus
	Gate        InspectGate
	Hooks       hooks.Config
	Agents      []agents.Definition
	Tier        budget.Tier
	Cwd         string
	ModelLabel  string
	// ActiveTools returns the resident tool names, for /doctor's summary.
	ActiveTools        func() ([]string, error)
	SettingsLoadedFrom []string
	// SandboxLine is /doctor's one-line sandbox state ("off (…)", "on
	// (seatbelt), …") — cli's sandboxReport, built once at startup and
	// handed through since InspectDeps has no settings.Settings/cwd of its
	// own to build it from directly.
	SandboxLine string
	// SandboxProblems are sandboxReport's own problems (a configured but
	// unavailable sandbox, an unsandboxed Linux Unix-socket note, …),
	// folded into /doctor's problem list alongside the MCP/tool ones.
	SandboxProblems []string
}

func truncate(text string, max int) string {
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	return string(r[:max-1]) + "…"
}

func hookCount(cfg hooks.Config) (total int, eventsUsed int) {
	for _, event := range hookEventOrder {
		groups := cfg[event]
		n := 0
		for _, g := range groups {
			n += len(g.Hooks)
		}
		if n > 0 {
			eventsUsed++
		}
		total += n
	}
	return total, eventsUsed
}

// InspectCommands returns the source for /mcp, /permissions, /hooks and
// /doctor. All four answer "what is actually configured right now?" — an
// answer otherwise spread across several files in several directories.
//
// /mcp and /permissions here are the report-only forms. When the
// integrator also registers ManageCommands, its versions of the same
// names (which additionally open a panel) are registered later and win.
func InspectCommands(deps InspectDeps) Source {
	cmds := []Command{
		{
			Name:        "mcp",
			Description: "Show MCP servers, their status and tool counts",
			Run: func(ctx context.Context, args string) (Result, error) {
				var statuses []ServerStatus
				if deps.MCPStatuses != nil {
					statuses = deps.MCPStatuses()
				}
				if len(statuses) == 0 {
					return Result{Output: []string{"No MCP servers configured. Add one with kiln mcp add — it writes ~/.claude.json/.mcp.json, the same files claude mcp add uses"}}, nil
				}
				var ok, failed, connecting []ServerStatus
				tools := 0
				for _, s := range statuses {
					switch {
					case s.Connecting:
						connecting = append(connecting, s)
					case s.OK:
						ok = append(ok, s)
						tools += s.ToolCount
					default:
						failed = append(failed, s)
					}
				}
				lines := []string{fmt.Sprintf("%d/%d connected, %d tools", len(ok), len(statuses), tools), ""}
				for _, s := range ok {
					lines = append(lines, fmt.Sprintf("  %-22s %4d tools  %dms", s.Name, s.ToolCount, s.Ms))
				}
				for _, s := range failed {
					lines = append(lines, fmt.Sprintf("  %-22s failed: %s", s.Name, truncate(orDefault(s.Error, "unknown"), 90)))
				}
				for _, s := range connecting {
					lines = append(lines, fmt.Sprintf("  %-22s connecting…", s.Name))
				}
				return Result{Output: lines}, nil
			},
		},
		{
			Name:        "permissions",
			Description: "Show permission mode, rules and this session's grants",
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.Gate == nil {
					return Result{Output: []string{"No permission gate in this session."}}, nil
				}
				p := deps.Gate.Permissions()
				grants := deps.Gate.SessionGrants()
				lines := []string{
					fmt.Sprintf("mode       %s", deps.Gate.Mode()),
					fmt.Sprintf("settings   %s", joinOrNone(deps.SettingsLoadedFrom)),
					"",
					fmt.Sprintf("workspace  %s", strings.Join(deps.Gate.Roots(), "\n           ")),
				}
				addRules := func(label string, list []string) {
					if len(list) == 0 {
						return
					}
					lines = append(lines, "", fmt.Sprintf("%s (%d)", label, len(list)))
					for _, r := range list {
						lines = append(lines, "  "+r)
					}
				}
				// Deny first: it wins over everything else, matching how the
				// decision is actually made.
				addRules("deny", p.Deny)
				addRules("allow", p.Allow)
				addRules("ask", p.Ask)
				if len(grants) > 0 {
					lines = append(lines, "", fmt.Sprintf("granted this session (%d) - not saved to settings", len(grants)))
					for _, g := range grants {
						lines = append(lines, "  "+g)
					}
				}
				return Result{Output: lines}, nil
			},
		},
		{
			Name:        "hooks",
			Description: "Show configured hooks and which events they fire on",
			Run: func(ctx context.Context, args string) (Result, error) {
				var configured []hooks.Event
				for _, e := range hookEventOrder {
					if len(deps.Hooks[e]) > 0 {
						configured = append(configured, e)
					}
				}
				if len(configured) == 0 {
					return Result{Output: []string{"No hooks configured. They are read from .claude/settings.json"}}, nil
				}
				var lines []string
				for _, event := range configured {
					lines = append(lines, string(event))
					for _, group := range deps.Hooks[event] {
						matcher := ""
						if group.MatcherPattern != "" {
							matcher = fmt.Sprintf("[%s] ", group.MatcherPattern)
						}
						for _, h := range group.Hooks {
							timeout := ""
							if h.Timeout > 0 {
								timeout = fmt.Sprintf(" (%ds)", h.Timeout)
							}
							lines = append(lines, fmt.Sprintf("  %s%s%s", matcher, truncate(h.Command, 100), timeout))
						}
					}
					lines = append(lines, "")
				}
				for len(lines) > 0 && lines[len(lines)-1] == "" {
					lines = lines[:len(lines)-1]
				}
				return Result{Output: lines}, nil
			},
		},
		{
			Name:        "doctor",
			Description: "Check the setup and report anything broken",
			Run: func(ctx context.Context, args string) (Result, error) {
				lines := []string{
					fmt.Sprintf("model      %s", deps.ModelLabel),
					"tier       " + TierSummary(deps.Tier),
				}

				var active []string
				if deps.ActiveTools != nil {
					var err error
					active, err = deps.ActiveTools()
					if err != nil {
						return Result{}, err
					}
				}
				lines = append(lines, "tools      "+ToolsSummary(len(active), deps.Tier))

				var statuses []ServerStatus
				if deps.MCPStatuses != nil {
					statuses = deps.MCPStatuses()
				}
				var failed []ServerStatus
				connected, connecting := 0, 0
				for _, s := range statuses {
					switch {
					case s.Connecting:
						connecting++
					case s.OK:
						connected++
					default:
						failed = append(failed, s)
					}
				}
				mcpLine := fmt.Sprintf("mcp        %d/%d connected", connected, len(statuses))
				if connecting > 0 {
					mcpLine += fmt.Sprintf(", %d connecting", connecting)
				}
				lines = append(lines, mcpLine)

				total, eventsUsed := hookCount(deps.Hooks)
				lines = append(lines, "hooks      "+HooksSummary(total, eventsUsed))
				lines = append(lines, fmt.Sprintf("agents     %d available", len(deps.Agents)))
				sandboxLine := deps.SandboxLine
				if sandboxLine == "" {
					sandboxLine = "off (sandbox.enabled is not set)"
				}
				lines = append(lines, "sandbox    "+sandboxLine)
				lines = append(lines, fmt.Sprintf("settings   %s", joinOrNone(deps.SettingsLoadedFrom)))

				var problems []string
				for _, s := range failed {
					problems = append(problems, fmt.Sprintf(`mcp "%s" is down: %s`, s.Name, truncate(orDefault(s.Error, "unknown"), 100)))
				}
				if len(active) == 0 {
					problems = append(problems, "no tools are available: the model cannot act")
				}
				if deps.Gate != nil && deps.Gate.Mode() == settings.ModeBypassPermissions {
					problems = append(problems, "permission mode is bypassPermissions: every tool call runs unchecked")
				}
				if unsupported := hooks.UnsupportedEvents(deps.Hooks); len(unsupported) > 0 {
					names := make([]string, len(unsupported))
					for i, e := range unsupported {
						names[i] = string(e)
					}
					problems = append(problems, fmt.Sprintf("hooks configured for %s kiln does not fire (yet): %s", plural.Count(len(unsupported), "event"), strings.Join(names, ", ")))
				}
				problems = append(problems, deps.SandboxProblems...)

				lines = append(lines, "")
				if len(problems) == 0 {
					lines = append(lines, "No problems found.")
				} else {
					lines = append(lines, plural.Count(len(problems), "problem")+":")
				}
				for _, p := range problems {
					lines = append(lines, "  - "+p)
				}
				return Result{Output: lines}, nil
			},
		},
	}

	return StaticSource(OriginBuiltin, cmds)
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// TierSummary, ToolsSummary and HooksSummary are the doctor report's rows,
// shared by /doctor, `kiln doctor` and the /config dialog so their wording
// cannot drift apart.
func TierSummary(t budget.Tier) string {
	return fmt.Sprintf("%s · %s window", t.Name, formatTokens(t.ContextWindow))
}

func ToolsSummary(available int, t budget.Tier) string {
	return fmt.Sprintf("%s always available · %s", plural.Count(available, "tool"), t.ToolStrategy.Describe())
}

func HooksSummary(hooks, events int) string {
	if hooks == 0 {
		return "none"
	}
	return plural.Count(hooks, "hook") + " on " + plural.Count(events, "event")
}
