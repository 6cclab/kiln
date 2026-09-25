package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
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

	result := c.Finish(true, session.SessionStats{}, "")
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

	result := c.Finish(true, session.SessionStats{}, "")
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

	result := c.Finish(true, session.SessionStats{}, "")

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
			`{"type":"result","ok":true,"text":"done","blocked":["a"],"usage":{"input":0,"output":0,"cache_read":0,"cache_write":0},"total_cost_usd":0,"duration_ms":0,"num_turns":0,"num_tool_calls":0}`,
		},
		{
			"result with no blocked",
			StreamEvent{Type: "result", OK: false, Text: ""},
			`{"type":"result","ok":false,"text":"","blocked":[],"usage":{"input":0,"output":0,"cache_read":0,"cache_write":0},"total_cost_usd":0,"duration_ms":0,"num_turns":0,"num_tool_calls":0}`,
		},
		{
			"result with enriched fields and a reason",
			StreamEvent{
				Type: "result", OK: false, Text: "", Blocked: []string{},
				Usage:        msg.Usage{Input: 812, Output: 34, CacheRead: 5, CacheWrite: 6},
				CostUSD:      0.0123,
				DurationMS:   4500,
				Turns:        2,
				NumToolCalls: 1,
				Reason:       "max-turns-exceeded",
			},
			`{"type":"result","ok":false,"text":"","blocked":[],"usage":{"input":812,"output":34,"cache_read":5,"cache_write":6},"total_cost_usd":0.0123,"duration_ms":4500,"num_turns":2,"num_tool_calls":1,"reason":"max-turns-exceeded"}`,
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
		OK:           true,
		Text:         "done",
		ToolCalls:    []toolCall{{Name: "bash", Arg: &arg}},
		Blocked:      []string{"Write(/etc)"},
		Usage:        msg.Usage{Input: 100, Output: 20, CacheRead: 3, CacheWrite: 4},
		CostUSD:      0.05,
		DurationMS:   1234,
		Turns:        2,
		NumToolCalls: 1,
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
  ],
  "usage": {
    "input": 100,
    "output": 20,
    "cache_read": 3,
    "cache_write": 4
  },
  "total_cost_usd": 0.05,
  "duration_ms": 1234,
  "num_turns": 2,
  "num_tool_calls": 1
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
  "blocked": [],
  "usage": {
    "input": 0,
    "output": 0,
    "cache_read": 0,
    "cache_write": 0
  },
  "total_cost_usd": 0,
  "duration_ms": 0,
  "num_turns": 0,
  "num_tool_calls": 0
}`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestFormatPrintResultJSONReasonOmittedWhenEmpty checks that "reason" is
// dropped entirely (omitempty) rather than rendered as "" — the field only
// exists to explain an abnormal stop like --max-turns.
func TestFormatPrintResultJSONReasonOmittedWhenEmpty(t *testing.T) {
	got := FormatPrintResult(PrintResult{Text: "x"}, "json")
	if strings.Contains(got, `"reason"`) {
		t.Fatalf("reason should be omitted when empty, got:\n%s", got)
	}
}

// TestFormatPrintResultJSONReasonPresent checks the mirror case: a non-empty
// Reason (e.g. from --max-turns) does render.
func TestFormatPrintResultJSONReasonPresent(t *testing.T) {
	got := FormatPrintResult(PrintResult{Text: "x", Reason: "max-turns-exceeded"}, "json")
	if !strings.Contains(got, `"reason": "max-turns-exceeded"`) {
		t.Fatalf("want reason rendered, got:\n%s", got)
	}
}

// TestCollectorFinishCountsTurnsAndCopiesStats checks Finish's enrichment:
// EventTurnEnd increments Turns, stats.Usage/Cost are copied verbatim, and
// DurationMS is positive once any time has passed.
func TestCollectorFinishCountsTurnsAndCopiesStats(t *testing.T) {
	events := harness.NewEvents()
	c := NewCollector(events)

	events.Emit(harness.Event{Type: harness.EventTurnEnd})
	events.Emit(harness.Event{Type: harness.EventTurnEnd})
	events.Emit(toolStart("bash", map[string]any{"command": "ls"}))

	stats := session.SessionStats{
		Usage: msg.Usage{Input: 812, Output: 34, Cost: msg.Cost{Total: 0.0042}},
	}
	result := c.Finish(true, stats, "")

	if result.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", result.Turns)
	}
	if result.NumToolCalls != 1 {
		t.Fatalf("NumToolCalls = %d, want 1", result.NumToolCalls)
	}
	if result.Usage != stats.Usage {
		t.Fatalf("Usage = %+v, want %+v", result.Usage, stats.Usage)
	}
	if result.CostUSD != 0.0042 {
		t.Fatalf("CostUSD = %v, want 0.0042", result.CostUSD)
	}
	if result.DurationMS < 0 {
		t.Fatalf("DurationMS = %d, want >= 0", result.DurationMS)
	}
	if result.Reason != "" {
		t.Fatalf("Reason = %q, want empty", result.Reason)
	}
}

// TestCollectorFinishCarriesReason checks Finish passes reason straight
// through to PrintResult.Reason (runPrintMode's --max-turns path sets
// "max-turns-exceeded" here).
func TestCollectorFinishCarriesReason(t *testing.T) {
	events := harness.NewEvents()
	c := NewCollector(events)
	result := c.Finish(false, session.SessionStats{}, "max-turns-exceeded")
	if result.Reason != "max-turns-exceeded" {
		t.Fatalf("Reason = %q, want max-turns-exceeded", result.Reason)
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
