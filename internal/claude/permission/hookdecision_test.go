package permission

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// A PreToolUse hook's permissionDecision, as Claude Code applies it
// (resolveHookPermissionDecision; hooks and permissions docs): "allow"
// skips the prompt but not deny rules, ask rules or the safety checks no
// allow approves; "ask" forces the prompt after deny rules. Andre's rtk
// hook rewrites "grep x f" to "rtk grep x f" and returns allow: that must
// run without the prompt it used to raise in acceptEdits.

// rtkBuild is a command the rtk hook rewrote that prompted in the QA run
// (prompts.log t7-1).
const rtkBuild = "rtk go build ./... && echo BUILD_OK"

func hookGate(t *testing.T, mode settings.PermissionMode, perms settings.Permissions, c Classifier) (*Gate, *promptRecorder, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	p := &promptRecorder{kind: PromptAllow}
	g := NewGate(GateOptions{Permissions: perms, Mode: mode, Roots: []string{root}, Prompt: p.prompt, Classifier: c})
	return g, p, root
}

func hookReq(r Request, decision, reason string) Request {
	r.HookDecision, r.HookReason = decision, reason
	return r
}

func TestHookAllowSkipsThePrompt(t *testing.T) {
	for _, mode := range []settings.PermissionMode{settings.ModeManual, settings.ModeAcceptEdits, settings.ModeDontAsk, settings.ModePlan} {
		t.Run(string(mode), func(t *testing.T) {
			g, p, _ := hookGate(t, mode, settings.Permissions{}, nil)
			blocked, out, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq(rtkBuild), HookAllow, "RTK auto-rewrite"))
			if err != nil || blocked != nil {
				t.Fatalf("blocked=%+v err=%v, want allowed", blocked, err)
			}
			if out != OutcomeAuto || len(p.reqs) != 0 {
				t.Errorf("outcome=%q prompts=%d, want auto-approved without a prompt", out, len(p.reqs))
			}
		})
	}
	// Without the hook's allow the same call prompts: the test above is
	// about the decision, not about the command being read-only.
	g, p, _ := hookGate(t, settings.ModeAcceptEdits, settings.Permissions{}, nil)
	if _, _, err := g.CheckWithOutcome(context.Background(), bashReq(rtkBuild)); err != nil || len(p.reqs) != 1 {
		t.Fatalf("without a hook decision: prompts=%d err=%v, want one prompt", len(p.reqs), err)
	}
}

func TestHookAllowSkipsTheAutoModeClassifier(t *testing.T) {
	c := blockAll("should not be asked")
	g, p, _ := hookGate(t, settings.ModeAuto, settings.Permissions{}, c)
	blocked, out, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq("rtk go build ./..."), HookAllow, ""))
	if err != nil || blocked != nil || out != OutcomeAuto {
		t.Fatalf("blocked=%+v out=%q err=%v, want auto-approved", blocked, out, err)
	}
	if len(c.calls) != 0 || len(p.reqs) != 0 {
		t.Errorf("classifier calls=%d prompts=%d, want none", len(c.calls), len(p.reqs))
	}
}

