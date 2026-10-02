package settings

import (
	"reflect"
	"testing"
)

func TestIsBroadAutoModeAllow(t *testing.T) {
	broad := []string{
		"Bash", "bash", "Bash()", "Bash(*)", "Bash( * )", "BashBackground", "bash_background(*)",
		"Bash(python*)", "Bash(python3:*)", "Bash(python -c *)", "Bash(node *)", "Bash(sh -c:*)",
		"Bash(npm run:*)", "Bash(npm run *)", "Bash(pnpm exec *)", "Bash(npx *)", "Bash(go run *)",
		"Bash(env *)", "Bash(xargs:*)", "Bash(sudo *)", "Bash(ssh *)", "Bash(eval *)",
		"Task", "Task(general-purpose)", "Agent", "Agent(*)",
	}
	for _, r := range broad {
		if !IsBroadAutoModeAllow(r) {
			t.Errorf("%q: want set aside in auto mode", r)
		}
	}
	narrow := []string{
		"Bash(npm test)", "Bash(npm run test)", "Bash(go test *)", "Bash(git status)", "Bash(shellcheck *)",
		"Bash(envsubst *)", "Bash(python scripts/check.py)", "Bash(make build)",
		"Read", "Edit(src/**)", "WebFetch(domain:example.com)", "mcp__db",
	}
	for _, r := range narrow {
		if IsBroadAutoModeAllow(r) {
			t.Errorf("%q: narrow rule set aside", r)
		}
	}
}

func TestWithoutBroadAutoModeAllows_KeepsSourcesAligned(t *testing.T) {
	a, b := RuleSource{File: "a"}, RuleSource{File: "b"}
	p := Permissions{
		Allow:     []string{"Bash(*)", "Bash(npm test)", "Bash(python:*)", "Read"},
		AllowFrom: []RuleSource{a, b, a, b},
		Deny:      []string{"Bash(*)"},
		Ask:       []string{"Task"},
	}
	got, aside := WithoutBroadAutoModeAllows(p)
	if !reflect.DeepEqual(got.Allow, []string{"Bash(npm test)", "Read"}) || !reflect.DeepEqual(got.AllowFrom, []RuleSource{b, b}) {
		t.Errorf("allow=%q from=%v", got.Allow, got.AllowFrom)
	}
	if !reflect.DeepEqual(aside, []string{"Bash(*)", "Bash(python:*)"}) {
		t.Errorf("aside = %q", aside)
	}
	if !reflect.DeepEqual(got.Deny, p.Deny) || !reflect.DeepEqual(got.Ask, p.Ask) {
		t.Error("deny and ask rules must be untouched")
	}
	if len(p.Allow) != 4 {
		t.Error("the input was modified")
	}
}
