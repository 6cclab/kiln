package settings

import "testing"

// TestPlanModeShellRegularFlow: in plan mode a read-only shell command
// runs, and any other goes through the regular permission flow — deny,
// ask, allow, and with no rule a prompt, never a refusal (Claude Code's
// permissions doc: "any other shell command goes through the regular
// permission flow while you are still planning"). Edits stay refused
// whatever the rules say.
func TestPlanModeShellRegularFlow(t *testing.T) {
	newPathFixture(t)
	cases := []struct {
		name, tool, arg string
		p               Permissions
		want            Decision
	}{
		{"no rule", "bash", "mkdir build", Permissions{}, Ask},
		{"bare Bash allow", "bash", "rm -rf build", Permissions{Allow: []string{"Bash"}}, Allow},
		{"Bash(*) allow", "bash", "npm install", Permissions{Allow: []string{"Bash(*)"}}, Allow},
		{"prefix allow", "bash", "npm test", Permissions{Allow: []string{"Bash(npm test)"}}, Allow},
		{"prefix allow, other command", "bash", "npm test && touch x", Permissions{Allow: []string{"Bash(npm test)"}}, Ask},
		{"background, no rule", "bash_background", "npm run dev", Permissions{}, Ask},
		{"background, Bash rule", "bash_background", "npm run dev", Permissions{Allow: []string{"Bash(npm run *)"}}, Allow},
		{"background, read-only", "bash_background", "ls", Permissions{}, Allow},
		{"deny wins", "bash", "rm -rf build", Permissions{Deny: []string{"Bash(rm *)"}, Allow: []string{"Bash"}}, Deny},
		{"ask beats allow", "bash", "npm test", Permissions{Ask: []string{"Bash(npm *)"}, Allow: []string{"Bash(npm test)"}}, Ask},
		{"read-only", "bash", "cat a.go && ls", Permissions{}, Allow},
		{"read-only, ask rule", "bash", "git log", Permissions{Ask: []string{"Bash(git log*)"}}, Ask},
		{"edit, allowed", "edit", "a.go", Permissions{Allow: []string{"Edit"}}, Deny},
		{"write, no rule", "write", "a.go", Permissions{}, Deny},
	}
	for _, c := range cases {
		if got := Decide(c.p, c.tool, c.arg, ModePlan); got != c.want {
			t.Errorf("%s: %s(%q) = %v, want %v", c.name, c.tool, c.arg, got, c.want)
		}
	}
	if !PlanOverridesAllow("edit", "a.go") || !PlanOverridesAllow("write", "a.go") {
		t.Error("PlanOverridesAllow misses an edit tool")
	}
	if PlanOverridesAllow("bash", "npm test") || PlanOverridesAllow("bash_background", "ls") || PlanOverridesAllow("read", "a.go") {
		t.Error("PlanOverridesAllow covers a call that takes the regular flow")
	}
}
