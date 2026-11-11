//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/learning"
)

func TestOptInOrdinaryRunObservationAndForget(t *testing.T) {
	home, sessDir := scratchHome(t)
	project := scratchProject(t)
	addr, _ := startFaux(t, "model: faux-1\nsteps:\n  - text: done\n")
	env := baseEnv(home, sessDir, addr)
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"learningAutomation":{"enabled":true,"maxOutcomes":2,"minSamples":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := runHarness(t, project, env, "-p", "a private prompt that must not be stored", "--permission-mode", "bypassPermissions", "--settings", settings)
	if run.Code != 0 {
		t.Fatalf("observed run: %s", run.Stderr)
	}
	listing := runHarness(t, project, env, "learning", "outcomes")
	if listing.Code != 0 {
		t.Fatalf("outcomes: %s", listing.Stderr)
	}
	var outcomes []learning.Outcome
	if err := json.Unmarshal([]byte(listing.Stdout), &outcomes); err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "completed" || strings.Contains(listing.Stdout, "private prompt") {
		t.Fatalf("outcome missing or unredacted: %s", listing.Stdout)
	}
	if err := os.WriteFile(settings, []byte(`{"learningAutomation":{"enabled":false,"maxOutcomes":2,"minSamples":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	run = runHarness(t, project, env, "-p", "another private prompt", "--permission-mode", "bypassPermissions", "--settings", settings)
	if run.Code != 0 {
		t.Fatalf("disabled run: %s", run.Stderr)
	}
	listing = runHarness(t, project, env, "learning", "outcomes")
	if listing.Code != 0 {
		t.Fatalf("outcomes after disabled run: %s", listing.Stderr)
	}
	if err := json.Unmarshal([]byte(listing.Stdout), &outcomes); err != nil || len(outcomes) != 1 {
		t.Fatalf("disabled run was observed: %s %v", listing.Stdout, err)
	}
	if err := os.WriteFile(settings, []byte(`{"learningEnabled":false,"learningAutomation":{"enabled":true,"maxOutcomes":2,"minSamples":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	run = runHarness(t, project, env, "-p", "master learning switch disabled", "--permission-mode", "bypassPermissions", "--settings", settings)
	if run.Code != 0 {
		t.Fatalf("master-disabled run: %s", run.Stderr)
	}
	listing = runHarness(t, project, env, "learning", "outcomes")
	if err := json.Unmarshal([]byte(listing.Stdout), &outcomes); err != nil || listing.Code != 0 || len(outcomes) != 1 {
		t.Fatalf("master-disabled run was observed: %s %v", listing.Stdout, err)
	}
	forgot := runHarness(t, project, env, "learning", "forget-outcomes")
	if forgot.Code != 0 {
		t.Fatalf("forget: %s", forgot.Stderr)
	}
	listing = runHarness(t, project, env, "learning", "outcomes")
	if listing.Code != 0 || strings.TrimSpace(listing.Stdout) != "[]" {
		t.Fatalf("forgotten outcomes remain: %s %s", listing.Stdout, listing.Stderr)
	}
}
