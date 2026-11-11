package tools

import (
	"strings"
	"testing"
)

func TestSkillSearchBoundedAndRelevant(t *testing.T) {
	records := []SkillRecord{{Name: "database", Description: "SQL migrations", Body: "secret body"}, {Name: "design", Description: "Color and typography"}}
	got := SearchSkills(records, "SQL", 5)
	if len(got) != 1 || got[0].Name != "database" {
		t.Fatalf("results: %+v", got)
	}
	text := SkillSearchText(records, "", 20)
	if len(text) > 80 || strings.Contains(text, "secret body") {
		t.Fatalf("unbounded or body leaked: %q", text)
	}
	if got := SearchSkills(records, "unmatched", 5); len(got) != 0 {
		t.Fatal(got)
	}
	if got := SearchSkills(records, "", 5); len(got) != 2 || got[0].Name != "database" {
		t.Fatal(got)
	}
}
