package harness

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/session"
)

// TestCompactionWritesEntry runs two ordinary prompt turns (so there is
// something on the branch to summarize), sets a tiny KeepRecentTokens
// budget so FindCutPoint has real history to cut, then calls Lane.Compact
// directly and asserts a session.EntryCompaction lands on the branch with
// a summary and the pre-compaction token count. It uses the real
// internal/compaction package end to end (compaction.Prepare +
// compaction.Compact), with the faux provider standing in for the
// summarization call — internal/compaction did not exist when this
// package's design started (see doc.go), but it landed before this test
// was written, so no fake Streamer is needed.
func TestCompactionWritesEntry(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first reply"
  - text: "second reply"
  - text: "a summary of the conversation so far"
`, []string{"bash"})
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 1})
	lane := rig.mustLane("main")

	if _, err := lane.Prompt(context.Background(), "first message", nil); err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	if _, err := lane.Prompt(context.Background(), "second message", nil); err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}

	var sawStart, sawEnd bool
	rig.H.Events().On(EventCompactionStart, func(Event) { sawStart = true })
	rig.H.Events().On(EventCompactionEnd, func(Event) { sawEnd = true })

	if err := lane.Compact(context.Background(), nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !sawStart || !sawEnd {
		t.Fatalf("compaction_start/end events: start=%v end=%v", sawStart, sawEnd)
	}

	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *session.Entry
	for i := range entries {
		if entries[i].Type == session.EntryCompaction {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no compaction entry found on branch after Compact")
	}
	if found.Summary == "" {
		t.Error("compaction entry has an empty summary")
	}
	if found.TokensBefore <= 0 {
		t.Errorf("compaction entry TokensBefore = %d, want > 0", found.TokensBefore)
	}
}

// TestAutoCompactionOnTinyWindow drives one prompt with compaction enabled
// and a KeepRecentTokens/ReserveTokens budget tiny enough, relative to the
// faux model's advertised context window, that ShouldCompact fires
// automatically between turns (see autoCompact in compaction.go, called
// from drive() after each turn). It asserts a compaction entry appears
// without any explicit Lane.Compact call.
func TestAutoCompactionOnTinyWindow(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "final"
  - text: "compaction summary"
  - text: "second reply"
  - text: "second compaction summary"
`, []string{"bash"})
	// ReserveTokens close to the faux model's ContextWindow (128000, pi-ai's
	// fauxProvider default, see internal/provider/faux) so ShouldCompact
	// trips after the first exchange's estimated tokens exceed
	// contextWindow-reserveTokens.
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 127992, KeepRecentTokens: 1})
	lane := rig.mustLane("main")

	// Two turns: the first is history the second's compaction can
	// summarise (a single turn is all "recent" and kept verbatim).
	for _, p := range []string{"go", "again"} {
		if _, err := lane.Prompt(context.Background(), p, nil); err != nil {
			t.Fatalf("Prompt %q: %v", p, err)
		}
	}

	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Type == session.EntryCompaction {
			return // found it
		}
	}
	t.Fatalf("no compaction entry found on branch; entries = %d", len(entries))
}
