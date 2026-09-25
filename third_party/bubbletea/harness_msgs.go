package tea

// HARNESS-PATCH: accessors so a model can count printed (tea.Println) lines
// and detect a screen clear, for a bottom-anchored live region.

// PrintedLines reports whether msg is a printed-lines message and its body.
func PrintedLines(msg Msg) (string, bool) {
	if m, ok := msg.(printLineMessage); ok {
		return m.messageBody, true
	}
	return "", false
}

// IsClearScreen reports whether msg is the tea.ClearScreen message.
func IsClearScreen(msg Msg) bool {
	_, ok := msg.(clearScreenMsg)
	return ok
}
