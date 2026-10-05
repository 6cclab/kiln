package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/andrepato/harness/internal/session"
)

// failOnOpMetaDeleteStorage wraps a session.Storage and fails exactly the
// first Commit whose writes include a delete of pi.op.meta/<id> — the
// write finishOperation (turn.go) always includes in its one terminal
// commit. It simulates a transient disk error on that specific commit
// (e.g. AppendTransaction's OpenFile/Write failing) without touching any
// other Commit call. Every read and every other Commit passes through to
// the wrapped storage unchanged.
type failOnOpMetaDeleteStorage struct {
	session.Storage
	failed bool
}

func (f *failOnOpMetaDeleteStorage) Commit(writes []session.Write) (session.CommitResult, error) {
	if !f.failed && commitsOpMetaDelete(writes) {
		f.failed = true
		return session.CommitResult{}, errors.New("fake disk error: commit failed")
	}
	return f.Storage.Commit(writes)
}

func commitsOpMetaDelete(writes []session.Write) bool {
	for _, w := range writes {
		vw, ok := w.(session.ValueWrite)
		if ok && vw.Namespace == session.NamespaceOpMeta && vw.Op == "delete" {
			return true
		}
	}
	return false
}

// TestFinishOperationCommitFailureIsSurfaced reproduces go-audit finding 3
// ("finishOperation discards the error of the commit that clears the
// operation"): the terminal commit that deletes pi.op.meta/pi.op.state,
// writes pi.result and clears pi.lane.state.currentOperationId fails, and
// asserts the failure is no longer silently swallowed.
func TestFinishOperationCommitFailureIsSurfaced(t *testing.T) {
	script := `
model: faux-1
steps:
  - text: "done"
`
	var fake *failOnOpMetaDeleteStorage
	rig := newTestRigWithStorage(t, script, nil, func(s session.Storage) session.Storage {
		fake = &failOnOpMetaDeleteStorage{Storage: s}
		return fake
	})
	lane := rig.mustLane("main")

	var faults []Event
	unsub := rig.H.Events().On(EventFault, func(ev Event) { faults = append(faults, ev) })
	defer unsub()

	result, err := lane.Prompt(context.Background(), "go", nil)

	if !fake.failed {
		t.Fatal("test bug: the fake never saw the finishOperation commit (pi.op.meta delete)")
	}
	// The turn itself genuinely completed — the assistant replied with no
	// tool calls — so the status must still say so.
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed (the turn itself succeeded; only the bookkeeping commit failed)", result.Status)
	}
	// But the failure of the commit that was supposed to record that must
	// not vanish: it has to come back on the result and/or the error
	// return (PromptAs returns result, result.Error).
	if result.Error == nil {
		t.Fatal("RunResult.Error is nil: the finishOperation commit failure was swallowed")
	}
	if err == nil {
		t.Fatal("Prompt's error return is nil: the finishOperation commit failure was swallowed")
	}
	if len(faults) == 0 {
		t.Fatal("no EventFault was emitted for the failed finishOperation commit")
	}

	// Storage.Commit only applies in memory after a successful append, so
	// a failed commit means NONE of its four writes landed: the lane
	// should still show the operation as current, matching what
	// PendingOperation (resume.go) already assumes a non-nil
	// currentOperationId means.
	st, err2 := lane.laneState()
	if err2 != nil {
		t.Fatal(err2)
	}
	if st.CurrentOperationID == nil {
		t.Fatal("lane no longer shows the operation as current, but its clearing commit failed")
	}
}

// TestPendingOperationIgnoresResultAlreadyRecorded exercises
// PendingOperation's defensive check directly (resume.go): even if
// pi.lane.state.currentOperationId were still set for an operation that
// already has a pi.result recorded (a divergence that should be
// impossible today, since finishOperation writes both in the same atomic
// Commit — but would be a real resume-replays-a-finished-turn bug if a
// future change ever split them, or some other path left the clear
// behind), PendingOperation must treat that operation as already finished
// rather than handing it back for Resume to replay.
func TestPendingOperationIgnoresResultAlreadyRecorded(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "unused"
`, nil)
	lane := rig.mustLane("main")

	operationID := lane.newID()

	resultW, err := session.SetValue(opResultAddr(operationID), OpResult{
		EndedAt: 1, Kind: "run", OperationID: operationID, StartedAt: 1, Status: StatusCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	laneState, err := lane.laneState()
	if err != nil {
		t.Fatal(err)
	}
	laneState.CurrentOperationID = &operationID
	laneStateW, err := session.SetValue(session.LaneStateValue("main"), laneState)
	if err != nil {
		t.Fatal(err)
	}
	// Hand-write exactly the divergent state: a result already recorded
	// for operationID, but pi.lane.state.currentOperationId still
	// pointing at it (as if the clear half of finishOperation's commit
	// never landed).
	if _, err := rig.Storage.Commit([]session.Write{resultW, laneStateW}); err != nil {
		t.Fatal(err)
	}

	if id, ok := lane.PendingOperation(); ok {
		t.Fatalf("PendingOperation() = (%q, true), want ok=false: operation %s already has a recorded result", id, operationID)
	}
}
