//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

func TestVerificationDirectoryPlanFindsNewFiles(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "test -f src/math.js", verificationDirectories: ["src"]}, id: verify1}
  - on_tool_result: verify1
    then:
      - tool_call: {name: verification_status, args: {directories: ["src"]}, id: current1}
  - on_tool_result: current1
    then:
      - tool_call: {name: write, args: {path: "src/new.js", content: "module.exports = 42;\n"}, id: write1}
  - on_tool_result: write1
    then:
      - tool_call: {name: verification_status, args: {directories: ["src"], mode: "plan"}, id: plan1}
  - on_tool_result: plan1
    then:
      - text: "A new source file requires reviewing the previous check."
`)
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "Track source inputs and plan verification after editing", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d: %s", res.Code, res.Stderr)
	}
	store, err := jsonl.Open(sessionFile(t, sessDir, proj), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	statuses := map[string]string{"toolu_current1": "unchanged", "toolu_plan1": "changed"}
	for _, entry := range store.ScanEntries(session.EntryScan{}) {
		result, ok := entry.Message.(msg.ToolResultMessage)
		if !ok || result.ToolName != "verification_status" {
			continue
		}
		var output struct {
			Scope   string `json:"scope"`
			Records []struct {
				Command     string   `json:"command"`
				Status      string   `json:"status"`
				Action      string   `json:"action"`
				Directories []string `json:"directories"`
			} `json:"records"`
		}
		if err := json.Unmarshal([]byte(msg.TextOf(result.Content)), &output); err != nil {
			t.Fatalf("missing dependency evidence: %s (%v)", msg.TextOf(result.Content), err)
		}
		want, found := statuses[result.ToolCallID]
		if !found || len(output.Records) != 1 || output.Records[0].Status != want || output.Records[0].Command != "test -f src/math.js" || !strings.Contains(output.Scope, "no task correctness") {
			t.Fatalf("unexpected status for %s: %+v", result.ToolCallID, output)
		}
		if want == "changed" && (output.Records[0].Action != "rerun" || len(output.Records[0].Directories) != 1 || output.Records[0].Directories[0] != "src") {
			t.Fatalf("plan missed new directory member: %+v", output)
		}
		delete(statuses, result.ToolCallID)
	}
	if len(statuses) != 0 {
		t.Fatalf("missing status results: %v", statuses)
	}
}
