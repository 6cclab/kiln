package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

func TestDiscoveredVerificationRestoresAbsentMetadata(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"go.mod": "module example.test/check\n\ngo 1.26\n", "src/a.go": "package src\n"} {
		if err := os.WriteFile(filepath.Join(cwd, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	allow := func(string) bool { return true }
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, json.RawMessage, tool.Update, tool.Invocation) (tool.Result, error) {
		return tool.Text("ok"), nil
	}}
	result, err := tools.VerificationBashTool(base, allow).Execute(context.Background(), json.RawMessage(`{"command":"go test ./src","verificationDiscover":"go"}`), nil, tool.Invocation{Cwd: cwd, ToolCallID: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	original := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "bash", ToolCallID: "discovered", Details: result.Details}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var replayed msg.ToolResultMessage
	if err := json.Unmarshal(encoded, &replayed); err != nil {
		t.Fatal(err)
	}
	state := &taskState{cwd: cwd, allowRead: allow}
	state.restore([]session.Entry{{Type: session.EntryMessage, Message: replayed}})
	proofs := state.verificationSnapshot()
	if len(proofs) != 1 || proofs[0].Discovery != "go-partial-v2" || len(proofs[0].MissingFiles) != 3 || !tools.VerificationFresh(cwd, proofs[0], allow) {
		t.Fatalf("replayed discovery proof: %+v", proofs)
	}
	proofs[0].MissingFiles[0] = "changed"
	if strings.Contains(strings.Join(state.verificationSnapshot()[0].MissingFiles, ","), "changed") {
		t.Fatal("snapshot shares mutable missing-file evidence")
	}
	if err := os.WriteFile(filepath.Join(cwd, "go.work"), []byte("go 1.26\nuse .\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if tools.VerificationFresh(cwd, state.verificationSnapshot()[0], allow) {
		t.Fatal("restored evidence missed newly created workspace metadata")
	}
	state.restore(nil)
	if len(state.verificationSnapshot()) != 0 {
		t.Fatal("discovered proof crossed branch boundary")
	}
}
