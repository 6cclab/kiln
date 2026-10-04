//go:build e2e

package e2e

// The OS sandbox, driven through the real binary with a scripted model:
// with "sandbox": {"enabled": true} in the project's settings, a bash call
// runs inside sandbox-exec, so manual mode auto-allows it (Claude Code's
// autoAllowBashIfSandboxed default), an attempt to write outside the
// workspace fails at the OS level, and the transcript the model sees says
// what the sandbox refused. Network access to a host that is not
// allowlisted is refused by kiln's proxy (print mode has nobody to ask), so
// no test here touches the network.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// sandboxEscapeScript's escape writes outside the workspace to a path that
// is not protected (a protected one, such as ~/.bashrc, is stopped by the
// permission gate before the sandbox sees it), so the gate auto-allows it
// and the OS sandbox is what refuses it.
const sandboxEscapeScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo pwned >> %s/escape.txt"}, id: b1}
  - on_tool_result: b1
    then:
      - tool_call: {name: bash, args: {command: "echo inside > inside.txt && cat inside.txt"}, id: b2}
  - on_tool_result: b2
    then:
      - tool_call: {name: bash, args: {command: "curl -sSf -m 10 http://example.com/"}, id: b3}
  - on_tool_result: b3
    then:
      - text: "done"
`

func TestSandbox_EscapeFailsAndTranscriptSaysSo(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the e2e sandbox run needs sandbox-exec (Linux bwrap is covered by argv tests)")
	}
	home, sessDir := scratchHome(t)
	home, _ = filepath.EvalSymlinks(home)
	proj := scratchProject(t)
	permWriteJSON(t, proj, map[string]any{"sandbox": map[string]any{"enabled": true}})
	addr, _ := startFaux(t, fmt.Sprintf(sandboxEscapeScript, home))

	run := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "try things", "--output-format", "json", "--permission-mode", "manual")
	if run.Code != 0 {
		t.Fatalf("exit %d\nstdout=%s\nstderr=%s", run.Code, run.Stdout, run.Stderr)
	}
	if strings.Contains(run.Stdout, "requires confirmation") {
		t.Errorf("sandboxed commands should be auto-allowed in manual mode: %s", run.Stdout)
	}

	if _, err := os.Stat(filepath.Join(home, "escape.txt")); err == nil {
		t.Error("~/escape.txt was written: the sandbox did not hold")
	}
	if b, err := os.ReadFile(filepath.Join(proj, "inside.txt")); err != nil || strings.TrimSpace(string(b)) != "inside" {
		t.Errorf("a write inside the workspace failed: %q %v", b, err)
	}

	data, err := os.ReadFile(sessionFile(t, sessDir, proj))
	if err != nil {
		t.Fatal(err)
	}
	transcript := string(data)
	for _, want := range []string{
		"Operation not permitted",                // the escape, refused by the OS
		"[kiln sandbox]",                         // the note kiln adds for the model
		"dangerouslyDisableSandbox",              // and how to retry outside it
		"Blocked network access: example.com:80", // the proxy refusing a host
	} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript lacks %q", want)
		}
	}
}

// sandboxNetScript has the model fetch a host outside the allowlist. It
// is a reserved .invalid name: a local address (127.0.0.1) is never
// offered for approval, and no name in a test can resolve to a public
// address that reaches a local server, so after approval the proxy's dial
// fails. Reaching a server through an approved host is covered by the
// real-sandbox network tests in internal/sandbox, which use a test resolver.
const sandboxNetScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "curl -sS -m 20 http://approve-me.invalid/"}, id: n1}
  - on_tool_result: n1
    then:
      - text: "net done"
`

// TestSandbox_NetworkApprovalPrompt: in manual mode, a sandboxed command
// reaching a host outside the allowlist pauses on a prompt (Claude Code's
// sandboxing docs, "Hosts outside your allowed domains"); answering Yes
// lets the request past the proxy's checks (it then fails to resolve, a
// 502 from the proxy rather than its 403 block), and the command completes.
func TestSandbox_NetworkApprovalPrompt(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs sandbox-exec")
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, sandboxNetScript)
	permWriteJSON(t, proj, map[string]any{"sandbox": map[string]any{"enabled": true}})

	s := startTUI(t, 110, 34, proj, home, sessDir, addr, "--permission-mode", "manual")
	defer s.Close()
	waitReady(t, s)
	s.Send("fetch it")
	s.SendKey("enter")
	target := "approve-me.invalid"
	// Wording fixed by qa/findings/20261004T205021Z-sandbox-network-
	// prompt-wording.json: the prompt asks about the host directly
	// ("Allow network access to <host>?"), not "Allow kiln to use sandbox
	// network?".
	if err := s.WaitFor("Allow network access to", 10*time.Second); err != nil {
		t.Fatalf("no network approval prompt: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	if err := s.WaitFor(target, 2*time.Second); err != nil {
		t.Fatalf("the prompt does not name %s: %v", target, err)
	}
	s.SendKey("1") // Yes
	if err := s.WaitFor("net done", 20*time.Second); err != nil {
		t.Fatalf("the run did not finish after approval: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	data, err := os.ReadFile(sessionFile(t, sessDir, proj))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "kiln sandbox proxy: ") || strings.Contains(string(data), "Blocked network access: approve-me.invalid") {
		t.Errorf("the approved request was not let past the proxy's checks; transcript:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(proj, ".kiln", "settings.local.json")); err == nil {
		t.Error("a plain Yes must not save a rule")
	}
}
