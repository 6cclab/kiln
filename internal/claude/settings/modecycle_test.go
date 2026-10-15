package settings

import "testing"

// TestNextPermissionMode_MatchesClaudeCode: one step from every mode, as
// Claude Code's cycle takes it. dontAsk leaves to manual, not auto.
func TestNextPermissionMode_MatchesClaudeCode(t *testing.T) {
	for cur, want := range map[PermissionMode]PermissionMode{
		ModeManual:            ModeAcceptEdits,
		ModeAcceptEdits:       ModePlan,
		ModePlan:              ModeAuto,
		ModeAuto:              ModeManual,
		ModeBypassPermissions: ModeAuto,
		ModeDontAsk:           ModeManual,
		"":                    ModeManual,
	} {
		if got := NextPermissionMode(cur); got != want {
			t.Errorf("NextPermissionMode(%q) = %q, want %q", cur, got, want)
		}
	}
}
