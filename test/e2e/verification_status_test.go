//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
)

func TestVerificationStatusUsesCommittedBashEvidence(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "test -f src/math.js", verificationFiles: ["src/math.js"]}, id: verify1}
  - on_tool_result: verify1
    then:
      - tool_call: {name: verification_status, args: {files: ["src/math.js"]}, id: status1}
  - on_tool_result: status1
    then:
      - text: "Checked scoped evidence."
`)
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "Run the check and inspect its evidence", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d: %s", res.Code, res.Stderr)
	}
	declared := false
	for _, def := range srv.Requests()[0].Tools {
		if def.Name == "verification_status" {
			declared = true
		}
	}
	if !declared {
		t.Fatal("verification_status not offered")
	}
	raw, err := os.ReadFile(sessionFile(t, sessDir, proj))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `\"status\":\"unchanged\"`) || !strings.Contains(string(raw), "declared inputs only; no task correctness or safe-to-skip claim") {
		t.Fatalf("missing bounded status result: %s", raw)
	}
}
