//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeEfficiencyCycleLimit(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: read, args: {path: a-missing}, id: a1}
  - on_tool_result: a1
    then:
      - tool_call: {name: read, args: {path: b-missing}, id: b1}
  - on_tool_result: b1
    then:
      - tool_call: {name: read, args: {path: a-missing}, id: a2}
  - on_tool_result: a2
    then:
      - tool_call: {name: read, args: {path: b-missing}, id: b2}
  - on_tool_result: b2
    then:
      - text: done
`
	for _, tc := range []struct {
		name, settings string
		requests       int
		failed         bool
	}{
		{"two cycles", `{"runtimeEfficiency":{"cycleLimit":2}}`, 4, true},
		{"explicit zero", `{"runtimeEfficiency":{"cycleLimit":0}}`, 5, false},
		{"default", "", 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, sessions := scratchHome(t)
			project := scratchProject(t)
			addr, faux := startFaux(t, script)
			args := []string{"-p", "Read the missing files", "--permission-mode", "bypassPermissions"}
			if tc.settings != "" {
				path := filepath.Join(t.TempDir(), "settings.json")
				if err := os.WriteFile(path, []byte(tc.settings), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--settings", path)
			}
			result := runHarness(t, project, baseEnv(home, sessions, addr), args...)
			if got := len(faux.Requests()); got != tc.requests {
				t.Fatalf("model requests = %d, want %d; exit=%d stdout=%q stderr=%q", got, tc.requests, result.Code, result.Stdout, result.Stderr)
			}
			if tc.failed {
				if result.Code == 0 || !strings.Contains(result.Stderr, "failed tool-call cycle") {
					t.Fatalf("expected cycle stop: exit=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
				}
			} else if result.Code != 0 || !strings.Contains(result.Stdout, "done") {
				t.Fatalf("disabled guard: exit=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}
