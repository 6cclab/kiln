//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapabilitySearchMixedDiscoveryAndMCPAdmission(t *testing.T) {
	fixture := mcpBuildFixture(t)
	cfg := mcpWriteConfig(t, map[string][]string{"fixture": {fixture}})
	addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: capability_search, args: {query: "echo"}, id: search1}
  - on_tool_result: search1
    then:
      - tool_call: {name: mcp__fixture__echo, args: {text: "round trip"}, id: echo1}
  - on_tool_result: echo1
    then:
      - tool_call: {name: capability_search, args: {query: "migration"}, id: search2}
  - on_tool_result: search2
    then:
      - tool_call: {name: skill, args: {skill: migration}, id: skill1}
  - on_tool_result: skill1
    then:
      - text: done
`)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	skillDir := filepath.Join(proj, ".claude", "skills", "migration")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: migration\ndescription: Database migration guidance\n---\nprivate-migration-body-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_POSTURE"] = "all"
	res := runHarness(t, proj, env,
		"-p", "Find echo and migration capabilities", "--output-format", "text",
		"--mcp-config", cfg, "--strict-mcp-config", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	requests := srv.Requests()
	if len(requests) < 5 {
		t.Fatalf("requests=%d, want at least 5", len(requests))
	}
	hasTool := func(index int, name string) bool {
		for _, candidate := range requests[index].Tools {
			if candidate.Name == name {
				return true
			}
		}
		return false
	}
	if !hasTool(0, "capability_search") || hasTool(0, "mcp__fixture__echo") || !hasTool(1, "mcp__fixture__echo") {
		t.Fatalf("active tools before/after search: %+v / %+v", requests[0].Tools, requests[1].Tools)
	}
	raw, err := os.ReadFile(sessionFile(t, sessDir, proj))
	if err != nil {
		t.Fatal(err)
	}
	transcript := string(raw)
	if !strings.Contains(transcript, "call mcp__fixture__echo") || !strings.Contains(transcript, "skill({\\\"skill\\\":\\\"migration\\\"})") || !strings.Contains(transcript, "private-migration-body-marker") {
		t.Fatalf("missing MCP/skill search or skill invocation: %s", transcript)
	}
}

func TestCapabilitySearchUnifiedFalseUsesLegacySearch(t *testing.T) {
	addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: skill_search, args: {query: "migration"}, id: search1}
  - on_tool_result: search1
    then:
      - text: done
`)
	home, sessDir := scratchHome(t)
	writeUserSettings(t, home, `{"skillDiscovery":{"unified":false}}`)
	proj := scratchProject(t)
	skillDir := filepath.Join(proj, ".claude", "skills", "migration")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: migration\ndescription: Database migration guidance\n---\nlegacy-skill-body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "Find migration guidance")
	if res.Code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", res.Code, res.Stdout, res.Stderr)
	}
	requests := srv.Requests()
	if len(requests) == 0 {
		t.Fatal("no model requests")
	}
	first := requests[0]
	if !strings.Contains(first.System, "through skill_search") || strings.Contains(first.System, "through capability_search") {
		t.Fatalf("unexpected discovery pointer: %s", first.System)
	}
	var legacy, unified bool
	for _, candidate := range first.Tools {
		legacy = legacy || candidate.Name == "skill_search"
		unified = unified || candidate.Name == "capability_search"
	}
	if !legacy || unified {
		t.Fatalf("legacy/unified tools = %v/%v", legacy, unified)
	}
	raw, err := os.ReadFile(sessionFile(t, sessDir, proj))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "migration: Database migration guidance") {
		t.Fatal("legacy skill_search did not return the installed skill")
	}
}
