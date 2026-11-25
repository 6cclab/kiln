package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
)

const goodSkill = "---\nname: release-flow\ndescription: Tag and publish a kiln release.\n---\n# Release flow\n"

func input(t *testing.T, memoryOn bool) Input {
	in := Input{SkillsDir: filepath.Join(t.TempDir(), "skills"), MemoryScope: "/proj", Session: "s1"}
	if memoryOn {
		in.Memory = []memory.Record{}
	}
	return in
}

// TestParseKeepsValidProposalsAndDropsTheRest: a skill file must stay in
// its skill's directory and a SKILL.md must load under its directory's
// name; a memory note needs a key, text, evidence and a known type, and
// is refused when memory is off. Valid proposals survive the bad ones.
func TestParseKeepsValidProposalsAndDropsTheRest(t *testing.T) {
	in := input(t, true)
	answer := "```json\n" + `{"proposals": [
	 {"kind": "skill", "skill": "release-flow", "file": "SKILL.md", "content": ` + quote(goodSkill) + `, "why": "the person walked through it"},
	 {"kind": "skill", "skill": "release-flow", "file": "references/tags.md", "content": "Tag with vX.Y.Z.", "why": "detail"},
	 {"kind": "skill", "skill": "release-flow", "file": "../../.ssh/config", "content": "x", "why": "escape"},
	 {"kind": "skill", "skill": "other", "file": "SKILL.md", "content": ` + quote(goodSkill) + `, "why": "wrong name"},
	 {"kind": "skill", "skill": "Bad_Name", "file": "SKILL.md", "content": "x", "why": "bad name"},
	 {"kind": "memory", "key": "test-command", "type": "procedure", "text": "Run make check.", "evidence": "The person said so.", "why": "stated"},
	 {"kind": "memory", "key": "guess", "type": "preference", "text": "Likes Go.", "evidence": "", "why": "inferred"},
	 {"kind": "memory", "key": "odd", "type": "trait", "text": "x", "evidence": "y", "why": "z"}
	]}` + "\n```"
	got, err := Parse(answer, in, nil)
	if err == nil || !strings.Contains(err.Error(), "dropped 5 proposal(s)") {
		t.Errorf("err = %v, want 5 dropped", err)
	}
	var targets []string
	for _, p := range got {
		targets = append(targets, p.Kind+":"+p.Target())
	}
	want := "skill:release-flow/SKILL.md skill:release-flow/references/tags.md memory:test-command"
	if strings.Join(targets, " ") != want {
		t.Fatalf("kept %v, want %s", targets, want)
	}
	if got[2].Scope != "/proj" || got[0].Session != "s1" || got[0].Base != "" {
		t.Errorf("proposals not stamped with scope, session and base: %+v", got)
	}

	if got, _ := Parse(`{"proposals":[{"kind":"memory","key":"k","type":"fact","text":"t","evidence":"e"}]}`, input(t, false), nil); len(got) != 0 {
		t.Errorf("memory proposal kept with memory off: %+v", got)
	}
	var notJSON *notJSONError
	if _, err := Parse("I think nothing.", in, nil); !errors.As(err, &notJSON) {
		t.Errorf("a reply with no JSON: err = %v, want notJSONError", err)
	}
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\n", "\\n") + `"`
}

// TestQueueAndApply: proposals queue with increasing ids and survive
// listing; approving a skill writes it, approving one whose file changed
// since it was proposed is refused, and a memory proposal lands in the
// store with its evidence marked as the review's.
func TestQueueAndApply(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	q := Queue{Dir: filepath.Join(dir, "proposals")}
	skillsDir := filepath.Join(dir, "skills")
	h := History{Path: filepath.Join(dir, "skill-history.jsonl")}
	store, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	added, err := q.Add([]Proposal{
		{Kind: KindSkill, Skill: "release-flow", File: "SKILL.md", Content: goodSkill, Why: "w"},
		{Kind: KindMemory, Scope: "/proj", Key: "test-command", Type: "procedure", Text: "Run make check.", Evidence: "said so", Why: "w"},
	})
	if err != nil || len(added) != 2 || added[0].ID != 1 || added[1].ID != 2 {
		t.Fatalf("Add = %+v, %v", added, err)
	}
	more, _ := q.Add([]Proposal{{Kind: KindSkill, Skill: "release-flow", File: "SKILL.md", Content: goodSkill + "more\n", Why: "w"}})
	if more[0].ID != 3 {
		t.Errorf("third id = %d, want 3", more[0].ID)
	}
	listed, err := q.List()
	if err != nil || len(listed) != 3 {
		t.Fatalf("List = %+v, %v", listed, err)
	}

	if err := Apply(ctx, listed[0], skillsDir, store, h, "s1"); err != nil {
		t.Fatalf("apply new skill: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(skillsDir, "release-flow", "SKILL.md")); string(data) != goodSkill {
		t.Errorf("skill file = %q", data)
	}
	// Proposal 3 was made when the file did not exist; it exists now.
	if err := Apply(ctx, listed[2], skillsDir, store, h, "s1"); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Errorf("stale proposal applied: %v", err)
	}
	if err := Apply(ctx, listed[1], skillsDir, store, h, "s1"); err != nil {
		t.Fatalf("apply memory: %v", err)
	}
	rec, err := store.Current(ctx, "/proj", "test-command", "")
	if err != nil || rec == nil || rec.Text != "Run make check." || rec.Evidence != "kiln review: said so" {
		t.Errorf("memory record = %+v, %v", rec, err)
	}
	if err := Apply(ctx, listed[1], skillsDir, nil, h, "s1"); err == nil {
		t.Error("memory applied with no store")
	}

	if changes, _ := h.List(); len(changes) != 1 || changes[0].Source != SourceReview || changes[0].Proposal != 1 || changes[0].Before != nil {
		t.Errorf("history = %+v, want one review creation from proposal 1", changes)
	}
	if err := q.Remove(1); err != nil {
		t.Fatal(err)
	}
	if err := q.Remove(1); err == nil {
		t.Error("removing a missing proposal did not fail")
	}
	if left, _ := q.List(); len(left) != 2 {
		t.Errorf("%d left, want 2", len(left))
	}
}
