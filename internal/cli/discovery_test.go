package cli

import (
	"context"
	"fmt"
	"github.com/andrepato/harness/internal/claude/skills"
	"strings"
	"testing"

	settings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tools"
)

func TestDiscoveryDoesNotMutateHistory(t *testing.T) {
	records := []tools.SkillRecord{{Name: "sql", Description: "database migrations"}, {Name: "design", Description: "colors"}}
	input := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("database migration")}}}
	hook := skillDiscoveryContext(records, settings.SkillDiscovery{}, func() int { return 256 })
	got, err := hook(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(input[0].(msg.UserMessage).Content) != 1 {
		t.Fatal("mutated stored history")
	}
	text := msg.TextOf(got[len(got)-1].(msg.UserMessage).Content)
	if !strings.Contains(text, "sql:") || strings.Contains(text, "design:") {
		t.Fatal(text)
	}
	again, _ := hook(context.Background(), input)
	if msg.TextOf(again[len(again)-1].(msg.UserMessage).Content) != text {
		t.Fatal("unstable selection")
	}
}

func TestDiscoveryModesAndExclusions(t *testing.T) {
	records := []tools.SkillRecord{{Name: "private"}, {Name: "public"}}
	filtered := discoveryRecords(records, settings.SkillDiscovery{Excluded: []string{"private"}})
	if len(filtered) != 1 || filtered[0].Name != "public" {
		t.Fatal(filtered)
	}
	for _, mode := range []string{"search", "catalog"} {
		input := []msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("public")}}}
		got, _ := skillDiscoveryContext(filtered, settings.SkillDiscovery{Mode: mode}, func() int { return 256 })(context.Background(), input)
		if len(got) != len(input) || len(got[0].(msg.UserMessage).Content) != 1 {
			t.Fatal(mode)
		}
	}
}

func TestDiscoveryLargeCatalogHasBoundedStartup(t *testing.T) {
	var catalog []skills.Skill
	var records []tools.SkillRecord
	for i := 0; i < 66; i++ {
		name := fmt.Sprintf("skill-%02d", i)
		description := strings.Repeat("database migrations ", 50)
		catalog = append(catalog, skills.Skill{Name: name, Description: description})
		records = append(records, tools.SkillRecord{Name: name, Description: description})
	}
	config := settings.SkillDiscovery{}
	startup := discoveryIndex(catalog, config, 3000)
	if len(startup) > 1024 || strings.Contains(startup, "skill-00") {
		t.Fatalf("catalog leaked into startup: %d bytes", len(startup))
	}
	suggestions := tools.SkillSearchText(records, "database", config.Budget(3000))
	if len(suggestions) > 4096 || suggestions == "" {
		t.Fatalf("suggestions size: %d", len(suggestions))
	}
	// Exact-name loading is independent of the bounded search result count.
	if got := tools.SearchSkills(records, "skill-65", 1); len(got) != 1 || got[0].Name != "skill-65" {
		t.Fatalf("last installed skill lost: %+v", got)
	}
}

func TestDiscoveryBudgetIncludesFraming(t *testing.T) {
	var records []tools.SkillRecord
	for i := 0; i < 20; i++ {
		records = append(records, tools.SkillRecord{Name: fmt.Sprintf("test-%d", i), Description: strings.Repeat("test ", 45)})
	}
	input := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("test")}}}
	out, err := skillDiscoveryContext(records, settings.SkillDiscovery{}, func() int { return 128 })(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	added := msg.TextOf(out[len(out)-1].(msg.UserMessage).Content)
	if added == "" || len(added) > 512 {
		t.Fatalf("full suggestion block has %d bytes", len(added))
	}
}
