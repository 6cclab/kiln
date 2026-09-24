package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
)

// Ported from print.ts's behavior (there is no dedicated print.test.ts in
// the TS reference; the shapes below are print.ts's StreamEvent union and
// runPrint/formatPrintResult logic, read directly from src/print.ts).

func doneEvent(text string) harness.Event {
	am := &msg.AssistantMessage{
		Role:    msg.RoleAssistant,
		Content: msg.Blocks{msg.Text(text)},
	}
	return harness.Event{
		Type:        harness.EventMessageUpdate,
		StreamEvent: &msg.StreamEvent{Type: msg.EventDone, Message: am},
	}
}

func toolStart(name string, args map[string]any) harness.Event {
	return harness.Event{Type: harness.EventToolStart, ToolName: name, ToolArgs: args}
}

func toolEnd(name string, isError bool) harness.Event {
	return harness.Event{
		Type:       harness.EventToolEnd,
		ToolName:   name,
		ToolResult: &msg.ToolResultMessage{IsError: isError},
	}
}

func TestCollectorAssistantText(t *testing.T) {
	events := harness.NewEvents()
	c := NewCollector(events)

	events.Emit(doneEvent("hello"))
	events.Emit(doneEvent("world"))

	result := c.Finish(true)
	if result.Text != "hello\nworld" {
		t.Fatalf("Text = %q", result.Text)
	}
}

func TestCollectorToolArgSelection(t *testing.T) {
	events := harness.NewEvents()
	c := NewCollector(events)

	events.Emit(toolStart("bash", map[string]any{"command": "echo hi", "path": "/tmp"}))
	events.Emit(toolStart("read", map[string]any{"path": "/tmp/x.txt"}))
	events.Emit(toolStart("noop", map[string]any{}))

	result := c.Finish(true)
	if len(result.ToolCalls) != 3 {
		t.Fatalf("ToolCalls = %+v", result.ToolCalls)
	}
	if result.ToolCalls[0].Arg == nil || *result.ToolCalls[0].Arg != "echo hi" {
		t.Fatalf("command should win over path: %+v", result.ToolCalls[0])
	}
	if result.ToolCalls[1].Arg == nil || *result.ToolCalls[1].Arg != "/tmp/x.txt" {
		t.Fatalf("path should be used when no command: %+v", result.ToolCalls[1])
	}
	if result.ToolCalls[2].Arg != nil {
		t.Fatalf("no command/path should leave Arg nil: %+v", result.ToolCalls[2])
	}
}

func TestCollectorToolEndIsError(t *testing.T) {
	events := harness.NewEvents()
	c := NewCollector(events)

	events.Emit(toolStart("bash", map[string]any{"command": "false"}))
	events.Emit(toolEnd("bash", true))

	var streamed []StreamEvent
	c.Stream = func(ev StreamEvent) { streamed = append(streamed, ev) }
	events.Emit(toolEnd("bash", false))

	if len(streamed) != 1 || streamed[0].Type != "tool_end" || streamed[0].IsError != false {
		t.Fatalf("streamed = %+v", streamed)
	}
}

func TestCollectorResultAlwaysLast(t *testing.T) {
	events := harness.NewEvents()
	c := NewCollector(events)
	c.GetBlocked = func() []string { return []string{"Write(/etc/passwd)"} }

	var streamed []StreamEvent
	c.Stream = func(ev StreamEvent) { streamed = append(streamed, ev) }

	events.Emit(toolStart("bash", map[string]any{"command": "ls"}))
	events.Emit(toolEnd("bash", false))
	events.Emit(doneEvent("done"))

	result := c.Finish(true)

	if len(streamed) == 0 || streamed[len(streamed)-1].Type != "result" {
		t.Fatalf("result event must be last: %+v", streamed)
	}
	last := streamed[len(streamed)-1]
	if !last.OK || last.Text != "done" || len(last.Blocked) != 1 || last.Blocked[0] != "Write(/etc/passwd)" {
		t.Fatalf("result event = %+v", last)
	}
	if !result.OK || result.Text != "done" || len(result.Blocked) != 1 {
		t.Fatalf("PrintResult = %+v", result)
	}
}

func TestStreamEventJSONShapes(t *testing.T) {
	arg := "echo hi"
	cases := []struct {
		name string
		ev   StreamEvent
		want string
	}{
		{
			"tool_start with arg",
			StreamEvent{Type: "tool_start", Name: "bash", Arg: &arg},
			`{"type":"tool_start","name":"bash","arg":"echo hi"}`,
		},
		{
			"tool_start without arg",
			StreamEvent{Type: "tool_start", Name: "noop"},
			`{"type":"tool_start","name":"noop"}`,
		},
		{
			"tool_end",
			StreamEvent{Type: "tool_end", Name: "bash", IsError: true},
			`{"type":"tool_end","name":"bash","isError":true}`,
		},
		{
			"tool_end not an error",
			StreamEvent{Type: "tool_end", Name: "bash", IsError: false},
			`{"type":"tool_end","name":"bash","isError":false}`,
		},
		{
			"assistant",
			StreamEvent{Type: "assistant", Text: "hi"},
			`{"type":"assistant","text":"hi"}`,
		},
		{
			"result",
			StreamEvent{Type: "result", OK: true, Text: "done", Blocked: []string{"a"}},
			`{"type":"result","ok":true,"text":"done","blocked":["a"]}`,
		},
		{
			"result with no blocked",
			StreamEvent{Type: "result", OK: false, Text: ""},
			`{"type":"result","ok":false,"text":"","blocked":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.ev)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Errorf("got %s, want %s", b, tc.want)
			}
		})
	}
}

func TestFormatPrintResultText(t *testing.T) {
	r := PrintResult{Text: "  hello  "}
	if got := FormatPrintResult(r, "text"); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatPrintResultJSON(t *testing.T) {
	arg := "echo hi"
	r := PrintResult{
		OK:        true,
		Text:      "done",
		ToolCalls: []toolCall{{Name: "bash", Arg: &arg}},
		Blocked:   []string{"Write(/etc)"},
	}
	got := FormatPrintResult(r, "json")
	want := `{
  "ok": true,
  "text": "done",
  "toolCalls": [
    {
      "name": "bash",
      "arg": "echo hi"
    }
  ],
  "blocked": [
    "Write(/etc)"
  ]
}`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatPrintResultJSONEmptyCollections(t *testing.T) {
	got := FormatPrintResult(PrintResult{Text: "x"}, "json")
	want := `{
  "ok": false,
  "text": "x",
  "toolCalls": [],
  "blocked": []
}`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatPrintResultStreamJSONIsEmpty(t *testing.T) {
	if got := FormatPrintResult(PrintResult{Text: "hi"}, "stream-json"); got != "" {
		t.Fatalf("stream-json must return empty, got %q", got)
	}
}

func TestWriteStreamEventWritesNDJSONLine(t *testing.T) {
	var sb strings.Builder
	WriteStreamEvent(&sb, assistantEvent("hi"))
	if sb.String() != `{"type":"assistant","text":"hi"}`+"\n" {
		t.Fatalf("got %q", sb.String())
	}
}
