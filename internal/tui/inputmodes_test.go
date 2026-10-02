package tui

import "testing"

// TestClassifyInput_HashIsAnOrdinaryPrompt: kiln dropped its own `#`
// memory-note input mode to match Claude Code, whose own prompt input has
// no such shortcut (only a bash prefix and a plain prompt). A line
// starting with `#` must no longer be classified - it goes to the model
// like any other line.
func TestClassifyInput_HashIsAnOrdinaryPrompt(t *testing.T) {
	for _, line := range []string{"#remember to run tests", "# a heading-looking line", "#"} {
		if c, ok := ClassifyInput(line); ok {
			t.Errorf("ClassifyInput(%q) = %+v, ok=true; want unclassified (an ordinary prompt)", line, c)
		}
	}
}

// TestClassifyInput_BangStillClassifies: `!` is still a mode - only `#`
// was removed.
func TestClassifyInput_BangStillClassifies(t *testing.T) {
	c, ok := ClassifyInput("!echo hi")
	if !ok || c.Mode != ModeBang || c.Body != "echo hi" {
		t.Errorf("ClassifyInput(%q) = %+v, %v", "!echo hi", c, ok)
	}
}

// TestClassifyInput_EmptyLineNeverClassifies: a bare "!" or "#" with
// nothing after it is too short to be a mode (ClassifyInput requires
// len(line) > 1), so it too is sent to the model unclassified.
func TestClassifyInput_EmptyLineNeverClassifies(t *testing.T) {
	for _, line := range []string{"!", "#", ""} {
		if _, ok := ClassifyInput(line); ok {
			t.Errorf("ClassifyInput(%q) should not classify", line)
		}
	}
}
