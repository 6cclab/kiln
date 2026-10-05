package cli

import (
	"testing"

	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
)

func TestStopHookActivityWording(t *testing.T) {
	one := []claudehooks.Command{{Command: "a"}}
	two := []claudehooks.Command{{Command: "a"}, {Command: "b"}}
	custom := []claudehooks.Command{{Command: "a"}, {Command: "b", StatusMessage: "Checking the tests"}}
	for _, tc := range []struct {
		name  string
		event claudehooks.Event
		cmds  []claudehooks.Command
		done  int
		want  string
	}{
		{"one stop hook", claudehooks.Stop, one, 0, "running stop hook"},
		{"one subagent stop hook", claudehooks.SubagentStop, one, 0, "running subagent stop hook"},
		{"several, counted by finished", claudehooks.Stop, two, 1, "running stop hooks… 1/2"},
		{"a statusMessage replaces the default", claudehooks.Stop, custom, 0, "Checking the tests… 0/2"},
		{"a lone statusMessage has no count", claudehooks.Stop, []claudehooks.Command{{StatusMessage: "Linting"}}, 0, "Linting…"},
	} {
		if got := stopHookActivity(tc.event, tc.cmds, tc.done); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
