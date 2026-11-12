//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real CLI run must stop before paying for another model request after
// repeated identical failures. The default and explicit zero retain the
// ordinary behavior, so the guard is genuinely opt in.
func TestRuntimeEfficiencyProgressLimit(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: read, args: {path: missing.txt}, id: read1}
  - on_tool_result: read1
    then:
      - tool_call: {name: read, args: {path: missing.txt}, id: read2}
  - on_tool_result: read2
    then:
      - tool_call: {name: read, args: {path: missing.txt}, id: read3}
  - on_tool_result: read3
    then:
      - text: done
`
	for _, tc := range []struct {
		name     string
		settings string
		requests int
		failed   bool
	}{
		{name: "limit two", settings: `{"runtimeEfficiency":{"progressLimit":2}}`, requests: 2, failed: true},
		{name: "explicit zero", settings: `{"runtimeEfficiency":{"progressLimit":0}}`, requests: 4},
		{name: "default", requests: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, sessions := scratchHome(t)
			project := scratchProject(t)
			addr, faux := startFaux(t, script)
			args := []string{"-p", "Read missing.txt", "--permission-mode", "bypassPermissions"}
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
				if result.Code == 0 || !strings.Contains(result.Stderr, "repeated the same failed read call 2 times") {
					t.Fatalf("bounded failure: exit=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
				}
			} else if result.Code != 0 || !strings.Contains(result.Stdout, "done") {
				t.Fatalf("disabled guard: exit=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
			}
		})
	}
}

// Bash verification bookkeeping includes each tool-call ID in result details.
// Repeated command failures must still count as the same failed operation.
func TestRuntimeEfficiencyProgressLimitRepeatedBashFailure(t *testing.T) {
	const script = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "exit 1"}, id: bash1}
  - on_tool_result: bash1
    then:
      - tool_call: {name: bash, args: {command: "exit 1"}, id: bash2}
  - on_tool_result: bash2
    then:
      - tool_call: {name: bash, args: {command: "exit 1"}, id: bash3}
  - on_tool_result: bash3
    then:
      - text: done
`
	home, sessions := scratchHome(t)
	project := scratchProject(t)
	addr, faux := startFaux(t, script)
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{"runtimeEfficiency":{"progressLimit":2}}`), 0600); err != nil {
		t.Fatal(err)
	}
	result := runHarness(t, project, baseEnv(home, sessions, addr), "-p", "Run the failing command", "--permission-mode", "bypassPermissions", "--settings", settings)
	if got := len(faux.Requests()); got != 2 {
		t.Fatalf("model requests = %d, want 2; exit=%d stdout=%q stderr=%q", got, result.Code, result.Stdout, result.Stderr)
	}
	if result.Code == 0 || !strings.Contains(result.Stderr, "repeated the same failed bash call 2 times") {
		t.Fatalf("bounded Bash failure: exit=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
}
