package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/review"
)

func ev(t harness.EventType) harness.Event { return harness.Event{Type: t} }

// TestReviewSchedulerRunsWhenDueAndIdle: the review starts only after a run
// ends with enough prompts behind it, once the idle delay passes; a
// successful review resets the count.
func TestReviewSchedulerRunsWhenDueAndIdle(t *testing.T) {
	ran := make(chan struct{}, 4)
	s := &reviewScheduler{everyPrompts: 2, everyToolCalls: 100, idle: 10 * time.Millisecond, run: func(context.Context) bool { ran <- struct{}{}; return true }}
	defer s.close()

	s.observe(ev(harness.EventRunStart))
	s.observe(ev(harness.EventRunEnd))
	select {
	case <-ran:
		t.Fatal("review ran after one prompt, want two")
	case <-time.After(50 * time.Millisecond):
	}
	s.observe(ev(harness.EventRunStart))
	s.observe(ev(harness.EventRunEnd))
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("review did not run after two prompts")
	}
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.cancel == nil && s.prompts == 0 })

	// Tool calls count too.
	for i := 0; i < 100; i++ {
		s.observe(ev(harness.EventToolEnd))
	}
	s.observe(ev(harness.EventRunEnd))
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("review did not run after 100 tool calls")
	}
}

// TestReviewSchedulerYieldsToThePerson: a prompt typed while a review is
// waiting drops it, and one typed while it runs cancels it; either way the
// counts stay, so the review is tried again after a later run.
func TestReviewSchedulerYieldsToThePerson(t *testing.T) {
	started := make(chan struct{}, 2)
	cancelled := make(chan struct{}, 2)
	s := &reviewScheduler{everyPrompts: 1, everyToolCalls: 100, idle: 30 * time.Millisecond, run: func(ctx context.Context) bool {
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return true
	}}
	defer s.close()

	s.observe(ev(harness.EventRunStart))
	s.observe(ev(harness.EventRunEnd))
	s.observe(ev(harness.EventRunStart)) // before the idle delay passes
	select {
	case <-started:
		t.Fatal("a waiting review started after a new prompt")
	case <-time.After(100 * time.Millisecond):
	}

	s.observe(ev(harness.EventRunEnd))
	<-started
	s.observe(ev(harness.EventRunStart)) // while it runs
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("a running review was not cancelled by a new prompt")
	}
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.cancel == nil })
	s.mu.Lock()
	prompts := s.prompts
	s.mu.Unlock()
	if prompts != 3 {
		t.Errorf("prompts = %d after a cancelled review, want the 3 kept", prompts)
	}
}

// TestReviewSchedulerKeepsCountsWhenItCannotRun: a review with nothing to
// continue (the run ended mid tool step, say) keeps the counts, so the
// next run that ends tries again.
func TestReviewSchedulerKeepsCountsWhenItCannotRun(t *testing.T) {
	ran := make(chan struct{}, 4)
	s := &reviewScheduler{everyPrompts: 1, everyToolCalls: 100, idle: time.Millisecond, run: func(context.Context) bool { ran <- struct{}{}; return false }}
	defer s.close()
	s.observe(ev(harness.EventRunStart))
	s.observe(ev(harness.EventRunEnd))
	<-ran
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.cancel == nil })
	s.mu.Lock()
	prompts := s.prompts
	s.mu.Unlock()
	if prompts != 1 {
		t.Fatalf("prompts = %d after a review that could not run, want 1 kept", prompts)
	}
	s.observe(ev(harness.EventRunEnd))
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("not tried again after the next run")
	}
}

func testStores(dir string, store *memory.Store) learningStores {
	return learningStores{
		queue:     review.Queue{Dir: filepath.Join(dir, "proposals")},
		history:   review.History{Path: filepath.Join(dir, "skill-history.jsonl")},
		usage:     review.Usage{Path: filepath.Join(dir, "skill-usage.json")},
		skillsDir: filepath.Join(dir, "skills"),
		memory:    store,
		session:   "s1",
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPendingListsApprovesAndRejects: /pending previews each proposal; a
// skill approval writes the file and a memory approval saves the note,
// each leaving the queue; a rejection only removes it; unknown ids and a
// bad verb are reported, not acted on.
func TestPendingListsApprovesAndRejects(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	queue := review.Queue{Dir: filepath.Join(dir, "proposals")}
	skillsDir := filepath.Join(dir, "skills")
	store, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ls := testStores(dir, store)
	skill := "---\nname: release-flow\ndescription: Tag and publish a kiln release.\n---\n# Release flow\n"
	if _, err := queue.Add([]review.Proposal{
		{Kind: review.KindSkill, Skill: "release-flow", File: "SKILL.md", Content: skill, Why: "walked through it"},
		{Kind: review.KindMemory, Scope: "/proj", Key: "check-first", Type: "preference", Text: "Run make check first.", Evidence: "The person said always.", Why: "stated"},
		{Kind: review.KindMemory, Scope: "/proj", Key: "guess", Type: "fact", Text: "Uses Go.", Evidence: "go.mod", Why: "seen"},
	}); err != nil {
		t.Fatal(err)
	}

	list := strings.Join(pendingRun(ctx, ls, ""), "\n")
	for _, want := range []string{"#1 new skill release-flow/SKILL.md — walked through it", "    + description: Tag and publish a kiln release.", "#2 memory check-first (preference) — stated", "    evidence: The person said always.", "/pending approve <id…|all>"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}

	out := strings.Join(pendingRun(ctx, ls, "approve 1 #2 9"), "\n")
	for _, want := range []string{"Approved #1: wrote", "Approved #2: saved memory check-first.", "No pending proposal 9."} {
		if !strings.Contains(out, want) {
			t.Errorf("approve output lacks %q:\n%s", want, out)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(skillsDir, "release-flow", "SKILL.md")); string(data) != skill {
		t.Errorf("skill not written: %q", data)
	}
	if rec, _ := store.Current(ctx, "/proj", "check-first", ""); rec == nil || rec.Text != "Run make check first." {
		t.Errorf("memory not saved: %+v", rec)
	}

	out = strings.Join(pendingRun(ctx, ls, "reject all"), "\n")
	if !strings.Contains(out, "Rejected #3 guess.") {
		t.Errorf("reject output = %q", out)
	}
	if rec, _ := store.Current(ctx, "/proj", "guess", ""); rec != nil {
		t.Errorf("a rejected note was saved: %+v", rec)
	}
	if left, _ := queue.List(); len(left) != 0 {
		t.Errorf("%d proposals left, want none", len(left))
	}
	if got := pendingRun(ctx, ls, "delete 1"); !strings.HasPrefix(got[0], "Usage:") {
		t.Errorf("bad verb = %q, want usage", got)
	}
}

// TestPendingNoticeCountsWaitingProposals: the startup line names how many
// proposals wait, and says nothing when none do.
func TestPendingNoticeCountsWaitingProposals(t *testing.T) {
	queue := review.Queue{Dir: filepath.Join(t.TempDir(), "proposals")}
	if got := pendingNotice(queue); got != "" {
		t.Errorf("empty queue notice = %q, want none", got)
	}
	if _, err := queue.Add([]review.Proposal{{Kind: review.KindMemory, Key: "a"}, {Kind: review.KindMemory, Key: "b"}}); err != nil {
		t.Fatal(err)
	}
	if got := pendingNotice(queue); got != "2 proposed changes from earlier learning reviews · /pending to review" {
		t.Errorf("notice = %q", got)
	}
}
