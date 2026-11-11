//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEfficiencyToolsAreOfferedAndExecutable(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "memory-enabled"
		if !enabled {
			name = "memory-disabled"
		}
		t.Run(name, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			script := `model: faux-1
steps:
  - tool_call: {name: skill_search, args: {query: "not-installed"}, id: skills1}
  - on_tool_result: skills1
    then:
`
			if enabled {
				script += `      - tool_call: {name: memory_write, args: {action: "remember", key: "runner", kind: "preference", text: "Use Go tests", evidence: "Explicit user instruction"}, id: memory1}
  - on_tool_result: memory1
    then:
      - tool_call: {name: memory_recall, args: {query: "runner"}, id: recall1}
  - on_tool_result: recall1
    then:
      - text: "done"
`
			} else {
				script += "      - text: done\n"
			}
			addr, srv := startFaux(t, script)
			args := []string{"-p", "Find a skill and remember my test runner preference.", "--permission-mode", "bypassPermissions"}
			if !enabled {
				path := filepath.Join(t.TempDir(), "settings.json")
				if err := os.WriteFile(path, []byte(`{"personalMemoryEnabled":false}`), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--settings", path)
			}
			res := runHarness(t, proj, baseEnv(home, sessDir, addr), args...)
			if res.Code != 0 {
				t.Fatalf("exit=%d stderr=%s", res.Code, res.Stderr)
			}
			requests := srv.Requests()
			if len(requests) == 0 {
				t.Fatal("no model requests")
			}
			for _, request := range requests {
				names := map[string]bool{}
				for _, tool := range request.Tools {
					names[tool.Name] = true
				}
				if !names["skill_search"] || names["memory_write"] != enabled || names["memory_recall"] != enabled {
					t.Fatalf("incorrect active tools: %v", names)
				}
			}
			raw, err := os.ReadFile(sessionFile(t, sessDir, proj))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "No matching skills fit") {
				t.Fatal("skill_search did not execute")
			}
			if enabled && (!strings.Contains(string(raw), "Stored project observation") || !strings.Contains(string(requests[len(requests)-1].Messages), "Use Go tests")) {
				t.Fatal("memory write/recall did not execute")
			}
		})
	}
}
