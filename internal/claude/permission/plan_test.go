package permission

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// Plan mode as Claude Code's docs describe it: edits stay blocked until
// the plan is approved, whatever the rules say; a read-only shell command
// runs; any other shell command goes through the regular permission flow
// (deny, ask, allow, and with no rule a prompt).

func planBash(cmd string) Request {
	return Request{ToolName: "bash", PrimaryArg: cmd, Args: map[string]any{"command": cmd}}
}

func planEdit(path string) Request {
	return Request{ToolName: "edit", PrimaryArg: path, Args: map[string]any{"path": path}}
}

// TestPlan_ShellCommandTakesTheRegularFlow: an allowed command runs, an
// unruled one is put to the user, a read-only one in the workspace runs
// unasked and one reading outside the workspace asks, as in manual mode.
func TestPlan_ShellCommandTakesTheRegularFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()
	allow := settings.Permissions{Allow: []string{"Bash(npm test)", "Bash(mkdir *)"}}
	g := NewGate(GateOptions{Permissions: allow, Mode: settings.ModePlan, Roots: []string{proj}})
	var asked []string
	g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
		asked = append(asked, req.PrimaryArg)
		return PromptChoice{Kind: PromptDeny}, nil
	})
	for _, c := range []struct {
		cmd  string
		asks bool
	}{
		{"npm test", false},
		{"mkdir build", false},
		{"git log", false},
		{"touch x", true},
		{"cat /etc/passwd", true},
	} {
		asked = nil
		blocked, err := g.Check(context.Background(), planBash(c.cmd))
		if err != nil {
			t.Fatal(err)
		}
		if got := len(asked) > 0; got != c.asks {
			t.Errorf("%q: asked=%v, want %v", c.cmd, got, c.asks)
		}
		if !c.asks && blocked != nil {
			t.Errorf("%q was blocked: %s", c.cmd, blocked.Reason)
		}
	}
}

// TestPlan_HeadlessRefusals: with nobody to ask, an unruled command is
// refused as any ask is; an edit keeps plan mode's own refusal text; a
// deny rule's refusal names the rules, not plan mode.
func TestPlan_HeadlessRefusals(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	proj := t.TempDir()
	p := settings.Permissions{Allow: []string{"Edit"}, Deny: []string{"Bash(rm *)"}}
	g := NewGate(GateOptions{Permissions: p, Mode: settings.ModePlan, Roots: []string{proj}})
	check := func(req Request) *BlockResult {
		t.Helper()
		blocked, err := g.Check(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return blocked
	}
	if b := check(planBash("touch x")); b == nil || !strings.Contains(b.Reason, "requires confirmation") {
		t.Errorf("headless plan-mode touch: %+v, want refused as needing confirmation", b)
	}
	const want = "plan mode is read-only, so edit is not available. Describe the change instead of making it."
	if b := check(planEdit(filepath.Join(proj, "a.go"))); b == nil || b.Reason != want {
		t.Errorf("plan-mode edit: %+v, want reason %q", b, want)
	}
	if b := check(planBash("rm -rf build")); b == nil || b.Reason != "blocked by permission rules." {
		t.Errorf("deny rule in plan mode: %+v, want the rules' refusal", b)
	}
}

// TestPlan_SessionGrants: a "don't ask again" grant from before planning
// still runs its command (the regular flow), but never lets an edit
// through.
func TestPlan_SessionGrants(t *testing.T) {
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
	if blocked, _ := g.Check(context.Background(), planBash("npm test")); blocked != nil || prompts != 0 {
		t.Errorf("granted npm test in plan: blocked=%v prompts=%d, want it to run unasked", blocked, prompts)
	}
	blocked, _ := g.Check(context.Background(), planEdit(file))
	if blocked == nil || !strings.Contains(blocked.Reason, "plan mode is read-only") {
		t.Errorf("granted edit in plan: %+v, want plan mode's refusal", blocked)
	}
	if prompts != 0 {
		t.Errorf("the edit prompted (%d prompts); plan mode refuses edits outright", prompts)
	}
}
