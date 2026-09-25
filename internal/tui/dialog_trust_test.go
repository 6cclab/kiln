package tui

import (
	"strings"
	"testing"
)

// TestTrustDialog_MatchesReferenceRows pins Render's exact rows for a
// known cwd at width 100, per the kiln design (docs/kiln-design.md):
// Trust draws its own full-width `─` rule as row 0 (unlike the
// numbered-option dialogs, which sit under the app-owned `▔` rule and are
// indented three), the wording is "kiln will be able to read, edit, and
// execute files here." (no "Security guide" row — the harness has no such
// doc to link), and the selected row's raised background pads it to the
// full width.
func TestTrustDialog_MatchesReferenceRows(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	cwd := "/private/var/folders/93/248j_5ds3ls8k4ggh_fxndjh0000gn/T/cc-ref/proj"
	d := NewTrustDialog(cwd, nil)
	got := d.Render(100, 40)

	want := []string{
		"────────────────────────────────────────────────────────────────────────────────────────────────────",
		" Accessing workspace:",
		" " + cwd,
		" Quick safety check: Is this a project you created or one you trust? (Like your own code, a",
		" well-known open source project, or work from your team). If not, take a moment to review what's in",
		" this folder first.",
		" kiln will be able to read, edit, and execute files here.",
		" ❯ No, exit",
		"   Yes, I trust this folder",
		" Enter to confirm · Esc to cancel",
	}

	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range want {
		row := got[i]
		if i == 7 { // the selected "No, exit" row carries the raised background
			row = strings.TrimRight(row, " ")
		}
		if row != want[i] {
			t.Errorf("row %d:\n got:  %q\n want: %q", i, row, want[i])
		}
	}
}

func TestTrustDialog_DefaultSelectionIsNoExit(t *testing.T) {
	d := NewTrustDialog("/tmp/proj", nil).(*trustDialog)
	if d.cursor != 0 {
		t.Errorf("default cursor = %d, want 0 (\"No, exit\")", d.cursor)
	}
}

func TestTrustDialog_EscAnswersFalse(t *testing.T) {
	var got *bool
	d := NewTrustDialog("/tmp/proj", func(trusted bool) { got = &trusted })
	consumed, closeIt, _ := d.HandleKey(keyMsg("esc"))
	if !consumed || !closeIt {
		t.Fatalf("esc: consumed=%v closeIt=%v", consumed, closeIt)
	}
	if got == nil || *got != false {
		t.Errorf("onAnswer got %v, want false", got)
	}
}

func TestTrustDialog_EnterAnswersCurrentSelection(t *testing.T) {
	var got *bool
	d := NewTrustDialog("/tmp/proj", func(trusted bool) { got = &trusted }).(*trustDialog)
	d.HandleKey(keyMsg("down")) // move to "Yes, I trust this folder"
	if d.cursor != 1 {
		t.Fatalf("cursor after down = %d, want 1", d.cursor)
	}
	consumed, closeIt, _ := d.HandleKey(keyMsg("enter"))
	if !consumed || !closeIt {
		t.Fatalf("enter: consumed=%v closeIt=%v", consumed, closeIt)
	}
	if got == nil || *got != true {
		t.Errorf("onAnswer got %v, want true", got)
	}
}

func TestTrustDialog_DigitKeysAnswerDirectly(t *testing.T) {
	var got *bool
	d := NewTrustDialog("/tmp/proj", func(trusted bool) { got = &trusted })
	consumed, closeIt, _ := d.HandleKey(keyMsg("2"))
	if !consumed || !closeIt {
		t.Fatalf("'2': consumed=%v closeIt=%v", consumed, closeIt)
	}
	if got == nil || *got != true {
		t.Errorf("'2' should answer trusted=true, got %v", got)
	}
}

func TestTrustDialog_RenderAt100And60(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)

	for _, width := range []int{100, 60} {
		d := NewTrustDialog("/some/reasonably/long/workspace/path/for/testing", nil)
		rows := d.Render(width, 40)
		if len(rows) == 0 {
			t.Fatalf("width %d: no rows rendered", width)
		}
		for i, r := range rows {
			if i == 0 {
				continue // the `─` rule is intentionally exactly width, not <=width-1
			}
			if VisibleWidth(r) > width {
				t.Errorf("width %d: row %d exceeds width: %q", width, i, r)
			}
		}
	}
}
