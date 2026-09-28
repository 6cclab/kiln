package tui

import (
	"strings"
	"testing"
)

// TestRenderShortcutsAlignsColumns: every row's second column starts at
// the same cell, glyph keys (⇧⇥, ⏎) included.
func TestRenderShortcutsAlignsColumns(t *testing.T) {
	rows := RenderShortcuts(120)
	col := -1
	for _, r := range rows {
		plain := stripANSI(r)
		var key string
		for _, s := range shortcutColumns[1] {
			if strings.Contains(plain, s.key+" ") {
				key = s.key
			}
		}
		if key == "" {
			continue
		}
		at := VisibleWidth(plain[:strings.Index(plain, key)])
		if col == -1 {
			col = at
		} else if at != col {
			t.Errorf("second column at cell %d, want %d:\n%s", at, col, strings.Join(rows, "\n"))
		}
	}
	if col < 0 {
		t.Fatal("no second-column keys found")
	}
}

// TestSlashPaletteHintKeepsDescriptionColumn: a command's argument hint
// sits in the name column, so its description starts where every other
// command's does.
func TestSlashPaletteHintKeepsDescriptionColumn(t *testing.T) {
	plain := func(it AutocompleteItem) string {
		return stripANSI(renderSlashCommandItem(it, false, 120, slashColumnFor([]AutocompleteItem{{Value: "agents"}, {Value: "add-dir", Hint: "<path>"}}), func(s string) string { return s }, Muted)[0])
	}
	a := plain(AutocompleteItem{Value: "agents", Description: "Manage subagents"})
	b := plain(AutocompleteItem{Value: "add-dir", Hint: "<path>", Description: "Allow tools to work in another directory"})
	if !strings.Contains(b, "/add-dir <path>") {
		t.Fatalf("hint not beside the name: %q", b)
	}
	if strings.Index(a, "Manage") != strings.Index(b, "Allow") {
		t.Errorf("descriptions start at different columns:\n%q\n%q", a, b)
	}
}
