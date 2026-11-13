package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

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

// A channel push that arrives mid-turn shows once: in the live region's
// queued rows, like a typed follow-up, and nowhere in scrollback until the
// lane drains it. It also committed its own "queued" block, so every push
// read twice.
func TestChannelEvent_QueuedWhileBusyShowsOnce(t *testing.T) {
	m, f := newTestModelWithBridge(t)
	m.busy = true
	m = updateModel(t, m, MsgChannelEvent{Source: "design-studio", Content: "Remove this copy", Meta: map[string]any{"request": "r1"}})

	if len(m.queued) != 1 {
		t.Fatalf("m.queued = %q, want the one push", m.queued)
	}
	m.commit([]string{"sentinel-after-push"})
	waitForPrinted(t, f, "sentinel-after-push")
	printed, _ := f.snapshot()
	for _, p := range printed {
		if strings.Contains(p, "Remove this copy") {
			t.Fatalf("the queued push was committed to scrollback before the lane drained it: %q", p)
		}
	}
	if n := strings.Count(ansi.Strip(strings.Join(m.liveTail(m.contentWidth()), "\n")), "Remove this copy"); n != 1 {
		t.Errorf("live region shows the push %d times, want 1", n)
	}

	// The lane's drain commits it once, as a delivered `you` block.
	m = updateModel(t, m, MsgQueue{Len: 0})
	m.commit([]string{"sentinel-after-drain"})
	waitForPrinted(t, f, "sentinel-after-drain")
	printed, _ = f.snapshot()
	n := 0
	for _, p := range printed {
		n += strings.Count(p, "Remove this copy")
	}
	if n != 1 {
		t.Errorf("after the drain the push is in scrollback %d times, want 1", n)
	}
}
