package permission

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// The sandbox's auto-allow comes after the protected-path check: a
// sandboxed bash command that writes a protected path kiln can name
// (.git/config, .envrc) still asks in manual and acceptEdits mode, and is
// refused where nobody can be asked (print mode, dontAsk), while an
// ordinary sandboxed command (go build) runs without a prompt.
func TestSandboxAutoAllowYieldsToProtectedPaths(t *testing.T) {
	sb := fakeSandbox{active: true, autoAllow: true, unsandboxed: true}
	for _, mode := range []settings.PermissionMode{settings.ModeAcceptEdits, settings.ModeManual} {
		for _, cmd := range []string{"echo x > .git/config", "echo 'export X=1' > .envrc"} {
			p := &promptLog{answer: PromptDeny}
			g := NewGate(GateOptions{Mode: mode, Roots: []string{work(t)}})
			g.SetPrompter(p.prompter)
			g.SetSandbox(sb)
			r, out, _ := g.CheckWithOutcome(context.Background(), sandboxBashReq(cmd, false))
			if out == OutcomeAuto || len(p.reqs) != 1 || r == nil {
				t.Errorf("%s, %q: outcome %q, %d prompts, block %+v; want a prompt (denied)", mode, cmd, out, len(p.reqs), r)
			}

			// No prompter (print mode): refused.
			g = NewGate(GateOptions{Mode: mode, Roots: []string{work(t)}})
			g.SetSandbox(sb)
			if r, out, _ := g.CheckWithOutcome(context.Background(), sandboxBashReq(cmd, false)); r == nil || out == OutcomeAuto {
				t.Errorf("%s, %q, print mode: outcome %q block %+v; want refused", mode, cmd, out, r)
			}
		}

		p := &promptLog{answer: PromptDeny}
		g := NewGate(GateOptions{Mode: mode, Roots: []string{work(t)}})
		g.SetPrompter(p.prompter)
		g.SetSandbox(sb)
		if r, out, _ := g.CheckWithOutcome(context.Background(), sandboxBashReq("go build ./...", false)); r != nil || out != OutcomeAuto || len(p.reqs) != 0 {
			t.Errorf("%s, go build: outcome %q, %d prompts, block %+v; want auto-allowed", mode, out, len(p.reqs), r)
		}
	}
}
