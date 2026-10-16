package cli

import (
	"testing"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// A turn's cost lands on the model that answered it, not the session's:
// on a routed lane faux-2 can answer while the session names another
// model. Before creditUsage read EventUsage.UsageModel, every turn was
// filed under the session's model.
func TestCreditUsageGoesToTheModelThatAnswered(t *testing.T) {
	by := map[string]msg.Usage{}
	add := func(key string, u msg.Usage) { by[key] = by[key].Add(u) }

	answered := msg.Usage{Input: 100, Output: 10, Cost: msg.Cost{Total: 0.00045}}
	creditUsage(harness.Event{Type: harness.EventUsage, UsageRow: &answered,
		UsageModel: &session.ModelRef{Provider: "faux", ModelID: "faux-2"}}, "kiln", "auto", add)
	if got := by["faux/faux-2"]; got.Input != 100 || got.Cost.Total != 0.00045 {
		t.Errorf("faux/faux-2 = %+v, want the turn's usage", got)
	}
	if _, ok := by["kiln/auto"]; ok {
		t.Errorf("the session's model was credited: %+v", by)
	}

	// An event that names no model (an older emitter) still goes to the
	// session's model; a side call to its own label.
	plain := msg.Usage{Input: 5}
	creditUsage(harness.Event{Type: harness.EventUsage, UsageRow: &plain}, "faux", "faux-1", add)
	side := msg.Usage{Input: 7}
	creditUsage(harness.Event{Type: harness.EventUsage, SideUsage: &side, UsageSource: "auto-mode classifier · faux/faux-1"}, "faux", "faux-1", add)
	if by["faux/faux-1"].Input != 5 || by["auto-mode classifier · faux/faux-1"].Input != 7 {
		t.Errorf("usage by model = %+v", by)
	}
}
