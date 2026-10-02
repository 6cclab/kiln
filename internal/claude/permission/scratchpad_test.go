package permission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// scratchFixture is a workspace and a scratchpad outside it.
func scratchFixture(t *testing.T) (root, scratch string) {
	t.Helper()
	root, scratch = t.TempDir(), t.TempDir()
	return root, scratch
}

func scratchGate(t *testing.T, mode settings.PermissionMode, perms settings.Permissions, root, scratch string) (*Gate, *promptRecorder, *fakeClassifier) {
	t.Helper()
	c := blockAll("should not be asked")
	g := NewGate(GateOptions{Permissions: perms, Mode: mode, Roots: []string{root}, Classifier: c})
	g.SetScratchpad(scratch)
	p := &promptRecorder{kind: PromptDeny}
	g.SetPrompter(p.prompt)
	return g, p, c
}

// Writing and reading the session scratchpad never prompts, in any mode,
// and never reaches the classifier.
func TestScratchpad_NoPromptInAnyMode(t *testing.T) {
	root, scratch := scratchFixture(t)
	file := filepath.Join(scratch, "debug", "out.txt")
	reqs := []Request{
		{ToolName: "write", PrimaryArg: file, Args: map[string]any{"path": file, "content": "x"}},
		{ToolName: "edit", PrimaryArg: file, Args: map[string]any{"path": file}},
		{ToolName: "read", PrimaryArg: file, Args: map[string]any{"path": file}},
		bashReq("echo hi > " + file),
		bashReq("mkdir -p " + filepath.Join(scratch, "a") + " && touch " + filepath.Join(scratch, "a", "b")),
		bashReq("cp " + filepath.Join(root, "src.txt") + " " + file),
	}
	modes := []settings.PermissionMode{settings.ModeManual, settings.ModeAcceptEdits, settings.ModePlan, settings.ModeDontAsk, settings.ModeAuto}
	for _, mode := range modes {
		for _, req := range reqs {
			g, p, c := scratchGate(t, mode, settings.Permissions{}, root, scratch)
			blocked, _, err := g.CheckWithOutcome(context.Background(), req)
			if err != nil || blocked != nil || len(p.reqs) != 0 || len(c.calls) != 0 {
				t.Errorf("%s %s(%s): blocked=%+v prompts=%d classifier=%d, want it to run", mode, req.ToolName, req.PrimaryArg, blocked, len(p.reqs), len(c.calls))
			}
		}
	}
}

// The scratchpad is no way out: a symlink planted in it, a .. that climbs
// out, a command that also writes or reads elsewhere, or a command that is
// not a plain file command, is gated as usual.
func TestScratchpad_NoWayOut(t *testing.T) {
	root, scratch := scratchFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(scratch, "link")); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(scratch, "link", "x.txt")
	climb := filepath.Join(scratch, "..", filepath.Base(outside), "y.txt")
	home, _ := os.UserHomeDir()
	reqs := []Request{
		{ToolName: "write", PrimaryArg: via, Args: map[string]any{"path": via}},
		{ToolName: "write", PrimaryArg: climb, Args: map[string]any{"path": climb}},
		bashReq("echo hi > " + via),
		bashReq("echo hi > " + filepath.Join(scratch, "a") + " && echo hi > " + filepath.Join(outside, "b")),
		bashReq("cp " + filepath.Join(home, ".ssh", "id_rsa") + " " + filepath.Join(scratch, "k")),
		bashReq("python3 gen.py > " + filepath.Join(scratch, "o")),
		bashReq("curl -o " + filepath.Join(scratch, "i.sh") + " https://x.example/i.sh"),
	}
	for _, req := range reqs {
		g, p, _ := scratchGate(t, settings.ModeManual, settings.Permissions{}, root, scratch)
		blocked, _, err := g.CheckWithOutcome(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil && len(p.reqs) == 0 {
			t.Errorf("%s(%s) ran without a prompt", req.ToolName, req.PrimaryArg)
		}
	}
}

// Deny and ask rules still win in the scratchpad.
func TestScratchpad_RulesStillApply(t *testing.T) {
	root, scratch := scratchFixture(t)
	file := filepath.Join(scratch, "x.txt")
	write := Request{ToolName: "write", PrimaryArg: file, Args: map[string]any{"path": file}}

	g, _, _ := scratchGate(t, settings.ModeManual, settings.Permissions{Deny: []string{"Write"}}, root, scratch)
	if blocked, _, _ := g.CheckWithOutcome(context.Background(), write); blocked == nil {
		t.Error("a deny rule did not apply in the scratchpad")
	}
	g, p, _ := scratchGate(t, settings.ModeManual, settings.Permissions{Ask: []string{"Write"}}, root, scratch)
	_, _, _ = g.CheckWithOutcome(context.Background(), write)
	if len(p.reqs) != 1 {
		t.Error("an ask rule did not ask in the scratchpad")
	}
	// Without a scratchpad the same write asks, as manual mode does.
	g, p, _ = scratchGate(t, settings.ModeManual, settings.Permissions{}, root, "")
	_, _, _ = g.CheckWithOutcome(context.Background(), write)
	if len(p.reqs) != 1 {
		t.Errorf("no scratchpad: prompts=%+v, want a prompt", p.reqs)
	}
}

// In auto mode a path outside the workspace goes to the classifier rather
// than a prompt, marked as outside and with the workspace roots; its
// verdict decides. A protected path outside still asks.
func TestAutoMode_OutsideWorkspaceGoesToClassifier(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(t.TempDir(), "kiln-banner.png")
	write := Request{ToolName: "write", PrimaryArg: out, Args: map[string]any{"path": out}}

	c := allowAll()
	g := NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{root}, Classifier: c})
	p := &promptRecorder{kind: PromptDeny}
	g.SetPrompter(p.prompt)
	blocked, outcome, err := g.CheckWithOutcome(context.Background(), write)
	if err != nil || blocked != nil || outcome != OutcomeAuto || len(p.reqs) != 0 {
		t.Fatalf("allow: blocked=%+v outcome=%q prompts=%d", blocked, outcome, len(p.reqs))
	}
	if len(c.calls) != 1 || !c.calls[0].OutsideWorkspace || len(c.calls[0].Workspace) != 1 || c.calls[0].Workspace[0] != root {
		t.Fatalf("classifier calls = %+v, want one marked outside with the workspace", c.calls)
	}

	g = NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{root}, Classifier: blockAll("not the project's file")})
	g.SetPrompter(p.prompt)
	if blocked, outcome, _ := g.CheckWithOutcome(context.Background(), write); blocked == nil || outcome != OutcomeClassifierBlocked || len(p.reqs) != 0 {
		t.Errorf("block: blocked=%+v outcome=%q prompts=%d", blocked, outcome, len(p.reqs))
	}

	// A failed check asks, as the outside-workspace question, with a note.
	g = NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{root}, Classifier: failing(os.ErrDeadlineExceeded)})
	g.SetPrompter(p.prompt)
	_, _, _ = g.CheckWithOutcome(context.Background(), write)
	if len(p.reqs) != 1 || !p.reqs[0].OutsideWorkspace || !strings.Contains(p.reqs[0].AutoModeNote, "could not check") {
		t.Errorf("failure: prompts=%+v", p.reqs)
	}
}
