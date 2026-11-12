package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func writeGoDiscoveryFixture(t *testing.T, cwd, path, body string) {
	t.Helper()
	full := filepath.Join(cwd, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func runGoDiscoveryFixture(t *testing.T, cwd, command string, limit int64) (VerificationEvidence, bool, int) {
	t.Helper()
	runs := 0
	base := &tool.Tool{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), Execute: func(_ context.Context, _ json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		runs++
		return tool.Text("ok"), nil
	}}
	raw, err := json.Marshal(map[string]any{"command": command, "verificationDiscover": "go", "verificationDirectoryByteLimit": limit})
	if err != nil {
		t.Fatal(err)
	}
	result, err := VerificationBashTool(base, func(string) bool { return true }).Execute(context.Background(), raw, nil, tool.Invocation{Cwd: cwd, ToolCallID: "call"})
	if err != nil {
		t.Fatal(err)
	}
	proof, ok := VerificationFromResult(result.Details)
	return proof, ok, runs
}

func goDiscoveryStatus(t *testing.T, cwd string, proof VerificationEvidence, raw string) verificationStatusRecord {
	t.Helper()
	status := VerificationStatusTool(cwd, func(string) bool { return true }, func() []VerificationEvidence { return []VerificationEvidence{proof} }, 512)
	result, err := status.Execute(context.Background(), json.RawMessage(raw), nil, tool.Invocation{})
	if err != nil || result.IsError {
		t.Fatalf("status=%+v err=%v", result, err)
	}
	var output struct {
		Records []verificationStatusRecord `json:"records"`
	}
	if err := json.Unmarshal([]byte(msg.TextOf(result.Content)), &output); err != nil || len(output.Records) != 1 {
		t.Fatalf("content=%s err=%v", msg.TextOf(result.Content), err)
	}
	return output.Records[0]
}

func TestGoVerificationDiscoveryTransitiveImportsAndUnrelatedPackage(t *testing.T) {
	cwd := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":         "module example.com/test\n",
		"app/app.go":     "package app\nimport _ \"example.com/test/lib\"\n",
		"lib/lib.go":     "package lib\nimport _ \"example.com/test/common\"\n",
		"common/c.go":    "package common\nconst V = 1\n",
		"other/other.go": "package other\nconst V = 1\n",
	} {
		writeGoDiscoveryFixture(t, cwd, path, body)
	}
	proof, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok || runs != 1 || len(proof.Directories) != 3 {
		t.Fatalf("proof=%+v ok=%v runs=%d", proof, ok, runs)
	}
	writeGoDiscoveryFixture(t, cwd, "other/other.go", "package other\nconst V = 2\n")
	if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "unchanged" {
		t.Fatalf("unrelated edit: %+v", got)
	}
	writeGoDiscoveryFixture(t, cwd, "common/c.go", "package common\nconst V = 2\n")
	if got := goDiscoveryStatus(t, cwd, proof, `{"files":["common/c.go"],"mode":"plan"}`); got.Status != "changed" || got.Action != "rerun" || got.Command != "go test ./app" {
		t.Fatalf("transitive edit: %+v", got)
	}
}

func TestGoVerificationDiscoveryLocalReplaceAndLimit(t *testing.T) {
	cwd := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":           "module example.com/test\nreplace (\nexample.com/ext => ./local/ext\n)\n",
		"app/app.go":       "package app\n",
		"local/ext/ext.go": "package ext\nconst V = 1\n",
	} {
		writeGoDiscoveryFixture(t, cwd, path, body)
	}
	proof, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok || runs != 1 || len(proof.Directories) != 2 {
		t.Fatalf("proof=%+v ok=%v runs=%d", proof, ok, runs)
	}
	writeGoDiscoveryFixture(t, cwd, "local/ext/ext.go", "package ext\nconst V = 2\n")
	if got := goDiscoveryStatus(t, cwd, proof, `{"mode":"plan"}`); got.Status != "changed" || got.Action != "rerun" {
		t.Fatalf("replacement edit: %+v", got)
	}
	if _, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 8); ok || runs != 1 {
		t.Fatalf("limit exceeded: proof=%v runs=%d", ok, runs)
	}
}

