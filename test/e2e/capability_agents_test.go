//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilitySearchDispatchableProjectAndPluginAgents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting string
		want    bool
	}{
		{name: "included", setting: `{"enabledPlugins":{"demo@market":true},"skillDiscovery":{"includeAgents":true}}`, want: true},
		{name: "disabled", setting: `{"enabledPlugins":{"demo@market":true},"skillDiscovery":{"includeAgents":false}}`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: capability_search, args: {query: "audit"}, id: search1}
  - on_tool_result: search1
    then:
      - text: done
`)
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			writeTextFile(t, filepath.Join(proj, ".claude", "agents", "audit.md"), "---\nname: audit\ndescription: Audit code changes\n---\nprivate-project-agent-prompt")
			pluginRoot := writePluginFixture(t, home, mcpBuildFixture(t))
			writeUserSettings(t, home, tc.setting)
			writeTextFile(t, filepath.Join(pluginRoot, "agents", "audit.md"), "---\nname: audit\ndescription: Audit plugin integration\n---\nprivate-plugin-agent-prompt")
			res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "Find audit help")
			if res.Code != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", res.Code, res.Stdout, res.Stderr)
			}
			requests := srv.Requests()
			if len(requests) < 2 {
				t.Fatalf("requests=%d", len(requests))
			}
			messages := string(requests[len(requests)-1].Messages)
			for _, want := range []string{"audit [agent]", "demo:audit [agent]", `task({\"subagent_type\":\"demo:audit\"`} {
				if strings.Contains(messages, want) != tc.want {
					t.Fatalf("search result presence %q = %v, want %v: %s", want, strings.Contains(messages, want), tc.want, messages)
				}
			}
			if strings.Contains(messages, "private-project-agent-prompt") || strings.Contains(messages, "private-plugin-agent-prompt") {
				t.Fatalf("agent prompt leaked into discovery: %s", messages)
			}
		})
	}
}
