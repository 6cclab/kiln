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

func TestGoVerificationDiscoveryBuildTaggedAndCgoFilesFlipStatus(t *testing.T) {
	cwd := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":           "module example.com/test\n",
		"app/app.go":       "package app\n",
		"app/app_linux.go": "//go:build linux\n\npackage app\nimport _ \"example.com/test/tagged\"\n",
		"app/cgo.go":       "package app\n/*\n#include \"x.h\"\n*/\nimport \"C\"\n",
		"app/x.h":          "#define X 1\n",
		"app/x.c":          "int x(void){return 1;}\n",
		"tagged/tagged.go": "package tagged\n",
	} {
		writeGoDiscoveryFixture(t, cwd, path, body)
	}
	proof, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok || runs != 1 {
		t.Fatalf("tagged/cgo package gave no proof: ok=%v runs=%d", ok, runs)
	}
	if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "unchanged" {
		t.Fatalf("unchanged: %+v", got)
	}
	for _, path := range []string{"app/app_linux.go", "tagged/tagged.go", "app/x.h", "app/x.c"} {
		saved, err := os.ReadFile(filepath.Join(cwd, path))
		if err != nil {
			t.Fatal(err)
		}
		writeGoDiscoveryFixture(t, cwd, path, string(saved)+"// edit\n")
		if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "changed" {
			t.Fatalf("edit of %s did not flip status: %+v", path, got)
		}
		writeGoDiscoveryFixture(t, cwd, path, string(saved))
	}
}

func TestGoVerificationDiscoveryEmbedTargetFlipsStatus(t *testing.T) {
	cwd := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":           "module example.com/test\n",
		"app/app.go":       "package app\nimport _ \"embed\"\n//go:embed static/*.txt data.json\nvar f string\n",
		"app/static/a.txt": "a\n",
		"app/data.json":    "{}\n",
	} {
		writeGoDiscoveryFixture(t, cwd, path, body)
	}
	proof, ok, _ := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok {
		t.Fatal("no proof")
	}
	for _, path := range []string{"app/static/a.txt", "app/data.json"} {
		writeGoDiscoveryFixture(t, cwd, path, "changed\n")
		if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "changed" {
			t.Fatalf("embed edit of %s: %+v", path, got)
		}
	}
}

func TestGoVerificationDiscoveryWorkspaceModules(t *testing.T) {
	cwd := t.TempDir()
	for path, body := range map[string]string{
		"go.work":          "go 1.24\n\nuse (\n\t.\n\t./lib\n)\n",
		"go.mod":           "module example.com/root\n",
		"app/app.go":       "package app\nimport _ \"example.com/lib/sub\"\n",
		"lib/go.mod":       "module example.com/lib\n\ngo 1.24\n",
		"lib/sub/sub.go":   "package sub\nimport _ \"example.com/root/common\"\n",
		"common/common.go": "package common\n",
		"other/other.go":   "package other\n",
	} {
		writeGoDiscoveryFixture(t, cwd, path, body)
	}
	proof, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok || runs != 1 {
		t.Fatalf("workspace discovery: ok=%v runs=%d", ok, runs)
	}
	writeGoDiscoveryFixture(t, cwd, "other/other.go", "package other\nconst V = 2\n")
	if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "unchanged" {
		t.Fatalf("unrelated edit: %+v", got)
	}
	for _, path := range []string{"lib/sub/sub.go", "lib/go.mod", "common/common.go"} {
		saved, _ := os.ReadFile(filepath.Join(cwd, path))
		writeGoDiscoveryFixture(t, cwd, path, string(saved)+"// edit\n")
		if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "changed" {
			t.Fatalf("edit of %s did not flip status: %+v", path, got)
		}
		writeGoDiscoveryFixture(t, cwd, path, string(saved))
	}
}

func TestGoVerificationDiscoveryWorkspaceModuleOutsideRoot(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "root")
	for path, body := range map[string]string{
		"root/go.work":    "go 1.24\nuse ../ext\nuse .\n",
		"root/go.mod":     "module example.com/root\n",
		"root/app/app.go": "package app\nimport _ \"example.com/ext\"\n",
		"ext/go.mod":      "module example.com/ext\n",
		"ext/ext.go":      "package ext\n",
	} {
		writeGoDiscoveryFixture(t, parent, path, body)
	}
	proof, ok, _ := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok {
		t.Fatal("no proof")
	}
	writeGoDiscoveryFixture(t, parent, "ext/ext.go", "package ext\nconst V = 2\n")
	if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "changed" {
		t.Fatalf("external workspace edit: %+v", got)
	}
}

