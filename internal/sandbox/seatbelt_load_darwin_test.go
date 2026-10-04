//go:build darwin

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeatbeltProfileLoads: sandbox-exec accepts every line of a profile
// with every option on (Seatbelt's regex dialect refuses some constructs,
// and a profile that fails to load makes every command fail, which tests
// that expect a refusal would mistake for one). Each line is loaded on its
// own, so a failure names the line.
func TestSeatbeltProfileLoads(t *testing.T) {
	p, ws := testPlan(t)
	for _, d := range []string{".git/modules/m", ".git/worktrees/w"} {
		os.MkdirAll(filepath.Join(ws, d), 0o755)
	}
	cfg := Config{
		DenyWrite:   []Rule{{Path: ws, Segs: []string{"**", "secret[!x]", "*.pem"}}},
		AllowWrite:  []Rule{{Path: "/tmp", Segs: []string{"build-*"}}},
		DenyRead:    p.DenyRead,
		AllowRead:   p.AllowRead,
		UnixSockets: []string{"/var/run/x.sock"}, MachLookup: []string{"com.example.*"},
		AllowLocalBinding: true, AppleEvents: true, WeakerNetwork: true,
	}
	p = buildPlan(cfg, ws, []string{ws}, p.TmpDir, filepath.Join(filepath.Dir(ws), "home"), 4242, 4243)
	prof, err := seatbeltProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(prof), "\n")
	for i, line := range lines[2:] {
		test := "(version 1)\n(allow default)\n" + line
		if out, err := exec.Command("/usr/bin/sandbox-exec", "-p", test, "/usr/bin/true").CombinedOutput(); err != nil {
			t.Errorf("line %d does not load: %s\n%s", i+2, line, out)
		}
	}
	if out, err := exec.Command("/usr/bin/sandbox-exec", "-p", prof, "/usr/bin/true").CombinedOutput(); err != nil {
		t.Errorf("profile does not load: %v %s", err, out)
	}
}
