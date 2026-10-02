package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/skills"
	"github.com/andrepato/harness/internal/tools"
)

// idxSkill builds a skills.Skill with a description long enough that
// truncation tests have real room to shrink it.
func idxSkill(name string, scope paths.Scope) skills.Skill {
	return skills.Skill{
		Name:        name,
		Description: fmt.Sprintf("The %s skill does a great many things: it handles scenario A, scenario B, and scenario C, each described here at some length so there is real room to truncate.", name),
		FilePath:    "/skills/" + name + "/SKILL.md",
		Scope:       scope,
	}
}

// TestFormatSkillsIndex_LargeBudgetNoChange: when everything fits with
// full descriptions, nothing is truncated and nothing is pointed at.
func TestFormatSkillsIndex_LargeBudgetNoChange(t *testing.T) {
	list := []skills.Skill{
		idxSkill("alpha", paths.ScopeProject),
		idxSkill("beta", paths.ScopeUser),
		idxSkill("gamma", paths.ScopePlugin),
	}
	got := formatSkillsIndex(list, 100_000)
	for _, s := range list {
		if !strings.Contains(got, s.Description) {
			t.Errorf("full description for %s missing at a large budget: %q", s.Name, got)
		}
	}
	if strings.Contains(got, "not listed here") {
		t.Errorf("a large budget must not produce a pointer line: %q", got)
	}
}

// TestFormatSkillsIndex_Order: project/local skills come first, then
// user, then plugin, regardless of input order.
func TestFormatSkillsIndex_Order(t *testing.T) {
	list := []skills.Skill{
		idxSkill("plug", paths.ScopePlugin),
		idxSkill("proj", paths.ScopeProject),
		idxSkill("usr", paths.ScopeUser),
	}
	got := formatSkillsIndex(list, 100_000)
	iProj := strings.Index(got, "<name>proj</name>")
	iUsr := strings.Index(got, "<name>usr</name>")
	iPlug := strings.Index(got, "<name>plug</name>")
	if !(iProj < iUsr && iUsr < iPlug) {
		t.Errorf("want project < user < plugin ordering, got positions proj=%d usr=%d plug=%d:\n%s", iProj, iUsr, iPlug, got)
	}
}

// TestFormatSkillsIndex_TruncatesDescriptionsWhenOverBudget: a budget
// that can't hold every full description, but can hold every skill bare,
// shrinks descriptions rather than dropping any skill.
func TestFormatSkillsIndex_TruncatesDescriptionsWhenOverBudget(t *testing.T) {
	list := []skills.Skill{
		idxSkill("alpha", paths.ScopeProject),
		idxSkill("beta", paths.ScopeUser),
		idxSkill("gamma", paths.ScopePlugin),
	}
	fullTotal := linesTokens(skillIndexEntry(list[0], list[0].Description)) +
		linesTokens(skillIndexEntry(list[1], list[1].Description)) +
		linesTokens(skillIndexEntry(list[2], list[2].Description))
	bareTotal := linesTokens(skillIndexEntry(list[0], "")) +
		linesTokens(skillIndexEntry(list[1], "")) +
		linesTokens(skillIndexEntry(list[2], ""))
	budget := (fullTotal + bareTotal) / 2 // strictly between bare and full
	if budget <= bareTotal || budget >= fullTotal {
		t.Fatalf("test setup: budget %d must sit strictly between bare %d and full %d", budget, bareTotal, fullTotal)
	}

	got := formatSkillsIndex(list, budget)
	for _, s := range list {
		if !strings.Contains(got, "<name>"+s.Name+"</name>") {
			t.Errorf("skill %s must still be named: %q", s.Name, got)
		}
		if strings.Contains(got, s.Description) {
			t.Errorf("skill %s's description should have been truncated, found it verbatim", s.Name)
		}
	}
	if strings.Contains(got, "not listed here") {
		t.Errorf("every skill fits bare, so there must be no pointer line: %q", got)
	}
}

// TestFormatSkillsIndex_TinyBudgetPointsAtOverflow: a budget too small to
// hold even every skill bare keeps as many as it can (priority order) and
// names the rest in a trailing pointer line - never silently dropped.
func TestFormatSkillsIndex_TinyBudgetPointsAtOverflow(t *testing.T) {
	list := []skills.Skill{
		idxSkill("keep-me", paths.ScopeProject),
		idxSkill("drop-me-one", paths.ScopePlugin),
		idxSkill("drop-me-two", paths.ScopePlugin),
	}
	oneBare := linesTokens(skillIndexEntry(list[0], ""))
	got := formatSkillsIndex(list, oneBare+2) // room for ~one bare entry

	if !strings.Contains(got, "<name>keep-me</name>") {
		t.Errorf("the highest-priority skill must still be listed: %q", got)
	}
	if strings.Contains(got, "<name>drop-me-one</name>") || strings.Contains(got, "<name>drop-me-two</name>") {
		t.Errorf("lower-priority skills should have been pushed to the pointer line, not listed: %q", got)
	}
	if !strings.Contains(got, "drop-me-one") || !strings.Contains(got, "drop-me-two") {
		t.Errorf("overflowed skills must still be named in the pointer line: %q", got)
	}
	if !strings.Contains(got, "skill tool") {
		t.Errorf("pointer line must say how to still reach them: %q", got)
	}
}

// TestSkillsCatalog_AppearsExactlyOnce: the skill tool's own description
// must never re-embed the catalog the system prompt's <available_skills>
// index already carries - verified by building both exactly as chat.go's
// Run does, and checking a skill's description string appears in the
// combined request text exactly once.
func TestSkillsCatalog_AppearsExactlyOnce(t *testing.T) {
	list := []skills.Skill{
		idxSkill("alpha", paths.ScopeProject),
		idxSkill("beta", paths.ScopeUser),
	}
	index := formatSkillsIndex(list, 100_000)

	var records []tools.SkillRecord
	for _, s := range list {
		records = append(records, tools.SkillRecord{Name: s.Name, Description: s.Description, Body: s.Content, Dir: tools.SkillDir(s.FilePath)})
	}
	skillTool := tools.SkillTool(records)

	combined := index + "\n" + skillTool.Description
	for _, s := range list {
		if n := strings.Count(combined, s.Description); n != 1 {
			t.Errorf("skill %s's description appears %d times in the built request, want exactly 1:\n%s", s.Name, n, combined)
		}
		if n := strings.Count(combined, "<name>"+s.Name+"</name>"); n != 1 {
			t.Errorf("skill %s's name tag appears %d times, want exactly 1", s.Name, n)
		}
	}
}
