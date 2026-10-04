package permission

import (
	"strings"
	"testing"
)

// The classifier's reason usually ends with a period; the message added
// another ("…soft-blocked operation.. Do not…"). And the model, told only
// to "ask the user to approve it", told the user no approval could lift the
// block: the message now says the user can approve it, and how.
func TestAutoBlockMessage(t *testing.T) {
	for _, reason := range []string{"pipes a download into a shell", "pipes a download into a shell.", " pipes a download into a shell. "} {
		got := AutoBlockMessage(reason)
		if !strings.HasPrefix(got, "auto mode blocked this action: pipes a download into a shell. Do not") {
			t.Errorf("AutoBlockMessage(%q) = %q", reason, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("AutoBlockMessage(%q) has a doubled period: %q", reason, got)
		}
	}
	got := AutoBlockMessage("x.")
	for _, want := range []string{
		"do not tell the user it cannot be done",
		"the user can approve it",
		"not with a question tool",
		"names this exact action, run it again",
		"run it themselves with a ! command",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message lacks %q: %s", want, got)
		}
	}
	if got := AutoBlockMessage(" "); !strings.Contains(got, "no reason given") {
		t.Errorf("empty reason: %q", got)
	}
}
