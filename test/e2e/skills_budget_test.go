//go:build e2e

// Package e2e: the model-visible skills catalog's budgeting
// (internal/cli's formatSkillsIndex, internal/budget's
// SkillsListingTokens), driven through the real binary. Verifies the
// promise behind the fix for the skills-catalog token cost: a skill that
// does not fit the budgeted listing is never actually unreachable - the
// model can still invoke it by its exact name through the `skill` tool,
// and the user can still invoke it by `/name`, even though neither of
// them ever sees it in the <available_skills> index.
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeOverflowSkills writes n project-scope skills under proj/.claude/
// skills, named "overflow-00".."overflow-<n-1>", each with a description
// long enough that not all of them fit a small-tier budget
// (internal/budget's SkillsListingTokens, ~328 tokens at a 32,768-token
// window) with full descriptions, nor even bare (name + location only).
// Each skill's body carries a distinctive, grep-able marker so a test can
// tell "this skill's content actually loaded" apart from "the model
// guessed similar text".
func writeOverflowSkills(t *testing.T, proj string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("overflow-%02d", i)
		dir := filepath.Join(proj, ".claude", "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`---
name: %s
description: "This is skill number %02d of a deliberately large overflow catalog built to exceed the small-tier skills listing budget, so some of these entries must be truncated or pushed off the listing entirely while staying invocable by exact name."
---
Body of %s. distinctive-marker-ovf-%02d.
`, name, i, name, i)
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// skillCallScript scripts a single turn that calls the `skill` tool for
// skill, then answers once it sees the tool result.
func skillCallScript(model, skill string) string {
	return fmt.Sprintf(`model: %s
steps:
  - tool_call: {name: skill, args: {skill: %q}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`, model, skill)
}

// TestSkillsBudget_OverflowedSkillStillInvocableByModel is the e2e half
// of the skills-listing budget fix: with a small enough tier (faux-2,
// 32,768 tokens) and enough skills to exceed SkillsListingTokens even
// bare, the lowest-priority skill ("overflow-19", last in load order)
// must be left off the <available_skills> index - but the `skill` tool
// must still run it when the model asks for it by its exact name, and
// the index must still name it in a trailing pointer line rather than
// silently losing it.
func TestSkillsBudget_OverflowedSkillStillInvocableByModel(t *testing.T) {
	const skillCount = 20
	const overflowed = "overflow-19"

	addr, srv := startFaux(t, skillCallScript("faux-2", overflowed))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeOverflowSkills(t, proj, skillCount)

	env := baseEnv(home, sessDir, addr)
	env["HARNESS_MODEL"] = "faux/faux-2" // the small-tier faux model: 32,768 tokens

	res := runHarness(t, proj, env, "-p", "run the overflow skill")
	if res.Code != 0 {
		t.Fatalf("exit %d\nstdout=%s\nstderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "done") {
		t.Fatalf("stdout = %q, want the scripted final reply", res.Stdout)
	}

	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	first := reqs[0]

	if strings.Contains(first.System, "<name>"+overflowed+"</name>") {
		t.Errorf("%s should have overflowed a %d-skill catalog at the small tier's budget, but is listed in the index:\n%s", overflowed, skillCount, first.System)
	}
	if !strings.Contains(first.System, overflowed) {
		t.Errorf("an overflowed skill must still be named (in the pointer line), found no mention of %s at all:\n%s", overflowed, first.System)
	}
	if !strings.Contains(first.System, "skill tool") {
		t.Errorf("the pointer line must say an overflowed skill is still reachable by the skill tool:\n%s", first.System)
	}

	// The follow-up request (after the skill tool ran) carries the
	// overflowed skill's own body back to the model - proving it is
	// genuinely invocable, not merely named in the pointer line.
	last := reqs[len(reqs)-1]
	if !strings.Contains(string(last.Messages), "distinctive-marker-ovf-19") {
		t.Errorf("tool result missing the overflowed skill's body:\n%s", string(last.Messages))
	}
}

// TestSkillsBudget_OverflowedSkillStillInvocableByUser is this fix's
// other half: a user typing /overflow-19 must still run it, exactly as
// if it had a place in the listing, because the slash palette
// (internal/commands, driven here through print mode's "-p '/name'"
// path) resolves user-invocable skills by name directly against
// internal/claude/skills.LoadSkills's full set - the same budgeted
// index that hides it from the model never reaches that path at all.
func TestSkillsBudget_OverflowedSkillStillInvocableByUser(t *testing.T) {
	const skillCount = 20
	const overflowed = "overflow-19"

	// This script never runs a tool; it only needs to answer whatever
	// prompt print mode hands it once /overflow-19 resolves to the
	// skill's own content as the model's prompt (chat.go's runPrintMode:
	// "a command whose Result carries a Prompt... becomes the model's
	// prompt instead of the original text").
	addr, srv := startFaux(t, "model: faux-2\nsteps:\n  - text: \"done\"\n")
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeOverflowSkills(t, proj, skillCount)

	env := baseEnv(home, sessDir, addr)
	env["HARNESS_MODEL"] = "faux/faux-2"

	res := runHarness(t, proj, env, "-p", "/"+overflowed)
	if res.Code != 0 {
		t.Fatalf("exit %d\nstdout=%s\nstderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "done") {
		t.Fatalf("stdout = %q, want the scripted final reply", res.Stdout)
	}

	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	first := reqs[0]

	// Confirm this run really did overflow the listing the same way the
	// model-invocation test does, so a pass here cannot be explained by
	// the skill having a place in the index after all.
	if strings.Contains(first.System, "<name>"+overflowed+"</name>") {
		t.Fatalf("test setup: %s was not overflowed, so this test proves nothing:\n%s", overflowed, first.System)
	}

	if !strings.Contains(string(first.Messages), "distinctive-marker-ovf-19") {
		t.Errorf("the user's /%s did not resolve to the overflowed skill's own content:\n%s", overflowed, string(first.Messages))
	}
}
