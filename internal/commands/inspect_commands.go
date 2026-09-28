package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/settings"
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
	Scope     string
	OK        bool
	ToolCount int
	Ms        int64
	Error     string
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

// defaultUnfiredHookEvents are the events the harness parses but does not
// yet fire. Saying so beats implying otherwise.
var defaultUnfiredHookEvents = map[hooks.Event]bool{
	hooks.Stop:         true,
	hooks.SubagentStop: true,
	hooks.Notification: true,
	hooks.PreCompact:   true,
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
	// UnfiredEvents overrides defaultUnfiredHookEvents when non-nil.
	UnfiredEvents map[hooks.Event]bool
	Agents        []agents.Definition
	Tier          budget.Tier
	Cwd           string
	ModelLabel    string
	// ActiveTools returns the resident tool names, for /doctor's summary.
	ActiveTools        func() ([]string, error)
	SettingsLoadedFrom []string
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
	unfired := deps.UnfiredEvents
	if unfired == nil {
		unfired = defaultUnfiredHookEvents
	}

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
					return Result{Output: []string{"No MCP servers configured. They are read from ~/.claude.json"}}, nil
				}
				var ok, failed []ServerStatus
				tools := 0
				for _, s := range statuses {
					if s.OK {
						ok = append(ok, s)
						tools += s.ToolCount
					} else {
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
					note := ""
					if unfired[event] {
						note = "  (parsed, not yet fired by this harness)"
					}
					lines = append(lines, fmt.Sprintf("%s%s", event, note))
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
					fmt.Sprintf("tier       %s (%d tokens)", deps.Tier.Name, deps.Tier.ContextWindow),
				}

				var active []string
				if deps.ActiveTools != nil {
					var err error
					active, err = deps.ActiveTools()
					if err != nil {
						return Result{}, err
					}
				}
				lines = append(lines, fmt.Sprintf(`tools      %d resident, strategy "%s"`, len(active), deps.Tier.ToolStrategy))

				var statuses []ServerStatus
				if deps.MCPStatuses != nil {
					statuses = deps.MCPStatuses()
				}
				var failed []ServerStatus
				for _, s := range statuses {
					if !s.OK {
						failed = append(failed, s)
					}
				}
				lines = append(lines, fmt.Sprintf("mcp        %d/%d connected", len(statuses)-len(failed), len(statuses)))

				total, eventsUsed := hookCount(deps.Hooks)
				lines = append(lines, fmt.Sprintf("hooks      %d across %d events", total, eventsUsed))
				lines = append(lines, fmt.Sprintf("agents     %d available", len(deps.Agents)))
				lines = append(lines, fmt.Sprintf("settings   %s", joinOrNone(deps.SettingsLoadedFrom)))

				var problems []string
				for _, s := range failed {
					problems = append(problems, fmt.Sprintf(`mcp "%s" is down: %s`, s.Name, truncate(orDefault(s.Error, "unknown"), 100)))
				}
				if len(active) == 0 {
					problems = append(problems, "no tools are resident - the model cannot act")
				}
				if deps.Gate != nil && deps.Gate.Mode() == settings.ModeBypassPermissions {
					problems = append(problems, "permission mode is bypassPermissions: every tool call runs unchecked")
				}

				lines = append(lines, "")
				if len(problems) == 0 {
					lines = append(lines, "No problems found.")
				} else {
					lines = append(lines, fmt.Sprintf("%d problem(s):", len(problems)))
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
