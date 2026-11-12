package editor

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// freshRows is what View must return: a render that never sees the cache.
func freshRows(m Model, width int) []string { return m.render(width) }

// TestViewCacheHit pins the mechanism: View twice with nothing changed
// renders once.
func TestViewCacheHit(t *testing.T) {
	m := New(testStyles())
	m.Focus()
	m.SetValue("hello")
	first := m.View(40)
	second := m.View(40)
	if m.cache.renders != 1 {
		t.Fatalf("render ran %d times for two identical View calls, want 1", m.cache.renders)
	}
	if !slices.Equal(first, second) {
		t.Fatal("cached rows differ from the first render")
	}
	// A copy of the Model (bubbletea passes it by value) shares the cache.
	cp := m
	cp.View(40)
	if m.cache.renders != 1 {
		t.Fatalf("a copy re-rendered: %d renders", m.cache.renders)
	}
}

// TestViewCacheInvalidation: after each kind of change the rows equal a
// fresh render and the cache re-rendered.
func TestViewCacheInvalidation(t *testing.T) {
	big := func(m Model) Model { m.SetValue(strings.Repeat("word ", 40)); return m }
	steps := []struct {
		name  string
		width int
		do    func(m *Model)
		start func(Model) Model
	}{
		{"typing", 40, func(m *Model) { *m, _, _ = m.Update(key('x')) }, nil},
		{"cursor move", 40, func(m *Model) { *m, _, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft}) }, func(m Model) Model { m.SetValue("abc"); return m }},
		{"scroll (cursor up through many lines)", 40, func(m *Model) {
			for range 12 {
				*m, _, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			}
		}, func(m Model) Model { m.SetValue(strings.Repeat("line\n", 20)); return m }},
		// The app resizes through SetWidth, then View at the same width.
		{"resize", 20, func(m *Model) { m.SetWidth(20) }, big},
		{"placeholder", 40, func(m *Model) { m.SetPlaceholder("something else") }, nil},
		{"suggestion", 40, func(m *Model) { m.SetSuggestion("ghost") }, nil},
		{"blur", 40, func(m *Model) { m.Blur() }, func(m Model) Model { m.SetValue("abc"); return m }},
		{"theme", 40, func(m *Model) {
			s := testStyles()
			s.Marker = ">"
			s.MarkerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000"))
			s.Rule = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00"))
			m.SetStyles(s)
		}, nil},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			m := New(testStyles())
			m.Focus()
			if st.start != nil {
				m = st.start(m)
			}
			m.View(40) // warm the cache at the pre-change state
			before := m.cache.renders
			st.do(&m)
			got := m.View(st.width)
			want := freshRows(m, st.width)
			if !slices.Equal(got, want) {
				t.Fatalf("stale rows after %s:\n got %q\nwant %q", st.name, got, want)
			}
			if m.cache.renders == before {
				t.Fatalf("%s did not invalidate the cache", st.name)
			}
		})
	}
}
