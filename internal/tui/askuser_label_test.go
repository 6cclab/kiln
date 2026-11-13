package tui

import "testing"

// TestAskUserOptionLabelSkipsARepeatedDescription: $.ui.ask's options carry
// their label as the description, which read "Alpha — Alpha" on screen.
func TestAskUserOptionLabelSkipsARepeatedDescription(t *testing.T) {
	for _, c := range []struct {
		opt  AskUserOptionView
		want string
	}{
		{AskUserOptionView{Label: "Alpha", Description: "Alpha"}, "Alpha"},
		{AskUserOptionView{Label: "Alpha", Description: "the first"}, "Alpha — the first"},
		{AskUserOptionView{Label: "Alpha"}, "Alpha"},
	} {
		if got := askUserOptionLabel(c.opt, false, false); got != c.want {
			t.Errorf("askUserOptionLabel(%+v) = %q, want %q", c.opt, got, c.want)
		}
	}
}
