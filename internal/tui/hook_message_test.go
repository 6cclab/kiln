package tui

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// A Stop hook's feedback is stored as a user message the model reads
// (msg.UserMessage.KilnHook), but no user wrote it: a replay must not show
// it as a "you" block, and Rewind must not offer it as a turn of its own.
func TestHookContinuationIsNotAUserTurn(t *testing.T) {
	entries := []session.Entry{
		{ID: "a", Seq: 1, Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("finish up")}}},
		{ID: "b", Seq: 2, Type: session.EntryMessage, Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("first reply")}}},
		{ID: "c", Seq: 3, Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, KilnHook: "Stop", Content: msg.Blocks{msg.Text("Stop hook asked to continue:\nrun the tests first")}}},
		{ID: "d", Seq: 4, Type: session.EntryMessage, Message: msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("second reply")}}},
	}
	out := strings.Join(RenderTranscriptEntries(entries, 80, false, "/tmp", nil, nil), "\n")
	if strings.Contains(out, "run the tests first") {
		t.Errorf("replay shows the hook's feedback as a user turn:\n%s", out)
	}
	for _, want := range []string{"finish up", "first reply", "second reply"} {
		if !strings.Contains(out, want) {
			t.Errorf("replay lost %q:\n%s", want, out)
		}
	}
	rewind := RewindEntriesFromSession(entries)
	if len(rewind) != 1 || rewind[0].ID != "a" {
		t.Errorf("rewind entries = %+v, want only the user's own message", rewind)
	}
}
