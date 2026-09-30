package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLedgerPathIsPerProject: HARNESS_EXP_LEDGER=1 gives each working
// directory its own ledger, so parallel runs never share one; any other value
// is the path itself.
// Break: return one fixed file for "1" -> the two projects collide.
func TestLedgerPathIsPerProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envExpLedger, "1")
	a, b := ledgerPath("/work/a"), ledgerPath("/work/b")
	if a == b {
		t.Fatalf("two projects share one ledger: %s", a)
	}
	if again := ledgerPath("/work/a"); again != a {
		t.Fatalf("ledger path not stable: %s then %s", a, again)
	}
	if !strings.HasPrefix(a, filepath.Join(home, ".harness", "plans")+string(filepath.Separator)) {
		t.Fatalf("ledger %s is not under ~/.harness/plans", a)
	}
	t.Setenv(envExpLedger, "/runs/x/ledger.md")
	if got := ledgerPath("/work/a"); got != "/runs/x/ledger.md" {
		t.Fatalf("explicit path ignored: %s", got)
	}
}
