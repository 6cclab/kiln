package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func TestVerificationStatusClassifiesDeclaredSnapshots(t *testing.T) {
	cwd := t.TempDir()
	file := filepath.Join(cwd, "source.go")
	if err := os.WriteFile(file, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	allow := func(path string) bool { return path == "source.go" }
	files, err := fingerprintFiles(context.Background(), cwd, []string{"source.go"}, allow)
	if err != nil {
		t.Fatal(err)
	}
	proof := VerificationEvidence{CallID: "call1", Command: "go test ./...", Files: files}
	current := []VerificationEvidence{proof}
	status := VerificationStatusTool(cwd, allow, func() []VerificationEvidence { return current }, 512)
	read := func(raw string) verificationStatusRecord {
		t.Helper()
		result, err := status.Execute(context.Background(), json.RawMessage(raw), nil, tool.Invocation{})
		if err != nil || result.IsError {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		var output struct {
			Records []verificationStatusRecord `json:"records"`
		}
		if err := json.Unmarshal([]byte(msg.TextOf(result.Content)), &output); err != nil || len(output.Records) != 1 {
			t.Fatalf("content=%s err=%v", msg.TextOf(result.Content), err)
		}
		return output.Records[0]
	}
	if got := read(`{}`); got.Status != "unchanged" || got.Command != proof.Command {
		t.Fatalf("unchanged: %+v", got)
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if got := read(`{"files":["source.go"]}`); got.Status != "changed" || len(got.Files) != 1 || got.Files[0] != "source.go" {
		t.Fatalf("permission change: %+v", got)
	}
	if err := os.WriteFile(file, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := read(`{"command":"go test ./..."}`); got.Status != "changed" {
		t.Fatalf("bytes change: %+v", got)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if got := read(`{}`); got.Status != "unavailable" || got.Command != "" || len(got.Files) != 0 {
		t.Fatalf("missing file: %+v", got)
	}
}

func TestVerificationStatusRedactsDeniedPathsAndBoundsOutput(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "secret.go"), []byte("hidden"), 0600); err != nil {
		t.Fatal(err)
	}
	proof := VerificationEvidence{CallID: "secret-call", Command: "cat secret.go", Files: []VerificationFile{{Path: "secret.go", SHA256: strings.Repeat("a", 64), Mode: 0600}}}
	toolStatus := VerificationStatusTool(cwd, func(string) bool { return false }, func() []VerificationEvidence { return []VerificationEvidence{proof} }, 128)
	result, err := toolStatus.Execute(context.Background(), json.RawMessage(`{}`), nil, tool.Invocation{})
	if err != nil || result.IsError || strings.Contains(msg.TextOf(result.Content), "secret.go") || !strings.Contains(msg.TextOf(result.Content), "unavailable") || len(msg.TextOf(result.Content)) > 512 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = toolStatus.Execute(context.Background(), json.RawMessage(`{"files":["secret.go"]}`), nil, tool.Invocation{})
	if err != nil || !result.IsError || strings.Contains(msg.TextOf(result.Content), "secret.go") {
		t.Fatalf("filter result=%+v err=%v", result, err)
	}
}
