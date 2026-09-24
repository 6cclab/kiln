package tui

import (
	"fmt"
	"sort"
)

// Editor action ids, matching pi-tui's TUI_KEYBINDINGS ids (the vocabulary
// ~/.claude/keybindings.json is written against). Only the actions the Go
// editor implements are listed; pi-tui also defines ids for its select and
// alt-screen widgets, which this port does not (yet) expose for override.
const (
	ActionSubmit          = "tui.input.submit"
	ActionNewLine         = "tui.input.newLine"
	ActionHistoryPrevious = "tui.editor.historyPrevious"
	ActionHistoryNext     = "tui.editor.historyNext"
	ActionWordLeft        = "tui.editor.cursorWordLeft"
	ActionWordRight       = "tui.editor.cursorWordRight"
	ActionDeleteWord      = "tui.editor.deleteWordBackward"
	ActionKillLine        = "tui.editor.deleteToLineEnd"
	ActionYank            = "tui.editor.yank"
	ActionUndo            = "tui.editor.undo"
	ActionLineStart       = "tui.editor.cursorLineStart"
	ActionLineEnd         = "tui.editor.cursorLineEnd"
)

// EditorBindings holds, for each editor action the Go port implements, the
// keys that trigger it. A binding can have more than one key (e.g. word-left
// defaults to alt+left, ctrl+left and alt+b at once); an override from
// ~/.claude/keybindings.json replaces the whole list with its one key,
// exactly as pi-tui's KeybindingsManager does (normalizeKeys of a single
// user string is a one-element list).
type EditorBindings struct {
	Submit          []string
	NewLine         []string
	HistoryPrevious []string
	HistoryNext     []string
	WordLeft        []string
	WordRight       []string
	DeleteWord      []string
	KillLine        []string
	Yank            []string
	Undo            []string
	LineStart       []string
	LineEnd         []string
}

// DefaultEditorBindings returns the defaults, ported verbatim from pi-tui's
// TUI_KEYBINDINGS (dist/keybindings.js) for the ids this editor implements.
func DefaultEditorBindings() EditorBindings {
	return EditorBindings{
		Submit:          []string{"enter"},
		NewLine:         []string{"shift+enter", "ctrl+j"},
		HistoryPrevious: nil, // pi-tui ships this unbound by default.
		HistoryNext:     nil, // pi-tui ships this unbound by default.
		WordLeft:        []string{"alt+left", "ctrl+left", "alt+b"},
		WordRight:       []string{"alt+right", "ctrl+right", "alt+f"},
		DeleteWord:      []string{"ctrl+w", "alt+backspace"},
		KillLine:        []string{"ctrl+k"},
		Yank:            []string{"ctrl+y"},
		Undo:            []string{"ctrl+-"},
		LineStart:       []string{"home", "ctrl+home", "ctrl+a"},
		LineEnd:         []string{"end", "ctrl+end", "ctrl+e"},
	}
}

// field returns a pointer to the EditorBindings field for a given action id,
// or nil if the id is not one this editor recognizes. A pointer lets
// ApplyOverrides both read the default and write the override through one
// lookup.
func (b *EditorBindings) field(action string) *[]string {
	switch action {
	case ActionSubmit:
		return &b.Submit
	case ActionNewLine:
		return &b.NewLine
	case ActionHistoryPrevious:
		return &b.HistoryPrevious
	case ActionHistoryNext:
		return &b.HistoryNext
	case ActionWordLeft:
		return &b.WordLeft
	case ActionWordRight:
		return &b.WordRight
	case ActionDeleteWord:
		return &b.DeleteWord
	case ActionKillLine:
		return &b.KillLine
	case ActionYank:
		return &b.Yank
	case ActionUndo:
		return &b.Undo
	case ActionLineStart:
		return &b.LineStart
	case ActionLineEnd:
		return &b.LineEnd
	default:
		return nil
	}
}

// ApplyOverrides applies ~/.claude/keybindings.json data (loaded by
// internal/claude/keybindings as action -> key) to the editor's defaults.
//
// This mirrors src/claude/keybindings.ts, which builds a pi-tui
// KeybindingsManager over TUI_KEYBINDINGS and the user's file: unrecognized
// action ids are ignored (a user file may bind ids for widgets — pi-tui's
// select and alt-screen bindings — that this port's global key router owns
// instead of the editor, and those are never touched here), a recognized id
// replaces its default key list with the single user key, and two
// recognized ids bound to the same key are a conflict, reported and never
// resolved: a user who bound two actions to the same key made a mistake
// only they can settle.
//
// Deviation from the TS: pi-tui's KeybindingsManager detects conflicts
// across its whole action vocabulary (editor, input, select, alt-screen).
// This port only implements the editor's subset of ids, so conflicts are
// only detected among those; a user file that collides two non-editor ids,
// or an editor id with a non-editor id, is not this function's concern.
func ApplyOverrides(defaults EditorBindings, bindings map[string]string) (EditorBindings, []string) {
	result := defaults

	byKey := map[string][]string{}
	for action, key := range bindings {
		field := result.field(action)
		if field == nil {
			continue
		}
		*field = []string{key}
		byKey[key] = append(byKey[key], action)
	}

	var conflicts []string
	keys := make([]string, 0, len(byKey))
	for key, actions := range byKey {
		if len(actions) > 1 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		actions := append([]string(nil), byKey[key]...)
		sort.Strings(actions)
		conflicts = append(conflicts, fmt.Sprintf("%s is bound to %s", key, joinAnd(actions)))
	}

	return result, conflicts
}

// joinAnd matches src/claude/keybindings.ts's wording exactly:
// `${key} is bound to ${keybindings.join(" and ")}`. JS Array.join(" and ")
// puts " and " between every pair, so three items render as "a and b and
// c", not "a, b and c" — the same rendering internal/claude/keybindings.go's
// joinAnd already uses for file-level conflicts.
func joinAnd(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for _, item := range items[1:] {
		out += " and " + item
	}
	return out
}
