package permission

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// In auto mode a command the sandbox runs under autoAllowBashIfSandboxed
// is approved without the classifier, as Claude Code's decision order
// approves sandboxed shell commands before the classifier step
// (code.claude.com/docs/en/permission-modes, "How the classifier
// evaluates actions"; kiln has no server-side review or per-command
// domains, its two exceptions). A command outside the sandbox (an
// excludedCommands match, an unsandboxed retry) still goes to the
// classifier, and deny rules still refuse first.
func TestSandboxAutoAllowSkipsClassifierInAutoMode(t *testing.T) {
	sb := fakeSandbox{active: true, autoAllow: true, unsandboxed: true}
	newGate := func(c Classifier, perms settings.Permissions) *Gate {
		g := NewGate(GateOptions{Permissions: perms, Mode: settings.ModeAuto, Roots: []string{work(t)}, Classifier: c})
		g.SetSandbox(sb)
		return g
	}

	c := blockAll("[test] should not be asked")
	g := newGate(c, settings.Permissions{})
	if r, out, err := g.CheckWithOutcome(context.Background(), sandboxBashReq("make build", false)); err != nil || r != nil || out != OutcomeAuto {
		t.Errorf("sandboxed command: block %+v outcome %q err %v, want auto-allowed", r, out, err)
	}
	if len(c.calls) != 0 {
		t.Errorf("the classifier reviewed a sandboxed, auto-allowed command (%d calls)", len(c.calls))
	}

	// A write auto mode's protected-path list names (protected.go) goes to
	// the classifier even inside the sandbox, as a protected-path write
	// does past an allow rule: the sandbox's own protected list is narrower
	// (it must leave git and builds working), so .husky hooks or .envrc
	// would otherwise be written unreviewed.
	for _, req := range []Request{sandboxBashReq("docker ps", false), sandboxBashReq("make build", true),
		sandboxBashReq("echo x > .husky/pre-commit", false), sandboxBashReq("echo x > .envrc", false),
		// No protected path named, but git's configuration changes: auto
		// mode classifies that whatever allows it (bashTouchesProtected).
		sandboxBashReq("git config core.hooksPath hooks", false)} {
		c := allowAll()
		g := newGate(c, settings.Permissions{})
		if _, _, err := g.CheckWithOutcome(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if len(c.calls) != 1 {
			t.Errorf("%q (disable=%v): classifier called %d times, want 1", req.PrimaryArg, req.Args[DisableSandboxArg], len(c.calls))
		}
	}

	c = allowAll()
	g = newGate(c, settings.Permissions{Deny: []string{"Bash(make *)"}})
	if r, _, _ := g.CheckWithOutcome(context.Background(), sandboxBashReq("make build", false)); r == nil {
		t.Error("a deny rule did not refuse a sandboxed command in auto mode")
	}
}
