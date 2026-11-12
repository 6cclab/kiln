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

func writeVerificationFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func directoryProof(t *testing.T, cwd string, allow func(string) bool, raw string) (VerificationEvidence, tool.Result) {
	t.Helper()
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), Execute: func(context.Context, json.RawMessage, tool.Update, tool.Invocation) (tool.Result, error) {
		return tool.Text("ok"), nil
	}}
	result, err := VerificationBashTool(base, allow).Execute(context.Background(), json.RawMessage(raw), nil, tool.Invocation{Cwd: cwd, ToolCallID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := VerificationFromResult(result.Details)
	return proof, result
}

func TestVerificationDirectoriesMembershipAndPlan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"file contents", func(t *testing.T, c string) { writeVerificationFile(t, filepath.Join(c, "src", "a.go"), "changed") }},
		{"added file", func(t *testing.T, c string) { writeVerificationFile(t, filepath.Join(c, "src", "b.go"), "new") }},
		{"removed file", func(t *testing.T, c string) {
			if err := os.Remove(filepath.Join(c, "src", "a.go")); err != nil {
				t.Fatal(err)
			}
		}},
		{"nested directory", func(t *testing.T, c string) {
			if err := os.Mkdir(filepath.Join(c, "src", "empty"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"file mode", func(t *testing.T, c string) {
			if err := os.Chmod(filepath.Join(c, "src", "a.go"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"removed root", func(t *testing.T, c string) {
			if err := os.RemoveAll(filepath.Join(c, "src")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			writeVerificationFile(t, filepath.Join(cwd, "src", "a.go"), "original")
			allow := func(string) bool { return true }
			proof, _ := directoryProof(t, cwd, allow, `{"command":"go test ./src/...","verificationDirectories":["src"]}`)
			if len(proof.Directories) != 1 || !VerificationFresh(cwd, proof, allow) {
				t.Fatalf("initial proof: %+v", proof)
			}
			tc.mutate(t, cwd)
			if VerificationFresh(cwd, proof, allow) {
				t.Fatal("mutated directory remained fresh")
			}
			result, err := VerificationStatusTool(cwd, allow, func() []VerificationEvidence { return []VerificationEvidence{proof} }, 1024).Execute(context.Background(), json.RawMessage(`{"mode":"plan"}`), nil, tool.Invocation{})
			if err != nil || result.IsError {
				t.Fatalf("status: %+v %v", result, err)
			}
			output := msg.TextOf(result.Content)
			if !strings.Contains(output, `"action":"rerun"`) || !strings.Contains(output, `"command":"go test ./src/..."`) {
				t.Fatalf("plan=%s", output)
			}
		})
	}
}

func TestVerificationDirectoriesUnavailableEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, string)
		raw     string
		allow   func(string) bool
	}{
		{"denied descendant", func(t *testing.T, c string) { writeVerificationFile(t, filepath.Join(c, "src", "secret"), "x") }, `{"command":"test","verificationDirectories":["src"]}`, func(p string) bool { return p != "src/secret" }},
		{"denied file plus directory", func(t *testing.T, c string) {
			writeVerificationFile(t, filepath.Join(c, "src", "a"), "x")
			writeVerificationFile(t, filepath.Join(c, "secret"), "x")
		}, `{"command":"test","verificationFiles":["secret"],"verificationDirectories":["src"]}`, func(p string) bool { return p != "secret" }},
		{"file limit", func(t *testing.T, c string) {
			writeVerificationFile(t, filepath.Join(c, "src", "a"), "x")
			writeVerificationFile(t, filepath.Join(c, "src", "b"), "x")
		}, `{"command":"test","verificationDirectories":["src"],"verificationDirectoryFileLimit":1}`, func(string) bool { return true }},
		{"byte limit", func(t *testing.T, c string) { writeVerificationFile(t, filepath.Join(c, "src", "a"), "xx") }, `{"command":"test","verificationDirectories":["src"],"verificationDirectoryByteLimit":1}`, func(string) bool { return true }},
		{"symlink", func(t *testing.T, c string) {
			writeVerificationFile(t, filepath.Join(c, "outside"), "x")
			if err := os.Symlink(filepath.Join(c, "outside"), filepath.Join(c, "src", "link")); err != nil {
				t.Fatal(err)
			}
		}, `{"command":"test","verificationDirectories":["src"]}`, func(string) bool { return true }},
		{"cancelled", func(t *testing.T, c string) { writeVerificationFile(t, filepath.Join(c, "src", "a"), "x") }, `{"command":"test","verificationDirectories":["src"]}`, func(string) bool { return true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			if err := os.Mkdir(filepath.Join(cwd, "src"), 0700); err != nil {
				t.Fatal(err)
			}
			tc.prepare(t, cwd)
			proof, result := directoryProof(t, cwd, tc.allow, tc.raw)
			if tc.name == "cancelled" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := fingerprintDirectories(ctx, cwd, []string{"src"}, tc.allow, 0, 0); err == nil {
					t.Fatal("cancelled walk accepted")
				}
				return
			}
			if len(proof.Directories) != 0 || strings.Contains(string(result.Details), `"taskVerification":`) {
				t.Fatalf("unexpected proof: %s", result.Details)
			}
			if _, ok := VerificationAttemptFromResult(result.Details); !ok {
				t.Fatal("attempt absent")
			}
		})
	}
}

func TestVerificationDirectoryLegacyAndValidation(t *testing.T) {
	cwd := t.TempDir()
	writeVerificationFile(t, filepath.Join(cwd, "src", "a"), "x")
	proof, _ := directoryProof(t, cwd, func(string) bool { return true }, `{"command":"test","verificationDirectories":["src"]}`)
	raw, _ := json.Marshal(map[string]any{"taskVerification": proof})
	if _, ok := VerificationFromResult(raw); !ok {
		t.Fatal("directory proof failed round trip")
	}
	proof.Directories[0].Files[0].Path = "elsewhere"
	raw, _ = json.Marshal(map[string]any{"taskVerification": proof})
	if _, ok := VerificationFromResult(raw); ok {
		t.Fatal("invalid descendant accepted")
	}
	legacy := VerificationEvidence{CallID: "old", Command: "test", Files: []VerificationFile{{Path: "src/a", SHA256: strings.Repeat("a", 64), Mode: 0600}}}
	raw, _ = json.Marshal(map[string]any{"taskVerification": legacy})
	if got, ok := VerificationFromResult(raw); !ok || len(got.Files) != 1 {
		t.Fatal("legacy proof rejected")
	}
}

func TestVerificationPlanRedactsDeniedNewFileAndHandlesBounds(t *testing.T) {
	cwd := t.TempDir()
	writeVerificationFile(t, filepath.Join(cwd, "src", "a"), "x")
	allow := func(path string) bool { return path != "src/secret" }
	proof, _ := directoryProof(t, cwd, allow, `{"command":"secret command text","verificationDirectories":["src"],"verificationDirectoryFileLimit":2}`)
	status := VerificationStatusTool(cwd, allow, func() []VerificationEvidence { return []VerificationEvidence{proof} }, 1024)
	writeVerificationFile(t, filepath.Join(cwd, "src", "secret"), "x")
	result, err := status.Execute(context.Background(), json.RawMessage(`{"mode":"plan"}`), nil, tool.Invocation{})
	if err != nil || result.IsError || strings.Contains(msg.TextOf(result.Content), "secret command text") || !strings.Contains(msg.TextOf(result.Content), `"action":"review"`) {
		t.Fatalf("denied plan: %s %v", msg.TextOf(result.Content), err)
	}
	if err := os.Remove(filepath.Join(cwd, "src", "secret")); err != nil {
		t.Fatal(err)
	}
	writeVerificationFile(t, filepath.Join(cwd, "src", "b"), "x")
	writeVerificationFile(t, filepath.Join(cwd, "src", "c"), "x")
	result, err = status.Execute(context.Background(), json.RawMessage(`{"mode":"plan"}`), nil, tool.Invocation{})
	if err != nil || result.IsError || !strings.Contains(msg.TextOf(result.Content), `"status":"unavailable"`) || !strings.Contains(msg.TextOf(result.Content), `"action":"review"`) || strings.Contains(msg.TextOf(result.Content), "secret command text") {
		t.Fatalf("bounds plan: %s %v", msg.TextOf(result.Content), err)
	}
}

func TestVerificationDirectoryNewFileFilterAndInvalidProof(t *testing.T) {
	cwd := t.TempDir()
	writeVerificationFile(t, filepath.Join(cwd, "src", "a"), "x")
	allow := func(string) bool { return true }
	proof, _ := directoryProof(t, cwd, allow, `{"command":"test","verificationDirectories":["src"]}`)
	writeVerificationFile(t, filepath.Join(cwd, "src", "new.js"), "x")
	result, err := VerificationStatusTool(cwd, allow, func() []VerificationEvidence { return []VerificationEvidence{proof} }, 512).Execute(context.Background(), json.RawMessage(`{"files":["src/new.js"]}`), nil, tool.Invocation{})
	if err != nil || result.IsError || !strings.Contains(msg.TextOf(result.Content), `"status":"changed"`) {
		t.Fatalf("new file filter: %s %v", msg.TextOf(result.Content), err)
	}
	if VerificationFresh(cwd, VerificationEvidence{}, allow) {
		t.Fatal("empty proof fresh")
	}
	proof.DirectoryFileLimit = 0
	if VerificationFresh(cwd, proof, allow) {
		t.Fatal("invalid proof fresh")
	}
}

func TestVerificationInvalidRequestedLimitsCannotBecomeValidAfterFileSubtraction(t *testing.T) {
	cwd := t.TempDir()
	writeVerificationFile(t, filepath.Join(cwd, "src", "a"), "x")
	writeVerificationFile(t, filepath.Join(cwd, "root"), "x")
	_, result := directoryProof(t, cwd, func(string) bool { return true }, `{"command":"test","verificationFiles":["root"],"verificationDirectories":["src"],"verificationDirectoryFileLimit":513}`)
	if _, ok := VerificationFromResult(result.Details); ok {
		t.Fatal("invalid limit accepted")
	}
}

func TestVerificationMissingDirectoryCannotRecordEvidence(t *testing.T) {
	cwd := t.TempDir()
	_, result := directoryProof(t, cwd, func(string) bool { return true }, `{"command":"test","verificationDirectories":["generated"]}`)
	if _, ok := VerificationFromResult(result.Details); ok {
		t.Fatal("missing directory recorded as snapshot")
	}
	if _, ok := VerificationAttemptFromResult(result.Details); !ok {
		t.Fatal("attempt absent")
	}
}
