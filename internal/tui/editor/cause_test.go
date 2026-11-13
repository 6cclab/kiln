package editor

import "testing"

// TestCauseNamesWhatChangedTheText: every way the buffer changes other
// than an edit key records its cause (the mods' prompt.change reads it), a
// typed key records none, and ClearCause forgets it.
func TestCauseNamesWhatChangedTheText(t *testing.T) {
	m := newTestModel()
	steps := []struct {
		name string
		do   func()
		want string
	}{
		{"typed key", func() { m, _, _ = m.Update(key('a')) }, ""},
		{"SetValue text", func() { m.SetValue("hello") }, "engine"},
		{"SetValue empty", func() { m.SetValue("") }, "clear"},
		{"Clear", func() { m.SetValue("x"); m.ClearCause(); m.Clear() }, "clear"},
		{"ReplaceCursorLine", func() { m.SetValue("/mo"); m.ClearCause(); m.ReplaceCursorLine(0, 3, "/model ") }, "engine"},
		{"history recall", func() {
			m.Clear()
			m.SetHistory([]string{"earlier prompt"})
			m.ClearCause()
			m, _, _ = m.Update(named("up"))
		}, "history"},
		{"SetCause", func() { m.SetCause("plugin:squad") }, "plugin:squad"},
		{"submit", func() { m.SetValue("go"); m.ClearCause(); m, _, _ = m.Update(named("enter")) }, "clear"},
	}
	for _, s := range steps {
		m.ClearCause()
		s.do()
		if got := m.Cause(); got != s.want {
			t.Errorf("%s: Cause() = %q, want %q (value %q)", s.name, got, s.want, m.Value())
		}
	}
	m.ClearCause()
	if m.Cause() != "" {
		t.Fatal("ClearCause left a cause")
	}
}

// TestSetValueCursorRoundTrips: SetValueCursor puts the cursor at a rune
// offset, across lines and multi-byte runes, clamped to the buffer, and
// CursorOffset reads the same offset back.
func TestSetValueCursorRoundTrips(t *testing.T) {
	m := newTestModel()
	text := "héllo\nwörld\nend"
	for _, c := range []struct{ in, want int }{
		{0, 0}, {3, 3}, {5, 5}, {6, 6}, {9, 9}, {12, 12}, {15, 15},
		{-4, 0}, {99, 15},
	} {
		m.SetValueCursor(text, c.in)
		if m.Value() != text {
			t.Fatalf("SetValueCursor changed the text: %q", m.Value())
		}
		if got := m.CursorOffset(); got != c.want {
			t.Errorf("SetValueCursor(%d): CursorOffset() = %d, want %d", c.in, got, c.want)
		}
	}
	m.SetValueCursor("", 3)
	if got := m.CursorOffset(); got != 0 {
		t.Errorf("empty buffer: CursorOffset() = %d, want 0", got)
	}
}
