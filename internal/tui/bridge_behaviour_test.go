package tui

import (
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
