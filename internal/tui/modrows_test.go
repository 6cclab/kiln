package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// drawerFor binds a row drawer that draws only `component`, recording the
// props and id it was asked with, and unbinds it when the test ends.
func drawerFor(t *testing.T, b *Bridge, component string) (props *map[string]any, id *string) {
	t.Helper()
	var gotProps map[string]any
	var gotID string
	b.SetRowDrawer(func(c, requestID string, p map[string]any, width int) []string {
		if c != component {
			return nil
		}
		gotProps, gotID = p, requestID
		return []string{"MOD " + component}
	})
	t.Cleanup(func() { b.SetRowDrawer(nil) })
	return &gotProps, &gotID
}

func TestModDrawsUserToolAndGroupRows(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	props, id := drawerFor(t, b, "UserMessage")
	rows := RenderUserMessage("fix the bug", 60)
	if len(rows) != 2 || rows[0] != "" || rows[1] != "MOD UserMessage" {
		t.Errorf("user rows = %q, want a blank then the mod's row", rows)
	}
	if (*props)["text"] != "fix the bug" || (*props)["isExpanded"] != true || (*props)["origin"].(map[string]any)["kind"] != "composer" || !strings.HasPrefix(*id, "user-") {
		t.Errorf("UserMessage props %v id %q", *props, *id)
	}

	props, _ = drawerFor(t, b, "ToolUse")
	view := ToolCallView{Name: "Read", PrimaryArg: "a.go", Status: CallOK, ResultLines: []string{"12 lines"}}
	if rows := RenderToolCall(view); len(rows) != 1 || rows[0] != "MOD ToolUse" {
		t.Errorf("tool rows = %q", rows)
	}
	if (*props)["tool"] != "Read" || (*props)["input"] != "a.go" || (*props)["output"] != "12 lines" || (*props)["isRunning"] != false || (*props)["isErrored"] != false {
		t.Errorf("ToolUse props = %v", *props)
	}
	view.Status = CallError
	RenderToolCall(view)
	if (*props)["isErrored"] != true {
		t.Errorf("a failed call's props = %v, want isErrored", *props)
	}

	props, _ = drawerFor(t, b, "ToolGroup")
	if rows := RenderReadGroup([]ToolCallView{view, {Name: "Grep", PrimaryArg: "x", Status: CallOK}}); len(rows) != 1 || rows[0] != "MOD ToolGroup" {
		t.Errorf("group rows = %q", rows)
	}
	if calls, _ := (*props)["calls"].([]any); len(calls) != 2 || (*props)["isActive"] != false {
		t.Errorf("ToolGroup props = %v", *props)
	}
}

// With no drawer, or one that draws other components, the engine's rows come
// back untouched.
func TestRowsAreTheEnginesWhenNoModDrawsThem(t *testing.T) {
	want := RenderUserMessageMeta("hello", "", 60)
	if got := RenderUserMessage("hello", 60); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("with no drawer: %q, want %q", got, want)
	}
	b := NewBridge("/tmp")
	defer b.Stop()
	drawerFor(t, b, "Spinner")
	if got := RenderUserMessage("hello", 60); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("with a drawer for another component: %q, want %q", got, want)
	}
}

// A mod's AssistantMessage drawing replaces the reply in a replay too.
func TestModDrawsAssistantRowsInReplay(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	props, _ := drawerFor(t, b, "AssistantMessage")
	entries := []session.Entry{
		{ID: "a", Seq: 1, Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("q")}}},
		{ID: "b", Seq: 2, Type: session.EntryMessage, Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("the answer")}}},
	}
	out := strings.Join(RenderTranscriptEntries(entries, 80, false, "/tmp", nil, nil), "\n")
	if !strings.Contains(out, "MOD AssistantMessage") || strings.Contains(out, "the answer") {
		t.Errorf("replay = %q, want the mod's row in place of the reply", out)
	}
	if (*props)["text"] != "the answer" || (*props)["isFirstOfReply"] != true {
		t.Errorf("AssistantMessage props = %v", *props)
	}
}

// A note (a one-line result, an interrupt, a decline) is an InfoNotice.
func TestModDrawsInfoNotice(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	props, _ := drawerFor(t, b, "InfoNotice")
	b.CommitNote("Interrupted")
	deadline := time.Now().Add(15 * time.Second)
	var printed []string
	for time.Now().Before(deadline) {
		if printed, _ = f.snapshot(); len(printed) >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	joined := strings.Join(printed, "\n")
	if !strings.Contains(joined, "MOD InfoNotice") || strings.Contains(joined, "Interrupted") {
		t.Errorf("committed note = %q, want the mod's row in place of the engine's", joined)
	}
	if (*props)["text"] != "Interrupted" || (*props)["command"] != nil {
		t.Errorf("InfoNotice props = %v", *props)
	}
}
