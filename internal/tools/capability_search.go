package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/tool"
)

// CapabilityCatalog is a snapshot of model-invocable skills and custom
// commands, dispatchable agents, and connected MCP tools. Callers filter
// unavailable entries first. Custom commands use skill records because Claude
// Code treats legacy commands as skills.
type CapabilityCatalog struct {
	Skills []SkillRecord
	Agents []agents.Definition
	MCP    []mcp.McpTool
	Scope  mcp.Posture
	State  *mcp.GateState
}

// CapabilityCatalogFunc supplies a fresh snapshot for each search. In
// particular, MCP servers may finish connecting after the session starts.
type CapabilityCatalogFunc func() CapabilityCatalog

// CapabilityHit is a ranked match. Action names the exact invocation the
// model can make; Reason identifies the query terms that matched metadata.
type CapabilityHit struct {
	Kind        string
	Name        string
	Description string
	Action      string
	Reason      string
	score       int
}

func capabilityTerms(query string) []string {
	parts := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	seen := make(map[string]bool, len(parts))
	terms := make([]string, 0, len(parts))
	for _, p := range parts {
		if !seen[p] {
			seen[p] = true
			terms = append(terms, p)
		}
	}
	return terms
}

func capabilityScore(name, description string, terms []string) (int, string) {
	name, description = strings.ToLower(name), strings.ToLower(description)
	var score int
	var matched []string
	for _, term := range terms {
		stem := strings.TrimSuffix(term, "s")
		if stem == "" {
			stem = term
		}
		part := 0
		switch {
		case name == term || name == stem:
			part = 10
		case strings.Contains(name, stem):
			part = 4
		}
		if strings.Contains(description, stem) {
			part++
		}
		if part > 0 {
			score += part
			matched = append(matched, term)
		}
	}
	return score, strings.Join(matched, ", ")
}

// SkillCapabilityAction formats a skill invocation with JSON-safe arguments.
func SkillCapabilityAction(name string) string {
	args, err := json.Marshal(struct {
		Skill string `json:"skill"`
	}{Skill: name})
	if err != nil {
		return ""
	}
	return "skill(" + string(args) + ")"
}

func agentCapabilityAction(name string) string {
	args, err := json.Marshal(struct {
		SubagentType string `json:"subagent_type"`
		Description  string `json:"description"`
		Prompt       string `json:"prompt"`
	}{SubagentType: name, Description: "Brief task label", Prompt: "Specific task instructions"})
	if err != nil {
		return ""
	}
	return "task(" + string(args) + ")"
}

