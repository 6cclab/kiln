package cli

import (
	"context"
	"strings"

	claudeagents "github.com/andrepato/harness/internal/claude/agents"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tools"
)

// capabilityCatalog reads the current MCP catalog at search time so a
// background connection or posture switch is visible on the next call.
// skills must already be filtered for model invocation and exclusions.
func capabilityCatalog(skills func() []tools.SkillRecord, session *mcpSession) tools.CapabilityCatalogFunc {
	return capabilityCatalogWithAgents(skills, nil, false, session)
}

// capabilityCatalogWithAgents includes only the definitions registered with
// task. Disabled discovery omits them without changing task's permissions.
func capabilityCatalogWithAgents(skills func() []tools.SkillRecord, defs []claudeagents.Definition, includeAgents bool, session *mcpSession) tools.CapabilityCatalogFunc {
	return func() tools.CapabilityCatalog {
		session.mu.Lock()
		mcpTools := append(session.tools[:0:0], session.tools...)
		posture := session.posture
		strategy := session.tier.ToolStrategy
		state := session.state
		session.mu.Unlock()
		catalog := tools.CapabilityCatalog{
			Skills: skills(), MCP: mcpTools,
			Scope: mcpgate.IndexScope(posture, strategy), State: state,
		}
		if includeAgents {
			catalog.Agents = append([]claudeagents.Definition(nil), defs...)
		}
		return catalog
	}
}

// capabilitySearchBudget reads the session tier rather than retaining the
// startup tier. A /model switch may change the discovery allowance.
func capabilitySearchBudget(config claudesettings.SkillDiscovery, session *mcpSession) func() int {
	return func() int {
		session.mu.Lock()
		allowance := session.tier.SkillsListingTokens
		session.mu.Unlock()
		return config.Budget(allowance)
	}
}

// capabilitySuggestionContext adds bounded metadata matches to the current
// user turn. It does not admit MCP tools; explicit capability_search does.
func capabilitySuggestionContext(catalog tools.CapabilityCatalogFunc, config claudesettings.SkillDiscovery, budget func() int) func(context.Context, []msg.Message) ([]msg.Message, error) {
	return func(ctx context.Context, transcript []msg.Message) ([]msg.Message, error) {
		if config.Mode == "search" || config.Mode == "catalog" {
			return transcript, nil
		}
		for i := len(transcript) - 1; i >= 0; i-- {
			user, ok := transcript[i].(msg.UserMessage)
			if !ok || user.KilnHook != "" {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			query := user.KilnTyped
			if query == "" {
				query = msg.TextOf(user.Content)
			}
			if strings.TrimSpace(query) == "" {
				return transcript, nil
			}
			const prefix = "<capability-suggestions>\n"
			const suffix = "Use skill to load instructions; use capability_search to enable MCP tools or find more.\n</capability-suggestions>"
			maxBytes := budget()*4 - len(prefix) - len(suffix)
			if maxBytes <= 0 {
				return transcript, nil
			}
			c := catalog()
			var b strings.Builder
			seen := map[string]bool{}
			// Pins come first, but only when still present in the filtered,
			// model-invocable skill catalog.
			for _, name := range config.Pinned {
				for _, s := range c.Skills {
					key := "skill\x00" + name
					if s.Name != name || seen[key] {
						continue
					}
					line := tools.CapabilitySuggestionLine(tools.CapabilityHit{Kind: "skill", Name: s.Name, Description: s.Description, Action: tools.SkillCapabilityAction(s.Name), Reason: "pinned"})
					if b.Len()+len(line) <= maxBytes {
						b.WriteString(line)
					}
					seen[key] = true
				}
			}
			for _, h := range tools.SearchCapabilities(c, query, 5) {
				if seen[h.Kind+"\x00"+h.Name] {
					continue
				}
				line := tools.CapabilitySuggestionLine(h)
				if b.Len()+len(line) <= maxBytes {
					b.WriteString(line)
				}
			}
			if b.Len() == 0 {
				return transcript, nil
			}
			out := append([]msg.Message(nil), transcript...)
			user.Content = append(append(msg.Blocks(nil), user.Content...), msg.Text(prefix+b.String()+suffix))
			if user.KilnTyped == "" {
				user.KilnTyped = query
			}
			out[i] = user
			return out, nil
		}
		return transcript, nil
	}
}
