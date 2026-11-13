package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// TestSkillTool_DescriptionDoesNotDuplicateCatalog documents the fix for
// double-paying the skills catalog: the tool's description is fixed and
// generic regardless of how many records it is built from, or how long
// their descriptions are - the catalog (name/description/location) lives
// exactly once, in the system prompt's <available_skills> index
// (internal/cli/chat.go's formatSkillsIndex), not here too.
func TestSkillTool_DescriptionDoesNotDuplicateCatalog(t *testing.T) {
	long := strings.Repeat("x", 500)
	withRecords := SkillTool([]SkillRecord{
		{Name: "greet", Description: "says hi", Body: "Hello.", Dir: "/skills/greet"},
		{Name: "demo:review", Description: "reviews code", Body: "Review it.", Dir: "/plugins/demo/commands"},
		{Name: "verbose", Description: long, Body: "b", Dir: "/d"},
	})
	empty := SkillTool(nil)
	if withRecords.Description != empty.Description {
		t.Errorf("description must not vary with the record set; got %q vs %q", withRecords.Description, empty.Description)
	}
	for _, name := range []string{"greet", "demo:review", "says hi", "reviews code", long} {
		if strings.Contains(withRecords.Description, name) {
			t.Errorf("description must not embed per-skill catalog data, found %q in %q", name, withRecords.Description)
		}
	}
	if !strings.Contains(withRecords.Description, "skill_search") {
		t.Errorf("description should point at skill_search, got %q", withRecords.Description)
	}
}

func TestSkillTool_InvokeReturnsBaseDirAndBody(t *testing.T) {
	tl := SkillTool([]SkillRecord{{Name: "greet", Description: "says hi", Body: "Hello there.", Dir: "/skills/greet"}})
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"skill":"greet"}`), func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	got := msg.TextOf(res.Content)
	want := "Base directory for this skill: /skills/greet\n\nHello there."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if res.IsError {
		t.Error("expected a successful result")
	}
}

func TestSkillToolObservedExactNameOnlyOnSuccess(t *testing.T) {
	var called []string
	tl := SkillToolObserved([]SkillRecord{{Name: "greet", Body: "Hello", Dir: "/d"}}, func(name string) { called = append(called, name) })
	for _, args := range []string{`{"skill":"unknown"}`, `{"skill":"greet"}`} {
		if _, err := tl.Execute(context.Background(), json.RawMessage(args), nil, tool.Invocation{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(called) != 1 || called[0] != "greet" {
		t.Fatalf("exact-name observations: %v", called)
	}
}

func TestSkillToolWithRewriteSeesTheFinalBody(t *testing.T) {
	rec := SkillRecord{Name: "greet", Body: "Hello.", Dir: "/d", Source: "plugin", Plugin: "p"}
	var seen SkillRecord
	var seenText string
	tl := SkillToolWith([]SkillRecord{rec}, nil, func(_ context.Context, r SkillRecord, text string) string {
		seen, seenText = r, text
		return text + " (rewritten)"
	})
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"skill":"greet","args":"in French"}`), nil, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if seen.Plugin != "p" || seenText != "Hello.\n\nin French" {
		t.Errorf("rewrite saw %+v %q, want the record and the body with its args", seen, seenText)
	}
	if got := msg.TextOf(res.Content); got != "Base directory for this skill: /d\n\nHello.\n\nin French (rewritten)" {
		t.Errorf("tool result = %q", got)
	}
}

func TestSkillTool_InvokeAppendsArgs(t *testing.T) {
	tl := SkillTool([]SkillRecord{{Name: "greet", Description: "says hi", Body: "Hello.", Dir: "/d"}})
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"skill":"greet","args":"in French"}`), func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	got := msg.TextOf(res.Content)
	if !strings.HasSuffix(got, "Hello.\n\nin French") {
		t.Errorf("got %q", got)
	}
}

func TestSkillTool_CommandRendererUsesPlaceholders(t *testing.T) {
	tl := SkillTool([]SkillRecord{{Name: "frontend:component", Dir: "/repo/.claude/commands/frontend", Render: func(args string) string {
		return "Build " + strings.Fields(args)[0]
	}}})
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"skill":"frontend:component","args":"button large"}`), nil, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.TextOf(res.Content); got != "Base directory for this skill: /repo/.claude/commands/frontend\n\nBuild button" {
		t.Fatalf("rendered command: %q", got)
	}
}

func TestSkillTool_UnknownSkillListsAvailable(t *testing.T) {
	tl := SkillTool([]SkillRecord{
		{Name: "greet", Description: "says hi", Body: "Hello.", Dir: "/d"},
		{Name: "bye", Description: "says bye", Body: "Bye.", Dir: "/d"},
	})
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"skill":"nope"}`), func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected an error result for an unknown skill")
	}
	got := msg.TextOf(res.Content)
	if !strings.Contains(got, "unknown skill") || !strings.Contains(got, "greet") || !strings.Contains(got, "bye") {
		t.Errorf("got %q, want it to name the unknown skill and list what's available", got)
	}
}

func TestSkillTool_NoSkillsIsUsable(t *testing.T) {
	tl := SkillTool(nil)
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"skill":"anything"}`), func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected an error result")
	}
}

// TestSkillTool_DisableModelInvocationIsTheCallers'Job documents that
// SkillTool itself does no filtering: chat.go's wiring is responsible for
// excluding disable-model-invocation:true skills before building the
// []SkillRecord, exactly as it excludes them from the tool's own catalog
// (an excluded skill's name is simply absent from records, so it is both
// undescribed and unreachable).
func TestSkillTool_UnlistedNameIsUnreachable(t *testing.T) {
	tl := SkillTool([]SkillRecord{{Name: "public", Description: "d", Body: "b", Dir: "/d"}})
	res, _ := tl.Execute(context.Background(), json.RawMessage(`{"skill":"hidden"}`), func(tool.Result) {}, tool.Invocation{})
	if !res.IsError {
		t.Error("a name not in the records list must be unreachable, exactly like a truly unknown one")
	}
}
