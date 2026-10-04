//go:build e2e

package e2e

import (
	"testing"
)

// TestResumeContinuesOnTheSessionsModel: `kiln -c` resumes on the model
// the session last ran on (as pi does), not the configured default, and
// an explicit --model still wins and is the model the requests go to.
// Before, -c showed the default model (and its window) in the footer while
// the lane kept sending to the session's model.
func TestResumeContinuesOnTheSessionsModel(t *testing.T) {
	addr, srv := startFaux(t, `
models:
  faux-1:
    - text: "one says hi"
      end_turn: true
  faux-2:
    - text: "two says hi"
      end_turn: true
    - text: "two again"
      end_turn: true
`)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr) // HARNESS_MODEL=faux/faux-1

	if res := runHarness(t, proj, env, "--model", "faux/faux-2", "-p", "hello", "--output-format", "text"); res.Code != 0 {
		t.Fatalf("first run: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if res := runHarness(t, proj, env, "-c", "-p", "again", "--output-format", "text"); res.Code != 0 {
		t.Fatalf("resume: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if res := runHarness(t, proj, env, "-c", "--model", "faux/faux-1", "-p", "switch", "--output-format", "text"); res.Code != 0 {
		t.Fatalf("resume with --model: exit %d, stderr=%s", res.Code, res.Stderr)
	}

	var got []string
	for _, r := range srv.Requests() {
		got = append(got, r.Model)
	}
	want := []string{"faux-2", "faux-2", "faux-1"}
	if len(got) != len(want) {
		t.Fatalf("requests went to %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("requests went to %v, want %v (resume on the session's model; --model wins)", got, want)
		}
	}
}
