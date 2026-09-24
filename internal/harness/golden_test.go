package harness

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrepato/harness/internal/session"
)

// referenceFixture is pi's own recorded session for one
// prompt -> assistant(toolCall task) -> toolResult -> assistant(toolCall)
// cycle: the golden record this test's write sequence is measured against.
const referenceFixture = "../../testdata/sessions/2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1.jsonl"

// fixtureCycleLines are the 1-based line numbers (within the fixture,
// counting the header as line 1) that make up one full
// prompt -> assistant(toolCall) -> toolResult -> assistant cycle, with the
// interior runs of pi.pending.assistant_frame appends already known to be
// homogeneous (verified by direct inspection of the fixture; see the phase
// report). Runs are collapsed the same way readTransactionLines +
// collapseFrameRuns collapses this test's own output, so line count itself
// is never compared — only line 2 (lane creation), 3 (prompt), 4
// (checkpoint), 5 (assistant.ready), 6 (assistant.effect_pending), 7-31
// (first stream, collapsed), 32 (assistant toolUse commit), 33 (tool_args),
// 34 (pending outcome), 35 (toolResult commit), 36 (assistant.ready), 37
// (assistant.effect_pending), 38-243 (second stream, collapsed), 244
// (second assistant commit).
var fixtureCycleLineRanges = [][2]int{
	{2, 2}, {3, 3}, {4, 4}, {5, 5}, {6, 6}, {7, 31},
	{32, 32}, {33, 33}, {34, 34}, {35, 35}, {36, 36}, {37, 37},
	{38, 243}, {244, 244},
}

func loadFixtureCycleTuples(t *testing.T) [][]writeTuple {
	t.Helper()
	all := readTransactionLines(t, referenceFixture)
	// all[i] is fixture line i+2 (index 0 is the fixture's first
	// transaction line, immediately after the header).
	var out [][]writeTuple
	for _, r := range fixtureCycleLineRanges {
		// Every range in fixtureCycleLineRanges of length > 1 is a run of
		// pure frame-append lines (verified in the phase report); collapse
		// it to one representative line, matching collapseFrameRuns.
		idx := r[0] - 2
		out = append(out, all[idx])
	}
	return out
}

// TestGoldenWriteSequence drives Lane.Prompt against a scripted faux
// provider through one full prompt -> assistant(toolCall bash) ->
// toolResult -> assistant(final text) cycle, then asserts the session's
// written JSONL matches the reference fixture's write sequence
// structurally: the same ordered sequence of (kind, entry-type-or-namespace,
// op) per transaction line, runs of streamed-chunk frame appends collapsed
// on both sides (their count is a function of provider chunking, not of
// the FSM). Ids, timestamps, seqs and message contents are never compared.
func TestGoldenWriteSequence(t *testing.T) {
	script := `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	rig := newTestRig(t, script, []string{"bash", "read", "edit", "write"})
	lane := rig.mustLane("main")

	result, err := lane.Prompt(context.Background(), "Use bash to say hi", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("Prompt status = %q, want completed (err=%v)", result.Status, result.Error)
	}

	got := collapseFrameRuns(readTransactionLines(t, rig.Path))
	want := loadFixtureCycleTuples(t)

	// got is expected to have exactly the cycle's lines (14) plus the
	// final 4-item completion commit this test's simple script reaches
	// (the fixture's session keeps running well past this cycle, so
	// there is nothing at the fixture to compare the completion commit
	// against; it is asserted against its known shape separately below).
	if len(got) < len(want) {
		t.Fatalf("got %d transaction lines, want at least %d (cycle) + 1 (completion):\n%s",
			len(got), len(want), diffTuples(got, want))
	}

	for i := range want {
		if !tupleLineEqual(got[i], want[i]) {
			t.Fatalf("transaction line %d diverges from fixture line %d:\n  got:  %v\n  want: %v\n\nfull comparison:\n%s",
				i+1, fixtureCycleLineRanges[i][0], got[i], want[i], diffTuples(got, want))
		}
	}

	// The line after the cycle is this run's completion: op.meta delete,
	// op.state delete, pi.result set, pi.lane.state set — matching the
	// fixture's own final transaction shape (verified separately at the
	// end of the real fixture file), even though the fixture's own run
	// does not finish here.
	completion := got[len(want)]
	wantCompletion := []writeTuple{
		{Kind: "value", Sub: session.NamespaceOpMeta, Op: "delete"},
		{Kind: "value", Sub: session.NamespaceOpState, Op: "delete"},
		{Kind: "value", Sub: session.NamespaceResult, Op: "set"},
		{Kind: "value", Sub: session.NamespaceLaneState, Op: "set"},
	}
	if !tupleLineEqual(completion, wantCompletion) {
		t.Fatalf("completion transaction = %v, want %v", completion, wantCompletion)
	}
}

func tupleLineEqual(a, b []writeTuple) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func diffTuples(got, want [][]writeTuple) string {
	s := ""
	n := len(got)
	if len(want) > n {
		n = len(want)
	}
	for i := 0; i < n; i++ {
		var g, w []writeTuple
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		mark := " "
		if !tupleLineEqual(g, w) {
			mark = "!"
		}
		s += fmt.Sprintf("%s line %2d  got=%v  want=%v\n", mark, i+1, g, w)
	}
	return s
}
