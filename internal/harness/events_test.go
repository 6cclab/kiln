package harness

import (
	"context"
	"testing"
)

// TestEventOrdering captures every event via OnAll and asserts the
// high-level ordering pi's own event stream guarantees for one prompt with
// a tool call: run_start, turn_start, message_start, message_update...,
// message_end, tool_start, tool_end, turn_end, ... , run_end.
func TestEventOrdering(t *testing.T) {
	script := `
model: faux-1
steps:
  - text: "on it"
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	rig := newTestRig(t, script, []string{"bash"})
	lane := rig.mustLane("main")

	var seq []EventType
	unsub := rig.H.Events().OnAll(func(ev Event) { seq = append(seq, ev.Type) })
	defer unsub()

	result, err := lane.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, err = %v", result.Status, result.Error)
	}

	mustBeforeFirst := func(a, b EventType) {
		ai, bi := indexOf(seq, a), indexOf(seq, b)
		if ai < 0 {
			t.Fatalf("event %s never fired; sequence = %v", a, seq)
		}
		if bi < 0 {
			t.Fatalf("event %s never fired; sequence = %v", b, seq)
		}
		if ai >= bi {
			t.Fatalf("expected %s before %s; sequence = %v", a, b, seq)
		}
	}

	// entry_added for the prompt's own user entry fires as soon as it is
	// committed, which is a step inside Prompt before it emits run_start
	// (see turn.go); run_start itself must still precede everything else
	// in the loop.
	if seq[len(seq)-1] != EventRunEnd {
		t.Fatalf("last event = %s, want run_end; sequence = %v", seq[len(seq)-1], seq)
	}
	mustBeforeFirst(EventRunStart, EventTurnStart)
	mustBeforeFirst(EventTurnStart, EventMessageStart)
	mustBeforeFirst(EventMessageStart, EventMessageEnd)
	mustBeforeFirst(EventMessageEnd, EventToolStart)
	mustBeforeFirst(EventToolStart, EventToolEnd)
	mustBeforeFirst(EventToolEnd, EventTurnEnd)
	mustBeforeFirst(EventTurnEnd, EventRunEnd)

	if countOf(seq, EventMessageUpdate) == 0 {
		t.Fatalf("expected at least one message_update; sequence = %v", seq)
	}
	// Exactly two turns (toolCall, then final text) in this script.
	if got := countOf(seq, EventTurnStart); got != 2 {
		t.Fatalf("turn_start count = %d, want 2; sequence = %v", got, seq)
	}
	if got := countOf(seq, EventTurnEnd); got != 2 {
		t.Fatalf("turn_end count = %d, want 2; sequence = %v", got, seq)
	}
}

func indexOf(seq []EventType, t EventType) int {
	for i, e := range seq {
		if e == t {
			return i
		}
	}
	return -1
}

func countOf(seq []EventType, t EventType) int {
	n := 0
	for _, e := range seq {
		if e == t {
			n++
		}
	}
	return n
}
