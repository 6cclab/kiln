//go:build e2e

package e2e

import (
	"regexp"
	"strings"
	"testing"
)

// TestCompactCommandReportsSizes: /compact says how big the conversation
// was and is, not just "Context compacted.", and says so when there was
// nothing old enough to summarise.
func TestCompactCommandReportsSizes(t *testing.T) {
	// ~12k tokens per reply; four turns outgrow faux-1's 32k retained tail.
	long := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 1100)
	var script strings.Builder
	script.WriteString("model: faux-1\nsteps:\n")
	for i := 0; i < 4; i++ {
		script.WriteString("  - text: \"" + long + "\"\n    end_turn: true\n")
	}
	script.WriteString("  - text: \"Summary: four long replies.\"\n")
	addr, requests := startFaux(t, script.String())
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := baseEnv(home, sessDir, addr)

	if res := runHarness(t, proj, env, "-p", "hello", "--output-format", "text"); res.Code != 0 {
		t.Fatalf("first run: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	early := runHarness(t, proj, env, "-c", "-p", "/compact")
	if !strings.Contains(early.Stdout, "Nothing to compact yet") {
		t.Errorf("one turn in, /compact should say there is nothing old to summarise:\n%s", early.Stdout)
	}
	if n := len(requests.Requests()); n != 1 {
		t.Errorf("nothing to summarise, but /compact sent %d model requests in total, want only the first turn's", n)
	}
	for i := 0; i < 3; i++ {
		if res := runHarness(t, proj, env, "-c", "-p", "more", "--output-format", "text"); res.Code != 0 {
			t.Fatalf("turn %d: exit %d, stderr=%s", i+2, res.Code, res.Stderr)
		}
	}
	res := runHarness(t, proj, env, "-c", "-p", "/compact")
	if res.Code != 0 {
		t.Fatalf("/compact: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	m := regexp.MustCompile(`Context compacted: conversation ~([\d.]+k?) → ~([\d.]+k?) tokens\.`).FindStringSubmatch(res.Stdout)
	if m == nil {
		t.Fatalf("no before/after sizes in:\n%s", res.Stdout)
	}
	_ = m
}
