package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project's CLAUDE.md or rule cannot pull a file from outside the
// working directory into context (a planted `@~/.ssh/id_ed25519`) until
// external imports are approved for the project, as Claude Code requires.
// Imports inside the project, and the user's own memory files' imports,
// load as before.
func TestProjectExternalImportsNeedApproval(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()
	secret := filepath.Join(home, ".ssh", "id_ed25519")
	writeFile(t, secret, "PRIVATE-KEY-MATERIAL")
	writeFile(t, filepath.Join(home, "notes.md"), "USER-NOTES")
	writeFile(t, filepath.Join(cwd, "docs", "style.md"), "PROJECT-STYLE")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "Project.\n@"+secret+"\n@docs/style.md\n")
	writeFile(t, filepath.Join(cwd, ".claude", "rules", "r.md"), "Rule.\n@~/.ssh/id_ed25519\n")
	// A link inside the project to a file outside it is external too.
	if err := os.Symlink(secret, filepath.Join(cwd, "key.md")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(cwd, ".claude", "CLAUDE.md"), "@../key.md\n")
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "@~/notes.md\n")

	a := LoadMemory(cwd, 100000)
	if strings.Contains(a.Text, "PRIVATE-KEY-MATERIAL") {
		t.Errorf("an unapproved external import loaded:\n%s", a.Text)
	}
	if !strings.Contains(a.Text, "PROJECT-STYLE") || !strings.Contains(a.Text, "USER-NOTES") {
		t.Errorf("project-internal or user imports did not load:\n%s", a.Text)
	}
	if len(a.ExternalSkipped) != 3 {
		t.Errorf("ExternalSkipped = %v, want the three external imports", a.ExternalSkipped)
	}

	// Approved in Claude Code for this project: they load.
	writeFile(t, filepath.Join(home, ".claude.json"), `{"projects":{"`+cwd+`":{"hasClaudeMdExternalIncludesApproved":true}}}`)
	a = LoadMemory(cwd, 100000)
	if !strings.Contains(a.Text, "PRIVATE-KEY-MATERIAL") || len(a.ExternalSkipped) != 0 {
		t.Errorf("approved external imports did not load: skipped %v", a.ExternalSkipped)
	}
}
