package commands

import (
	"context"
	"fmt"
	"sort"

	claudeplugins "github.com/andrepato/harness/internal/claude/plugins"
)

// PluginReportCommand builds /plugin: a report of every active plugin
// (installed, enabled, and — for a project-scope install — applicable to
// cwd) with what it contributed, plus installed-but-disabled ones, and a
// pointer at Claude Code's own /plugin for actually installing/enabling
// one — kiln reads the same installed_plugins.json and settings.json
// files Claude Code writes, but does not itself manage them.
//
// active is the plugin set already resolved for this session
// (internal/cli/chat.go's activePlugins, loaded once at session start,
// matching how deps.Agents/deps.Skills are threaded through); the
// installed-but-disabled section re-reads cwd's installed_plugins.json on
// every invocation instead, so a plugin enabled or disabled mid-session
// shows up correctly on the next /plugin without restarting.
func PluginReportCommand(cwd string, active []claudeplugins.Plugin) Source {
	return StaticSource(OriginBuiltin, []Command{
		{
			Name:        "plugin",
			Description: "Report active Claude Code plugins and what they contributed",
			Run: func(ctx context.Context, args string) (Result, error) {
				return Result{Output: renderPluginReport(cwd, active)}, nil
			},
		},
	})
}

func renderPluginReport(cwd string, active []claudeplugins.Plugin) []string {
	var lines []string
	if len(active) == 0 {
		lines = append(lines, "No active plugins.")
	} else {
		lines = append(lines, fmt.Sprintf("%d active plugin(s):", len(active)))
		for _, p := range active {
			lines = append(lines, fmt.Sprintf("  %s@%s  v%s  (%s)", p.Name, p.Marketplace, p.Version, p.Scope))
			var contributions []string
			if n := len(claudeplugins.Skills(p)); n > 0 {
				contributions = append(contributions, fmt.Sprintf("%d skill(s)", n))
			}
			if n := len(claudeplugins.Commands(p)); n > 0 {
				contributions = append(contributions, fmt.Sprintf("%d command(s)", n))
			}
			if n := len(claudeplugins.Agents(p)); n > 0 {
				contributions = append(contributions, fmt.Sprintf("%d agent(s)", n))
			}
			hookCount := 0
			for _, groups := range claudeplugins.Hooks(p) {
				for _, g := range groups {
					hookCount += len(g.Hooks)
				}
			}
			if hookCount > 0 {
				contributions = append(contributions, fmt.Sprintf("%d hook(s)", hookCount))
			}
			if n := len(claudeplugins.MCPServers(p)); n > 0 {
				contributions = append(contributions, fmt.Sprintf("%d MCP server(s)", n))
			}
			if len(contributions) == 0 {
				lines = append(lines, "    (contributes nothing kiln loads)")
			} else {
				for _, c := range contributions {
					lines = append(lines, "    "+c)
				}
			}
		}
	}

	var disabled []claudeplugins.Installed
	for _, ins := range claudeplugins.ListInstalled(cwd) {
		if !ins.Enabled || !ins.Applicable {
			disabled = append(disabled, ins)
		}
	}
	if len(disabled) > 0 {
		sort.Slice(disabled, func(i, j int) bool { return disabled[i].Key < disabled[j].Key })
		lines = append(lines, "", fmt.Sprintf("%d installed but not active:", len(disabled)))
		for _, ins := range disabled {
			why := "disabled"
			if ins.Enabled && !ins.Applicable {
				why = "project-scope install outside this directory"
			}
			lines = append(lines, fmt.Sprintf("  %s@%s  v%s  (%s, %s)", ins.Name, ins.Marketplace, ins.Version, ins.Scope, why))
		}
	}

	lines = append(lines, "", "Installs and enabled/disabled state are managed with Claude Code's own /plugin — kiln reads the same files, it does not write them.")
	return lines
}
