package mcp

import (
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/budget"
)

// Tool gating — the whole point of the Phase 0 measurement this ports.
//
// Measured on the real catalog (165 tools across 9 servers, 2026-09-22)
// against a 32,768-token window:
//
//	full schemas, all 165      31,897 tokens   97% of the window
//	index, all 165              7,597 tokens   23%
//	index, coding posture       1,286 tokens    4%
//
// The plan assumed indexing would be nearly free. It is not — 23% of the
// window is a lot. The 6x saving comes from Layer 0 simply not indexing
// servers that are irrelevant to the task, which is why postures do more
// work here than the dynamic search does. Ported from gating.ts.

// Posture is a static, free filter over which servers may be indexed at
// all. A coding session has no business carrying UniFi or occupational
// therapy tools, and no runtime machinery is needed to know that. Borrowed
// from Hermes' toolsets.py.
type Posture struct {
	Name        string
	Description string
	// Servers whose tools may be indexed at all. "*" means every server.
	Servers []string
}

// Postures are the built-in postures, ported verbatim from gating.ts.
var Postures = []Posture{
	{
		Name:        "coding",
		Description: "Code, deployments and observability. The default.",
		Servers:     []string{"infisical", "argocd-mcp", "personal-kb", "homelab-kb", "claude-relay", "sentry", "github"},
	},
	{
		Name:        "ops",
		Description: "Infrastructure and monitoring.",
		Servers:     []string{"grafana", "proxmox", "argocd-mcp", "unifi-mcp", "pocket-id", "infisical"},
	},
	{
		Name:        "all",
		Description: "Every configured server. Expensive on a small context window.",
		Servers:     []string{"*"},
	},
}

// PostureByName looks up a posture by name.
func PostureByName(name string) (Posture, bool) {
	for _, p := range Postures {
		if p.Name == name {
			return p, true
		}
	}
	return Posture{}, false
}

// InPosture reports whether tool belongs to active's servers.
//
// A server that no built-in posture names at all is in scope for every
// posture: the lists exist to keep known, noisy servers (UniFi, Grafana)
// out of a coding session, not to hide a server the user just configured
// until they discover /posture. Without this rule a new server was
// indexed by nothing, tool_search could never admit it, and the model's
// call was refused.
func InPosture(t McpTool, active Posture) bool {
	for _, s := range active.Servers {
		if s == "*" || s == t.Server {
			return true
		}
	}
	return !knownServer(t.Server)
}

// knownServer reports whether any built-in posture names server.
func knownServer(server string) bool {
	for _, p := range Postures {
		for _, s := range p.Servers {
			if s == server {
				return true
			}
		}
	}
	return false
}

// BuildIndex renders one "qualifiedName: description" line per tool.
// Descriptions are first-line only, truncated to 160 runes: MCP
// descriptions routinely run to paragraphs, and the index exists precisely
// to avoid paying for them.
func BuildIndex(tools []McpTool) string {
	lines := make([]string, len(tools))
	for i, t := range tools {
		desc := t.Description
		if nl := strings.IndexByte(desc, '\n'); nl >= 0 {
			desc = desc[:nl]
		}
		if len(desc) > 160 {
			desc = desc[:160]
		}
		lines[i] = fmt.Sprintf("%s: %s", t.QualifiedName, desc)
	}
	return strings.Join(lines, "\n")
}

// IndexPromptText wraps BuildIndex's output the way cli.ts's system prompt
// assembly does, or returns "" when there is nothing to index (mirroring
// the `scoped.length ? ... : ""` guard in cli.ts — an empty posture must
// not spend tokens announcing an empty index).
func IndexPromptText(tools []McpTool) string {
	if len(tools) == 0 {
		return ""
	}
	return fmt.Sprintf(
		"Additional tools are available but not loaded. Call tool_search to enable any you need.\n\n<available_tools>\n%s\n</available_tools>",
		BuildIndex(tools),
	)
}

// Score is the relevance of a tool to a set of query terms.
//
// Flat substring matching scored every tool containing "dashboard"
// identically, so a search for "list dashboards" returned
// `update_dashboard` and `alerting_manage_silences` while missing
// `search_dashboards` — the tie was broken by catalog order. Weighting name
// over description fixes that: a term in the tool's own name is a far
// stronger signal than one buried in prose.
func Score(t McpTool, terms []string) int {
	name := strings.ToLower(t.Name)
	description := strings.ToLower(t.Description)

	total := 0
	for _, term := range terms {
		// Singular/plural is the common case for these queries ("dashboards"
		// vs the tool's "dashboard"), and is not worth a stemmer.
		stem := strings.TrimSuffix(term, "s")
		switch {
		case name == term || name == stem:
			total += 10
		case strings.Contains(name, stem):
			total += 4
		}
		if strings.Contains(description, stem) {
			total += 1
		}
	}
	return total
}

// GateState is a session's tool_search admission state: tools admitted for
// the rest of the session, by qualified name. Mirrors gating.ts's
// GateState, whose `admitted` is a JS Set — insertion-ordered, and iterated
// in that order by `[...state.admitted]` in activeToolNames. Not safe for
// concurrent use without external synchronization, same as that Set.
type GateState struct {
	Admitted map[string]struct{}
	order    []string
}

// NewGateState builds an empty GateState.
func NewGateState() *GateState {
	return &GateState{Admitted: map[string]struct{}{}}
}

// Admit adds name to the admitted set. Re-admitting an already-admitted
// name is a no-op, matching Set.add's idempotence and its insertion-order
// semantics (a re-add does not move the entry).
func (s *GateState) Admit(name string) {
	if _, ok := s.Admitted[name]; ok {
		return
	}
	s.Admitted[name] = struct{}{}
	s.order = append(s.order, name)
}

// IsAdmitted reports whether name has been admitted.
func (s *GateState) IsAdmitted(name string) bool {
	_, ok := s.Admitted[name]
	return ok
}

// AdmittedNames returns the admitted names in admission order.
func (s *GateState) AdmittedNames() []string {
	return append([]string(nil), s.order...)
}

// ActiveToolNames returns which MCP tools should be active, given the
// tier's strategy. Mirrors gating.ts's activeToolNames.
//
// full-schemas skips gating entirely — on a 200k window the whole catalog
// is ~16% and the round-trip through search costs more than it saves.
// posture-index and full-index both start gated; they differ in how much
// of the catalog the index describes, which is handled by IndexPromptText,
// not here.
func ActiveToolNames(tools []McpTool, posture Posture, strategy budget.ToolStrategy, state *GateState, resident []string) []string {
	out := append([]string(nil), resident...)

	if strategy == budget.StrategyFullSchemas {
		for _, t := range tools {
			if InPosture(t, posture) {
				out = append(out, t.QualifiedName)
			}
		}
		return out
	}

	out = append(out, "tool_search")
	out = append(out, state.AdmittedNames()...)
	return out
}
