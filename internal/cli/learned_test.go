package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/review"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

const releaseSkill = "---\nname: release-flow\ndescription: Tag and publish a kiln release.\n---\n# Release flow\n"

// TestAgentSkillWritesAreLoggedAndRevertible: a skill the agent writes
// with the write tool, then changes with edit, lands in the skill
// history; /learned lists who made it and its use, /learned history shows
// both changes, and /learned revert undoes the edit. Writes outside
// ~/.kiln/skills are not logged.
func TestAgentSkillWritesAreLoggedAndRevertible(t *testing.T) {
	dir := t.TempDir()
	ls := testStores(dir, nil)
	env := execenv.New(dir)
	env.OnWrite = recordSkillWrite(ls.history, ls.skillsDir, "s1")
	set := tools.Builtins(env)
	path := filepath.Join(ls.skillsDir, "release-flow", "SKILL.md")
	run := func(name string, args map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(args)
		tl, _ := set.Get(name)
		res, err := tl.Execute(context.Background(), raw, nil, tool.Invocation{ToolName: name, Cwd: dir})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %s", name, err, msg.TextOf(res.Content))
		}
	}
	run("write", map[string]any{"path": path, "content": releaseSkill})
	run("edit", map[string]any{"path": path, "edits": []map[string]any{{"oldText": "# Release flow", "newText": "# Release flow\n\nRun make check first."}}})
	run("write", map[string]any{"path": filepath.Join(dir, "notes.txt"), "content": "not a skill"})

	if err := ls.usage.RecordUse(ls.skillsDir, filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	list := strings.Join(learnedRun(ls, "", time.Now()), "\n")
	if !strings.Contains(list, "release-flow · kiln (/learn, /refine or asked) · used 1 time, last just now") {
		t.Errorf("/learned:\n%s", list)
	}
	hist := strings.Join(learnedRun(ls, "history", time.Now()), "\n")
	for _, want := range []string{"#2 just now · updated release-flow/SKILL.md · kiln", "#1 just now · created release-flow/SKILL.md · kiln"} {
		if !strings.Contains(hist, want) {
			t.Errorf("history lacks %q:\n%s", want, hist)
		}
	}
	if strings.Contains(hist, "notes.txt") {
		t.Errorf("a write outside the skills was logged:\n%s", hist)
	}
	out := strings.Join(learnedRun(ls, "revert 2", time.Now()), "\n")
	if !strings.Contains(out, "Reverted #2 (updated release-flow/SKILL.md) as #3") {
		t.Errorf("revert output = %q", out)
	}
	if data, _ := os.ReadFile(path); string(data) != releaseSkill {
		t.Errorf("after revert: %q", data)
	}
	if out := learnedRun(ls, "revert 2", time.Now()); !strings.HasPrefix(out[0], "Not reverted:") {
		t.Errorf("second revert of #2 = %q, want refused", out)
	}
	if out := learnedRun(ls, "bogus", time.Now()); !strings.HasPrefix(out[0], "Usage:") {
		t.Errorf("bad args = %q", out)
	}
}

// TestCuratorArchiveIsApprovedOrKept: an idle kiln-made skill is queued for
// archiving once at startup; approving archives it (revertible), and
// rejecting remembers the keep so it is not proposed again.
func TestCuratorArchiveIsApprovedOrKept(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ls := testStores(dir, nil)
	for _, name := range []string{"old-one", "old-two"} {
		p := filepath.Join(ls.skillsDir, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: d\n---\n"
		_ = os.WriteFile(p, []byte(content), 0o644)
		_ = ls.history.RecordWrite(ls.skillsDir, p, nil, false, []byte(content), review.SourceReview, "s0", 1)
		old := time.Now().Add(-45 * 24 * time.Hour)
		_ = os.Chtimes(p, old, old)
	}
	if n := curateSkills(ls.queue, ls.history, ls.usage, ls.skillsDir, time.Now()); n != 2 {
		t.Fatalf("curator queued %d, want 2", n)
	}
	if n := curateSkills(ls.queue, ls.history, ls.usage, ls.skillsDir, time.Now()); n != 0 {
		t.Fatalf("curator queued %d more on the next start, want 0", n)
	}
	list := strings.Join(pendingRun(ctx, ls, ""), "\n")
	if !strings.Contains(list, "#1 archive skill old-one — unused for 45 days, never used") {
		t.Errorf("/pending:\n%s", list)
	}
	out := strings.Join(pendingRun(ctx, ls, "approve 1"), "\n") + "\n" + strings.Join(pendingRun(ctx, ls, "reject 2"), "\n")
	if !strings.Contains(out, "archived old-one") || !strings.Contains(out, "Kept old-two") {
		t.Errorf("approve/reject output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(ls.skillsDir, "old-one")); !os.IsNotExist(err) {
		t.Error("approved archive left the skill in place")
	}
	if n := curateSkills(ls.queue, ls.history, ls.usage, ls.skillsDir, time.Now()); n != 0 {
		t.Errorf("a kept skill was proposed again (%d)", n)
	}
	changes, _ := ls.history.List()
	last := changes[len(changes)-1]
	if out := learnedRun(ls, "revert "+itoa(last.N), time.Now()); !strings.HasPrefix(out[0], "Reverted") {
		t.Fatalf("restore = %q", out)
	}
	if _, err := os.Stat(filepath.Join(ls.skillsDir, "old-one", "SKILL.md")); err != nil {
		t.Error("revert did not restore the archived skill")
	}
}

// TestReviewToolRunnerReadsOnlyWhatItMay: the review reads kiln's skills
// freely, other files only when the session may read them without asking,
// and loads skills; a read that would ask is refused without running.
func TestReviewToolRunnerReadsOnlyWhatItMay(t *testing.T) {
	dir := t.TempDir()
	skillsDir := filepath.Join(dir, "skills")
	skillFile := filepath.Join(skillsDir, "release-flow", "SKILL.md")
	_ = os.MkdirAll(filepath.Dir(skillFile), 0o755)
	_ = os.WriteFile(skillFile, []byte(releaseSkill), 0o644)
	secret := filepath.Join(dir, "secret.txt")
	_ = os.WriteFile(secret, []byte("hidden"), 0o644)
	open := filepath.Join(dir, "open.txt")
	_ = os.WriteFile(open, []byte("fine"), 0o644)
	set := tools.Builtins(execenv.New(dir))
	run := reviewToolRunner(set, dir, skillsDir, func(p string) bool { return p == open })
	read := func(p string) (string, bool) {
		return run(context.Background(), msg.NewToolCall("c", "read", map[string]any{"path": p}))
	}
	if text, isErr := read(skillFile); isErr || !strings.Contains(text, "Tag and publish") {
		t.Errorf("kiln skill read = %q, %v", text, isErr)
	}
	if text, isErr := read("open.txt"); isErr || !strings.Contains(text, "fine") {
		t.Errorf("allowed read = %q, %v", text, isErr)
	}
	if text, isErr := read(secret); !isErr || strings.Contains(text, "hidden") {
		t.Errorf("read needing approval = %q, %v; want refused", text, isErr)
	}
}
