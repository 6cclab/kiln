package harness

import (
	"context"
	"testing"

	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/session"
)

// ContextTokens is the lane's one context figure: the measured size of
// the last request while the model that measured it is still in use, and
// the chars/4 estimate the turn loop checks requests with once another
// model takes over (its tokenizer counts the same text differently).
func TestContextTokensFollowsTheModel(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "a reply"
    usage: {input: 90000, output: 6000}
`, []string{"bash"})
	lane := rig.mustLane("main")
	if _, err := lane.Prompt(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	got, err := lane.ContextTokens()
	if err != nil {
		t.Fatal(err)
	}
	if got != 96000 {
		t.Fatalf("on the measuring model: ContextTokens = %d, want the measured 96000", got)
	}

	if err := lane.SetModel(session.ModelRef{Provider: fauxprovider.ProviderID, ModelID: fauxprovider.ModelID2}, ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := rig.Storage.ScanBranch(session.BranchScan{Start: mustTip(t, lane), Order: "oldestFirst"})
	want := lane.requestTokens(entriesToTranscript(entries))
	got, _ = lane.ContextTokens()
	if got != want || got >= 96000 {
		t.Fatalf("after a switch: ContextTokens = %d, want the request estimate %d", got, want)
	}
}

func mustTip(t *testing.T, l *Lane) string {
	t.Helper()
	tip, ok := l.GetTipID()
	if !ok {
		t.Fatal("no tip")
	}
	return tip
}
