package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
)

// The QA run: mid-session, a project .claude/settings.json gained
// allow ["Bash(go test *)"], and "go test" kept prompting until kiln was
// restarted. Claude Code watches its settings files and reloads the
// permission rules; kiln now does too.

type reloadFixture struct {
	cwd, home string
	gate      *permission.Gate
	mu        sync.Mutex
	notices   []string
	trusted   bool
	reloadMu  sync.Mutex
}

func newReloadFixture(t *testing.T) *reloadFixture {
	t.Helper()
	f := &reloadFixture{cwd: t.TempDir(), home: t.TempDir()}
	t.Setenv("HOME", f.home)
	t.Setenv("USERPROFILE", f.home)
	t.Setenv("HARNESS_TRUST_ALL", "")
	s := claudesettings.LoadSettings(f.cwd, claudesettings.LoadOptions{})
	f.gate = permission.NewGate(permission.GateOptions{Permissions: s.Permissions, Mode: claudesettings.ModeManual, Roots: []string{f.cwd}})
	return f
}

func (f *reloadFixture) reloader() settingsReloader {
	return settingsReloader{
		mu:      &f.reloadMu,
		cwd:     f.cwd,
		gate:    f.gate,
		trusted: func() bool { return f.trusted },
		notice: func(s string) {
			f.mu.Lock()
			f.notices = append(f.notices, s)
			f.mu.Unlock()
		},
	}
}

