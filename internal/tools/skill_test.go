package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func TestSkillTool_DescriptionListsEveryRecord(t *testing.T) {
	tl := SkillTool([]SkillRecord{
		{Name: "greet", Description: "says hi", Body: "Hello.", Dir: "/skills/greet"},
		{Name: "demo:review", Description: "reviews code", Body: "Review it.", Dir: "/plugins/demo/commands"},
	})
	if !strings.Contains(tl.Description, "- greet: says hi") {
		t.Errorf("description missing greet entry: %q", tl.Description)
	}
	if !strings.Contains(tl.Description, "- demo:review: reviews code") {
		t.Errorf("description missing plugin entry: %q", tl.Description)
	}
}

func TestSkillTool_DescriptionTruncatesLongDescriptions(t *testing.T) {
	long := strings.Repeat("x", 500)
	tl := SkillTool([]SkillRecord{{Name: "verbose", Description: long, Body: "b", Dir: "/d"}})
	if strings.Contains(tl.Description, long) {
		t.Error("expected the long description to be truncated, found it verbatim")
	}
	if !strings.Contains(tl.Description, strings.Repeat("x", descriptionTruncateLen)) {
		t.Error("expected a 200-char prefix of the description")
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
	if !strings.Contains(tl.Description, "No skills are currently available") {
		t.Errorf("got %q", tl.Description)
	}
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
