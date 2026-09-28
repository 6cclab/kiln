// Package cli, this file: MCP wiring — connecting the hub, tool gating
// (postures, tool_search, resident tools) and the re-gating that a model
// switch or a /posture change both need. Ported from harness/src/cli.ts's
// MCP block (roughly lines 180-348) and its onModelChanges re-gating
// (lines 686-704).
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/provider"
)

// residentAll is gating.ts's own RESIDENT list, kept as a documented
// constant even though residentToolNames (below) may still drop
// "session_search" when this process has no session index open.
//
// As of phase 6 every name here names a real tool: "exit_plan_mode",
// "task", "bash_background", "bash_output" and "kill_shell" all exist now
// (internal/tools/planmode.go, task.go, backgroundshell.go), so the phase
// 5 residentPhase6Only filter that used to hide them is gone.
var residentAll = []string{
	"bash", "read", "edit", "write",
	"session_search",
	"todo_write",
	"exit_plan_mode",
	"task",
	"bash_background",
	"bash_output",
	"web_fetch",
	"kill_shell",
	"skill",
	"web_search",
}

// residentToolNames filters residentAll down to what this run actually
// has: "session_search" drops when hasSessionSearch is false
// (internal/search failed to open — see chat.go's own comment on that
// check); "web_search" drops when webSearchAllowed is false (a permission
// deny rule matched "WebSearch" — see chat.go's construction of that
// bool). Dropping it here, from the resident/active list, is what keeps it
// from ever being declared to Anthropic for this session: the tool is
// still registered in the tool set (defensive Execute), just never
// offered.
// webSearchAllowed reports whether the resolved permission deny list
// blocks declaring web_search: true unless a deny rule matches Claude
// Code's name for it, "WebSearch" (case-insensitively, the same as any
// other bare-tool-name deny rule -- see settings.MatchesRule). A deny rule
// is the only thing checked here: plan mode is fine with the tool declared
// since it is read-only, and settings.Decide's other mode branches are not
// consulted for declaration, only for whether a call may execute.
func webSearchAllowed(perms claudesettings.Permissions) bool {
	for _, r := range perms.Deny {
		if claudesettings.MatchesRule(r, "WebSearch", "") {
			return false
		}
	}
	return true
}

func residentToolNames(hasSessionSearch, webSearchAllowed bool) []string {
	out := make([]string, 0, len(residentAll))
	for _, name := range residentAll {
		if name == "session_search" && !hasSessionSearch {
			continue
		}
		if name == "web_search" && !webSearchAllowed {
			continue
		}
		out = append(out, name)
	}
	return out
}

// defaultPostureName is cli.ts's own default when HARNESS_POSTURE is unset.
const defaultPostureName = "coding"

// resolvePosture reads HARNESS_POSTURE (default "coding") and resolves it
// against mcp.Postures, falling back to the first posture (matching
// cli.ts's `posture(...) ?? POSTURES[0]`) when the name is unknown.
func resolvePosture() mcpgate.Posture {
	name := os.Getenv("HARNESS_POSTURE")
	if name == "" {
		name = defaultPostureName
	}
	if p, ok := mcpgate.PostureByName(name); ok {
		return p
	}
	return mcpgate.Postures[0]
}

// warnFailedServers writes cli.ts's own connect warning for every server
// that failed to connect: "mcp: <name> unavailable — <error>", the error
// truncated to 100 runes (cli.ts: `s.error?.slice(0, 100)`).
func warnFailedServers(stderr io.Writer, statuses []mcpgate.ServerStatus) {
	for _, s := range statuses {
		if s.OK {
			continue
		}
		fmt.Fprintf(stderr, "mcp: %s unavailable — %s\n", s.Name, truncateRunes100(s.Error))
	}
}

func truncateRunes100(s string) string {
	r := []rune(s)
	if len(r) <= 100 {
		return s
	}
	return string(r[:100])
}

// scopedMCPTools filters tools to the ones active's posture makes
// searchable, mirroring cli.ts's `mcpTools.filter((t) => inPosture(t, activePosture))`.
func scopedMCPTools(tools []mcpgate.McpTool, active mcpgate.Posture) []mcpgate.McpTool {
	var out []mcpgate.McpTool
	for _, t := range tools {
		if mcpgate.InPosture(t, active) {
			out = append(out, t)
		}
	}
	return out
}

