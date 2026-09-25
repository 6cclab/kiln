package agent

import (
	"context"
	"testing"
)

// TestResumeIncomplete_NoopOnCleanSession guards the idempotence of the
// crash-resume path: a session with no in-flight operation resumes
// nothing and emits no notice, and a nil session is a no-op rather than a
// panic. The interrupted-operation case is driven end to end by
// test/e2e/session_gap_test.go's TestSession_CrashMidToolThenResume.
func TestResumeIncomplete_NoopOnCleanSession(t *testing.T) {
	if resumed, err := ResumeIncomplete(context.Background(), nil, nil); resumed || err != nil {
		t.Fatalf("nil session: resumed=%v err=%v, want false, nil", resumed, err)
	}

	_, started, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n", nil)
	var notices []string
	resumed, err := ResumeIncomplete(context.Background(), started, func(s string) { notices = append(notices, s) })
	if err != nil || resumed {
		t.Fatalf("clean session: resumed=%v err=%v, want false, nil", resumed, err)
	}
	if len(notices) != 0 {
		t.Fatalf("clean session emitted notices: %v", notices)
	}
}
