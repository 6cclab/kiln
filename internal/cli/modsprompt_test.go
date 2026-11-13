package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	claudeplugins "github.com/andrepato/harness/internal/claude/plugins"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/tui"
)

// promptRuntime starts a mods runtime on the prompter fixture: a mod that
// hooks every prompt.* event the cli raises.
func promptRuntime(t *testing.T) *modsRuntime {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	src, err := os.ReadFile(filepath.Join("..", "mods", "testdata", "mods", "prompter", "hooks", "register.ts"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	hooks := filepath.Join(root, "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "hooks.json"), []byte(`{"modules":["./register.ts"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "register.ts"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	r := newModsRuntime([]claudeplugins.Plugin{{Key: "prompter@mp", Name: "prompter", Root: root}}, "model-x", 2000)
	if r == nil {
		t.Fatal("no runtime")
	}
	if errs := r.start(context.Background(), t.TempDir(), "s1", egressDeps{}); len(errs) != 0 {
		t.Fatalf("start: %v", errs)
	}
	t.Cleanup(r.close)
	return r
}

func TestMentionsRaisePromptMention(t *testing.T) {
	r := promptRuntime(t)
	dir := t.TempDir()
	for name, text := range map[string]string{".env": "SECRET=1\n", "real.txt": "the real file\n", "redirect.txt": "the typed file\n", "plain.txt": "plain\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := ResolveMentions("@.env @redirect.txt @plain.txt go", Options{Cwd: dir, Tier: &large, Mods: r})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]Mention{}
	for _, m := range out.Mentions {
		by[m.Raw] = m
	}
	if got := by[".env"].Skipped; !strings.Contains(got, "refused by a mod: a secret") {
		t.Errorf(".env skipped = %q, want a refusal by the mod", got)
	}
	if strings.Contains(out.Prompt, "SECRET=1") {
		t.Errorf("a refused file was inlined:\n%s", out.Prompt)
	}
	if !strings.Contains(out.Prompt, "the real file") || strings.Contains(out.Prompt, "the typed file") {
		t.Errorf("redirected mention should read real.txt:\n%s", out.Prompt)
	}
	if !strings.Contains(out.Prompt, "mention-context") {
		t.Errorf("the mod's context block is missing:\n%s", out.Prompt)
	}
	if !strings.Contains(out.Prompt, "plain") {
		t.Errorf("an unhooked mention was dropped:\n%s", out.Prompt)
	}
}

func TestMentionsWithoutModsAreUnchanged(t *testing.T) {
	dir := setupMentionDir(t)
	out, err := ResolveMentions("@small.ts explain", Options{Cwd: dir, Tier: &large, Mods: nil})
	if err != nil || !strings.Contains(out.Prompt, "export const x = 1;") {
		t.Fatalf("nil mods changed the mention: %v\n%s", err, out.Prompt)
	}
}

func TestModeRemindersRaisePromptAttachment(t *testing.T) {
	r := promptRuntime(t)
	mode := claudesettings.ModePlan
	rem := remindersAt(&mode)
	rem.attach = func(typ, text string) (string, bool) { return r.attachment(typ, text, nil) }
	got := rem.TakeForPrompt()
	if !strings.Contains(got, "You are in PLAN MODE.") || !strings.Contains(got, "!\n</system-reminder>") {
		t.Fatalf("the plan_mode attachment hook did not rewrite the reminder: %q", got)
	}
}

func TestSubmitRaisesPromptSubmit(t *testing.T) {
	r := promptRuntime(t)
	r.print = true
	text, blocks, drop := r.submit(context.Background(), tui.PromptSubmission{Text: "hello"})
	if drop != "" || text != "HELLO" || len(blocks) != 1 || blocks[0] != "ctx:" {
		t.Fatalf("submit = %q %v %q, want the hook's rewrite and context", text, blocks, drop)
	}
	if _, _, drop := r.submit(context.Background(), tui.PromptSubmission{Text: "forbidden"}); drop != "no forbidden words" {
		t.Fatalf("drop = %q", drop)
	}
	_, _, drop = r.submit(context.Background(), tui.PromptSubmission{Text: "forbidden", Command: &mods.PromptCommand{Name: "x"}})
	if drop != "no forbidden words" {
		t.Fatalf("a command can still be dropped, got %q", drop)
	}
}

func TestSubmitWithoutModsPassesThrough(t *testing.T) {
	var r *modsRuntime
	text, blocks, drop := r.submit(context.Background(), tui.PromptSubmission{Text: "hi"})
	if text != "hi" || blocks != nil || drop != "" {
		t.Fatalf("nil runtime changed the prompt: %q %v %q", text, blocks, drop)
	}
}

func TestComposePromptThroughHooks(t *testing.T) {
	r := promptRuntime(t)
	got := r.composePrompt([]mods.PromptSection{
		{ID: "base", Text: "BASE", Scope: "shared"},
		{ID: "memory", Text: "MEM", Scope: "session"},
		{ID: "skills", Text: "", Scope: "session"},
	})
	want := "BASE [S]\n\nEXTRA model-x"
	if got != want {
		t.Fatalf("composePrompt = %q, want %q", got, want)
	}
}

func TestComposePromptWithoutModsJoinsNonEmpty(t *testing.T) {
	var r *modsRuntime
	got := r.composePrompt([]mods.PromptSection{{ID: "a", Text: "A"}, {ID: "b"}, {ID: "c", Text: "C"}})
	if got != "A\n\nC" {
		t.Fatalf("composePrompt = %q", got)
	}
}

// $.session.surfaces lists kiln's one local terminal while the TUI runs, and
// nothing under -p (no driver).
func TestSessionSurfacesFollowTheTUI(t *testing.T) {
	r := promptRuntime(t)
	svc := r.services(mods.Services{})
	if got := svc.Surfaces(); len(got) != 0 {
		t.Errorf("Surfaces under -p = %v, want none", got)
	}
	if r.newDriver() == nil {
		t.Fatal("no driver")
	}
	if got := svc.Surfaces(); len(got) != 1 || got[0] != "terminal" {
		t.Errorf("Surfaces with the TUI = %v, want [terminal]", got)
	}
}

// Under -p there is no input line: the $.prompt.* methods answer that.
func TestPromptServicesWithoutAComposer(t *testing.T) {
	r := promptRuntime(t)
	svc := r.services(mods.Services{})
	if _, ok := svc.PromptRead(); ok {
		t.Error("PromptRead reports a box with no TUI")
	}
	if a := svc.PromptFill(context.Background(), "x", "replace", "p"); a.Filled || a.Refusal != "no_composer" {
		t.Errorf("PromptFill = %+v, want no_composer", a)
	}
	if svc.PromptSuggest(context.Background(), "x") {
		t.Error("PromptSuggest shown with no TUI")
	}
	if err := svc.PromptSubmit(context.Background(), "x", nil, "p", false); err == nil || !strings.Contains(err.Error(), "-p") {
		t.Errorf("PromptSubmit err = %v, want the -p answer", err)
	}
}
