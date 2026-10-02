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
)

const sandboxEscapeScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo pwned >> %s/.bashrc"}, id: b1}
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

	if _, err := os.Stat(filepath.Join(home, ".bashrc")); err == nil {
		t.Error("~/.bashrc was written: the sandbox did not hold")
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
