package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/session"
)

const compactDecisionScript = `
model: faux-1
steps:
  - text: "first reply"
    end_turn: true
  - text: "second reply"
    end_turn: true
  - text: "the summary"
    end_turn: true
`

func hasCompaction(t *testing.T, lane *Lane) bool {
	t.Helper()
	entries, _ := lane.FindEntries(context.Background())
	for _, e := range entries {
		if e.Type == session.EntryCompaction {
			return true
		}
	}
	return false
}

// A handler that answers Skip leaves the conversation as it is, and sees the
// trigger, the instructions and the conversation the compaction would read.
func TestCompactDecisionSkipLeavesTheConversation(t *testing.T) {
	rig := newTestRig(t, compactDecisionScript, []string{"bash"})
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 100000})
	var got CompactInfo
	rig.H.Hooks().OnCompactDecision(func(_ context.Context, info CompactInfo) (CompactVerdict, error) {
		got = info
		return CompactVerdict{Skip: "not now"}, nil
	})
	lane := rig.mustLane("main")
	for _, p := range []string{"one", "two"} {
		if _, err := lane.Prompt(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	custom := "keep the plan"
	if err := lane.Compact(WithCompactTrigger(context.Background(), "plugin"), &custom); err != nil {
		t.Fatal(err)
	}
	if hasCompaction(t, lane) {
		t.Error("a compaction ran although a handler said Skip")
	}
	if got.Trigger != "plugin" || got.Instructions != "keep the plan" || len(got.Messages) == 0 {
		t.Errorf("handler saw %+v, want trigger plugin, the instructions and the conversation", got)
	}
}

// Without a Skip the compaction goes ahead, a manual one reporting "manual",
// and a handler's rewritten instructions replace the asked ones.
func TestCompactDecisionRewritesInstructions(t *testing.T) {
	rig := newTestRig(t, compactDecisionScript, []string{"bash"})
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 100000})
	var trigger string
	rig.H.Hooks().OnCompactDecision(func(_ context.Context, info CompactInfo) (CompactVerdict, error) {
		trigger = info.Trigger
		ins := strings.ToUpper(info.Instructions)
		return CompactVerdict{Instructions: &ins}, nil
	})
	lane := rig.mustLane("main")
	for _, p := range []string{"one", "two"} {
		if _, err := lane.Prompt(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := lane.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if trigger != "manual" {
		t.Errorf("trigger = %q, want manual", trigger)
	}
	if !hasCompaction(t, lane) {
		t.Error("no compaction ran although no handler skipped it")
	}
}
