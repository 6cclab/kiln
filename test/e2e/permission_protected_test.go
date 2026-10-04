//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// permProtectedEditScript has the model use the edit tool on .git/config to
// set core.hooksPath: the route a model took in a real acceptEdits run when
// the sandbox kept bash away from the file. Git would later run whatever
// that directory holds, outside any sandbox.
const permProtectedEditScript = `model: faux-1
steps:
  - tool_call: {name: edit, args: {path: .git/config, edits: [{oldText: "[core]", newText: "[core]\n\thooksPath = .githooks"}]}, id: e1}
  - on_tool_result: e1
    then:
      - text: "done"
`

// TestPermission_ProtectedEditRefusedInAcceptEdits: in acceptEdits print
// mode a file-tool write to .git/config is a protected-path write, which
// needs the user's approval in every mode but bypassPermissions; print mode
// cannot ask, so it is refused. The file stays as it was, and the refusal
// the model reads (and the session stores) names the protected path and
// tells it not to route around it.
func TestPermission_ProtectedEditRefusedInAcceptEdits(t *testing.T) {
	addr, srv := startFaux(t, permProtectedEditScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	gitConfig := filepath.Join(proj, ".git", "config")
	const original = "[core]\n\trepositoryformatversion = 0\n\tbare = false\n"
	if err := os.MkdirAll(filepath.Dir(gitConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitConfig, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	run := runHarness(t, proj, baseEnv(home, sessDir, addr),
		"-p", "set up the hooks", "--output-format", "json", "--permission-mode", "acceptEdits")
	if run.Code != 0 {
		t.Fatalf("exit code %d, want 0 (a refusal is feedback to the model)\nstdout=%s\nstderr=%s", run.Code, run.Stdout, run.Stderr)
	}
	var res struct {
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
		t.Fatalf("parse --output-format json: %v\nstdout=%s", err, run.Stdout)
	}
	var reason string
	for _, b := range res.Blocked {
		if strings.HasPrefix(b, "edit(") {
			reason = b
		}
	}
	if reason == "" {
		t.Fatalf("the edit of .git/config was not refused; blocked=%v", res.Blocked)
	}
	for _, want := range []string{"protected path", "user's approval", "Do not try to change it another way"} {
		if !strings.Contains(reason, want) {
			t.Errorf("refusal %q lacks %q", reason, want)
		}
	}

	got, err := os.ReadFile(gitConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf(".git/config changed:\n%s", got)
	}

	// What the model read back: the second request carries the tool result.
	reqs := srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("got %d model requests, want the follow-up carrying the tool result", len(reqs))
	}
	if second := string(reqs[1].Messages); !strings.Contains(second, "protected path") {
		t.Errorf("the model was not told the path is protected:\n%s", second)
	}
	// And the transcript the session stored.
	raw, err := os.ReadFile(sessionFile(t, sessDir, proj))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "protected path") {
		t.Errorf("the session transcript does not show the refusal:\n%s", raw)
	}
}