func (f *reloadFixture) write(t *testing.T, rel, content string) string {
	t.Helper()
	p := filepath.Join(f.cwd, rel)
	if strings.HasPrefix(rel, "~/") {
		p = filepath.Join(f.home, rel[2:])
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *reloadFixture) decide(cmd string) claudesettings.Decision {
	return claudesettings.DecideIn(f.gate.Permissions(), f.cwd, "bash", cmd, claudesettings.ModeManual)
}

func TestSettingsReload_WatchAppliesANewProjectRule(t *testing.T) {
	f := newReloadFixture(t)
	f.trusted = true // an untrusted folder holds the new allow rule (TestSettingsReload_UntrustedProjectAllowHeld)
	if got := f.decide("go test -count=1 ./..."); got != claudesettings.Ask {
		t.Fatalf("before: %v, want ask", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.reloader().watch(ctx, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond) // the watcher's baseline is the empty state
	f.write(t, ".claude/settings.json", `{"permissions":{"allow":["Bash(go test *)","Bash(go build *)"]}}`)

	deadline := time.Now().Add(5 * time.Second)
	for f.decide("go test -count=1 ./...") != claudesettings.Allow {
		if time.Now().After(deadline) {
			t.Fatalf("the new allow rule never applied; rules %+v", f.gate.Permissions())
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.notices) == 0 || !strings.Contains(f.notices[0], filepath.Join(".claude", "settings.json")) {
		t.Errorf("notices = %v, want one naming the file", f.notices)
	}
}

func TestSettingsReload_DenyAddedAndRemoved(t *testing.T) {
	f := newReloadFixture(t)
	r := f.reloader()
	user := f.write(t, "~/.claude/settings.json", `{"permissions":{"allow":["Bash"],"deny":["Bash(rm *)"]}}`)
	r.reload([]string{user})
	if got := f.decide("rm -rf build"); got != claudesettings.Deny {
		t.Fatalf("after adding the deny rule: %v, want deny", got)
	}
	f.write(t, "~/.claude/settings.json", `{"permissions":{"allow":["Bash"]}}`)
	r.reload([]string{user})
	if got := f.decide("rm -rf build"); got != claudesettings.Allow {
		t.Errorf("after removing it: %v, want allow", got)
	}
}

// A file caught halfway through a save (or with a typo) must not drop its
// deny rules: the reload keeps every rule as it was and says why.
func TestSettingsReload_UnreadableFileKeepsTheRules(t *testing.T) {
	f := newReloadFixture(t)
	r := f.reloader()
	p := f.write(t, ".claude/settings.json", `{"permissions":{"deny":["Bash(rm *)"]}}`)
	r.reload([]string{p})
	f.write(t, ".claude/settings.json", `{"permissions":{"deny":["Bash(rm *)"`)
	r.reload([]string{p})
	if got := f.decide("rm -rf build"); got != claudesettings.Deny {
		t.Errorf("deny rule lost to a malformed file: %v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if last := f.notices[len(f.notices)-1]; !strings.Contains(last, "could not be read") {
		t.Errorf("notice = %q", last)
	}
}

// A repository-supplied .kiln/settings.local.json (its .kiln a symlink, as
// a clone could ship) keeps its allow rules held on a reload until the
// folder is trusted; its deny rules apply either way.
func TestSettingsReload_RepoSuppliedKilnFileStaysHeld(t *testing.T) {
	f := newReloadFixture(t)
	shipped := t.TempDir()
	if err := os.WriteFile(filepath.Join(shipped, "settings.local.json"), []byte(`{"permissions":{"allow":["Bash(curl *)"],"deny":["Bash(rm *)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shipped, filepath.Join(f.cwd, ".kiln")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	r := f.reloader()
	r.reload([]string{filepath.Join(f.cwd, ".kiln", "settings.local.json")})
	if got := f.decide("curl https://example.com"); got != claudesettings.Ask {
		t.Errorf("untrusted: curl = %v, want ask (allow held)", got)
	}
	if got := f.decide("rm -rf build"); got != claudesettings.Deny {
		t.Errorf("untrusted: rm = %v, want deny", got)
	}
	f.trusted = true
	r.reload([]string{filepath.Join(f.cwd, ".kiln", "settings.local.json")})
	if got := f.decide("curl https://example.com"); got != claudesettings.Allow {
		t.Errorf("trusted: curl = %v, want allow", got)
	}
}

// Command-line rules (--allowed-tools, --disallowed-tools) survive a
// reload.
func TestSettingsReload_KeepsCommandLineRules(t *testing.T) {
	f := newReloadFixture(t)
	f.trusted = true
	f.gate = permission.NewGate(permission.GateOptions{
		Permissions: claudesettings.Permissions{Allow: []string{"Bash(make *)"}, Deny: []string{"Bash(git push *)"}},
		Mode:        claudesettings.ModeManual, Roots: []string{f.cwd},
	})
	p := f.write(t, ".claude/settings.json", `{"permissions":{"allow":["Bash(go test *)"]}}`)
	f.reloader().reload([]string{p})
	if f.decide("make build") != claudesettings.Allow || f.decide("git push") != claudesettings.Deny || f.decide("go test ./...") != claudesettings.Allow {
		t.Errorf("rules after reload: %+v", f.gate.Permissions())
	}
}

// Before the folder is trusted, a reload holds a project's allow rules as
// startup did (a hostile clone cannot add one mid-session and have it
// apply), while its deny rules apply at once. Accepting the trust dialog
// then applies the allow rules as the file says now.
func TestSettingsReload_UntrustedProjectAllowHeld(t *testing.T) {
	f := newReloadFixture(t)
	r := f.reloader()
	p := f.write(t, ".claude/settings.json", `{"permissions":{"allow":["Bash(curl *)"],"deny":["Bash(rm *)"]}}`)
	r.reload([]string{p})
	if got := f.decide("curl https://example.com"); got != claudesettings.Ask {
		t.Errorf("untrusted reload: curl = %v, want ask (allow held)", got)
	}
	if got := f.decide("rm -rf build"); got != claudesettings.Deny {
		t.Errorf("untrusted reload: rm = %v, want deny", got)
	}

	// Edited again before the dialog is answered: trust applies the file
	// as it is now, not the rules seen at the last reload.
	f.write(t, ".claude/settings.json", `{"permissions":{"allow":["Bash(wget *)"],"deny":["Bash(rm *)"]}}`)
	f.trusted = true
	r.applyTrust()
	if got := f.decide("wget https://example.com"); got != claudesettings.Allow {
		t.Errorf("after trust: wget = %v, want allow", got)
	}
	if got := f.decide("curl https://example.com"); got != claudesettings.Ask {
		t.Errorf("after trust: curl = %v, want ask (the rule left the file)", got)
	}
	if got := f.decide("rm -rf build"); got != claudesettings.Deny {
		t.Errorf("after trust: rm = %v, want deny", got)
	}
}

// A project file that does not parse when trust is accepted keeps the
// rules as they were and says why, as a reload does.
func TestSettingsReload_TrustWithUnreadableFileKeepsRules(t *testing.T) {
	f := newReloadFixture(t)
	r := f.reloader()
	p := f.write(t, ".claude/settings.json", `{"permissions":{"deny":["Bash(rm *)"]}}`)
	r.reload([]string{p})
	f.write(t, ".claude/settings.json", `{"permissions":{"deny":[`)
	f.trusted = true
	r.applyTrust()
	if got := f.decide("rm -rf build"); got != claudesettings.Deny {
		t.Errorf("deny lost on trust: %v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if last := f.notices[len(f.notices)-1]; !strings.HasPrefix(last, "Folder trusted, but") {
		t.Errorf("notice = %q", last)
	}
}

// A reload that read the files while the folder was untrusted, and is
// about to swap in the rules it read, must not land after the trust
// dialog applied the trusted ones: the allow rules trust released would
// be dropped again. applyTrust waits for it, then applies.
func TestSettingsReload_TrustDuringReloadKeepsAllowRules(t *testing.T) {
	f := newReloadFixture(t)
	p := f.write(t, ".claude/settings.json", `{"permissions":{"allow":["Bash(curl *)"]}}`)
	var trustMu sync.Mutex
	r := f.reloader()
	trustDone := make(chan struct{})
	r.loaded = func() {
		// The poller has read the files as untrusted; the person accepts
		// the dialog now.
		r.loaded = nil
		trustMu.Lock()
		f.trusted = true
		trustMu.Unlock()
		go func() {
			trusting := r
			trusting.loaded = nil
			trusting.applyTrust()
			close(trustDone)
		}()
		time.Sleep(50 * time.Millisecond) // applyTrust runs now, if nothing stops it
	}
	r.trusted = func() bool { trustMu.Lock(); defer trustMu.Unlock(); return f.trusted }
	r.reload([]string{p})
	<-trustDone
	if got := f.decide("curl https://example.com"); got != claudesettings.Allow {
		t.Errorf("curl = %v after trust, want allow (a stale reload dropped the trusted rules)", got)
	}
}
