package settings

import "testing"

// An MCP rule names a server, or one tool of it, never a string prefix
// (Claude Code's toolMatchesRule: the server names must be equal, and the
// tool part absent or "*"). A prefix match let "mcp__homelab" allow every
// tool of a different server, "homelab-kb", including ones that delete.
func TestMatchesRuleMCPServerExact(t *testing.T) {
	cases := []struct {
		rule, tool string
		want       bool
	}{
		{"mcp__homelab", "mcp__homelab__list", true},
		{"mcp__homelab", "mcp__homelab-kb__hk_search", false},
		{"mcp__homelab", "mcp__homelab-kb__hk_delete_entry", false},
		{"mcp__homelab", "mcp__homelabx__list", false},
		{"mcp__homelab__*", "mcp__homelab__list", true},
		{"mcp__homelab__*", "mcp__homelab-kb__list", false},
		{"mcp__pocket-id__user_get", "mcp__pocket-id__user_get", true},
		{"mcp__pocket-id__user_get", "mcp__pocket-id__user_get_all", false},
		{"mcp__pocket-id__user_get", "mcp__pocket-id__user_delete", false},
		{"mcp__pocket-id__user", "mcp__pocket-id__user_get", false},
		{"mcp__a__b__c", "mcp__a__b__c", true},
		// A rule for another server's tool, or a plain tool name, never
		// covers an MCP tool, whatever underscores the names share.
		{"mcp__a_b__c", "mcp__ab__c", false},
		{"mcpabc", "mcp__a__bc", false},
		{"Read", "mcp__fs__read", false},
		{"Edit", "mcp__fs__edit", false},
		{"mcp__", "mcp__x__y", false},
		{"mcp__a_b__c(x)", "mcp__ab__c", false},
	}
	for _, c := range cases {
		if got := MatchesRule(c.rule, c.tool, ""); got != c.want {
			t.Errorf("MatchesRule(%q, %q) = %v, want %v", c.rule, c.tool, got, c.want)
		}
	}
	// The same holds where the gate decides: a bare server rule allows
	// none of a server whose name merely starts with it.
	p := Permissions{Allow: []string{"mcp__homelab"}}
	if got := Decide(p, "mcp__homelab-kb__hk_delete_entry", "", ModeAcceptEdits); got != Ask {
		t.Errorf("Decide(allow mcp__homelab, mcp__homelab-kb tool) = %v, want ask", got)
	}
	if got := Decide(p, "mcp__homelab__list", "", ModeAcceptEdits); got != Allow {
		t.Errorf("Decide(allow mcp__homelab, its own tool) = %v, want allow", got)
	}
	if got := Decide(Permissions{Deny: []string{"mcp__homelab"}}, "mcp__homelab__list", "", ModeBypassPermissions); got != Deny {
		t.Errorf("deny mcp__homelab on its own tool = %v, want deny", got)
	}
}
