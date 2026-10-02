//go:build darwin

package settings

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

// TestRound4_VolInodePath (HIGH 3): /.vol/<dev>/<inode> is the file it
// names; and a bash operand under /.vol that no rule matches is unsure.
//
// macOS only: /.vol and syscall.Stat_t's Dev/Ino fields are both
// darwin-specific, which is why this moved out of pathrules_round4_test.go
// into its own darwin-tagged file (the old runtime.GOOS skip only kept it
// from running elsewhere; it still failed to compile on Windows, which has
// no syscall.Stat_t).
func TestRound4_VolInodePath(t *testing.T) {
	f := newPathFixture(t)
	mkfile(t, f.h("secrets/k"))
	var st syscall.Stat_t
	if err := syscall.Stat(f.h("secrets/k"), &st); err != nil {
		t.Fatal(err)
	}
	alias := fmt.Sprintf("/.vol/%d/%d", st.Dev, st.Ino)
	if _, err := os.Stat(alias); err != nil {
		t.Skip("no /.vol here")
	}
	p := Permissions{Deny: []string{"Read(~/secrets/**)"}}
	if Decide(p, "read", alias, ModeAuto) != Deny {
		t.Errorf("read %s was not denied", alias)
	}
	if got := bashVerdict(p, "cat "+alias); got != Deny {
		t.Errorf("bash cat %s = %v, want deny", alias, got)
	}
	other := Permissions{Deny: []string{"Read(.env)"}}
	if got := bashVerdict(other, "cat "+alias); got != Ask {
		t.Errorf("bash cat of a /.vol path = %v, want ask", got)
	}
}
