package harness

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

// A side call's usage (auto mode's classifier) goes into the session's
// totals, so the footer's cost and a resumed session's count it, and
// UsageByModel files it under its own source for /cost.
func TestRecordSideUsageCountsInTotals(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n    usage: {input: 100, output: 10}\n", []string{"bash"})
	lane := rig.mustLane("main")
	if _, err := lane.Prompt(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	var ev *Event
	rig.H.Events().On(EventUsage, func(e Event) { ev = &e })
	side := msg.Usage{Input: 5000, CacheWrite: 7000, Output: 30}
	side.Cost.Total = 1.25
	if err := rig.H.RecordSideUsage("auto-mode classifier · p/m", side); err != nil {
		t.Fatal(err)
	}
	if ev == nil || ev.UsageRow != nil || ev.SideUsage == nil || ev.UsageTotals == nil {
		t.Fatalf("event = %+v, want a side usage event with totals and no context row", ev)
	}
	if got := rig.H.Stats().Usage; got.Input != 5100 || got.Cost.Total < 1.25 {
		t.Fatalf("session totals = %+v, want the side call included", got)
	}
	by := rig.H.UsageByModel("fallback")
	if u := by["auto-mode classifier · p/m"]; u.Input != 5000 {
		t.Fatalf("UsageByModel = %+v, want the side call under its source", by)
	}
	// The conversation's context is not the side call's.
	if n, _ := lane.ContextTokens(); n != 110 {
		t.Fatalf("ContextTokens = %d after a side call, want the conversation's 110", n)
	}
}
