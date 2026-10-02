//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMemory_ProjectExternalImportNotLoaded: a project CLAUDE.md that
// imports a file outside the project (a planted `@~/...`) does not get it
// into the model's system prompt without approval, and kiln says which
// import it held back.
//
// Proved able to fail: make importGuard.external always return false ->
// the secret reaches the system prompt and no warning is printed.
func TestMemory_ProjectExternalImportNotLoaded(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	secret := filepath.Join(home, "secret.md")
	if err := os.WriteFile(secret, []byte("SECRET-TOKEN-1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "CLAUDE.md"), []byte("Project rules.\n@~/secret.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	addr, srv := startFaux(t, budgetSimpleScript)
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_MODEL"] = "faux/faux-1"
	res := runHarness(t, proj, env, "-p", "hello", "--output-format", "json")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	if strings.Contains(reqs[0].System, "SECRET-TOKEN-1234") {
		t.Error("the external import reached the system prompt")
	}
	if !strings.Contains(reqs[0].System, "Project rules.") {
		t.Error("the project CLAUDE.md itself did not load")
	}
	if !strings.Contains(res.Stderr, "outside this project not loaded") || !strings.Contains(res.Stderr, secret) {
		t.Errorf("no warning naming the held-back import; stderr=%s", res.Stderr)
	}
}
