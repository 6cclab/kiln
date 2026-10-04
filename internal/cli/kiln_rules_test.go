package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/writesettings"
)

// TestRemoveKilnRule: /permissions deletes a rule kiln saved (in
// .kiln/settings.local.json) but refuses one from Claude Code's
// .claude/settings.local.json, naming that file and leaving it as it was.
func TestRemoveKilnRule(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	ccFile := filepath.Join(cwd, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(ccFile), 0o755); err != nil {
		t.Fatal(err)
	}
	ccBody := `{"permissions":{"allow":["Bash(ls *)"]}}`
	if err := os.WriteFile(ccFile, []byte(ccBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writesettings.AddRule(cwd, writesettings.Allow, "Bash(npm test *)"); err != nil {
		t.Fatal(err)
	}
	s := claudesettings.LoadSettings(cwd, claudesettings.LoadOptions{Trusted: true})
	gate := permission.NewGate(permission.GateOptions{Permissions: s.Permissions, Roots: []string{cwd}})

	err := removeKilnRule(gate, cwd, writesettings.Allow, "Bash(ls *)")
	if err == nil || !strings.Contains(err.Error(), ccFile) {
		t.Errorf("deleting a .claude rule: err = %v, want a refusal naming %s", err, ccFile)
	}
	if data, _ := os.ReadFile(ccFile); string(data) != ccBody {
		t.Errorf(".claude/settings.local.json changed: %s", data)
	}
	if !contains(gate.Permissions().Allow, "Bash(ls *)") {
		t.Error("the refused rule was dropped from the gate")
	}

	if err := removeKilnRule(gate, cwd, writesettings.Allow, "Bash(npm test *)"); err != nil {
		t.Fatalf("deleting kiln's own rule: %v", err)
	}
	if writesettings.HasRule(cwd, writesettings.Allow, "Bash(npm test *)") || contains(gate.Permissions().Allow, "Bash(npm test *)") {
		t.Error("kiln's own rule was not deleted from the file and the gate")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
