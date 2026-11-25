package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHistoryRevertsWritesAndArchives: every change kiln makes to a skill
// is logged with what it replaced; a revert puts it back, refuses a file
// changed since, and is itself revertible. Provenance is the source of a
// skill's creation; a skill with no history was made by hand.
func TestHistoryRevertsWritesAndArchives(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	h := History{Path: filepath.Join(dir, "skill-history.jsonl")}
	path := filepath.Join(skillsDir, "release-flow", "SKILL.md")
	write := func(content, source string) {
		t.Helper()
		before, err := os.ReadFile(path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := h.RecordWrite(skillsDir, path, before, err == nil, []byte(content), source, "s1", 0); err != nil {
			t.Fatal(err)
		}
	}
	write("v1", SourceAgent)
	write("v2", SourceReview)
	if err := h.RecordWrite(skillsDir, filepath.Join(dir, "elsewhere.txt"), nil, false, []byte("x"), SourceAgent, "", 0); err != nil {
		t.Fatal(err)
	}
	changes, _ := h.List()
	if len(changes) != 2 || changes[1].N != 2 || *changes[1].Before != "v1" {
		t.Fatalf("history = %+v, want two writes, the second replacing v1", changes)
	}
	if p := Provenance(changes); p["release-flow"] != SourceAgent || len(p) != 1 {
		t.Errorf("provenance = %v, want release-flow made by the agent", p)
	}

	undo, err := h.Revert(skillsDir, 2, "s2")
	if err != nil || undo.Reverts != 2 || undo.Source != SourceRevert {
		t.Fatalf("Revert(2) = %+v, %v", undo, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "v1" {
		t.Fatalf("after revert the file holds %q, want v1", data)
	}
	if _, err := h.Revert(skillsDir, 2, "s2"); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Errorf("reverting #2 again: err = %v, want refused", err)
	}
	if _, err := h.Revert(skillsDir, undo.N, "s2"); err != nil {
		t.Fatalf("reverting the revert: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "v2" {
		t.Errorf("after reverting the revert the file holds %q, want v2", data)
	}
	if err := os.WriteFile(path, []byte("edited by hand"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Revert(skillsDir, 1, "s2"); err == nil {
		t.Error("reverted a file edited by hand since")
	}

	arch, err := Archive(h, skillsDir, "release-flow", SourceCurator, "s3", 7)
	if err != nil || arch.Proposal != 7 {
		t.Fatalf("Archive = %+v, %v", arch, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("archived skill still in the skills directory")
	}
	if !strings.HasPrefix(arch.Archive, filepath.Join(dir, "skills-archive")) {
		t.Errorf("archived to %s", arch.Archive)
	}
	if _, err := h.Revert(skillsDir, arch.N, "s3"); err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "edited by hand" {
		t.Errorf("restored skill holds %q", data)
	}

	// Reverting a creation removes the file and its empty directory.
	other := filepath.Join(skillsDir, "fresh", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(other, []byte("new"), 0o644)
	_ = h.RecordWrite(skillsDir, other, nil, false, []byte("new"), SourceAgent, "", 0)
	changes, _ = h.List()
	if _, err := h.Revert(skillsDir, changes[len(changes)-1].N, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(other)); !os.IsNotExist(err) {
		t.Error("reverting a new skill left its directory")
	}
}

// TestCuratorProposesOnlyIdleKilnSkills: a kiln-made skill unused and
// unchanged for ArchiveAfter is proposed for archiving once; a recent
// one, one made by hand, one already queued and one the person kept are
// not.
func TestCuratorProposesOnlyIdleKilnSkills(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-40 * 24 * time.Hour)
	for _, name := range []string{"idle", "fresh", "by-hand", "queued", "kept"} {
		writeSkill(t, dir, name, "---\nname: "+name+"\ndescription: d\n---\n")
		if name != "fresh" {
			_ = os.Chtimes(filepath.Join(dir, name, "SKILL.md"), old, old)
		}
	}
	u := Usage{Path: filepath.Join(t.TempDir(), "usage.json")}
	if err := u.RecordUse(dir, filepath.Join(dir, "idle", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if err := u.RecordUse(dir, "/elsewhere/skill/SKILL.md"); err != nil {
		t.Fatal(err)
	}
	_ = u.Keep("kept")
	stored := u.Load()
	if stored["idle"].Uses != 1 || len(stored) != 2 {
		t.Fatalf("usage = %+v", stored)
	}
	// The one use was 40 days ago.
	r := stored["idle"]
	r.LastUsed = old.UnixMilli()
	stored["idle"] = r

	activity := Activity(dir, stored)
	if len(activity) != 5 || activity["fresh"].Idle(time.Now()) > time.Hour {
		t.Fatalf("activity = %+v", activity)
	}
	if !strings.Contains(activity["idle"].attrs(), `stale="true"`) || strings.Contains(activity["fresh"].attrs(), "stale") {
		t.Errorf("attrs: idle %q, fresh %q", activity["idle"].attrs(), activity["fresh"].attrs())
	}
	provenance := map[string]string{"idle": SourceAgent, "fresh": SourceReview, "queued": SourceReview, "kept": SourceAgent}
	pending := []Proposal{{Kind: KindArchive, Skill: "queued"}}
	got := CuratorProposals(activity, provenance, pending, time.Now())
	if len(got) != 1 || got[0].Skill != "idle" || got[0].Kind != KindArchive || !strings.Contains(got[0].Why, "used 1 time") {
		t.Fatalf("CuratorProposals = %+v, want only idle", got)
	}
}
