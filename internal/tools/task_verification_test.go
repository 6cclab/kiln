package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/tool"
)

func TestVerificationBashToolBindsSuccessfulCommandToUnchangedInputs(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "source.go")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	readable := func(path string) bool { return path == "source.go" }
	for _, tc := range []struct {
		name      string
		mutate    bool
		failed    bool
		allowed   bool
		wantProof bool
	}{
		{"success", false, false, true, true},
		{"changed while running", true, false, true, false},
		{"failed command", false, true, true, false},
		{"read denied", false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
				if tc.mutate {
					if err := os.WriteFile(path, []byte("after"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				result := tool.Text("ran")
				result.IsError = tc.failed
				return result, nil
			}}
			allow := readable
			if !tc.allowed {
				allow = func(string) bool { return false }
			}
			wrapped := VerificationBashTool(base, allow)
			result, err := wrapped.Execute(context.Background(), json.RawMessage(`{"command":"go test ./...","verificationFiles":["source.go"]}`), nil, tool.Invocation{Cwd: cwd, ToolCallID: "bash-1"})
			if err != nil {
				t.Fatal(err)
			}
			proof, ok := VerificationFromResult(result.Details)
			if ok != tc.wantProof {
				t.Fatalf("proof=%v details=%s", ok, result.Details)
			}
			if ok {
				if proof.Command != "go test ./..." || !VerificationFresh(cwd, proof, readable) {
					t.Fatalf("unexpected proof: %+v", proof)
				}
				if err := os.WriteFile(path, []byte("later edit"), 0600); err != nil {
					t.Fatal(err)
				}
				if VerificationFresh(cwd, proof, readable) {
					t.Fatal("edit retained verification")
				}
			}
		})
	}
}

func TestVerificationBashToolRefusesEscapesAndSymlinks(t *testing.T) {
	cwd := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cwd, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", outside, "link"} {
		t.Run(path, func(t *testing.T) {
			if _, err := fingerprintFiles(context.Background(), cwd, []string{path}, func(string) bool { return true }); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
}

func TestTaskStateToolRejectsInvalidCheckpoint(t *testing.T) {
	for _, raw := range []string{`{"objective":""}`, `{"objective":"x","constraints":[""]}`} {
		result, err := TaskStateTool().Execute(context.Background(), json.RawMessage(raw), nil, tool.Invocation{})
		if err != nil || !result.IsError {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	result, err := TaskStateTool().Execute(context.Background(), json.RawMessage(`{"objective":"Ship change","constraints":["No paid calls"]}`), nil, tool.Invocation{})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var checkpoint TaskCheckpoint
	if err := json.Unmarshal(result.Details, &checkpoint); err != nil || checkpoint.Version != 1 || checkpoint.Objective != "Ship change" {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
}