// SearchCapabilities ranks skill, agent and MCP metadata together. It never
// reads skill bodies, agent prompts or MCP schemas, and only includes MCP
// tools in the active scope.
func SearchCapabilities(c CapabilityCatalog, query string, limit int) []CapabilityHit {
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	terms := capabilityTerms(query)
	var hits []CapabilityHit
	// The skill tool resolves the last record with a qualified name. Search
	// must describe that same record when catalogs contain duplicates.
	lastSkills := make(map[string]int, len(c.Skills))
	for i, s := range c.Skills {
		lastSkills[s.Name] = i
	}
	for i, s := range c.Skills {
		if lastSkills[s.Name] != i {
			continue
		}
		score, reason := capabilityScore(s.Name, s.Description, terms)
		if len(terms) > 0 && score == 0 {
			continue
		}
		hits = append(hits, CapabilityHit{Kind: "skill", Name: s.Name, Description: s.Description, Action: SkillCapabilityAction(s.Name), Reason: reason, score: score})
	}
	// task's dispatcher resolves the first definition for a name. Search
	// must describe that same definition when catalogs contain duplicates.
	seenAgents := make(map[string]bool, len(c.Agents))
	for _, a := range c.Agents {
		if seenAgents[a.Name] {
			continue
		}
		seenAgents[a.Name] = true
		score, reason := capabilityScore(a.Name, a.Description, terms)
		if len(terms) > 0 && score == 0 {
			continue
		}
		// The prompt is a placeholder, not the agent definition's own
		// instructions. Dispatch remains subject to task's normal controls.
		action := agentCapabilityAction(a.Name)
		hits = append(hits, CapabilityHit{Kind: "agent", Name: a.Name, Description: a.Description, Action: action, Reason: reason, score: score})
	}
	for _, t := range c.MCP {
		if !mcp.InPosture(t, c.Scope) {
			continue
		}
		score, reason := capabilityScore(t.Name, t.Description, terms)
		if len(terms) > 0 && score == 0 {
			continue
		}
		hits = append(hits, CapabilityHit{Kind: "mcp", Name: t.QualifiedName, Description: t.Description, Action: "call " + t.QualifiedName, Reason: reason, score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].Name != hits[j].Name {
			return hits[i].Name < hits[j].Name
		}
		return hits[i].Kind < hits[j].Kind
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func capabilityLine(h CapabilityHit) string {
	desc := firstSentence(h.Description)
	if desc == "" {
		desc = "No description provided."
	}
	reason := h.Reason
	if reason == "" {
		reason = "catalog browse"
	}
	return fmt.Sprintf("- %s [%s]: %s Match: %s. Action: %s\n", h.Name, h.Kind, desc, reason, h.Action)
}

// CapabilitySuggestionLine renders one bounded metadata match for a prompt
// suggestion. MCP actions still require an explicit search for admission.
func CapabilitySuggestionLine(h CapabilityHit) string {
	if h.Kind == "mcp" {
		h.Action = "capability_search to enable " + h.Name
	}
	return capabilityLine(h)
}

// CapabilitySearchTool returns a bounded discovery tool with a fixed budget.
// Use CapabilitySearchToolDynamic when the model tier can change in-session.
func CapabilitySearchTool(catalog CapabilityCatalogFunc, budget int, onAdmit OnAdmit) *tool.Tool {
	return CapabilitySearchToolDynamic(catalog, func() int { return budget }, onAdmit)
}

// CapabilitySearchToolDynamic reads the current discovery budget per call.
// MCP matches that actually fit it are admitted using the existing gate state.
func CapabilitySearchToolDynamic(catalog CapabilityCatalogFunc, budget func() int, onAdmit OnAdmit) *tool.Tool {
	return &tool.Tool{
		Name: "capability_search", Label: "Search capabilities",
		Description: "Search installed skills, custom commands, dispatchable agents, and available MCP tools together by topic. Returns reasons and exact actions. Matching MCP tools become callable after search; use skill to load instructions or task to dispatch an agent.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"maxResults":{"type":"integer","description":"Maximum matches, up to 10"}},"required":["query"]}`),
		Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			if err := ctx.Err(); err != nil {
				return tool.Result{}, err
			}
			var args toolSearchArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return tool.Result{}, fmt.Errorf("capability_search: decoding arguments: %w", err)
			}
			limit := 5
			if args.MaxResults != nil && *args.MaxResults > 0 && *args.MaxResults <= 10 {
				limit = *args.MaxResults
			}
			c := catalog()
			hits := SearchCapabilities(c, args.Query, limit)
			maxBytes := budget() * 4
			if maxBytes < 0 {
				maxBytes = 0
			}
			var b strings.Builder
			var admitted []string
			for _, h := range hits {
				line := capabilityLine(h)
				if b.Len()+len(line) > maxBytes {
					continue
				}
				b.WriteString(line)
				if h.Kind == "mcp" && c.State != nil && !c.State.IsAdmitted(h.Name) {
					admitted = append(admitted, h.Name)
				}
			}
			if b.Len() == 0 {
				message := "No matching capabilities fit the discovery budget or active MCP posture. Try a narrower query or switch posture."
				if len(message) > maxBytes {
					message = message[:maxBytes]
				}
				return tool.Text(message), nil
			}
			for _, name := range admitted {
				c.State.Admit(name)
			}
			if len(admitted) > 0 && onAdmit != nil {
				if err := onAdmit(ctx, admitted); err != nil {
					return tool.Result{}, fmt.Errorf("capability_search: admitting tools: %w", err)
				}
			}
			return tool.Text(b.String()), nil
		},
	}
}
