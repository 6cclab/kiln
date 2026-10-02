//go:build e2e

package e2e

import (
	"testing"
	"time"
)

// bashBackgroundScript runs a command that backgrounds a long job, the way
// a model starts a server before curling it.
const bashBackgroundScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "sleep 120 & echo $! > job.pid; echo started"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`

// The background-job-cleanup tests (TestPrint_BashBackgroundJob_* and
// TestTUI_Hangup_StopsBackgroundJobs) that used to live here moved to
// bash_background_unix_test.go: they check SIGHUP/SIGKILL semantics via
// syscall.Kill, which only Unix has.

// TestTUI_BusyLineSkipsLeadingCd: the busy line names the command a bash
// call runs, not the directory its leading cd moves to (qa/findings
// *busy-line-shows-cd-path).
func TestTUI_BusyLineSkipsLeadingCd(t *testing.T) {
	addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "cd /usr/share/../share/../share && sleep 3"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	defer s.Close()
	if err := s.WaitFor("describe a task", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	s.Send("go\r")
	if err := s.WaitFor("Running sleep 3", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}