func TestGoVerificationDiscoveryImportedTreeExceedsSharedLimit(t *testing.T) {
	cwd := t.TempDir()
	writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/test\n")
	writeGoDiscoveryFixture(t, cwd, "app/app.go", "package app\nimport _ \"example.com/test/lib\"\n")
	for i := 0; i < defaultVerificationDirectoryFiles; i++ {
		writeGoDiscoveryFixture(t, cwd, fmt.Sprintf("lib/file%03d.go", i), "package lib\n")
	}
	if _, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0); ok || runs != 1 {
		t.Fatalf("shared limit exceeded: proof=%v runs=%d", ok, runs)
	}
}

func TestGoVerificationDiscoveryUnsupportedSourceFeatures(t *testing.T) {
	for _, source := range []string{"//go:build linux\npackage app\n", "package app\nimport \"C\"\n"} {
		cwd := t.TempDir()
		writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/test\n")
		writeGoDiscoveryFixture(t, cwd, "app/app.go", source)
		if _, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0); ok || runs != 1 {
			t.Fatalf("unsupported source %q: proof=%v runs=%d", strings.TrimSpace(source), ok, runs)
		}
	}
}

func TestGoVerificationDiscoverySiblingAndAbsoluteReplace(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		name := "relative"
		if absolute {
			name = "absolute"
		}
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			cwd := filepath.Join(parent, "module")
			sibling := filepath.Join(parent, "sibling")
			writeGoDiscoveryFixture(t, cwd, "app/app.go", "package app\n")
			writeGoDiscoveryFixture(t, sibling, "ext.go", "package sibling\nconst V = 1\n")
			target := "../sibling"
			if absolute {
				var err error
				target, err = filepath.EvalSymlinks(sibling)
				if err != nil {
					t.Fatal(err)
				}
			}
			writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/test\nreplace example.com/external => "+target+"\n")
			proof, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
			wantPath := "../sibling"
			if absolute {
				wantPath = target
			}
			if !ok || runs != 1 || len(proof.Directories) != 2 || proof.Directories[1].Path != wantPath {
				t.Fatalf("proof=%+v ok=%v runs=%d", proof, ok, runs)
			}
			writeGoDiscoveryFixture(t, sibling, "ext.go", "package sibling\nconst V = 2\n")
			if VerificationFresh(cwd, proof, func(string) bool { return true }) {
				t.Fatal("sibling edit left evidence fresh")
			}
			filter, err := json.Marshal(map[string]any{"files": []string{filepath.Join(wantPath, "ext.go")}, "mode": "plan"})
			if err != nil {
				t.Fatal(err)
			}
			if got := goDiscoveryStatus(t, cwd, proof, string(filter)); got.Status != "changed" || got.Action != "rerun" || got.Command != "go test ./app" {
				t.Fatalf("sibling edit: %+v", got)
			}
		})
	}
}

func TestGoVerificationDiscoveryScanReadsWithinEntryCeiling(t *testing.T) {
	cwd := t.TempDir()
	for i := 0; i < 2000; i++ {
		writeGoDiscoveryFixture(t, cwd, fmt.Sprintf("app/file%04d.go", i), "package app\n")
	}
	for _, recursive := range []bool{false, true} {
		name := "single-package"
		if recursive {
			name = "recursive"
		}
		t.Run(name, func(t *testing.T) {
			scan := &goDiscoveryScan{cwd: cwd, allowRead: func(string) bool { return true }}
			var err error
			if recursive {
				err = scan.selectedPackages("app", true)
			} else {
				err = scan.packageImports("app")
			}
			if !errors.Is(err, errVerificationBounds) || scan.entries > maxVerificationDirectoryFiles {
				t.Fatalf("recursive=%v entries=%d err=%v", recursive, scan.entries, err)
			}
		})
	}
}
