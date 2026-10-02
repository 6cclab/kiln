//go:build e2e && darwin

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestPermission_MacAliasesDenied (final verification HIGH 1-3): on macOS,
// ~/ſecrets/k (APFS folds ſ to s), the /System/Volumes/Data firmlink
// spelling and the /.vol/<dev>/<inode> path all open ~/secrets/k. In
// bypassPermissions only a deny rule can stop them; Read(~/secrets/**)
// must, and the file's contents must never reach the model.
//
// macOS only: /.vol and syscall.Stat_t's Dev/Ino fields are both
// darwin-specific, which is why this moved out of permission_pathrules_test.go
// into its own darwin-tagged file (the old runtime.GOOS skip only kept it
// from running elsewhere; it still failed to compile on Windows, which has
// no syscall.Stat_t).
func TestPermission_MacAliasesDenied(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	secret := filepath.Join(home, "secrets", "k")
	pathRulesWriteFile(t, secret, "TOPSECRET-4f2a\n")
	real, err := filepath.EvalSymlinks(secret)
	if err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(secret, &st); err != nil {
		t.Fatal(err)
	}
	aliases := []string{
		filepath.Join(home, "ſecrets", "K"),
		"/System/Volumes/Data" + real,
		fmt.Sprintf("/.vol/%d/%d", st.Dev, st.Ino),
	}
	var script strings.Builder
	script.WriteString("model: faux-1\nsteps:\n")
	for i, a := range aliases {
		if _, err := os.Stat(a); err != nil {
			t.Skipf("%s does not open the file here", a)
		}
		if i == 0 {
			fmt.Fprintf(&script, "  - tool_call: {name: read, args: {path: %q}, id: r%d}\n", a, i)
		} else {
			fmt.Fprintf(&script, "  - on_tool_result: r%d\n    then:\n      - tool_call: {name: read, args: {path: %q}, id: r%d}\n", i-1, a, i)
		}
	}
	fmt.Fprintf(&script, "  - on_tool_result: r%d\n    then:\n      - text: \"done\"\n", len(aliases)-1)
	pathRulesWriteFile(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"deny":["Read(~/secrets/**)"]}}`)
	addr, srv := startFaux(t, script.String())

	res, _ := pathRulesRun(t, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	for _, a := range aliases {
		if !pathRulesBlocked(res, "read("+a+")") {
			t.Errorf("read %s not blocked by Read(~/secrets/**); blocked=%v", a, res.Blocked)
		}
	}
	for _, r := range srv.Requests() {
		if strings.Contains(string(r.Body), "TOPSECRET-4f2a") {
			t.Fatal("the secret reached the model")
		}
	}
}
