package settings

import (
	"path/filepath"
	"strings"
	"testing"
)

// Tests of the API that Read/Edit path rules added (RuleSource, DecideIn,
// FileRuleWarnings, filePathVerdicts). pathrules_test.go holds the ones
// written against the older API.

// TestFileRuleWarnings: every Write/MultiEdit/NotebookEdit/Glob path rule
// is reported, naming the replacement and what kiln does with it; Read,
// Edit and bare tool rules are not.
func TestFileRuleWarnings(t *testing.T) {
	p := Permissions{
		Allow: []string{"Write(docs/**)", "Edit(src/**)", "Write", "Bash(git *)"},
		Deny:  []string{"MultiEdit(secrets/**)", "Glob(private/**)", "Read(.env)"},
		Ask:   []string{"NotebookEdit(nb/**)"},
	}
	got := FileRuleWarnings(p)
	want := []string{
		"Permission rule Write(docs/**) in allow is not matched by file permission checks (kiln ignores it); use Edit(docs/**) instead.",
		"Permission rule MultiEdit(secrets/**) in deny is not matched by file permission checks (kiln applies it as Edit(secrets/**)); use Edit(secrets/**) instead.",
		"Permission rule Glob(private/**) in deny is not matched by file permission checks (kiln applies it as Read(private/**)); use Read(private/**) instead.",
		"Permission rule NotebookEdit(nb/**) in ask is not matched by file permission checks (kiln applies it as Edit(nb/**)); use Edit(nb/**) instead.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestLoadSettings_RuleSources: each rule carries its file's scope and
// anchor, aligned with the merged lists.
func TestLoadSettings_RuleSources(t *testing.T) {
	f := newPathFixture(t)
	extra := filepath.Join(t.TempDir(), "extra.json")
	writeSettings(t, f.h(".claude/settings.json"), `{"permissions":{"allow":["Read(/a)"],"deny":["Read(/u1)","Read(/u2)"]}}`)
	writeSettings(t, f.p(".claude/settings.json"), `{"permissions":{"deny":["Read(/p)"],"ask":["Edit(/q)"]}}`)
	writeSettings(t, f.p(".claude/settings.local.json"), `{"permissions":{"deny":["Read(/l)"]}}`)
	writeSettings(t, extra, `{"permissions":{"deny":["Read(/x)"]}}`)
	p := LoadSettings(f.proj, LoadOptions{Extra: extra}).Permissions

	user := RuleSource{Scope: "user", File: f.h(".claude/settings.json"), Root: f.h(".claude")}
	project := RuleSource{Scope: "project", File: f.p(".claude/settings.json")}
	local := RuleSource{Scope: "local", File: f.p(".claude/settings.local.json")}
	extraSrc := RuleSource{Scope: "local", File: extra, Root: filepath.Dir(extra)}

	wantDeny := []RuleSource{user, user, project, local, extraSrc}
	if len(p.DenyFrom) != len(p.Deny) || len(p.DenyFrom) != len(wantDeny) {
		t.Fatalf("Deny=%v DenyFrom=%+v", p.Deny, p.DenyFrom)
	}
	for i := range wantDeny {
		if p.DenyFrom[i] != wantDeny[i] {
			t.Errorf("DenyFrom[%d] (%s) = %+v, want %+v", i, p.Deny[i], p.DenyFrom[i], wantDeny[i])
		}
	}
	if len(p.AllowFrom) != 1 || p.AllowFrom[0] != user {
		t.Errorf("AllowFrom = %+v", p.AllowFrom)
	}
	if len(p.AskFrom) != 1 || p.AskFrom[0] != project {
		t.Errorf("AskFrom = %+v", p.AskFrom)
	}
}

// TestDecideIn_UsesGivenCwd: the working directory passed in, not the
// process's, anchors relative rules and relative path arguments.
func TestDecideIn_UsesGivenCwd(t *testing.T) {
	newPathFixture(t) // the process cwd: a different directory
	proj := t.TempDir()
	p := Permissions{Deny: []string{"Read(/secrets/**)", "Read(.env)"}}
	if DecideIn(p, proj, "read", "secrets/k", ModeAuto) != Deny {
		t.Error("relative argument did not resolve against the given cwd")
	}
	if DecideIn(p, proj, "read", filepath.Join(proj, "a", ".env"), ModeAuto) != Deny {
		t.Error("relative rule did not anchor at the given cwd")
	}
}

// TestPathRules_CaseFolding: where the filesystem ignores case, a deny or
// ask rule ignores it too (".ENV" opens .env); an allow rule never does.
func TestPathRules_CaseFolding(t *testing.T) {
	f := newPathFixture(t)
	saved := foldCase
	t.Cleanup(func() { foldCase = saved })

	foldCase = true
	if !denies("Read(.env)", "read", f.p("a/.ENV")) {
		t.Error("Read(.env) deny missed a/.ENV")
	}
	if !denies("Edit(/Secrets/**)", "write", f.p("SECRETS/k")) {
		t.Error("Edit(/Secrets/**) deny missed SECRETS/k")
	}
	if !asks("Read(*.PEM)", "read", f.p("k.pem")) {
		t.Error("Read(*.PEM) ask missed k.pem")
	}
	if allows("Edit(src/**)", "edit", f.p("SRC/a.go")) {
		t.Error("an allow rule matched across case")
	}

	foldCase = false
	if denies("Read(.env)", "read", f.p("a/.ENV")) {
		t.Error("case folded with foldCase off")
	}
}

// TestFilePathVerdicts_ReadAllow covers the allow rows Decide cannot show
// for the read tool (read never asks): Read(src/**) allow is <cwd>/src
// only, and a symlink out of an allowed directory is not allowed.
func TestFilePathVerdicts_ReadAllow(t *testing.T) {
	f := newPathFixture(t)
	c := newMatchCtx(f.proj)
	allow := func(rule, path string) bool {
		_, _, a := filePathVerdicts(Permissions{Allow: []string{rule}}, c, "read", path)
		return a
	}
	if !allow("Read(src/**)", f.p("src/app.ts")) {
		t.Error("Read(src/**) allow did not cover <cwd>/src/app.ts")
	}
	if allow("Read(src/**)", f.p("vendor/pkg/src/lib.js")) {
		t.Error("Read(src/**) allow covered a nested src")
	}
	if !allow("Read(**/src/**)", f.p("vendor/pkg/src/lib.js")) {
		t.Error("Read(**/src/**) allow did not cover a nested src")
	}
	if allow("Glob(src/**)", f.p("src/app.ts")) {
		t.Error("a Glob(...) allow rule approved a read")
	}
	if !allow("Read(./.env)", f.p(".env")) || allow("Read(./.env)", f.p("sub/.env")) {
		t.Error("Read(./.env) allow must cover <cwd>/.env only")
	}
}
