package settings

import "testing"

// TestPlanModeShellAsks: in plan mode a shell command that is not
// read-only asks whatever allow rules say (Claude Code without its plan
// classifier: "commands outside the built-in read-only set prompt for
// approval"); edits stay refused; read-only commands, ask rules and deny
// rules behave as in every mode.
func TestPlanModeShellAsks(t *testing.T) {
	newPathFixture(t)
	cases := []struct {
		name, tool, arg string
		p               Permissions
		want            Decision
	}{
		{"no rule", "bash", "mkdir build", Permissions{}, Ask},
		{"bare Bash allow", "bash", "rm -rf build", Permissions{Allow: []string{"Bash"}}, Ask},
		{"Bash(*) allow", "bash", "npm install", Permissions{Allow: []string{"Bash(*)"}}, Ask},
		{"prefix allow", "bash", "npm test", Permissions{Allow: []string{"Bash(npm test)"}}, Ask},
		{"background, no rule", "bash_background", "npm run dev", Permissions{}, Ask},
		{"background allowed", "bash_background", "npm run dev", Permissions{Allow: []string{"bash_background"}}, Ask},
		{"deny wins", "bash", "rm -rf build", Permissions{Deny: []string{"Bash(rm *)"}, Allow: []string{"Bash"}}, Deny},
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
	for _, c := range []struct{ tool, arg string }{{"bash", "npm test"}, {"bash_background", "ls"}, {"edit", "a.go"}} {
		if !PlanOverridesAllow(c.tool, c.arg) {
			t.Errorf("PlanOverridesAllow(%s, %q) = false", c.tool, c.arg)
		}
	}
	if PlanOverridesAllow("bash", "git status") || PlanOverridesAllow("read", "a.go") {
		t.Error("PlanOverridesAllow covers a read-only call")
	}
}
