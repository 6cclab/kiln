//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

func TestVerificationDiscoveryTracksNewWorkspaceMetadata(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	for name, body := range map[string]string{
		"go.mod":      "module example.test/verification\n\ngo 1.26\n",
		"src/math.go": "package src\nfunc Add(a, b int) int { return a + b }\n",
		"src/math_test.go": "package src\nimport \"testing\"\n" +
			"func TestAdd(t *testing.T) { if Add(2, 3) != 5 { t.Fatal(\"wrong sum\") } }\n",
	} {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "go test ./src", verificationDiscover: go}, id: verify1}
  - on_tool_result: verify1
    then:
      - tool_call: {name: verification_status, args: {files: ["go.work"]}, id: current1}
  - on_tool_result: current1
    then:
      - tool_call: {name: write, args: {path: "go.work", content: "go 1.26\nuse .\n"}, id: write1}
  - on_tool_result: write1
    then:
      - tool_call: {name: verification_status, args: {files: ["go.work"], mode: plan}, id: plan1}
  - on_tool_result: plan1
    then:
      - text: "Workspace metadata changed; rerun the recorded check."
`)
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "Track this Go check and changes to its workspace metadata", "--permission-mode", "bypassPermissions")
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
				Command   string `json:"command"`
				Status    string `json:"status"`
				Action    string `json:"action"`
				Discovery string `json:"discovery"`
			} `json:"records"`
		}
		if err := json.Unmarshal([]byte(msg.TextOf(result.Content)), &output); err != nil {
			t.Fatalf("missing discovery evidence: %s (%v)", msg.TextOf(result.Content), err)
		}
		want, found := statuses[result.ToolCallID]
		if !found || len(output.Records) != 1 || output.Records[0].Status != want || output.Records[0].Command != "go test ./src" || output.Records[0].Discovery != "go-partial-v1" || !strings.Contains(output.Scope, "no task correctness") {
			t.Fatalf("unexpected discovery status for %s: %+v", result.ToolCallID, output)
		}
		if want == "changed" && output.Records[0].Action != "rerun" {
			t.Fatalf("plan missed new workspace metadata: %+v", output)
		}
		delete(statuses, result.ToolCallID)
	}
	if len(statuses) != 0 {
		t.Fatalf("missing discovery status results: %v", statuses)
	}
}