func TestHookAllowDoesNotBeatRules(t *testing.T) {
	t.Run("deny rule denies", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeAcceptEdits, settings.Permissions{Deny: []string{"Bash(rtk rm *)"}}, nil)
		blocked, _, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq("rtk rm -rf build"), HookAllow, ""))
		if err != nil || blocked == nil || len(p.reqs) != 0 {
			t.Fatalf("blocked=%+v prompts=%d err=%v, want denied without a prompt", blocked, len(p.reqs), err)
		}
	})
	t.Run("ask rule asks, and offers no grant", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeAcceptEdits, settings.Permissions{Ask: []string{"Bash(rtk git push *)"}}, nil)
		blocked, out, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq("rtk git push origin main"), HookAllow, ""))
		if err != nil || blocked != nil || out != OutcomeApproved || len(p.reqs) != 1 {
			t.Fatalf("blocked=%+v out=%q prompts=%d err=%v, want one prompt", blocked, out, len(p.reqs), err)
		}
		if r := p.reqs[0]; r.Grantable || len(r.DontAskRules) != 0 || !r.ModeSwitchMoot {
			t.Errorf("prompt grantable=%v rules=%v moot=%v, want no grant and no mode switch", r.Grantable, r.DontAskRules, r.ModeSwitchMoot)
		}
	})
	t.Run("ask rule asks in auto mode too, without the classifier", func(t *testing.T) {
		c := allowAll()
		g, p, _ := hookGate(t, settings.ModeAuto, settings.Permissions{Ask: []string{"Bash(rtk git push *)"}}, c)
		if _, _, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq("rtk git push origin main"), HookAllow, "")); err != nil {
			t.Fatal(err)
		}
		if len(p.reqs) != 1 || len(c.calls) != 0 {
			t.Errorf("prompts=%d classifier=%d, want one prompt and no classifier", len(p.reqs), len(c.calls))
		}
	})
	t.Run("protected path still asks", func(t *testing.T) {
		g, p, root := hookGate(t, settings.ModeAcceptEdits, settings.Permissions{}, nil)
		path := filepath.Join(root, ".git", "config")
		req := hookReq(Request{ToolName: "write", PrimaryArg: path, Args: map[string]any{"path": path, "content": "x"}}, HookAllow, "")
		if _, _, err := g.CheckWithOutcome(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if len(p.reqs) != 1 || p.reqs[0].Grantable {
			t.Fatalf("prompts=%d, want one prompt with no grant", len(p.reqs))
		}
	})
	t.Run("a bash write to a protected path still asks", func(t *testing.T) {
		for _, mode := range []settings.PermissionMode{settings.ModeManual, settings.ModeAcceptEdits} {
			g, p, _ := hookGate(t, mode, settings.Permissions{}, nil)
			if _, _, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq("echo '[core]' >> .git/config"), HookAllow, "")); err != nil {
				t.Fatal(err)
			}
			if len(p.reqs) != 1 {
				t.Errorf("%s: prompts=%d, want one", mode, len(p.reqs))
			}
		}
		c := allowAll()
		g, p, _ := hookGate(t, settings.ModeAuto, settings.Permissions{}, c)
		if _, _, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq("git config core.hooksPath /tmp/h"), HookAllow, "")); err != nil {
			t.Fatal(err)
		}
		if len(c.calls)+len(p.reqs) == 0 {
			t.Error("auto mode: a hook allow skipped both the classifier and the user for a git config write")
		}
	})
	t.Run("protected path refused in dontAsk", func(t *testing.T) {
		g, _, root := hookGate(t, settings.ModeDontAsk, settings.Permissions{}, nil)
		path := filepath.Join(root, ".claude", "settings.json")
		req := hookReq(Request{ToolName: "write", PrimaryArg: path, Args: map[string]any{"path": path, "content": "{}"}}, HookAllow, "")
		if blocked, _, _ := g.CheckWithOutcome(context.Background(), req); blocked == nil {
			t.Fatal("a hook allow approved a protected-path write in dontAsk")
		}
	})
	t.Run("plan mode still refuses an edit", func(t *testing.T) {
		g, p, root := hookGate(t, settings.ModePlan, settings.Permissions{}, nil)
		path := filepath.Join(root, "a.go")
		req := hookReq(Request{ToolName: "edit", PrimaryArg: path, Args: map[string]any{"path": path}}, HookAllow, "")
		if blocked, _, _ := g.CheckWithOutcome(context.Background(), req); blocked == nil || len(p.reqs) != 0 {
			t.Fatalf("blocked=%+v prompts=%d, want plan mode's refusal", blocked, len(p.reqs))
		}
	})
	t.Run("a file it cannot see while path rules exist still asks", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeAcceptEdits, settings.Permissions{Deny: []string{"Read(./secrets/**)"}}, nil)
		if _, _, err := g.CheckWithOutcome(context.Background(), hookReq(bashReq("rtk cat $F"), HookAllow, "")); err != nil {
			t.Fatal(err)
		}
		if len(p.reqs) != 1 {
			t.Errorf("prompts=%d, want one", len(p.reqs))
		}
	})
}

