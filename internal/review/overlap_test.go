package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
)

// The duplicate qwen3.8 proposed in a live run: the check-order lesson as
// both a reference file and a memory note.
const (
	orderBefore = "# Check order\n\n1. go vet ./...\n2. go test ./...\n3. staticcheck ./...\n"
	orderAfter  = "# Check order\n\nFast checks first so failures surface quickly, slow tests last.\n\n1. staticcheck ./...\n2. go vet ./...\n3. go test ./...\n"
	orderNote   = "When running checks before a commit, run fast tools first (staticcheck, vet) and slow tests last, so failures surface quickly."
)

// TestSameLessonJudgesNotesAgainstWhatASkillAdds: the check-order note
// repeats what the reference adds; a different lesson, a note matching
// only lines the skill already had, and a very short note do not.
func TestSameLessonJudgesNotesAgainstWhatASkillAdds(t *testing.T) {
	added := addedText(orderBefore, orderAfter)
	if !sameLesson(orderNote, added) {
		t.Errorf("the check-order note was not judged a duplicate of %q", added)
	}
	for _, note := range []string{
		"This repo uses pnpm; add packages with pnpm add, never npm install.",
		"Run go vet and go test before every commit.", // lines the file already had
		"Prefer staticcheck.",
	} {
		if sameLesson(note, added) {
			t.Errorf("%q judged a duplicate of %q", note, added)
		}
	}
}

// TestRunKeepsOneLessonInOnePlace: a memory_write repeating a skill
// proposal made earlier in the review is refused with the reason; one
// made before the skill proposal, or in a JSON answer, is dropped at the
// end; one repeating a kept note is refused too. A different lesson is
// kept.
func TestRunKeepsOneLessonInOnePlace(t *testing.T) {
	in := input(t, true)
	in.Memory = []memory.Record{{Key: "check-order", Kind: "procedure", Text: orderNote}}
	ref := filepath.Join(in.SkillsDir, "ci-checks", "references", "order.md")
	if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(ref, []byte(orderBefore), 0o644)
	writeRef := msg.NewToolCall("w", "write", map[string]any{"path": ref, "content": orderAfter})
	note := func(id, key, text string) msg.ToolCall {
		return msg.NewToolCall(id, "memory_write", map[string]any{"action": "remember", "key": key, "kind": "procedure", "text": text, "evidence": "The person said so."})
	}
	tools := func(context.Context, msg.ToolCall) (string, bool) { return orderBefore, false }

	// Note after the skill: refused on the spot.
	in.Memory = []memory.Record{}
	s := &scripted{replies: []*msg.AssistantMessage{
		calls(msg.NewToolCall("r", "read", map[string]any{"path": ref})),
		calls(writeRef, note("n", "fast-checks-first", orderNote), note("p", "pnpm", "This repo uses pnpm; add packages with pnpm add, never npm install.")),
		text("Proposed the order and a pnpm note."),
	}}
	got, st, err := Run(context.Background(), conversation(s), in, tools, nil)
	if err != nil || targets(got) != "skill:ci-checks/references/order.md memory:pnpm" || st.Captured != 2 {
		t.Fatalf("Run = %s, %+v, %v", targets(got), st, err)
	}
	if r := msg.TextOf(s.sent[2][len(s.sent[2])-2].(msg.ToolResultMessage).Content); !strings.Contains(r, "already in the proposal for skill ci-checks/references/order.md") {
		t.Errorf("duplicate note result = %q", r)
	}

	// Note before the skill: dropped at the end.
	s = &scripted{replies: []*msg.AssistantMessage{
		calls(msg.NewToolCall("r", "read", map[string]any{"path": ref})),
		calls(note("n", "fast-checks-first", orderNote)),
		calls(writeRef),
		text("Done."),
	}}
	got, st, _ = Run(context.Background(), conversation(s), in, tools, nil)
	if targets(got) != "skill:ci-checks/references/order.md" || len(st.Duplicates) != 1 {
		t.Fatalf("note first: %s, duplicates %v", targets(got), st.Duplicates)
	}

	// Repeating a kept note: refused.
	in.Memory = []memory.Record{{Key: "check-order", Kind: "procedure", Text: orderNote}}
	s = &scripted{replies: []*msg.AssistantMessage{calls(note("n", "fast-checks", orderNote)), text("NOTHING more.")}}
	got, _, _ = Run(context.Background(), conversation(s), in, tools, nil)
	if len(got) != 0 {
		t.Fatalf("a note repeating a kept one was kept: %s", targets(got))
	}
}

func targets(ps []Proposal) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Kind+":"+p.Target())
	}
	return strings.Join(out, " ")
}
