package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	claudecommands "github.com/andrepato/harness/internal/claude/commands"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

func TestCommandSkillRecordsPrecedenceAndInvocation(t *testing.T) {
	skills := []tools.SkillRecord{{Name: "deploy", Description: "skill version", Body: "skill body"}}
	commands := []claudecommands.CommandFile{
		claudecommands.ParseCommandFile("---\ndescription: personal deploy\n---\npersonal $ARGUMENTS", "deploy", "", claudecommands.Personal, "/home/.claude/commands/deploy.md"),
		claudecommands.ParseCommandFile("---\ndescription: project deploy\n---\nproject $1", "deploy", "", claudecommands.Project, "/repo/.claude/commands/deploy.md"),
		claudecommands.ParseCommandFile("---\ndescription: plugin deploy\n---\nplugin $ARGUMENTS", "deploy", "ops", claudecommands.Plugin, "/plugin/commands/deploy.md"),
	}
	got := commandSkillRecords(skills, commands)
	if len(got) != 2 || got[0].Name != "deploy" || got[0].Description != "project deploy" || got[0].Render("one two") != "project one" || got[1].Name != "ops:deploy" || got[1].Render("one two") != "plugin one two" {
		t.Fatalf("merged records: %+v", got)
	}
	if got[0].Dir != "/repo/.claude/commands" {
		t.Fatalf("command base directory: %q", got[0].Dir)
	}
}

func TestCommandSkillRecordsDisabledShadowsAndExclusions(t *testing.T) {
	skills := []tools.SkillRecord{{Name: "review", Description: "skill review", Body: "private skill"}}
	commands := []claudecommands.CommandFile{
		claudecommands.ParseCommandFile("---\ndescription: private review\ndisable-model-invocation: true\n---\nprivate command", "review", "", claudecommands.Project, "/repo/review.md"),
		claudecommands.ParseCommandFile("---\ndescription: plugin reports\n---\nreport", "reports", "ops", claudecommands.Plugin, "/plugin/reports.md"),
	}
	got := discoveryRecords(commandSkillRecords(skills, commands), claudesettings.SkillDiscovery{Excluded: []string{"ops:reports"}})
	if len(got) != 0 {
		t.Fatalf("disabled or excluded command exposed: %+v", got)
	}
}

func TestCommandSkillRecordsKeepsQualifiedNamespace(t *testing.T) {
	commands := []claudecommands.CommandFile{
		claudecommands.ParseCommandFile("---\ndescription: Frontend components\n---\nfront", "component", "frontend", claudecommands.Project, "/repo/frontend/component.md"),
		claudecommands.ParseCommandFile("---\ndescription: Plugin components\n---\nplugin", "component", "ops:frontend", claudecommands.Plugin, "/plugin/frontend/component.md"),
	}
	got := commandSkillRecords(nil, commands)
	if len(got) != 2 || got[0].Name != "frontend:component" || got[1].Name != "ops:frontend:component" || strings.Contains(got[0].Description, "Plugin") {
		t.Fatalf("namespace collision: %+v", got)
	}
}

func TestCommandDiscoveryIsMetadataOnlyAndBudgeted(t *testing.T) {
	command := claudecommands.ParseCommandFile("---\ndescription: Review database migrations\n---\nSECRET COMMAND INSTRUCTIONS", "review", "db", claudecommands.Plugin, "/plugin/commands/review.md")
	records := commandSkillRecords(nil, []claudecommands.CommandFile{command})
	search := tools.CapabilitySearchTool(func() tools.CapabilityCatalog {
		return tools.CapabilityCatalog{Skills: records}
	}, 32, nil)
	result, err := search.Execute(context.Background(), json.RawMessage(`{"query":"database"}`), nil, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	got := msg.TextOf(result.Content)
	if !strings.Contains(got, "db:review [skill]") || strings.Contains(got, "SECRET") || len(got) > 128 {
		t.Fatalf("metadata search: %q", got)
	}
}
