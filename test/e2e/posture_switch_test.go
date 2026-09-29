//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestTUI_PostureSwitchReachesToolSearch: after /posture ops, tool_search
// finds a server the startup (coding) posture excluded, and the tool it
// enables is callable. tool_search used to keep the startup posture and a
// gate state /posture had replaced, so the model reported the server as
// missing and admitted tools never became active.
func TestTUI_PostureSwitchReachesToolSearch(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	mcpConfig := budgetBuildMCPFixtureConfigNamed(t, "grafana")
	addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: tool_search, args: {query: "echo text back"}, id: ts1}
  - on_tool_result: ts1
    then:
      - tool_call: {name: mcp__grafana__echo, args: {text: "posture-ok"}, id: e1}
  - on_tool_result: e1
    then:
      - text: "done"
`)
	s := startTUI(t, 120, 40, proj, home, sessDir, addr, "--mcp-config", mcpConfig, "--permission-mode", "bypassPermissions")
	defer s.Close()
	waitReady(t, s)
	s.Send("/posture ops")
	s.SendKey("enter")
	time.Sleep(500 * time.Millisecond)
	s.Send("use grafana to echo")
	s.SendKey("enter")
	if err := s.WaitFor("done", 15*time.Second); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	waitTurnSettled(t, s)

	var sawEnable, sawEcho bool
	for _, r := range srv.Requests() {
		body := string(r.Body)
		if strings.Contains(body, "mcp__grafana__echo") && strings.Contains(body, "Enabled") {
			sawEnable = true
		}
		if strings.Contains(body, "posture-ok") && !strings.Contains(body, "not available to this agent") {
			sawEcho = true
		}
	}
	if !sawEnable {
		t.Errorf("tool_search after /posture ops did not enable mcp__grafana__echo")
	}
	if !sawEcho {
		t.Errorf("mcp__grafana__echo was not callable after being enabled")
	}
	if rows := strings.Join(s.Rows(), "\n"); strings.Contains(rows, "not available to this agent") {
		t.Errorf("the enabled tool was refused:\n%s", rows)
	}
}
