package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
)

func TestBridge_RetryScheduled_SendsHumanisedMessage(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	b.handleEvent(harness.Event{
		Type: harness.EventRetryScheduled, Attempt: 1, MaxAttempts: 4,
		DelayMs: 1500, RetryError: "provider request failed: status=529 body=overloaded",
	}, ts, 4000)

	deadline := time.Now().Add(2 * time.Second)
	var gotRetry MsgRetry
	var sawRetry, sawLabel bool
	for time.Now().Before(deadline) && (!sawRetry || !sawLabel) {
		_, sent := f.snapshot()
		for _, m := range sent {
			switch v := m.(type) {
			case MsgRetry:
				gotRetry = v
				sawRetry = true
			case MsgSpinnerLabel:
				if v.Text == "Reconnecting" {
					sawLabel = true
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !sawRetry {
		t.Fatal("no MsgRetry sent for EventRetryScheduled")
	}
	if !sawLabel {
		t.Fatal("no MsgSpinnerLabel{\"Reconnecting\"} sent for EventRetryScheduled")
	}
	if gotRetry.Message != "Stream interrupted · 529 overloaded" {
		t.Errorf("Message = %q, want %q", gotRetry.Message, "Stream interrupted · 529 overloaded")
	}
	if gotRetry.Attempt != 1 || gotRetry.MaxAttempts != 4 {
		t.Errorf("Attempt/MaxAttempts = %d/%d, want 1/4", gotRetry.Attempt, gotRetry.MaxAttempts)
	}
	if gotRetry.Delay != 1500*time.Millisecond {
		t.Errorf("Delay = %v, want 1.5s", gotRetry.Delay)
	}
}

func TestBridge_RetryStart_SendsAttempt(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	b.handleEvent(harness.Event{Type: harness.EventRetryStart, Attempt: 2}, ts, 4000)

	deadline := time.Now().Add(2 * time.Second)
	var sawStart bool
	var gotAttempt int
	for time.Now().Before(deadline) && !sawStart {
		_, sent := f.snapshot()
		for _, m := range sent {
			if v, ok := m.(MsgRetryStart); ok {
				sawStart = true
				gotAttempt = v.Attempt
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !sawStart {
		t.Fatal("no MsgRetryStart sent for EventRetryStart")
	}
	if gotAttempt != 2 {
		t.Errorf("Attempt = %d, want 2", gotAttempt)
	}
}

// TestBridge_StreamText_Throttled checks that rapid text deltas within
// streamThrottle collapse to at most one MsgStreamText send, while
// EventMessageEnd always flushes the final state regardless (D's spec).
func TestBridge_StreamText_Throttled(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	for i := 0; i < 5; i++ {
		b.handleStreamEvent(&msg.StreamEvent{Type: msg.EventTextDelta, Delta: "x"}, ts)
	}
	time.Sleep(10 * time.Millisecond)

	_, sent := f.snapshot()
	count := 0
	for _, m := range sent {
		if _, ok := m.(MsgStreamText); ok {
			count++
		}
	}
	if count > 1 {
		t.Errorf("MsgStreamText sent %d times within streamThrottle for 5 rapid deltas, want at most 1", count)
	}

	b.handleEvent(harness.Event{
		Type:    harness.EventMessageEnd,
		Message: &msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("xxxxx")}},
	}, ts, 4000)

	deadline := time.Now().Add(2 * time.Second)
	var sawFinal bool
	for time.Now().Before(deadline) && !sawFinal {
		_, sent := f.snapshot()
		for _, m := range sent {
			if v, ok := m.(MsgStreamText); ok && v.Text == "xxxxx" {
				sawFinal = true
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !sawFinal {
		t.Fatal("EventMessageEnd did not send the final MsgStreamText{\"xxxxx\"}")
	}
}

// TestBridge_StreamText_ResetsBetweenMessages is the regression for the
// defect where a turn's separate assistant text messages (each its own
// EventMessageStart/EventMessageEnd pair around a tool call) ran together
// in the live region because ts.streamed was only reset on
// EventThinkingStart and once per turn. EventMessageStart must reset it so
// the second message's live stream starts clean instead of appending onto
// the first message's already-committed text.
func TestBridge_StreamText_ResetsBetweenMessages(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)
	ts := &turnState{toolStarts: map[string]toolStart{}}

	// First assistant message streams and ends.
	b.handleEvent(harness.Event{Type: harness.EventMessageStart}, ts, 4000)
	b.handleStreamEvent(&msg.StreamEvent{Type: msg.EventTextDelta, Delta: "Let me write the limiter."}, ts)
	b.handleEvent(harness.Event{
		Type:    harness.EventMessageEnd,
		Message: &msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("Let me write the limiter.")}},
	}, ts, 4000)

	// A tool call happens in between (not modelled here beyond the turn
	// state persisting across it), then a second assistant message starts.
	// The sleep clears streamThrottle's window so the second delta's send
	// below is not itself throttled away by the first message's send.
	time.Sleep(streamThrottle + 10*time.Millisecond)
	b.handleEvent(harness.Event{Type: harness.EventMessageStart}, ts, 4000)
	if got := ts.streamed.String(); got != "" {
		t.Fatalf("ts.streamed not reset at EventMessageStart: got %q, want empty", got)
	}
	b.handleStreamEvent(&msg.StreamEvent{Type: msg.EventTextDelta, Delta: "Now the Redis client."}, ts)

	deadline := time.Now().Add(2 * time.Second)
	var gotSecond string
	for time.Now().Before(deadline) {
		_, sent := f.snapshot()
		for _, m := range sent {
			if v, ok := m.(MsgStreamText); ok && strings.Contains(v.Text, "Redis") {
				gotSecond = v.Text
			}
		}
		if gotSecond != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if gotSecond != "Now the Redis client." {
		t.Errorf("second message's live text = %q, want %q (no concatenation with the first message)", gotSecond, "Now the Redis client.")
	}
}

// TestBridge_FreezeBefore_SkipsIdenticalConsecutiveFreeze is the
// regression for a real session where pressing Escape while three
// subagents were live produced, in transcript order: the subagents panel
// (2/3 done), a "✕ Declined …" note, the SAME subagents panel again (2/3
// done, identical content), then the still-live busy line. The panel's
// Apply clears its frozen flag on every subagent event by design (so a
// still-running dispatch makes it live again), and every commit calls
// FreezeBefore first — so a panel unfrozen by an unrelated in-flight tick
// and refrozen before its rendered content actually changed was committed
// twice. FreezeBefore now skips committing when the hook's output is
// byte-identical to the last thing it actually committed.
func TestBridge_FreezeBefore_SkipsIdenticalConsecutiveFreeze(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)

	calls := 0
	b.SetFreezeHook(func() []string {
		calls++
		// Simulates Freeze() returning the panel's current (unchanged)
		// rendered lines each time it is asked, exactly as
		// SubagentPanelState.Freeze does when Apply flipped frozen back to
		// false without the visible summary having moved.
		return []string{"", "2/3 done"}
	})

	b.FreezeBefore()
	b.FreezeBefore() // simulates Apply() having unfrozen it again in between

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if calls >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if calls < 2 {
		t.Fatalf("freeze hook called %d times, want at least 2 (both FreezeBefore calls should still consult it)", calls)
	}

	time.Sleep(10 * time.Millisecond)
	printed, _ := f.snapshot()
	panelCommits := 0
	for _, p := range printed {
		if strings.Contains(p, "2/3 done") {
			panelCommits++
		}
	}
	if panelCommits > 1 {
		t.Errorf("panel committed %d times for identical consecutive freezes, want 1", panelCommits)
	}

	// A genuinely changed freeze must still commit.
	b.SetFreezeHook(func() []string { return []string{"", "3/3 done"} })
	b.FreezeBefore()
	deadline = time.Now().Add(2 * time.Second)
	var sawChanged bool
	for time.Now().Before(deadline) && !sawChanged {
		printed, _ := f.snapshot()
		for _, p := range printed {
			if strings.Contains(p, "3/3 done") {
				sawChanged = true
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !sawChanged {
		t.Fatal("a genuinely changed freeze after a de-duplicated one was not committed")
	}
}

func TestBridge_InFlightTools_ReportsStartedNotEnded(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	b.ts = &turnState{toolStarts: map[string]toolStart{}}

	b.handleEvent(harness.Event{Type: harness.EventToolStart, ToolCallID: "t1", ToolName: "bash", ToolArgs: map[string]any{"command": "sleep 5"}}, b.ts, 4000)
	b.handleEvent(harness.Event{Type: harness.EventToolStart, ToolCallID: "t2", ToolName: "edit", ToolArgs: map[string]any{"file_path": "a.go"}}, b.ts, 4000)
	// t2 finishes; only t1 should remain in flight.
	b.handleEvent(harness.Event{Type: harness.EventToolEnd, ToolCallID: "t2", ToolName: "edit", ToolResult: &msg.ToolResultMessage{}}, b.ts, 4000)

	views := b.InFlightTools()
	if len(views) != 1 {
		t.Fatalf("InFlightTools() len = %d, want 1", len(views))
	}
	if views[0].Status != CallError {
		t.Errorf("Status = %v, want CallError", views[0].Status)
	}
	if views[0].PrimaryArg != "sleep 5" {
		t.Errorf("PrimaryArg = %q, want %q", views[0].PrimaryArg, "sleep 5")
	}
}

func TestWebSearchBusyLabel(t *testing.T) {
	got, ok := webSearchBusyLabel(`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go generics"}}`)
	if !ok || got != "Searching the web for go generics" {
		t.Errorf("label = %q, %v", got, ok)
	}
	if _, ok := webSearchBusyLabel(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}`); ok {
		t.Error("a search result block is not a search start")
	}
}

// TestBridge_ModelSwitch_CarriesContextWindow checks that ModelSwitch's
// "usable" argument (the new tier's ContextWindow, as internal/cli/tui.go's
// OnModelChanged passes resolved.Tier.ContextWindow) reaches MsgModelInfo
// rather than being dropped on the floor - the bug that left the footer's
// context meter showing the previous model's window after /model.
func TestBridge_ModelSwitch_CarriesContextWindow(t *testing.T) {
	b := NewBridge("/tmp")
	defer b.Stop()
	f := &fakeSink{}
	b.setSink(f)

	b.ModelSwitch("anthropic/claude-opus-4-8", "opus", 200000)

	got := waitForOneSent(t, f)
	info, ok := got.(MsgModelInfo)
	if !ok {
		t.Fatalf("sent %#v, want MsgModelInfo", got)
	}
	if info.Label != "anthropic/claude-opus-4-8" {
		t.Errorf("Label = %q, want anthropic/claude-opus-4-8", info.Label)
	}
	if info.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000 (it must not stay 0/stale)", info.ContextWindow)
	}
}

func TestFaultHint(t *testing.T) {
	cases := map[string]string{
		"Unauthorized (401): invalid api key":          "/login",
		"Overloaded (529)":                             "Send again shortly",
		"Too Many Requests (429): slow down":           "Wait a minute",
		"Not Found (404): model: claude-x":             "/model lists",
		"Bad Request (400): prompt is too long":        "",
		"stream interrupted: connection reset by peer": "",
	}
	for msg, want := range cases {
		got := faultHint(msg)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("faultHint(%q) = %q, want it to contain %q", msg, got, want)
		}
	}
}
