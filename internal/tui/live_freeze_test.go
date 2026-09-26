package tui

import (
	"strings"
	"testing"
	"time"
)

// newFreezeTestModel is newTestModelWithBridge (app_behaviour_test.go) with
// the bridge handle also returned, for tests that need it directly (none
// of these currently do, but keeping the same shape avoids a second nearly
// identical helper).
func newFreezeTestModel(t *testing.T) (Model, *Bridge, *fakeSink) {
	t.Helper()
	m, f := newTestModelWithBridge(t)
	return m, m.cfg.Bridge, f
}

// waitForNPrinted polls f.snapshot() until at least n lines have printed,
// or fails after a short deadline — Bridge.Commit only enqueues; the
// actual Println happens on run()'s own goroutine (bridge.go's doc
// comment). Distinct from app_behaviour_test.go's waitForPrinted (which
// waits for one block containing a given substring): these tests care
// about how many blocks landed and in what order, not just whether one
// particular block eventually shows up.
func waitForNPrinted(t *testing.T, f *fakeSink, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if printed, _ := f.snapshot(); len(printed) >= n {
			return printed
		}
		time.Sleep(time.Millisecond)
	}
	printed, _ := f.snapshot()
	t.Fatalf("timed out waiting for %d printed blocks, got %d: %v", n, len(printed), printed)
	return nil
}

// TestLiveFreeze_PlanFreezesBeforeNextCommit is the "live while last" rule's
// core case: a plan checklist goes live (MsgTodos), then something else
// commits (a tool call) — the plan must freeze and land in the transcript
// BEFORE that tool call's own block, not after it and not still live.
func TestLiveFreeze_PlanFreezesBeforeNextCommit(t *testing.T) {
	m, _, f := newFreezeTestModel(t)

	m.commit([]string{"first text block"})
	waitForNPrinted(t, f, 1)

	m.plan.Set([]TodoView{{Content: "step one", Status: TodoInProgressStatus}})
	if got := m.plan.Live(); len(got) != 1 {
		t.Fatalf("plan should be live after Set, got %d items", len(got))
	}

	// Nothing else has committed yet: the plan must still be live, not
	// frozen, and must not have printed anything of its own.
	if printed, _ := f.snapshot(); len(printed) != 1 {
		t.Fatalf("plan should not commit on its own; printed = %v", printed)
	}

	m.commit([]string{"a diff block"})
	printed := waitForNPrinted(t, f, 3) // first text, frozen plan, diff block

	planIdx, diffIdx := -1, -1
	for i, p := range printed {
		if strings.Contains(p, "step one") {
			planIdx = i
		}
		if strings.Contains(p, "a diff block") {
			diffIdx = i
		}
	}
	if planIdx == -1 {
		t.Fatalf("frozen plan never committed: %v", printed)
	}
	if diffIdx == -1 {
		t.Fatalf("diff block never committed: %v", printed)
	}
	if planIdx >= diffIdx {
		t.Errorf("plan committed at %d, diff at %d — plan must freeze BEFORE the diff", planIdx, diffIdx)
	}
	// And it must have left the live region: rendering the plan live now
	// (as liveLines' renderPlanLive would) reports nothing.
	if got := m.plan.Live(); len(got) != 0 {
		t.Errorf("plan should be frozen (not live) after freezing, got %d items", len(got))
	}
}

// TestLiveFreeze_NewTodoWriteMakesPlanLiveAgain: once frozen, a later
// MsgTodos (a fresh todo_write) makes the plan live again with its new
// state — this pass's "re-emits the block" behaviour.
func TestLiveFreeze_NewTodoWriteMakesPlanLiveAgain(t *testing.T) {
	m, _, f := newFreezeTestModel(t)

	m.plan.Set([]TodoView{{Content: "step one", Status: TodoInProgressStatus}})
	m.commit([]string{"something else"}) // freezes the plan
	waitForNPrinted(t, f, 2)
	if got := m.plan.Live(); len(got) != 0 {
		t.Fatalf("plan should be frozen, got %d live items", len(got))
	}

	nm, _ := m.update(MsgTodos{Items: []TodoView{{Content: "step two", Status: TodoInProgressStatus}}})
	m = nm.(Model)

	got := m.plan.Live()
	if len(got) != 1 || got[0].Content != "step two" {
		t.Fatalf("a fresh MsgTodos should make the plan live again with its new state, got %+v", got)
	}
}

// TestLiveFreeze_FinishTurnCommitsPlanOnce: finishTurn's own explicit
// freeze must not double-commit a plan the automatic hook already froze
// mid-turn, and must commit exactly once for a plan that never triggered
// the hook (nothing else committed after it went live).
func TestLiveFreeze_FinishTurnCommitsPlanOnce(t *testing.T) {
	m, _, f := newFreezeTestModel(t)

	m.plan.Set([]TodoView{{Content: "only step", Status: TodoInProgressStatus}})
	// Nothing else commits mid-turn: finishTurn is the only thing that
	// ever freezes this plan.
	nm := m.finishTurn(msgTurnResult{})
	m = nm

	printed := waitForNPrinted(t, f, 1)
	count := 0
	for _, p := range printed {
		if strings.Contains(p, "only step") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("plan should commit exactly once from finishTurn, got %d occurrences in: %v", count, printed)
	}
	if got := m.plan.Live(); len(got) != 0 {
		t.Errorf("plan should be reset (and frozen) after finishTurn, got %d live items", len(got))
	}

	// Re-run with something committing mid-turn first (the hook freezes it
	// early); finishTurn's own explicit freeze must be a no-op then, not a
	// second commit.
	m2, _, f2 := newFreezeTestModel(t)
	m2.plan.Set([]TodoView{{Content: "frozen early", Status: TodoInProgressStatus}})
	m2.commit([]string{"mid-turn block"}) // freezes the plan right here
	waitForNPrinted(t, f2, 2)
	m2 = m2.finishTurn(msgTurnResult{})
	time.Sleep(20 * time.Millisecond) // let the committer drain; nothing more should arrive
	printed2, _ := f2.snapshot()
	count2 := 0
	for _, p := range printed2 {
		if strings.Contains(p, "frozen early") {
			count2++
		}
	}
	if count2 != 1 {
		t.Fatalf("plan frozen mid-turn must not commit again from finishTurn, got %d occurrences in: %v", count2, printed2)
	}
}
