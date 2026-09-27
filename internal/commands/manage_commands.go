package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/writesettings"
)

// modes is the cycle order /permissions' "m" action steps through.
var modes = []settings.PermissionMode{
	settings.ModeManual,
	settings.ModeAcceptEdits,
	settings.ModeAuto,
	settings.ModePlan,
	settings.ModeDontAsk,
	settings.ModeBypassPermissions,
}

// ManageGate is the subset of permission.Gate that the manage panels act
// on. It extends InspectGate with the mutation SetMode needs; rule
// mutation itself goes through ManageDeps.SaveRule/RemoveRule so both the
// in-memory gate and .claude/settings.local.json move together.
type ManageGate interface {
	InspectGate
	SetMode(settings.PermissionMode)
}

// MCPTool names one tool an MCP server exposes, for /mcp's "t" action.
type MCPTool struct {
	Name   string
	Server string
}

// ManageDeps is everything manageCommands binds against.
type ManageDeps struct {
	Gate               ManageGate
	MCPStatuses        func() []ServerStatus
	MCPTools           func() []MCPTool
	Hooks              hooks.Config
	Agents             []agents.Definition
	Cwd                string
	ModelLabel         string
	SettingsLoadedFrom []string
	// MCPConfigPath is the mcpServers config file /mcp's "User MCPs
	// (<path>)" section header names (~/.claude.json, or --mcp-config's
	// value). Empty renders the header without a path — the integrator
	// has not wired this yet; see the handback report.
	MCPConfigPath string

	// SaveRule persists a rule to .claude/settings.local.json AND applies
	// it to the in-memory gate: in memory so the next tool call obeys it,
	// on disk so it survives a restart. The integrator implements it with
	// gate.AddRule + writesettings.AddRule.
	SaveRule func(list writesettings.RuleList, rule string) error
	// RemoveRule is SaveRule's inverse (gate.RemoveRule + writesettings.RemoveRule).
	RemoveRule func(list writesettings.RuleList, rule string) error
}

func ruleListOf(list string) writesettings.RuleList {
	switch list {
	case "deny":
		return writesettings.Deny
	case "ask":
		return writesettings.Ask
	default:
		return writesettings.Allow
	}
}

func permissionsText(deps ManageDeps) []string {
	p := deps.Gate.Permissions()
	lines := []string{
		fmt.Sprintf("mode       %s", deps.Gate.Mode()),
		fmt.Sprintf("workspace  %s", strings.Join(deps.Gate.Roots(), ", ")),
		"",
	}
	for _, entry := range []struct {
		name string
		list []string
	}{{"deny", p.Deny}, {"allow", p.Allow}, {"ask", p.Ask}} {
		if len(entry.list) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s (%d)", entry.name, len(entry.list)))
		for _, r := range entry.list {
			lines = append(lines, "  "+r)
		}
	}
	return lines
}

func permissionsModal(deps ManageDeps) *ModalSpec {
	p := deps.Gate.Permissions()
	var items []Item
	for _, entry := range []struct {
		name string
		list []string
	}{{"deny", p.Deny}, {"allow", p.Allow}, {"ask", p.Ask}} {
		for _, rule := range entry.list {
			items = append(items, Item{Value: entry.name + ":" + rule, Label: rule, Description: entry.name})
		}
	}
	for _, grant := range deps.Gate.SessionGrants() {
		items = append(items, Item{Value: "session:" + grant, Label: grant, Description: "session only"})
	}

	return &ModalSpec{
		Title: "Permissions",
		Kind:  "permissions",
		Header: []string{
			fmt.Sprintf("mode       %s", deps.Gate.Mode()),
			fmt.Sprintf("workspace  %s", strings.Join(deps.Gate.Roots(), ", ")),
			fmt.Sprintf("settings   %s", joinOrNone(deps.SettingsLoadedFrom)),
		},
		Items: items,
		Actions: []Action{
			{Key: "m", Label: "cycle mode"},
			{Key: "d", Label: "delete rule"},
			{Key: "p", Label: "promote to deny"},
		},
		Act: func(key, value string) (string, error) {
			switch key {
			case "m":
				next := modes[(indexOfMode(deps.Gate.Mode())+1)%len(modes)]
				deps.Gate.SetMode(next)
				return fmt.Sprintf("mode is now %s", next), nil
			case "d":
				list, rule, ok := splitRule(value)
				if !ok {
					return "", nil
				}
				if list == "session" {
					return "session grants clear when the session ends; nothing to delete", nil
				}
				if deps.RemoveRule != nil {
					if err := deps.RemoveRule(ruleListOf(list), rule); err != nil {
						return "", err
					}
				}
				return fmt.Sprintf("removed %s from %s", rule, list), nil
			case "p":
				_, rule, ok := splitRule(value)
				if !ok {
					return "", nil
				}
				if deps.SaveRule != nil {
					if err := deps.SaveRule(writesettings.Deny, rule); err != nil {
						return "", err
					}
				}
				return fmt.Sprintf("%s is now denied", rule), nil
			}
			return "", nil
		},
	}
}

