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

func TestGoVerificationDiscovery(t *testing.T) {
	cwd := t.TempDir()
	for path, body := range map[string]string{
		"go.mod": "module example.com/test\n", "go.sum": "sum", "go.work": "go 1.24\n", "go.work.sum": "worksum",
		"pkg/a.go": "package pkg\n", "pkg/sub/b.go": "package sub\n", "extra.txt": "extra",
	} {
		full := filepath.Join(cwd, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		return tool.Text("ok"), nil
	}}
	allow := func(string) bool { return true }
	wrapped := VerificationBashTool(base, allow)
	for _, tc := range []struct {
		name, command string
		proof         bool
	}{
		{"package", "go test ./pkg", true},
		{"recursive", "go test ./pkg/...", true},
		{"chain", "go test ./pkg && echo hi", false},
		{"assignment", "X=1 go test ./pkg", false},
		{"substitution", "go test ./$(echo pkg)", false},
		{"flag", "go test -run Test ./pkg", false},
		{"root", "go test ./...", true},
		{"redirect", "go test ./pkg >out", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"command": tc.command, "verificationDiscover": "go"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := wrapped.Execute(context.Background(), raw, nil, tool.Invocation{Cwd: cwd, ToolCallID: "call"})
			if err != nil {
				t.Fatal(err)
			}
			proof, ok := VerificationFromResult(result.Details)
			if ok != tc.proof {
				t.Fatalf("proof=%v details=%s", ok, result.Details)
			}
			if _, ok := VerificationAttemptFromResult(result.Details); !ok {
				t.Fatal("missing attempt")
			}
			if !tc.proof {
				attempt, _ := VerificationAttemptFromResult(result.Details)
				if attempt.DiscoveryUnsupported == "" {
					t.Fatal("unsupported discovery reason missing")
				}
			}
			if !ok {
				return
			}
			wantDir := "pkg"
			if tc.name == "root" {
				wantDir = "."
			}
			if proof.Discovery != goPartialDiscovery || len(proof.Directories) != 1 || proof.Directories[0].Path != wantDir || len(proof.Files) != 4 {
				t.Fatalf("unexpected discovery: %+v", proof)
			}
			if !VerificationFresh(cwd, proof, allow) {
				t.Fatal("fresh inferred proof rejected")
			}
		})
	}
}

func TestGoVerificationDiscoveryFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(string) error
		allow func(string) bool
	}{
		{"denied", nil, func(path string) bool { return path != "pkg/a.go" }},
		{"missing", func(cwd string) error { return os.Remove(filepath.Join(cwd, "go.mod")) }, func(string) bool { return true }},
		{"symlink", func(cwd string) error { return os.Symlink("a.go", filepath.Join(cwd, "pkg", "link.go")) }, func(string) bool { return true }},
		{"over budget", func(cwd string) error {
			return os.WriteFile(filepath.Join(cwd, "pkg", "huge"), []byte(strings.Repeat("x", 1024)), 0600)
		}, func(string) bool { return true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			if err := os.Mkdir(filepath.Join(cwd, "pkg"), 0700); err != nil {
				t.Fatal(err)
			}
			for path := range map[string]bool{"go.mod": true, "pkg/a.go": true, "extra": true} {
				if err := os.WriteFile(filepath.Join(cwd, path), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.setup != nil {
				if err := tc.setup(cwd); err != nil {
					t.Fatal(err)
				}
			}
			base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
				return tool.Text("ok"), nil
			}}
			raw := `{"command":"go test ./pkg","verificationDiscover":"go","verificationFiles":["extra"]}`
			if tc.name == "over budget" {
				raw = `{"command":"go test ./pkg","verificationDiscover":"go","verificationFiles":["extra"],"verificationDirectoryByteLimit":3}`
			}
			result, err := VerificationBashTool(base, tc.allow).Execute(context.Background(), json.RawMessage(raw), nil, tool.Invocation{Cwd: cwd, ToolCallID: "call"})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := VerificationFromResult(result.Details); ok {
				t.Fatalf("unexpected partial proof: %s", result.Details)
			}
		})
	}
}

