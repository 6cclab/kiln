package harness

import "testing"

// TestLaneToolSchemaTokens covers the measured Tools segment /context
// reports: the figure must come from the lane's own active tool schemas,
// scale with how many are active, and be zero when none are. Before this
// was wired, /context fell back to budget.ToolStrategyCost — a planning
// ceiling per strategy that, in a real session, overshot the entire
// measured context and drove the Conversation segment to zero.
func TestLaneToolSchemaTokens(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n", nil)

	l, err := rig.H.Lane("main")
	if err != nil {
		t.Fatalf("Lane: %v", err)
	}

	names := rig.H.toolSet().Names()
	if len(names) < 2 {
		t.Fatalf("need at least 2 registered tools, got %d", len(names))
	}

	if err := l.SetActiveTools([]string{}); err != nil {
		t.Fatalf("SetActiveTools(none): %v", err)
	}
	none, err := l.ToolSchemaTokens()
	if err != nil {
		t.Fatalf("ToolSchemaTokens: %v", err)
	}
	if none != 0 {
		t.Errorf("ToolSchemaTokens with no active tools = %d, want 0", none)
	}

	if err := l.SetActiveTools(names[:1]); err != nil {
		t.Fatalf("SetActiveTools(one): %v", err)
	}
	one, err := l.ToolSchemaTokens()
	if err != nil {
		t.Fatalf("ToolSchemaTokens: %v", err)
	}
	if one <= 0 {
		t.Fatalf("ToolSchemaTokens with one active tool = %d, want > 0", one)
	}

	if err := l.SetActiveTools(names[:2]); err != nil {
		t.Fatalf("SetActiveTools(two): %v", err)
	}
	two, err := l.ToolSchemaTokens()
	if err != nil {
		t.Fatalf("ToolSchemaTokens: %v", err)
	}
	if two <= one {
		t.Errorf("ToolSchemaTokens: two tools = %d, one = %d, want it to grow with the active set", two, one)
	}
}
