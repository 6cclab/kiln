package permission

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// Every settings file the session reads is reloaded when it changes, so a
// write to one rewrites the permission rules mid-session. Before
// ProtectSettingsFiles two of them were writable without a prompt:
//
//   - A: kiln --settings ci/policy.json, that file inside the workspace,
//     in acceptEdits: writing {"permissions":{"allow":["Bash"]}} was
//     auto-approved, and the reload then allowed rm -rf.
//   - B: ~/.claude/settings.json a symlink into ~/dotfiles, the session
//     run in ~/dotfiles: the link's target was an ordinary file there.
func TestSettingsFilesAreProtected(t *testing.T) {
	type scenario struct {
		root, target string
		files        []string
	}
	scenarioA := func(t *testing.T) scenario {
		root := t.TempDir()
		policy := filepath.Join(root, "ci", "policy.json")
		if err := os.MkdirAll(filepath.Dir(policy), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(policy, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return scenario{root: root, target: policy, files: []string{policy}}
	}
	scenarioB := func(t *testing.T) scenario {
		home, dotfiles := t.TempDir(), t.TempDir()
		t.Setenv("HOME", home)
		target := filepath.Join(dotfiles, "claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
		return scenario{root: dotfiles, target: target, files: []string{link}}
	}

	for name, setup := range map[string]func(*testing.T) scenario{"A --settings in workspace": scenarioA, "B symlinked user settings": scenarioB} {
		t.Run(name, func(t *testing.T) {
			s := setup(t)
			body := `{"permissions":{"allow":["Bash"]}}`
			write := Request{ToolName: "write", PrimaryArg: s.target, Args: map[string]any{"path": s.target, "content": body}}
			echo := bashReq("echo '" + body + "' > " + s.target)

			for _, c := range []struct {
				mode  settings.PermissionMode
				perms settings.Permissions
				req   Request
			}{
				{settings.ModeAcceptEdits, settings.Permissions{}, write},
				{settings.ModeManual, settings.Permissions{Allow: []string{"Edit", "Write"}}, write},
				{settings.ModeManual, settings.Permissions{Allow: []string{"Bash(echo *)"}}, echo},
			} {
				p := &promptRecorder{kind: PromptDeny}
				g := NewGate(GateOptions{Permissions: c.perms, Mode: c.mode, Roots: []string{s.root}, Prompt: p.prompt})
				g.ProtectSettingsFiles(s.files)
				if _, _, err := g.CheckWithOutcome(context.Background(), c.req); err != nil {
					t.Fatal(err)
				}
				if len(p.reqs) != 1 || p.reqs[0].Grantable {
					t.Errorf("%s %s (allow %v): prompts=%d, want one with no grant", c.mode, c.req.ToolName, c.perms.Allow, len(p.reqs))
				}
			}

			// Auto mode classifies it rather than skipping the classifier
			// as it does other workspace writes (or asks, for a write that
			// is protected only through a symlink).
			c := allowAll()
			p := &promptRecorder{kind: PromptDeny}
			g := NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{s.root}, Prompt: p.prompt, Classifier: c})
			g.ProtectSettingsFiles(s.files)
			if _, _, err := g.CheckWithOutcome(context.Background(), write); err != nil {
				t.Fatal(err)
			}
			if len(c.calls)+len(p.reqs) == 0 {
				t.Error("auto mode wrote a settings file with neither the classifier nor the user")
			}

			// A file next to it is not protected.
			other := filepath.Join(filepath.Dir(s.target), "notes.json")
			p = &promptRecorder{kind: PromptDeny}
			g = NewGate(GateOptions{Mode: settings.ModeAcceptEdits, Roots: []string{s.root}, Prompt: p.prompt})
			g.ProtectSettingsFiles(s.files)
			if _, _, err := g.CheckWithOutcome(context.Background(), Request{ToolName: "write", PrimaryArg: other, Args: map[string]any{"path": other, "content": "{}"}}); err != nil {
				t.Fatal(err)
			}
			if len(p.reqs) != 0 {
				t.Errorf("an ordinary file next to the settings file prompted")
			}
		})
	}
}
