package tui

import (
	"reflect"
	"testing"
)

func TestDefaultEditorBindings(t *testing.T) {
	d := DefaultEditorBindings()
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"Submit", d.Submit, []string{"enter"}},
		{"NewLine", d.NewLine, []string{"shift+enter", "ctrl+j"}},
		{"WordLeft", d.WordLeft, []string{"alt+left", "ctrl+left", "alt+b"}},
		{"WordRight", d.WordRight, []string{"alt+right", "ctrl+right", "alt+f"}},
		{"DeleteWord", d.DeleteWord, []string{"ctrl+w", "alt+backspace"}},
		{"KillLine", d.KillLine, []string{"ctrl+k"}},
		{"Yank", d.Yank, []string{"ctrl+y"}},
		{"Undo", d.Undo, []string{"ctrl+-"}},
		{"LineStart", d.LineStart, []string{"home", "ctrl+home", "ctrl+a"}},
		{"LineEnd", d.LineEnd, []string{"end", "ctrl+end", "ctrl+e"}},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if d.HistoryPrevious != nil {
		t.Errorf("HistoryPrevious = %v, want unbound", d.HistoryPrevious)
	}
	if d.HistoryNext != nil {
		t.Errorf("HistoryNext = %v, want unbound", d.HistoryNext)
	}
}

func TestApplyOverridesReplacesTheWholeKeyList(t *testing.T) {
	defaults := DefaultEditorBindings()
	got, conflicts := ApplyOverrides(defaults, map[string]string{
		ActionSubmit: "ctrl+s",
	})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	if !reflect.DeepEqual(got.Submit, []string{"ctrl+s"}) {
		t.Fatalf("Submit = %v, want [ctrl+s]", got.Submit)
	}
	// Untouched actions keep their defaults.
	if !reflect.DeepEqual(got.NewLine, defaults.NewLine) {
		t.Fatalf("NewLine = %v, want unchanged default %v", got.NewLine, defaults.NewLine)
	}
}

func TestApplyOverridesIgnoresUnrecognizedActions(t *testing.T) {
	defaults := DefaultEditorBindings()
	got, conflicts := ApplyOverrides(defaults, map[string]string{
		"tui.select.confirm": "ctrl+s", // a real pi-tui id, but not the editor's.
	})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", conflicts)
	}
	if !reflect.DeepEqual(got, defaults) {
		t.Fatalf("got = %+v, want defaults unchanged %+v", got, defaults)
	}
}

func TestApplyOverridesReportsAConflict(t *testing.T) {
	defaults := DefaultEditorBindings()
	_, conflicts := ApplyOverrides(defaults, map[string]string{
		ActionSubmit:   "ctrl+x",
		ActionKillLine: "ctrl+x",
	})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want exactly one", conflicts)
	}
	want := "ctrl+x is bound to " + joinAnd(sortedTwo(ActionKillLine, ActionSubmit))
	if conflicts[0] != want {
		t.Fatalf("conflict = %q, want %q", conflicts[0], want)
	}
}

func TestApplyOverridesConflictWordingWithThreeActions(t *testing.T) {
	defaults := DefaultEditorBindings()
	_, conflicts := ApplyOverrides(defaults, map[string]string{
		ActionSubmit:   "ctrl+x",
		ActionKillLine: "ctrl+x",
		ActionUndo:     "ctrl+x",
	})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v, want exactly one", conflicts)
	}
	want := "ctrl+x is bound to " + joinAnd([]string{ActionKillLine, ActionUndo, ActionSubmit})
	if conflicts[0] != want {
		t.Fatalf("conflict = %q, want %q", conflicts[0], want)
	}
}

func TestApplyOverridesNeverResolvesAConflict(t *testing.T) {
	// Conflicts are reported, never resolved: both actions keep the
	// colliding key rather than one silently winning.
	defaults := DefaultEditorBindings()
	got, conflicts := ApplyOverrides(defaults, map[string]string{
		ActionSubmit:   "ctrl+x",
		ActionKillLine: "ctrl+x",
	})
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v", conflicts)
	}
	if !reflect.DeepEqual(got.Submit, []string{"ctrl+x"}) || !reflect.DeepEqual(got.KillLine, []string{"ctrl+x"}) {
		t.Fatalf("Submit = %v, KillLine = %v, want both set to ctrl+x", got.Submit, got.KillLine)
	}
}

// sortedTwo returns [a, b] sorted lexically, for building the expected
// message without duplicating joinAnd's sort behavior inline.
func sortedTwo(a, b string) []string {
	if a < b {
		return []string{a, b}
	}
	return []string{b, a}
}
