//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestCost_ResumedSessionCountsEarlierRuns: /cost in a session continued
// with -c reports the context the earlier run left (12k in + 3k out), not
// zero from the relaunch. (Spend is seeded the same way; faux is free, so
// the dollar figure cannot show it here.)
func TestCost_ResumedSessionCountsEarlierRuns(t *testing.T) {
	addr, _ := startFaux(t, `model: faux-1
steps:
  - text: "hi"
    usage: {input: 12000, output: 3000}
`)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)
	if res := runHarness(t, proj, env, "-p", "hello", "--output-format", "text"); res.Code != 0 {
		t.Fatalf("first run: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	res := runHarness(t, proj, env, "-c", "-p", "/cost")
	if res.Code != 0 {
		t.Fatalf("/cost: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "15.0k tokens in context") {
		t.Errorf("/cost after -c does not include the earlier run:\n%s", res.Stdout)
	}
}
