package tui

import "testing"

func TestFrameChannelEvent(t *testing.T) {
	got := frameChannelEvent("design-studio", "Make the header sticky", map[string]any{
		"project": "margin",
		"request": "c_1",
		"event":   "request",
		"status":  "open",
		"page":    "board / home",
		"missing": "", // empty string is skipped
	})
	want := `<channel source="design-studio" project="margin" request="c_1" event="request" status="open" page="board / home">
Make the header sticky
</channel>`
	if got != want {
		t.Errorf("frame mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Meta attributes render in a fixed order regardless of map order, and only
// present keys appear.
func TestFrameChannelEvent_DeterministicAndSparse(t *testing.T) {
	got := frameChannelEvent("design-studio", "The owner answered D3", map[string]any{
		"decision": "d_3",
		"event":    "decision",
		"project":  "margin",
	})
	want := `<channel source="design-studio" project="margin" decision="d_3" event="decision">
The owner answered D3
</channel>`
	if got != want {
		t.Errorf("frame mismatch:\n got: %q\nwant: %q", got, want)
	}
}
