package permission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// A prompt tells the UI which "Yes, and switch to …" options would mean
// anything: none for a protected-path write (no mode but bypass approves
// it) or an ask rule, and not accept edits while it is already on. The QA
// run saw a protected .claude/settings.json edit in acceptEdits offer
// "switch to accept edits", which did nothing.
func TestPromptSaysWhichModeSwitchMeansAnything(t *testing.T) {
	f := newProtectedFixture(t)
	ask := func(mode settings.PermissionMode, perms settings.Permissions, req Request) Request {
		t.Helper()
		g := f.gate(mode, perms, nil)
		p := &promptRecorder{kind: PromptAllow}
		g.SetPrompter(p.prompt)
		if _, _, err := g.CheckWithOutcome(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if len(p.reqs) != 1 {
			t.Fatalf("prompts = %d, want 1", len(p.reqs))
		}
		return p.reqs[0]
	}

	protected := editReq(filepath.Join(f.root, ".claude", "settings.json"))
	if r := ask(settings.ModeAcceptEdits, settings.Permissions{}, protected); !r.InAcceptEdits || !r.ModeSwitchMoot {
		t.Errorf("protected edit in acceptEdits: inAcceptEdits=%v moot=%v, want both", r.InAcceptEdits, r.ModeSwitchMoot)
	}
	if r := ask(settings.ModeManual, settings.Permissions{}, protected); r.InAcceptEdits || !r.ModeSwitchMoot {
		t.Errorf("protected edit in manual: inAcceptEdits=%v moot=%v, want moot only", r.InAcceptEdits, r.ModeSwitchMoot)
	}
	if r := ask(settings.ModeManual, settings.Permissions{Ask: []string{"Bash(make *)"}}, bashReq("make deploy")); !r.ModeSwitchMoot {
		t.Error("ask-rule prompt: moot = false, want true")
	}
	// An ordinary prompt still offers the switch.
	if r := ask(settings.ModeManual, settings.Permissions{}, editReq(filepath.Join(f.root, "main.go"))); r.ModeSwitchMoot || r.InAcceptEdits {
		t.Errorf("ordinary edit in manual: moot=%v inAcceptEdits=%v, want neither", r.ModeSwitchMoot, r.InAcceptEdits)
	}
	if r := ask(settings.ModeAcceptEdits, settings.Permissions{}, bashReq("make build")); r.ModeSwitchMoot || !r.InAcceptEdits {
		t.Errorf("bash in acceptEdits: moot=%v inAcceptEdits=%v, want inAcceptEdits only", r.ModeSwitchMoot, r.InAcceptEdits)
	}
}
