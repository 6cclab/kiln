//go:build darwin

package permission

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// A protected directory reached by a spelling only the OS maps back to
// it — a /.vol/<device>/<inode> path — is still protected: the check also
// compares the kernel's own name for the path (execenv.CanonicalPath).
func TestAutoMode_ProtectedPathVolSpelling(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(gitDir, &st); err != nil {
		t.Fatal(err)
	}
	vol := fmt.Sprintf("/.vol/%d/%d/config", st.Dev, st.Ino)
	c := allowAll()
	g := NewGate(GateOptions{Permissions: settings.Permissions{Allow: []string{"Bash(echo *)"}}, Mode: settings.ModeAuto, Roots: []string{root}, Classifier: c})
	if _, _, err := g.CheckWithOutcome(context.Background(), bashReq("echo '[core]' > "+vol)); err != nil {
		t.Fatal(err)
	}
	if len(c.calls) != 1 {
		t.Errorf("a write to %s (.git/config) skipped the classifier", vol)
	}
}