func TestHookAskForcesThePrompt(t *testing.T) {
	cases := []struct {
		name  string
		mode  settings.PermissionMode
		perms settings.Permissions
		req   Request
	}{
		{"read-only bash", settings.ModeManual, settings.Permissions{}, bashReq("ls")},
		{"allow rule", settings.ModeManual, settings.Permissions{Allow: []string{"Bash(make *)"}}, bashReq("make build")},
		{"acceptEdits edit", settings.ModeAcceptEdits, settings.Permissions{}, Request{ToolName: "edit", PrimaryArg: "a.go", Args: map[string]any{"path": "a.go"}}},
		{"bypassPermissions", settings.ModeBypassPermissions, settings.Permissions{}, bashReq("make build")},
		{"auto mode, no classifier", settings.ModeAuto, settings.Permissions{}, bashReq("make build")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := allowAll()
			g, p, root := hookGate(t, c.mode, c.perms, cl)
			req := c.req
			if path, ok := req.Args["path"].(string); ok {
				req.Args = map[string]any{"path": filepath.Join(root, path)}
				req.PrimaryArg = req.Args["path"].(string)
			}
			blocked, out, err := g.CheckWithOutcome(context.Background(), hookReq(req, HookAsk, "check the target first."))
			if err != nil || blocked != nil || out != OutcomeApproved {
				t.Fatalf("blocked=%+v out=%q err=%v, want approved at a prompt", blocked, out, err)
			}
			if len(p.reqs) != 1 || len(cl.calls) != 0 {
				t.Fatalf("prompts=%d classifier=%d, want one prompt and no classifier", len(p.reqs), len(cl.calls))
			}
			r := p.reqs[0]
			if r.Grantable || !r.ModeSwitchMoot {
				t.Errorf("grantable=%v moot=%v, want neither a grant nor a mode switch", r.Grantable, r.ModeSwitchMoot)
			}
			if r.AutoModeNote != "A PreToolUse hook asked for your approval: check the target first." {
				t.Errorf("note = %q", r.AutoModeNote)
			}
		})
	}
	t.Run("a path outside the workspace is still marked", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeManual, settings.Permissions{}, nil)
		outside := filepath.Join(t.TempDir(), "notes.txt")
		req := Request{ToolName: "write", PrimaryArg: outside, Args: map[string]any{"path": outside, "content": "x"}}
		if _, _, err := g.CheckWithOutcome(context.Background(), hookReq(req, HookAsk, "")); err != nil {
			t.Fatal(err)
		}
		if len(p.reqs) != 1 || !p.reqs[0].OutsideWorkspace {
			t.Errorf("prompts=%+v, want one marked outside the workspace", p.reqs)
		}
	})
	t.Run("a deny rule still denies", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeManual, settings.Permissions{Deny: []string{"Bash(make *)"}}, nil)
		if blocked, _, _ := g.CheckWithOutcome(context.Background(), hookReq(bashReq("make build"), HookAsk, "")); blocked == nil || len(p.reqs) != 0 {
			t.Fatalf("blocked=%+v prompts=%d, want the deny rule's refusal", blocked, len(p.reqs))
		}
	})
	t.Run("dontAsk refuses it", func(t *testing.T) {
		g, p, _ := hookGate(t, settings.ModeDontAsk, settings.Permissions{Allow: []string{"Bash(make *)"}}, nil)
		if blocked, _, _ := g.CheckWithOutcome(context.Background(), hookReq(bashReq("make build"), HookAsk, "")); blocked == nil || len(p.reqs) != 0 {
			t.Fatalf("blocked=%+v prompts=%d, want refused", blocked, len(p.reqs))
		}
	})
	t.Run("headless refuses it and says why", func(t *testing.T) {
		root := t.TempDir()
		g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{root}})
		blocked, _, _ := g.CheckWithOutcome(context.Background(), hookReq(bashReq("ls"), HookAsk, "needs a human"))
		if blocked == nil || !strings.Contains(blocked.Reason, "needs a human") {
			t.Fatalf("blocked=%+v, want a refusal naming the hook's reason", blocked)
		}
	})
	t.Run("a session grant does not skip it", func(t *testing.T) {
		g, p, root := hookGate(t, settings.ModeManual, settings.Permissions{}, nil)
		path := filepath.Join(root, "notes.txt")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		req := Request{ToolName: "web_fetch", PrimaryArg: "https://go.dev", Args: map[string]any{"url": "https://go.dev"}}
		p.kind = PromptAllowAlways
		if _, _, err := g.CheckWithOutcome(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		p.kind = PromptAllow
		if _, _, err := g.CheckWithOutcome(context.Background(), hookReq(req, HookAsk, "")); err != nil {
			t.Fatal(err)
		}
		if len(p.reqs) != 2 {
			t.Errorf("prompts=%d, want the hook's ask to prompt past the session grant", len(p.reqs))
		}
	})
}