func indexOfMode(m settings.PermissionMode) int {
	for i, x := range modes {
		if x == m {
			return i
		}
	}
	return -1
}

func splitRule(value string) (list, rule string, ok bool) {
	i := strings.Index(value, ":")
	if i < 0 {
		return "", "", false
	}
	return value[:i], value[i+1:], true
}

func mcpStatusesText(statuses []ServerStatus) []string {
	var ok []ServerStatus
	tools := 0
	for _, s := range statuses {
		if s.OK {
			ok = append(ok, s)
			tools += s.ToolCount
		}
	}
	lines := []string{fmt.Sprintf("%d/%d connected, %d tools", len(ok), len(statuses), tools), ""}
	for _, s := range statuses {
		if s.OK {
			lines = append(lines, fmt.Sprintf("  %-22s %4d tools  %dms", s.Name, s.ToolCount, s.Ms))
		} else {
			lines = append(lines, fmt.Sprintf("  %-22s failed: %s", s.Name, truncate(orDefault(s.Error, "unknown"), 90)))
		}
	}
	return lines
}

// mcpModal builds /mcp's dialog spec (commands.ModalSpec.Kind == "mcp",
// rendered by internal/tui's dialogMCP — see that file's doc comment).
//
// The harness reads MCP servers from exactly one flat source
// (~/.claude.json, or --mcp-config's file: see internal/mcp/config.go),
// unlike Claude Code's own /mcp, which sections servers into "User MCPs",
// a per-connector "claude.ai" group and "Built-in MCPs". Reproducing
// those extra sections is not possible from the harness's actual state —
// there is no connector-origin metadata anywhere in internal/mcp — so
// this builds exactly one "User MCPs (<path>)" section from
// deps.MCPConfigPath (empty renders the header with no path). It also
// never emits ⚠ (needs authentication) or ◯ (disabled): the harness's
// mcp.ServerStatus has no such states (see internal/mcp/hub.go — OK is
// the only signal), so every server is either ✔ (OK) or ✘ (failed); a
// failed server's marker carries no trailing text, matching
// dialog-mcp.txt's plain ✘ rows (its ⚠ rows are the ones with the "needs
// authentication" suffix, which the harness cannot distinguish).
func mcpModal(deps ManageDeps) *ModalSpec {
	var statuses []ServerStatus
	if deps.MCPStatuses != nil {
		statuses = deps.MCPStatuses()
	}
	sorted := append([]ServerStatus{}, statuses...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var toolsByServer map[string][]string
	if deps.MCPTools != nil {
		toolsByServer = make(map[string][]string)
		for _, t := range deps.MCPTools() {
			toolsByServer[t.Server] = append(toolsByServer[t.Server], t.Name)
		}
	}

	group := "User MCPs"
	if deps.MCPConfigPath != "" {
		group = fmt.Sprintf("User MCPs (%s)", deps.MCPConfigPath)
	}

	items := make([]Item, 0, len(sorted))
	for _, s := range sorted {
		it := Item{
			Value: s.Name,
			Label: s.Name,
			Group: group,
			Ms:    s.Ms,
			Tools: toolsByServer[s.Name],
		}
		if s.OK {
			it.Marker = "✔"
			it.Description = fmt.Sprintf("%d tools", s.ToolCount)
		} else {
			it.Marker = "✘"
			it.Error = s.Error
			it.Detail = s.Detail
		}
		items = append(items, it)
	}

	return &ModalSpec{
		Title: "Manage MCP servers",
		Kind:  "mcp",
		// Header is unused today: dialogMCP (internal/tui/dialog_mcp.go)
		// renders its own list, not commandDialog's generic Header/Items
		// loop, since Kind == "mcp" routes to it instead. Kept plural-
		// correct anyway (pluralServers-equivalent) for whichever future
		// consumer reads ModalSpec.Header directly (print mode, a test).
		Header: []string{mcpServerCount(len(statuses))},
		Items:  items,
	}
}

// mcpServerCount renders "N server"/"N servers".
func mcpServerCount(n int) string {
	if n == 1 {
		return "1 server"
	}
	return fmt.Sprintf("%d servers", n)
}

func agentsModal(deps ManageDeps) *ModalSpec {
	items := make([]Item, 0, len(deps.Agents))
	for _, a := range deps.Agents {
		model := a.Model
		if model == "" {
			model = "inherit"
		}
		tools := "all tools"
		if a.Tools != nil {
			tools = fmt.Sprintf("%d tools", len(a.Tools))
		}
		items = append(items, Item{Value: a.Name, Label: a.Name, Description: fmt.Sprintf("%s · %s · %s", model, tools, truncate(a.Description, 60))})
	}
	return &ModalSpec{
		Title:  "Subagents",
		Kind:   "agents",
		Header: []string{"dispatched with the task tool · defined in .claude/agents"},
		Items:  items,
		Actions: []Action{
			{Key: "s", Label: "show prompt"},
			{Key: "w", Label: "where defined"},
		},
		Act: func(key, value string) (string, error) {
			var found *agents.Definition
			for i := range deps.Agents {
				if deps.Agents[i].Name == value {
					found = &deps.Agents[i]
					break
				}
			}
			if found == nil {
				return "", nil
			}
			switch key {
			case "s":
				return truncate(collapseSpace(found.Prompt), 400), nil
			case "w":
				return found.Path, nil
			}
			return "", nil
		},
	}
}

func configModal(deps ManageDeps) *ModalSpec {
	total, eventsUsed := hookCount(deps.Hooks)
	p := deps.Gate.Permissions()
	var mcpOK, mcpTotal int
	if deps.MCPStatuses != nil {
		statuses := deps.MCPStatuses()
		mcpTotal = len(statuses)
		for _, s := range statuses {
			if s.OK {
				mcpOK++
			}
		}
	}
	items := []Item{
		{Value: "model", Label: "model", Description: deps.ModelLabel},
		{Value: "mode", Label: "permission mode", Description: string(deps.Gate.Mode())},
		{Value: "workspace", Label: "workspace roots", Description: strings.Join(deps.Gate.Roots(), ", ")},
		{Value: "settings", Label: "settings loaded from", Description: joinOrNone(deps.SettingsLoadedFrom)},
		{Value: "rules", Label: "permission rules", Description: fmt.Sprintf("%d allow · %d deny · %d ask", len(p.Allow), len(p.Deny), len(p.Ask))},
		{Value: "hooks", Label: "hooks", Description: fmt.Sprintf("%d across %d events", total, eventsUsed)},
		{Value: "agents", Label: "subagents", Description: fmt.Sprintf("%d available", len(deps.Agents))},
		{Value: "mcp", Label: "mcp servers", Description: fmt.Sprintf("%d/%d connected", mcpOK, mcpTotal)},
		{Value: "cwd", Label: "working directory", Description: deps.Cwd},
	}
	return &ModalSpec{
		Title:  "Configuration",
		Kind:   "config",
		Header: []string{"read-only; each line says where the value came from"},
		Items:  items,
	}
}

// ManageCommands returns the source for /permissions, /mcp, /agents and
// /config: the four that MANAGE rather than report. Each returns both a
// ModalSpec and the text summary, so print mode (harness -p "/mcp") stays
// useful in a script.
//
// Registered after InspectCommands in cli.ts's order, so these shadow
// InspectCommands' /permissions and /mcp, and BuiltinCommands' /agents,
// via qualified-name precedence in Registry.List.
func ManageCommands(deps ManageDeps) Source {
	screens := []struct {
		name string
		desc string
		spec func() *ModalSpec
		text func() []string
	}{
		{"permissions", "View and edit permission rules", func() *ModalSpec { return permissionsModal(deps) }, func() []string { return permissionsText(deps) }},
		{"mcp", "View MCP servers and their tools", func() *ModalSpec { return mcpModal(deps) }, func() []string {
			var statuses []ServerStatus
			if deps.MCPStatuses != nil {
				statuses = deps.MCPStatuses()
			}
			return mcpStatusesText(statuses)
		}},
		{"agents", "Manage subagents", func() *ModalSpec { return agentsModal(deps) }, func() []string {
			if len(deps.Agents) == 0 {
				return []string{"No subagents. Define them in .claude/agents/*.md"}
			}
			lines := []string{fmt.Sprintf("%d available:", len(deps.Agents))}
			for _, a := range deps.Agents {
				model := ""
				if a.Model != "" {
					model = fmt.Sprintf(" (%s)", a.Model)
				}
				lines = append(lines, fmt.Sprintf("  %s%s\n    %s", a.Name, model, a.Description))
			}
			return lines
		}},
		{"config", "Show settings and where they came from", func() *ModalSpec { return configModal(deps) }, func() []string {
			return []string{
				fmt.Sprintf("model      %s", deps.ModelLabel),
				fmt.Sprintf("mode       %s", deps.Gate.Mode()),
				fmt.Sprintf("workspace  %s", strings.Join(deps.Gate.Roots(), ", ")),
				fmt.Sprintf("settings   %s", joinOrNone(deps.SettingsLoadedFrom)),
				fmt.Sprintf("agents     %d", len(deps.Agents)),
			}
		}},
	}

	cmds := make([]Command, 0, len(screens))
	for _, s := range screens {
		s := s
		cmds = append(cmds, Command{
			Name:        s.name,
			Description: s.desc,
			Run: func(ctx context.Context, args string) (Result, error) {
				return Result{Modal: s.spec(), Output: s.text()}, nil
			},
		})
	}
	return StaticSource(OriginBuiltin, cmds)
}
