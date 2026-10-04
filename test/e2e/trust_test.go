//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// trustBashScript asks for one command the project's allow rule covers and
// manual mode would otherwise prompt for.
const trustBashScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo ran | tee ran.txt"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`

// untrustedTUI starts kiln with the trust dialog enabled (screen.Start
// turns it off by default) in a project whose .claude/settings.json allows
// the script's command and has SessionStart and SessionEnd hooks that
// leave marker files.
func untrustedTUI(t *testing.T, prepare ...func(proj, home string)) (s *screen.Screen, proj, home string, requests func() []recordedMessages) {
	t.Helper()
	proj, home, sessDir, addr, requests := tuiFixture(t, trustBashScript)
	settings := `{
  "permissions": {"allow": ["Bash(tee *)"]},
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "touch session-start; echo CONTEXT-FROM-SESSION-START"}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "touch session-end"}]}]
  }
}`
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range prepare {
		p(proj, home)
	}
	tuiExtraOpts = []screen.Option{screen.WithEnv("HARNESS_TRUST_ALL", "")}
	t.Cleanup(func() { tuiExtraOpts = nil })
	s = startTUI(t, 100, 30, proj, home, sessDir, addr)
	if err := s.WaitFor("Yes, I trust this folder", 10*time.Second); err != nil {
		t.Fatalf("no trust dialog: %v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	return s, proj, home, requests
}

func waitFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// No hook runs before the folder is trusted, as in Claude Code. Accepting
// the dialog starts them (SessionStart runs then, and its output reaches
// the first prompt) and applies the project's allow rules, without a
// restart.
func TestTUI_Trust_HooksAndAllowWaitForTrust(t *testing.T) {
	s, proj, home, requests := untrustedTUI(t)
	defer s.Exit()

	time.Sleep(time.Second) // time for a hook that should not run to run
	if _, err := os.Stat(filepath.Join(proj, "session-start")); err == nil {
		t.Fatal("the project's SessionStart hook ran before the folder was trusted")
	}

	s.SendKey("2")
	if !waitFile(filepath.Join(proj, "session-start"), 10*time.Second) {
		t.Fatal("SessionStart did not run once the folder was trusted")
	}
	data, err := os.ReadFile(filepath.Join(home, ".harness", "trusted.json"))
	if err != nil || !strings.Contains(string(data), proj) {
		t.Errorf("trust not recorded in the scratch HOME: %v %s", err, data)
	}
	time.Sleep(500 * time.Millisecond) // the hook's output reaches the TUI

	waitReady(t, s)
	s.Send("go")
	s.SendKey("enter")
	if !waitFile(filepath.Join(proj, "ran.txt"), 15*time.Second) {
		t.Fatalf("the project's allow rule did not apply after trust:\n%s", strings.Join(s.Rows(), "\n"))
	}
	reqs := requests()
	if len(reqs) == 0 || !strings.Contains(string(reqs[0]), "CONTEXT-FROM-SESSION-START") {
		t.Errorf("the first prompt lacks the SessionStart context: %d requests", len(reqs))
	}
}

// runsBeforeTrust makes proj a repository whose own git config names a
// clean filter (as a hostile clone can: .git/config plus .gitattributes)
// and gives the person a user-scope MCP server, each leaving a marker
// file when it runs. It returns the two markers.
func runsBeforeTrust(t *testing.T) (prepare func(proj, home string), filterRan, mcpRan func(proj string) string) {
	t.Helper()
	filterRan = func(proj string) string { return filepath.Join(proj, "..", filepath.Base(proj)+"-filter-ran") }
	mcpRan = func(proj string) string { return filepath.Join(proj, "..", filepath.Base(proj)+"-mcp-ran") }
	prepare = func(proj, home string) {
		gitc := func(args ...string) {
			cmd := exec.Command("git", append([]string{"-C", proj}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
		gitc("init", "-q", "-b", "trustbranch")
		gitc("add", "src/math.js")
		gitc("commit", "-qm", "init")
		gitc("config", "filter.mark.clean", "touch '"+filterRan(proj)+"'; cat")
		if err := os.WriteFile(filepath.Join(proj, ".gitattributes"), []byte("* filter=mark\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// A tracked file changed but the same size: git status must hash
		// it, through the filter, to tell.
		mathJS := filepath.Join(proj, "src", "math.js")
		data, err := os.ReadFile(mathJS)
		if err != nil {
			t.Fatal(err)
		}
		data[0] = 'F'
		if err := os.WriteFile(mathJS, data, 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := `{"mcpServers":{"marker":{"command":"sh","args":["-c","touch '` + mcpRan(proj) + `'; sleep 30"]}}}`
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return prepare, filterRan, mcpRan
}

// Before the folder is trusted kiln runs nothing the folder configures:
// no git that reads its config (a clean filter would run), and no MCP
// server, not even the person's own (a stdio server starts in the
// folder). The banner still names the branch, read from .git's files.
// Accepting the dialog starts both.
func TestTUI_Trust_NoGitOrMCPBeforeTrust(t *testing.T) {
	prepare, filterRan, mcpRan := runsBeforeTrust(t)
	s, proj, _, _ := untrustedTUI(t, prepare)
	defer s.Exit()

	time.Sleep(1500 * time.Millisecond) // time for what should not run to run
	if _, err := os.Stat(filterRan(proj)); err == nil {
		t.Error("the repository's git filter ran before the folder was trusted")
	}
	if _, err := os.Stat(mcpRan(proj)); err == nil {
		t.Error("an MCP server started before the folder was trusted")
	}
	if !strings.Contains(strings.Join(s.Rows(), "\n"), "branch trustbranch") {
		t.Errorf("banner lacks the branch:\n%s", strings.Join(s.Rows(), "\n"))
	}

	s.SendKey("2")
	if !waitFile(mcpRan(proj), 10*time.Second) {
		t.Error("the MCP server did not start once the folder was trusted")
	}
	if !waitFile(filterRan(proj), 10*time.Second) {
		t.Error("git status did not run once the folder was trusted")
	}
}

// Declining starts nothing either.
func TestTUI_Trust_DeclineStartsNoGitOrMCP(t *testing.T) {
	prepare, filterRan, mcpRan := runsBeforeTrust(t)
	s, proj, _, _ := untrustedTUI(t, prepare)
	time.Sleep(time.Second)
	s.SendKey("1")
	if code, err := s.Exit(); err != nil {
		t.Fatalf("exit %d: %v", code, err)
	}
	for _, f := range []string{filterRan(proj), mcpRan(proj)} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s exists after declining", filepath.Base(f))
		}
	}
}

// "No, exit" runs no hook at all: SessionEnd included.
func TestTUI_Trust_DeclineRunsNoHooks(t *testing.T) {
	s, proj, _, _ := untrustedTUI(t)
	s.SendKey("1")
	if code, err := s.Exit(); err != nil {
		t.Fatalf("exit %d: %v", code, err)
	}
	for _, f := range []string{"session-start", "session-end"} {
		if _, err := os.Stat(filepath.Join(proj, f)); err == nil {
			t.Errorf("%s hook ran in a folder the person declined to trust", f)
		}
	}
}
