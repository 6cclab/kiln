package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/sandbox"
)

func writeProjectSettings(t *testing.T, proj, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pretendPlatform makes the sandbox see another platform for one test.
func pretendPlatform(t *testing.T, goos string) {
	t.Helper()
	old := sandboxOptions
	sandboxOptions = func(o sandbox.Options) sandbox.Options {
		o.GOOS = goos
		return o
	}
	t.Cleanup(func() { sandboxOptions = old })
}

func runPrint(t *testing.T, mode string) (int, string, string) {
	t.Helper()
	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "run a command"
	args.OutputFormat = "json"
	args.PermissionMode = mode
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	return code, stdout.String(), stderr.String()
}

// TestRun_Sandbox_AutoAllowsInManualMode: the same command manual mode
// refuses with nobody to ask (TestRun_ManualMode_BlocksBash) runs when the
// sandbox is on, because it runs sandboxed (autoAllowBashIfSandboxed
// defaults to true).
func TestRun_Sandbox_AutoAllowsInManualMode(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs sandbox-exec")
	}
	startFaux(t, bashCallScript)
	proj := scratchProject(t)
	writeProjectSettings(t, proj, `{"sandbox":{"enabled":true}}`)

	code, stdout, stderr := runPrint(t, "manual")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var parsed struct {
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("parse: %v\n%s", err, stdout)
	}
	if len(parsed.Blocked) != 0 {
		t.Errorf("blocked = %v, want the sandboxed command auto-allowed", parsed.Blocked)
	}
	if _, err := os.Stat(filepath.Join(proj, "hi.txt")); err != nil {
		t.Errorf("the command did not run: %v", err)
	}
}

// TestRun_Sandbox_Unavailable: enabled but impossible here, kiln warns
// once and runs commands unsandboxed (Claude Code's fallback), so manual
// mode asks again; with failIfUnavailable it refuses to start.
func TestRun_Sandbox_Unavailable(t *testing.T) {
	startFaux(t, bashCallScript)
	proj := scratchProject(t)
	pretendPlatform(t, "plan9")

	writeProjectSettings(t, proj, `{"sandbox":{"enabled":true}}`)
	code, stdout, stderr := runPrint(t, "manual")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Count(stderr, "the sandbox cannot start") != 1 || !strings.Contains(stderr, "not supported on plan9") {
		t.Errorf("want one startup warning naming the reason, stderr = %q", stderr)
	}
	if !strings.Contains(stdout, "requires confirmation") {
		t.Errorf("an unavailable sandbox must not auto-allow: %s", stdout)
	}

	writeProjectSettings(t, proj, `{"sandbox":{"enabled":true,"failIfUnavailable":true}}`)
	code, _, stderr = runPrint(t, "manual")
	if code == 0 || !strings.Contains(stderr, "failIfUnavailable") {
		t.Errorf("failIfUnavailable: exit %d, stderr %q", code, stderr)
	}
}

func TestDoctor_Sandbox(t *testing.T) {
	startFaux(t, unreadScript)
	proj := scratchProject(t)
	doctor := func() string {
		var stdout, stderr bytes.Buffer
		if code := Doctor(context.Background(), baseArgs(), &stdout, &stderr); code != 0 {
			t.Fatalf("doctor exit %d: %s", code, stderr.String())
		}
		return stdout.String()
	}
	if out := doctor(); !strings.Contains(out, "sandbox    off") {
		t.Errorf("sandbox off not reported:\n%s", out)
	}

	writeProjectSettings(t, proj, `{"sandbox":{"enabled":true,"allowUnsandboxedCommands":false,"network":{"allowedDomains":["github.com"]}}}`)
	if runtime.GOOS == "darwin" {
		out := doctor()
		if !strings.Contains(out, "sandbox    on (sandbox-exec (Seatbelt)), auto-allow, strict (no unsandboxed retry), 1 allowed domain(s)") {
			t.Errorf("sandbox on not reported:\n%s", out)
		}
	}

	pretendPlatform(t, "windows")
	out := doctor()
	if !strings.Contains(out, "sandbox    enabled, unavailable: sandboxing is not supported on windows") ||
		!strings.Contains(out, "bash commands run unsandboxed") {
		t.Errorf("unavailable sandbox not reported as a problem:\n%s", out)
	}
}
