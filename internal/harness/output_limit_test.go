package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

// A reply that stops at the output token limit with no tool call is
// continued, as Claude Code does: Claude Code's resume message is
// committed as a marked user message and the model is asked again in the
// same run. After maxOutputLimitRecoveries continuations the run ends
// with a fault saying why.
func TestOutputLimitContinuesTheReply(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "part one"
    stop_reason: max_tokens
    end_turn: true
  - text: "part two"
    end_turn: true
`, []string{"bash"})
	lane := rig.mustLane("main")
	var faults []string
	rig.H.Events().On(EventFault, func(ev Event) { faults = append(faults, ev.Err.Error()) })

	res, err := lane.Prompt(context.Background(), "write it all", nil)
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("Prompt = %+v, %v; want completed", res, err)
	}
	reqs := rig.Faux.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model requests, want 2 (the cut reply, then its continuation)", len(reqs))
	}
	var second []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(reqs[1].Messages, &second); err != nil {
		t.Fatal(err)
	}
	last := second[len(second)-1]
	if last.Role != "user" || !strings.Contains(string(last.Content), "Output token limit hit. Resume directly") {
		t.Fatalf("second request ends with %s %s, want the resume message", last.Role, last.Content)
	}
	var marked int
	entries, _ := lane.FindEntries(context.Background())
	for _, e := range entries {
		if m, ok := e.Message.(msg.UserMessage); ok && m.KilnHook == outputLimitSource {
			marked++
		}
	}
	if marked != 1 {
		t.Errorf("%d continuation messages marked %q on the branch, want 1", marked, outputLimitSource)
	}
	if len(faults) != 0 {
		t.Errorf("faults = %q, want none when the continuation finished the reply", faults)
	}
}

// Past maxOutputLimitRecoveries the run stops asking and reports it; a
// reply cut with no text gets the while-reasoning message instead.
func TestOutputLimitGivesUpWithAFault(t *testing.T) {
	step := "  - thinking: \"still planning\"\n    stop_reason: max_tokens\n    end_turn: true\n"
	rig := newTestRig(t, "model: faux-1\nsteps:\n"+strings.Repeat(step, maxOutputLimitRecoveries+1), []string{"bash"})
	lane := rig.mustLane("main")
	var faults []string
	rig.H.Events().On(EventFault, func(ev Event) { faults = append(faults, ev.Err.Error()) })

	res, err := lane.Prompt(context.Background(), "plan it", nil)
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("Prompt = %+v, %v; want completed", res, err)
	}
	reqs := rig.Faux.Requests()
	if len(reqs) != maxOutputLimitRecoveries+1 {
		t.Fatalf("%d model requests, want %d", len(reqs), maxOutputLimitRecoveries+1)
	}
	if !strings.Contains(string(reqs[1].Messages), "while you were still reasoning") {
		t.Errorf("a reply cut with no text was not continued with the while-reasoning message: %s", reqs[1].Messages)
	}
	if len(faults) != 1 || !strings.Contains(faults[0], "stopped at the output token limit") {
		t.Errorf("faults = %q, want one saying the reply kept stopping at the limit", faults)
	}
}