// mcpSession bundles everything that changes together across the
// session's life: a model switch changes tier (and so the tool strategy),
// a tool_search admission or a /posture switch changes state/posture, and
// either one means the lane's active tools need recomputing. Keeping them
// on one struct is what lets onAdmit, /posture and /model all call the
// same regate() rather than three copies of activeToolNames' argument
// list drifting apart.
type mcpSession struct {
	// mu guards tools and tier: the background MCP connect writes them
	// from its own goroutine while tool_search's admit and /model's switch
	// read them from turn and command goroutines.
	mu       sync.Mutex
	tools    []mcpgate.McpTool
	posture  mcpgate.Posture
	state    *mcpgate.GateState
	resident []string
	tier     budget.Tier
	// lane is nil until agent.Start returns; tool_search's onAdmit can only
	// ever fire once a turn is running, by which point lane is always set —
	// same as cli.ts's `session` variable, captured by closure before it is
	// assigned.
	lane laneSetter
	// rebuild regenerates what is built from the posture — tool_search's
	// scope and gate state, and the server index in the system prompt —
	// after a /posture switch. Nil until the session wires it.
	rebuild func()
}

// laneSetter is the minimal surface mcpSession.regate needs from
// *harness.Lane, kept as an interface so this file does not need to import
// internal/harness just to name the concrete type in a field.
type laneSetter interface {
	SetActiveTools(names []string) error
}

func (m *mcpSession) activeToolNames() []string {
	m.mu.Lock()
	tools, strategy := m.tools, m.tier.ToolStrategy
	m.mu.Unlock()
	return mcpgate.ActiveToolNames(tools, m.indexScope(), strategy, m.state, m.resident)
}

// indexScope is the posture that scopes the index, tool_search and
// admission under the current tier's strategy (mcpgate.IndexScope): the
// configured posture on posture-index, every server on the full
// strategies. Read fresh each time because /model can change the tier.
func (m *mcpSession) indexScope() mcpgate.Posture {
	m.mu.Lock()
	strategy := m.tier.ToolStrategy
	m.mu.Unlock()
	return mcpgate.IndexScope(m.posture, strategy)
}

// regate recomputes the active tool set and pushes it onto the lane. A nil
// lane (not started yet) is a silent no-op rather than an error: tool
// gating cannot matter before there is a lane to gate.
func (m *mcpSession) regate() error {
	if m.lane == nil {
		return nil
	}
	return m.lane.SetActiveTools(m.activeToolNames())
}

// setTools replaces the catalog the gate works over.
func (m *mcpSession) setTools(tools []mcpgate.McpTool) {
	m.mu.Lock()
	m.tools = tools
	m.mu.Unlock()
}

// switchModel applies a full model switch: resolve, move the lane's model
// and the harness's compaction settings (agent.SetModel), then re-gate the
// tool set for the new tier's strategy. Used by /model's SwitchModel dep.
func switchModel(ctx context.Context, reg *provider.Registry, started *agent.Started, mcpSess *mcpSession, providerID, modelID string) (budget.Tier, error) {
	if p, ok := reg.Provider(providerID); ok {
		if err := p.RefreshModels(ctx); err != nil {
			// Non-fatal: Resolve below fails on its own if the model truly
			// is not known, same reasoning as chat.go's initial resolve.
			_ = err
		}
	}
	resolved, err := reg.Resolve(providerID, modelID)
	if err != nil {
		return budget.Tier{}, err
	}
	if err := agent.SetModel(ctx, started, resolved); err != nil {
		return budget.Tier{}, err
	}
	mcpSess.mu.Lock()
	mcpSess.tier = resolved.Tier
	mcpSess.mu.Unlock()
	if err := mcpSess.regate(); err != nil {
		return budget.Tier{}, err
	}
	return resolved.Tier, nil
}

// switchPosture applies a /posture switch: admitted tools are cleared
// (they were chosen under the old posture's assumptions) and the lane is
// re-gated under the new posture, mirroring cli.ts's inline /posture
// handler.
func switchPosture(mcpSess *mcpSession, name string) error {
	p, ok := mcpgate.PostureByName(name)
	if !ok {
		return fmt.Errorf("unknown posture %q", name)
	}
	mcpSess.posture = p
	mcpSess.state = mcpgate.NewGateState()
	// tool_search and the prompt's server index were built for the old
	// posture and hold the old gate state: without a rebuild, a search
	// after the switch saw the old posture's servers and admitted tools
	// into a state nothing read.
	if mcpSess.rebuild != nil {
		mcpSess.rebuild()
	}
	return mcpSess.regate()
}
