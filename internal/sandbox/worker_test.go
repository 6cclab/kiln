package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestWorkerConfigMandatoryAndRebased(t *testing.T) {
	parent := Config{
		Excluded: []string{"git"}, AllowUnsandboxed: true,
		FilesystemDisabled: false, AllowWrite: []Rule{{Path: "/outside"}},
		DenyRead:      []Rule{{Path: "/parent/private"}, {Path: "/external/private"}},
		DenyWrite:     []Rule{{Path: "/parent/config", Segs: []string{"*.toml"}}},
		AllowRead:     []Rule{{Path: "/parent/public"}},
		HTTPProxyPort: 8000, SOCKSProxyPort: 9000,
		AllowedDomains: []string{"example.com"}, AllowAllUnixSockets: true,
		UnixSockets: []string{"/tmp/sock"}, MachLookup: []string{"*"},
		WeakerNetwork: true, AppleEvents: true,
	}
	c := workerConfig(parent, "/parent", "/child")
	if !c.Enabled || !c.FailIfUnavailable || c.AllowUnsandboxed || len(c.Excluded) != 0 ||
		len(c.AllowWrite) != 0 || c.HTTPProxyPort != 0 || c.SOCKSProxyPort != 0 ||
		len(c.AllowedDomains) != 0 || !c.StrictAllowlist || c.AllowAllUnixSockets ||
		len(c.UnixSockets) != 0 || len(c.MachLookup) != 0 || c.WeakerNetwork || c.AppleEvents {
		t.Fatalf("worker configuration retained an escape: %+v", c)
	}
	if !reflect.DeepEqual(c.DenyRead, []Rule{{Path: "/parent/private"}, {Path: "/child/private"}, {Path: "/external/private"}}) {
		t.Errorf("read denials = %+v", c.DenyRead)
	}
	if !reflect.DeepEqual(c.DenyWrite, []Rule{{Path: "/parent/config", Segs: []string{"*.toml"}}, {Path: "/child/config", Segs: []string{"*.toml"}}}) {
		t.Errorf("write denials = %+v", c.DenyWrite)
	}
	if !reflect.DeepEqual(c.AllowRead, []Rule{{Path: "/parent/public"}, {Path: "/child/public"}}) {
		t.Errorf("read allowances = %+v", c.AllowRead)
	}
	if len(parent.AllowWrite) != 1 || len(parent.Excluded) != 1 {
		t.Fatal("parent configuration mutated")
	}
}

func TestWorkerRejectsUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("worker Seatbelt supported on macOS")
	}
	if _, err := NewWorker(Config{}, Options{}, "/parent", "/child"); err == nil {
		t.Fatal("worker factory accepted a platform without supported isolation")
	}
}

func TestWorkerPlanExcludesSharedRoots(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("worker plan requires macOS Seatbelt")
	}
	base := t.TempDir()
	parent, child, tmp := filepath.Join(base, "parent"), filepath.Join(base, "child"), filepath.Join(base, "tmp")
	for _, d := range []string{parent, child, tmp} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	m := New(workerConfig(Config{AllowWrite: []Rule{{Path: base}}}, parent, child), Options{Cwd: child, TmpDir: tmp})
	m.worker = &workerScope{parent: parent, child: child, temp: tmp, gitDirs: []string{filepath.Join(parent, ".git")}}
	p, proxy, err := m.plan(child)
	if err != nil {
		t.Fatal(err)
	}
	if proxy != nil || p.HTTPProxyPort != 0 || p.SOCKSProxyPort != 0 {
		t.Fatal("worker plan opened a proxy")
	}
	if !reflect.DeepEqual(p.WriteRoots, []string{child, realPath(tmp)}) || len(p.GitDirs) != 0 || len(p.NewGitDirs) != 0 {
		t.Fatalf("worker write roots leaked: %v, git dirs %v, new git dirs %v", p.WriteRoots, p.GitDirs, p.NewGitDirs)
	}
	for _, want := range []string{parent, filepath.Join(parent, ".git"), filepath.Join(child, ".git")} {
		found := false
		for _, r := range p.DenyWrite {
			found = found || r.Path == want
		}
		if !found {
			t.Errorf("missing write denial for %s", want)
		}
	}
}

func TestInheritedRulesRebaseLexicalSymlink(t *testing.T) {
	base := t.TempDir()
	parent, child, outside := filepath.Join(base, "parent"), filepath.Join(base, "child"), filepath.Join(base, "outside")
	for _, d := range []string{parent, child, outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(parent, "link")); err != nil {
		t.Fatal(err)
	}
	got := inheritedRules([]Rule{{Path: filepath.Join(parent, "link", "secret")}}, parent, child)
	want := filepath.Join(child, "link", "secret")
	if len(got) != 2 || got[1].Path != want {
		t.Fatalf("symlink rule was not mapped by lexical path: %+v", got)
	}
}
