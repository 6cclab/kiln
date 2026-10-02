package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/compaction"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/session"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
)

// fauxRequestTokens is a recorded request's size by the chars/4 estimate:
// system prompt, tool schemas and messages, as sent on the wire.
func fauxRequestTokens(r tkfaux.Request) int {
	return (len(r.System) + len(r.Messages) + len(fmt.Sprint(r.Tools)) + 3) / 4
}

func requestsFor(srv *tkfaux.Server, model string) []tkfaux.Request {
	var out []tkfaux.Request
	for _, r := range srv.Requests() {
		if r.Model == model {
			out = append(out, r)
		}
	}
	return out
}

// longText is about n tokens of plain prose.
func longText(n int) string {
	return strings.Repeat("The quick brown fox jumps over the lazy dog. ", n*4/45)
}

const smallWindow = 32768 // faux-2's context window

func smallTier() CompactionSettings {
	return CompactionSettings{Enabled: true, ReserveTokens: 3276, KeepRecentTokens: 8192}
}

// TestModelSwitchNextTurnFitsSmallWindow: ~60k tokens of conversation on
// faux-1 (128k window), then a switch to faux-2 (32k). The next turn must
// compact with faux-2 in requests that each fit its window, and the turn's
// own request must fit too. Before the fix the summary request carried
// everything older than the retained tail, ~52k tokens, in one request to
// the 32k model.
func TestModelSwitchNextTurnFitsSmallWindow(t *testing.T) {
	var script strings.Builder
	script.WriteString("models:\n  faux-1:\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&script, "    - text: %q\n      end_turn: true\n", fmt.Sprintf("reply %d: %s", i, longText(6000)))
	}
	script.WriteString("  faux-2:\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&script, "    - text: %q\n      end_turn: true\n", fmt.Sprintf("summary part %d", i+1))
	}
	rig := newTestRig(t, script.String(), []string{"bash"})
	rig.H.SetCompactionSettings(CompactionSettings{Enabled: true, ReserveTokens: 12800, KeepRecentTokens: 32000})
	lane := rig.mustLane("main")
	for i := 0; i < 10; i++ {
		if _, err := lane.Prompt(context.Background(), fmt.Sprintf("question %d", i), nil); err != nil {
			t.Fatalf("turn %d: %v", i, err)
		}
	}

	if err := lane.SetModel(session.ModelRef{Provider: fauxprovider.ProviderID, ModelID: fauxprovider.ModelID2}, ""); err != nil {
		t.Fatal(err)
	}
	rig.H.SetCompactionSettings(smallTier())
	res, err := lane.Prompt(context.Background(), "and now?", nil)
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("turn on the small model: status %q, err %v", res.Status, err)
	}

	small := requestsFor(rig.Faux, fauxprovider.ModelID2)
	if len(small) < 3 {
		t.Fatalf("%d requests to faux-2; want a compaction in parts plus the turn", len(small))
	}
	for i, r := range small {
		if n := fauxRequestTokens(r); n > smallWindow {
			t.Errorf("faux-2 request %d is ~%d tokens, over its %d-token window", i+1, n, smallWindow)
		}
	}
	last := small[len(small)-1]
	if !strings.Contains(string(last.Messages), "summary part") {
		t.Error("the turn after compacting was not sent the summary")
	}
}

// TestOverflowRefusedWhenNothingToCompact: one message larger than the
// window, with nothing older to summarise, is refused before sending.
func TestOverflowRefusedWhenNothingToCompact(t *testing.T) {
	rig := newTestRig(t, "models:\n  faux-2:\n    - text: \"never\"\n", []string{"bash"})
	lane := rig.mustLane("main")
	if err := lane.SetModel(session.ModelRef{Provider: fauxprovider.ProviderID, ModelID: fauxprovider.ModelID2}, ""); err != nil {
		t.Fatal(err)
	}
	rig.H.SetCompactionSettings(smallTier())
	res, err := lane.Prompt(context.Background(), longText(40000), nil)
	var overflow *ContextOverflowError
	if !errors.As(err, &overflow) && !errors.As(res.Error, &overflow) {
		t.Fatalf("status %q, err %v, result err %v; want a ContextOverflowError", res.Status, err, res.Error)
	}
	if n := len(rig.Faux.Requests()); n != 0 {
		t.Errorf("%d requests sent for a prompt that cannot fit", n)
	}
}

// TestProviderOverflowCompactsAndRetriesOnce: a provider that refuses a
// request as too long gets one compaction and one retry, as pi and Claude
// Code do.
func TestProviderOverflowCompactsAndRetriesOnce(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first reply"
    end_turn: true
  - text: "second reply"
    end_turn: true
  - error: {status: 400, type: invalid_request_error, message: "prompt is too long: 140000 tokens > 128000 maximum"}
  - text: "a summary"
    end_turn: true
  - text: "third reply"
`, []string{"bash"})
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 1})
	lane := rig.mustLane("main")
	for _, p := range []string{"one", "two", "three"} {
		res, err := lane.Prompt(context.Background(), p, nil)
		if err != nil || res.Status != StatusCompleted {
			t.Fatalf("prompt %q: status %q, err %v, result err %v", p, res.Status, err, res.Error)
		}
	}
	reqs := rig.Faux.Requests()
	if len(reqs) != 5 {
		t.Fatalf("%d requests, want 5 (two turns, the refused one, the summary, the retry)", len(reqs))
	}
	if !strings.Contains(string(reqs[4].Messages), "a summary") {
		t.Error("the retry was not sent the compacted conversation")
	}
}