func TestGoVerificationDiscoveryMissingMetadataChangesStatus(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{"go.mod": "module example.com/test\n", "pkg/a.go": "package pkg\n"} {
		if err := os.WriteFile(filepath.Join(cwd, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	allow := func(string) bool { return true }
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		return tool.Text("ok"), nil
	}}
	result, err := VerificationBashTool(base, allow).Execute(context.Background(), json.RawMessage(`{"command":"go test ./pkg","verificationDiscover":"go"}`), nil, tool.Invocation{Cwd: cwd, ToolCallID: "call"})
	if err != nil {
		t.Fatal(err)
	}
	proof, ok := VerificationFromResult(result.Details)
	if !ok || len(proof.MissingFiles) != 3 || !VerificationFresh(cwd, proof, allow) {
		t.Fatalf("proof=%+v ok=%v", proof, ok)
	}
	if err := os.WriteFile(filepath.Join(cwd, "go.work"), []byte("go 1.24\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if VerificationFresh(cwd, proof, allow) {
		t.Fatal("new go.work did not stale evidence")
	}
	status := VerificationStatusTool(cwd, allow, func() []VerificationEvidence { return []VerificationEvidence{proof} }, 512)
	state, err := status.Execute(context.Background(), json.RawMessage(`{"files":["go.work"],"mode":"plan"}`), nil, tool.Invocation{})
	if err != nil || state.IsError || !strings.Contains(msg.TextOf(state.Content), `"action":"rerun"`) {
		t.Fatalf("status=%+v err=%v", state, err)
	}
	denied := VerificationStatusTool(cwd, func(path string) bool { return path != "go.work" }, func() []VerificationEvidence { return []VerificationEvidence{proof} }, 512)
	state, err = denied.Execute(context.Background(), json.RawMessage(`{"mode":"status"}`), nil, tool.Invocation{})
	if err != nil || state.IsError || !strings.Contains(msg.TextOf(state.Content), `"status":"unavailable"`) || strings.Contains(msg.TextOf(state.Content), "go.work") {
		t.Fatalf("denied status=%+v err=%v", state, err)
	}
}

func TestGoVerificationDiscoveryRejectsMetadataCreatedDuringCommand(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{"go.mod": "module example.com/test\n", "pkg/a.go": "package pkg\n"} {
		if err := os.WriteFile(filepath.Join(cwd, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		if err := os.WriteFile(filepath.Join(cwd, "go.work"), []byte("go 1.24\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return tool.Text("ok"), nil
	}}
	result, err := VerificationBashTool(base, func(string) bool { return true }).Execute(context.Background(), json.RawMessage(`{"command":"go test ./pkg","verificationDiscover":"go"}`), nil, tool.Invocation{Cwd: cwd, ToolCallID: "call"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := VerificationFromResult(result.Details); ok {
		t.Fatalf("evidence survived metadata creation: %s", result.Details)
	}
}

func TestGoVerificationDiscoveryRejectsBraceExpansion(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "go.mod"), []byte("module example.com/test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cwd, "pkg{,other}"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "pkg{,other}", "a.go"), []byte("package pkg\n"), 0600); err != nil {
		t.Fatal(err)
	}
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		return tool.Text("ok"), nil
	}}
	result, err := VerificationBashTool(base, func(string) bool { return true }).Execute(context.Background(), json.RawMessage(`{"command":"go test ./pkg{,other}","verificationDiscover":"go"}`), nil, tool.Invocation{Cwd: cwd, ToolCallID: "call"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := VerificationFromResult(result.Details); ok {
		t.Fatalf("brace expansion accepted: %s", result.Details)
	}
}

func TestGoVerificationDiscoveryRejectsMalformedMissingFiles(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := VerificationEvidence{CallID: "call", Command: "go test ./pkg", Discovery: goPartialDiscovery,
		Files:              []VerificationFile{{Path: "go.mod", SHA256: hash}, {Path: "go.sum", SHA256: hash}},
		MissingFiles:       []string{"go.work", "go.work.sum"},
		Directories:        []VerificationDirectory{{Path: "pkg", Files: []VerificationFile{}, Subdirectories: []VerificationSubdirectory{}}},
		DirectoryFileLimit: 128, DirectoryByteLimit: maxVerificationDirectoryBytes}
	for _, tc := range []struct {
		name    string
		missing []string
	}{
		{"duplicate", []string{"go.work", "go.work", "go.work.sum"}},
		{"overlap", []string{"go.sum", "go.work", "go.work.sum"}},
		{"unknown", []string{"go.work", "go.work.sum", "arbitrary"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof := base
			proof.MissingFiles = tc.missing
			raw, err := json.Marshal(map[string]any{"taskVerification": proof})
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := VerificationFromResult(raw); ok {
				t.Fatalf("malformed proof accepted: %s", raw)
			}
		})
	}
	base.Discovery = "go-future-v2"
	raw, err := json.Marshal(map[string]any{"taskVerification": base})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := VerificationFromResult(raw); ok {
		t.Fatalf("unknown discovery accepted: %s", raw)
	}
}
