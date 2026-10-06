package harness

import (
	"context"
	"encoding/json"
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
    text: "too late (cache attempt)"
    end_turn: true
  - delay: 3s
    text: "too late (serialized attempt)"
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
	// Compaction's first retry-loop attempt tries the cache-friendly path
	// (fastpath.go), then falls back to the serialized path when it too
	// stalls; both count as one "the model stopped responding" retry.
	// The retry-loop's second attempt succeeds on its own cache-path try
	// ("a summary" carries no tool call, so it is not a fallback), which
	// is why the script needs only one quick reply after the two slow
	// ones, not two.
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
    text: "late (attempt 1, cache)"
    end_turn: true
  - delay: 3s
    text: "late (attempt 1, serialized)"
    end_turn: true
  - delay: 3s
    text: "late again (attempt 2, cache)"
    end_turn: true
  - delay: 3s
    text: "late again (attempt 2, serialized)"
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

// TestCompactReusesTheLiveRequestPrefix asserts /compact sends the summary
// request with the live turn's own system prompt and messages, so a
// provider's prefix cache (Ollama's KV cache, Anthropic's prompt cache)
// is reused instead of the whole conversation being read again.
func TestCompactReusesTheLiveRequestPrefix(t *testing.T) {
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
	reqs := rig.Faux.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3 (two turns, one summary)", len(reqs))
	}
	live, summary := reqs[1], reqs[2]
	if summary.System != live.System {
		t.Errorf("summary request system prompt differs from the live turn's:\n got %.120q\nwant %.120q", summary.System, live.System)
	}
	var liveMsgs, sumMsgs []json.RawMessage
	if err := json.Unmarshal(live.Messages, &liveMsgs); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(summary.Messages, &sumMsgs); err != nil {
		t.Fatal(err)
	}
	if len(sumMsgs) <= len(liveMsgs) {
		t.Fatalf("summary request has %d messages, want the live turn's %d plus the reply and the summary request", len(sumMsgs), len(liveMsgs))
	}
	// cache_control marks where a breakpoint goes, which moves from turn to
	// turn; it is not part of the cached content, so it is compared without.
	for i := range liveMsgs {
		if withoutCacheControl(t, sumMsgs[i]) != withoutCacheControl(t, liveMsgs[i]) {
			t.Fatalf("summary request message %d differs from the live turn's:\n got %s\nwant %s", i, sumMsgs[i], liveMsgs[i])
		}
	}
}

// withoutCacheControl returns raw with every "cache_control" key removed.
func withoutCacheControl(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var strip func(any)
	strip = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			delete(x, "cache_control")
			for _, e := range x {
				strip(e)
			}
		case []any:
			for _, e := range x {
				strip(e)
			}
		}
	}
	strip(v)
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
