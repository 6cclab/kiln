package cli

import (
	"testing"

	claudesettings "github.com/andrepato/harness/internal/claude/settings"
)

// TestWebSearchAllowed_DenyRuleSuppresses guards that a permission deny
// rule for "WebSearch" (Claude Code's name for the tool) is what keeps
// web_search out of residentToolNames -- the mechanism the design calls
// for ("if a deny rule matches WebSearch, do not declare the tool"),
// exercised here as chat.go/subcommands.go actually call it: through
// webSearchAllowed feeding residentToolNames.
func TestWebSearchAllowed_DenyRuleSuppresses(t *testing.T) {
	cases := []struct {
		name string
		deny []string
		want bool
	}{
		{"no rules", nil, true},
		{"unrelated deny", []string{"Bash(rm:*)"}, true},
		{"exact deny", []string{"WebSearch"}, false},
		// MatchesRule lower-cases both sides for a bare rule, so this must
		// match case-insensitively the same as every other bare tool-name
		// deny rule (Bash, Read, Edit, ...).
		{"case-insensitive deny", []string{"websearch"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			perms := claudesettings.Permissions{Deny: tc.deny}
			if got := webSearchAllowed(perms); got != tc.want {
				t.Fatalf("webSearchAllowed(deny=%v) = %v, want %v", tc.deny, got, tc.want)
			}
		})
	}
}

// TestResidentToolNames_WebSearch guards that residentToolNames drops
// "web_search" exactly when told to, alongside the pre-existing
// "session_search" gating, and otherwise includes it.
func TestResidentToolNames_WebSearch(t *testing.T) {
	has := func(names []string, want string) bool {
		for _, n := range names {
			if n == want {
				return true
			}
		}
		return false
	}

	allowed := residentToolNames(true, true)
	if !has(allowed, "web_search") {
		t.Fatalf("residentToolNames(true, true) = %v, want it to include web_search", allowed)
	}

	denied := residentToolNames(true, false)
	if has(denied, "web_search") {
		t.Fatalf("residentToolNames(true, false) = %v, want it to omit web_search", denied)
	}
	// The deny only removes web_search; every other resident tool stays.
	if !has(denied, "bash") || !has(denied, "read") {
		t.Fatalf("residentToolNames(true, false) = %v, want the rest of residentAll intact", denied)
	}
}