func TestGoVerificationDiscoveryGoSumAndToolchainFlipStatus(t *testing.T) {
	cwd := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":     "module example.com/test\n\ngo 1.24\n",
		"go.sum":     "example.com/dep v1.0.0 h1:aaa=\n",
		"app/app.go": "package app\n",
	} {
		writeGoDiscoveryFixture(t, cwd, path, body)
	}
	proof, ok, _ := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok {
		t.Fatal("no proof")
	}
	for path, body := range map[string]string{
		"go.sum": "example.com/dep v1.0.1 h1:bbb=\n",
		"go.mod": "module example.com/test\n\ngo 1.24\n\ntoolchain go1.24.5\n",
	} {
		saved, _ := os.ReadFile(filepath.Join(cwd, path))
		writeGoDiscoveryFixture(t, cwd, path, body)
		if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "changed" {
			t.Fatalf("%s edit: %+v", path, got)
		}
		writeGoDiscoveryFixture(t, cwd, path, string(saved))
	}
}

func TestGoVerificationDiscoveryEnvironmentFlipsStatus(t *testing.T) {
	cwd := t.TempDir()
	writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/test\n")
	writeGoDiscoveryFixture(t, cwd, "app/app.go", "package app\n")
	t.Setenv("GOFLAGS", "-count=1")
	t.Setenv("GOOS", "linux")
	proof, ok, _ := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok || proof.Env["GOFLAGS"] != "-count=1" || proof.Env["GOOS"] != "linux" || len(proof.Env) != 2 {
		t.Fatalf("env not recorded: ok=%v env=%v", ok, proof.Env)
	}
	if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "unchanged" {
		t.Fatalf("unchanged env: %+v", got)
	}
	if !VerificationFresh(cwd, proof, func(string) bool { return true }) {
		t.Fatal("fresh proof rejected")
	}
	t.Setenv("GOFLAGS", "-tags=integration")
	got := goDiscoveryStatus(t, cwd, proof, `{}`)
	if got.Status != "changed" || len(got.Env) != 1 || got.Env[0] != "GOFLAGS" || VerificationFresh(cwd, proof, func(string) bool { return true }) {
		t.Fatalf("GOFLAGS change: %+v", got)
	}
	t.Setenv("GOFLAGS", "-count=1")
	t.Setenv("CGO_ENABLED", "0")
	if got := goDiscoveryStatus(t, cwd, proof, `{}`); got.Status != "changed" || len(got.Env) != 1 || got.Env[0] != "CGO_ENABLED" {
		t.Fatalf("newly set variable: %+v", got)
	}
}

func TestGoVerificationDiscoveryV1RecordsStillReadable(t *testing.T) {
	cwd := t.TempDir()
	writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/test\n")
	writeGoDiscoveryFixture(t, cwd, "app/app.go", "package app\n")
	t.Setenv("GOFLAGS", "-count=1")
	proof, ok, _ := runGoDiscoveryFixture(t, cwd, "go test ./app", 0)
	if !ok {
		t.Fatal("no proof")
	}
	proof.Discovery, proof.Env = goPartialDiscoveryV1, nil
	details, _ := json.Marshal(map[string]any{"taskVerification": proof})
	old, ok := VerificationFromResult(details)
	if !ok {
		t.Fatal("v1 record rejected")
	}
	t.Setenv("GOFLAGS", "-race")
	if got := goDiscoveryStatus(t, cwd, old, `{}`); got.Status != "unchanged" {
		t.Fatalf("v1 compares no environment: %+v", got)
	}
	proof.Env = map[string]string{"GOFLAGS": "x"}
	details, _ = json.Marshal(map[string]any{"taskVerification": proof})
	if _, ok := VerificationFromResult(details); ok {
		t.Fatal("v1 record with env accepted")
	}
	proof.Discovery, proof.Env = goPartialDiscovery, map[string]string{"PATH": "x"}
	details, _ = json.Marshal(map[string]any{"taskVerification": proof})
	if _, ok := VerificationFromResult(details); ok {
		t.Fatal("unlisted env name accepted")
	}
}

func TestGoVerificationDiscoveryOversizedEnvironmentSavesNoProof(t *testing.T) {
	cwd := t.TempDir()
	writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/test\n")
	writeGoDiscoveryFixture(t, cwd, "app/app.go", "package app\n")
	t.Setenv("GOFLAGS", strings.Repeat("x", maxGoVerificationEnvBytes+1))
	if _, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0); ok || runs != 1 {
		t.Fatalf("ok=%v runs=%d", ok, runs)
	}
}

func TestGoVerificationDiscoveryWorkspaceLimitSavesNoProof(t *testing.T) {
	cwd := t.TempDir()
	writeGoDiscoveryFixture(t, cwd, "go.work", "go 1.24\nuse ./lib\n")
	writeGoDiscoveryFixture(t, cwd, "go.mod", "module example.com/root\n")
	writeGoDiscoveryFixture(t, cwd, "app/app.go", "package app\nimport _ \"example.com/lib\"\n")
	writeGoDiscoveryFixture(t, cwd, "lib/go.mod", "module example.com/lib\n")
	for i := 0; i < defaultVerificationDirectoryFiles; i++ {
		writeGoDiscoveryFixture(t, cwd, fmt.Sprintf("lib/file%03d.go", i), "package lib\n")
	}
	if _, ok, runs := runGoDiscoveryFixture(t, cwd, "go test ./app", 0); ok || runs != 1 {
		t.Fatalf("workspace over shared limit: proof=%v runs=%d", ok, runs)
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
