package harness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/session"
)

// compactionEvents records the retry and fault events of one run.
type compactionEvents struct {
	mu     sync.Mutex
	retry  []Event
	faults []error
}

func watchCompaction(h *Harness) *compactionEvents {
	c := &compactionEvents{}
	h.Events().On(EventCompactionRetry, func(ev Event) {
		c.mu.Lock()
		c.retry = append(c.retry, ev)
		c.mu.Unlock()
	})
	h.Events().On(EventFault, func(ev Event) {
		c.mu.Lock()
		c.faults = append(c.faults, ev.Err)
		c.mu.Unlock()
	})
	return c
}

// An automatic compaction whose summary request stalls is sent again, the
// retry is announced (for the progress row), and when the retry succeeds
// no failure is left behind.
func TestAutoCompactionRetriesAStallAndLeavesNoFault(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first reply"
    end_turn: true
  - delay: 3s
    text: "a summary too late"
    end_turn: true
  - text: "a summary"
    end_turn: true
  - text: "second reply"
    end_turn: true
`, []string{"bash"})
	rig.H.opts.StallFirstEvent = func(int) time.Duration { return 300 * time.Millisecond }
	lane := rig.mustLane("main")
	if _, err := lane.Prompt(context.Background(), "first message", nil); err != nil {
		t.Fatal(err)
	}
	// Close enough to the faux window that the next prompt compacts first.
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 127992, KeepRecentTokens: 1})
	seen := watchCompaction(rig.H)

	res, err := lane.Prompt(context.Background(), "second message", nil)
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("status %q err %v", res.Status, err)
	}
	if len(seen.retry) != 1 || seen.retry[0].Attempt != 2 || seen.retry[0].RetryError != "the model stopped responding" {
		t.Fatalf("retry events = %+v, want one announcing attempt 2 because the model stopped responding", seen.retry)
	}
	if len(seen.faults) != 0 {
		t.Fatalf("faults = %v, want none after a compaction that recovered", seen.faults)
	}
	entries, _ := lane.FindEntries(context.Background())
	found := false
	for _, e := range entries {
		if e.Type == session.EntryCompaction && strings.Contains(e.Summary, "a summary") && !strings.Contains(e.Summary, "too late") {
			found = true
		}
	}
	if !found {
		t.Fatal("no compaction entry with the retry's summary")
	}
}

// When the retry fails too, and the request fits without compacting, the
// failure is reported once, in plain words, and the turn still runs.
func TestAutoCompactionFailureReportedInPlainWords(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first reply"
    end_turn: true
  - delay: 3s
    text: "late"
    end_turn: true
  - delay: 3s
    text: "late again"
    end_turn: true
  - text: "second reply"
    end_turn: true
`, []string{"bash"})
	rig.H.opts.StallFirstEvent = func(int) time.Duration { return 300 * time.Millisecond }
	lane := rig.mustLane("main")
	if _, err := lane.Prompt(context.Background(), "first message", nil); err != nil {
		t.Fatal(err)
	}
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 127992, KeepRecentTokens: 1})
	seen := watchCompaction(rig.H)

	res, err := lane.Prompt(context.Background(), "second message", nil)
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("status %q err %v", res.Status, err)
	}
	if len(seen.faults) != 1 {
		t.Fatalf("faults = %v, want exactly one", seen.faults)
	}
	var failed *CompactionFailedError
	if !errors.As(seen.faults[0], &failed) {
		t.Fatalf("fault %v is not a CompactionFailedError", seen.faults[0])
	}
	if got := seen.faults[0].Error(); got != "Auto-compaction failed: the model stopped responding. The conversation is as it was" {
		t.Fatalf("fault text = %q", got)
	}
}

// An explicit /compact summarises even when the automatic rule would keep
// the whole conversation as recent: asked for, it compacts, as Claude
// Code's /compact does. (It said "Nothing to compact yet" at 68% full.)
func TestManualCompactSummarisesRecentTurns(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first reply"
    end_turn: true
  - text: "second reply"
    end_turn: true
  - text: "the summary"
    end_turn: true
`, []string{"bash"})
	// Everything fits in keep-recent: the automatic rule summarises nothing.
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 100000})
	lane := rig.mustLane("main")
	for _, p := range []string{"one", "two"} {
		if _, err := lane.Prompt(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := lane.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := lane.FindEntries(context.Background())
	for _, e := range entries {
		if e.Type == session.EntryCompaction && strings.Contains(e.Summary, "the summary") {
			return
		}
	}
	t.Fatal("/compact wrote no compaction entry for a conversation of recent turns")
}
