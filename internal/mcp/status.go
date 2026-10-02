package mcp

import (
	"fmt"
	"sort"
	"strings"
)

// truncate mirrors inspect-commands.ts's local truncate helper: text longer
// than max is cut and given an ellipsis, counted in runes for the "…"
// itself as inspect-commands.ts's does with `text.length <= max`.
func truncate(text string, max int) string {
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	return string(r[:max-1]) + "…"
}

// RenderMCPReport renders the `/mcp` command's output, ported from
// inspect-commands.ts: a summary line, then connected servers sorted by
// tool count descending, then failed servers with their error truncated to
// 90 runes.
func RenderMCPReport(statuses []ServerStatus) string {
	if len(statuses) == 0 {
		return "No MCP servers configured. Add one with kiln mcp add — it writes ~/.claude.json/.mcp.json, the same files claude mcp add uses"
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

	sorted := append([]ServerStatus(nil), ok...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ToolCount > sorted[j].ToolCount })

	lines := []string{fmt.Sprintf("%d/%d connected, %d tools", len(ok), len(statuses), tools), ""}
	for _, s := range sorted {
		lines = append(lines, fmt.Sprintf("  %-22s %4d tools  %dms", s.Name, s.ToolCount, s.Ms))
	}
	for _, s := range failed {
		errText := s.Error
		if errText == "" {
			errText = "unknown"
		}
		lines = append(lines, fmt.Sprintf("  %-22s failed: %s", s.Name, truncate(errText, 90)))
	}
	return strings.Join(lines, "\n")
}

// RenderToolsReport renders the `/tools` command's output, ported from
// builtins.ts: the resident tool count followed by one name per line.
func RenderToolsReport(active []string) string {
	lines := append([]string{fmt.Sprintf("%d resident:", len(active))}, mapIndent(active)...)
	return strings.Join(lines, "\n")
}

func mapIndent(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "  " + n
	}
	return out
}
