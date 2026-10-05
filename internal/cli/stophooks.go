package cli

import (
	"fmt"
	"strings"

	claudehooks "github.com/andrepato/harness/internal/claude/hooks"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
)

// stopVerdict turns a Stop or SubagentStop hook chain's outcome into the
// harness's answer, and tells the user what the hooks decided:
//
//   - a hook killed by Esc decided nothing, and the run ends as
//     interrupted;
//   - {"continue": false} ends the run, with its stopReason shown;
//   - a block (exit 2, or "decision": "block") keeps the run going: the
//     reason is shown, and sent to the model as a user message.
func stopVerdict(event claudehooks.Event, outcome claudehooks.Outcome, notice func(string)) harness.StopVerdict {
	switch {
	case outcome.Cancelled:
		return harness.StopVerdict{Interrupted: true}
	case outcome.Stopped:
		if outcome.StopReason != "" {
			notice(string(event) + " hook stopped the turn: " + outcome.StopReason)
		}
		return harness.StopVerdict{}
	case outcome.Blocked != nil:
		reason := outcome.Blocked.Reason
		notice(string(event) + " hook asked to continue: " + reason)
		return harness.StopVerdict{
			Continue: true,
			Message:  string(event) + " hook asked to continue:\n" + reason,
			Source:   string(event),
		}
	}
	return harness.StopVerdict{}
}

// lastReplyText is the text of the reply that ended the turn, for a Stop
// hook's last_assistant_message.
// stopHookActivity is the busy row's text while a Stop or SubagentStop
// hook chain runs, as Claude Code words it: "running stop hook" (or
// "running subagent stop hook") for one hook, "running stop hooks… 1/2"
// for several, where the count is how many have finished. A hook's own
// statusMessage, if any hook in the chain sets one, replaces the default:
// "<message>…", with the same count when there are several.
func stopHookActivity(event claudehooks.Event, commands []claudehooks.Command, done int) string {
	total := len(commands)
	for _, c := range commands {
		if c.StatusMessage != "" {
			if total == 1 {
				return c.StatusMessage + "…"
			}
			return fmt.Sprintf("%s… %d/%d", c.StatusMessage, done, total)
		}
	}
	if total == 1 {
		if event == claudehooks.SubagentStop {
			return "running subagent stop hook"
		}
		return "running stop hook"
	}
	return fmt.Sprintf("running stop hooks… %d/%d", done, total)
}

func lastReplyText(m *msg.AssistantMessage) string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(msg.TextOf(m.Content))
}
