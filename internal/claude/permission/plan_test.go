package permission

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// Plan mode as Claude Code runs it without its auto-mode classifier
// (permission-modes, "Analyze before you edit with plan mode"): edits stay
// blocked until the plan is approved, and a shell command outside the
// read-only set prompts — whatever allow rules or session grants say.

func planBash(cmd string) Request {
	return Request{ToolName: "bash", PrimaryArg: cmd, Args: map[string]any{"command": cmd}}
}

func planEdit(path string) Request {
	return Request{ToolName: "edit", PrimaryArg: path, Args: map[string]any{"path": path}}
}

// TestPlan_ShellCommandAsksWhateverAllowRulesSay: a mutating command that
// an allow rule covers is put to the user in plan mode, not run and not
// refused; a read-only one still runs without asking.
func TestPlan_ShellCommandAsksWhateverAllowRulesSay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()
	allow := settings.Permissions{Allow: []string{"Bash(npm test)", "Bash(mkdir *)"}}
	g := NewGate(GateOptions{Permissions: allow, Mode: settings.ModePlan, Roots: []string{proj}})
	var asked []string
	g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
		asked = append(asked, req.PrimaryArg)
		return PromptChoice{Kind: PromptDeny}, nil
	})
	for _, cmd := range []string{"npm test", "mkdir build", "touch x"} {
		blocked, err := g.Check(context.Background(), planBash(cmd))
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Errorf("%q ran in plan mode without asking", cmd)
		}
	}
	if strings.Join(asked, "|") != "npm test|mkdir build|touch x" {
		t.Errorf("prompted for %q, want every command asked about", asked)
	}
	asked = nil
	if blocked, _ := g.Check(context.Background(), planBash("git log")); blocked != nil || len(asked) != 0 {
		t.Errorf("read-only git log: blocked=%v asked=%v, want it to run unasked", blocked, asked)
	}
}

// TestPlan_HeadlessRefusesTheAsk: with nobody to ask, the prompt is
// refused as any ask is, and an edit keeps plan mode's own refusal text.
func TestPlan_HeadlessRefusesTheAsk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()
	allow := settings.Permissions{Allow: []string{"Bash(npm test)", "Edit"}}
	g := NewGate(GateOptions{Permissions: allow, Mode: settings.ModePlan, Roots: []string{proj}})
	blocked, err := g.Check(context.Background(), planBash("npm test"))
	if err != nil {
		t.Fatal(err)
	}
	if blocked == nil || !strings.Contains(blocked.Reason, "requires confirmation") {
		t.Errorf("headless plan-mode npm test: %+v, want refused as needing confirmation", blocked)
	}
	blocked, err = g.Check(context.Background(), planEdit(filepath.Join(proj, "a.go")))
	if err != nil {
		t.Fatal(err)
	}
	const want = "plan mode is read-only, so edit is not available. Describe the change instead of making it."
	if blocked == nil || blocked.Reason != want {
		t.Errorf("plan-mode edit: %+v, want reason %q", blocked, want)
	}
}

// TestPlan_SessionGrantDoesNotApply: a "don't ask again" grant from
// before planning counts as an allow, so it neither runs a mutating
// command unasked nor lets an edit through in plan mode.
func TestPlan_SessionGrantDoesNotApply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()
	g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{proj}})
	prompts := 0
	g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
		prompts++
		return PromptChoice{Kind: PromptAllowAlways}, nil
	})
	file := filepath.Join(proj, "a.go")
	for _, req := range []Request{planBash("npm test"), planEdit(file)} {
		if blocked, err := g.Check(context.Background(), req); err != nil || blocked != nil {
			t.Fatalf("manual-mode grant for %s: blocked=%v err=%v", req.ToolName, blocked, err)
		}
	}
	if prompts != 2 {
		t.Fatalf("prompts = %d, want 2 (one per grant)", prompts)
	}

	g.SetMode(settings.ModePlan)
	prompts = 0
	if blocked, _ := g.Check(context.Background(), planBash("npm test")); blocked != nil || prompts != 1 {
		t.Errorf("granted npm test in plan: blocked=%v prompts=%d, want asked again", blocked, prompts)
	}
	blocked, _ := g.Check(context.Background(), planEdit(file))
	if blocked == nil || !strings.Contains(blocked.Reason, "plan mode is read-only") {
		t.Errorf("granted edit in plan: %+v, want plan mode's refusal", blocked)
	}
	if prompts != 1 {
		t.Errorf("the edit prompted (%d prompts); plan mode refuses edits outright", prompts)
	}
}
