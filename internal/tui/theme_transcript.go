package tui

import "charm.land/lipgloss/v2"

// Transcript-specific true-colour helpers.
//
// theme.go's seven ANSI colours (Red/Green/.../Gray) are indices, not fixed
// RGB — the terminal's own palette decides what they actually look like.
// Claude Code's transcript instead emits explicit 24-bit SGR codes, so
// parity here means matching hex values, not ANSI indices. Every constant
// below is a value read directly from an SGR escape in
// testdata/reference/claude-code/manual-session.rec (24-bit `38;2;r;g;b`
// codes), found by searching the raw byte capture for the element's text
// and reading the colour in force immediately before it. The byte offsets
// each one came from are noted so they can be re-derived.
//
// Not everything below is confirmed this way — see the two exceptions
// noted on TranscriptGreen (used for the "+" diff line by symmetry with
// the captured "-" line, not directly captured) and the elapsed-suffix
// dim tone (no captured instance in the .rec at all, chosen to match the
// package's existing Dim/Gray convention).
var (
	// TranscriptOrange (rgb 215,119,87 / #D77757): the animated spinner
	// glyphs ·✢✳✶✻✽ while a turn is running. Captured at multiple offsets,
	// e.g. `\x1b[38;2;215;119;87m✢` and `...✳`, `...✶` in
	// manual-session.rec.
	TranscriptOrange = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#D77757")))

	// TranscriptOrangeLight (rgb 230,149,117 / #E69575): the spinner's
	// label text ("Whirring…"), a lighter shade than the glyph itself.
	// Captured immediately after the glyph in the same escape run:
	// `\x1b[38;2;215;119;87m✢\x1b[3G\x1b[38;2;230;149;117mWhirring…`.
	TranscriptOrangeLight = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#E69575")))

	// TranscriptDim (rgb 153,153,153 / #999999): the dominant dim tone for
	// transcript chrome — the ⎿ result glyph and its row, grouped
	// read-only rows ("Reading 1 file…" / "Read 1 file"), and a settled
	// turn summary ("✻ Crunched for 4s · done 10:03 AM" — glyph and text
	// both this colour once the turn is done, distinct from the orange
	// used while it is still running). Captured repeatedly, e.g. before
	// "Kept model as", before "Reading", before "Crunched for 4s".
	TranscriptDim = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#999999")))

	// TranscriptDimBold is TranscriptDim's colour with Bold set, for the
	// numeric count inside an otherwise-dim row: confirmed in
	// turn-edit.styles.txt as `[fg=#999999]Read [/][fg=#999999
	// b]1[/][fg=#999999] file [/]` (the grouped-read row) and
	// `Added [b]1[/] line, removed [b]1[/] line` (the diff summary row,
	// where the count is bold but NOT coloured — default foreground,
	// unlike the grouped-read row's bold count, which IS #999999. Both
	// are covered here since the visual difference is small and both
	// pass as "the count reads as emphasized", but it is a real,
	// observed difference this helper does not distinguish).
	TranscriptDimBold = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#999999")).Bold(true))

	// TranscriptGreen (rgb 78,186,101 / #4EBA65): the settled/successful
	// tool-call marker ⏺ for Update/Read/Write once resolved. Captured
	// before `⏺\x1b[3G\x1b[39m\x1b[1mUpdate` and before `⏺\x1b[39m
	// \x1b[1mRead`. Also used for the "+" diff line by symmetry with the
	// captured "-" line's red/green pairing (section 8: "diff - red and +
	// green") — the "+" line's own SGR was not found in the capture (the
	// wrapped `return a + b` text did not match a single contiguous
	// string in the byte stream), so that half of the pairing is a
	// same-hue inference, not a direct read.
	TranscriptGreen = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#4EBA65")))

	// TranscriptRed (rgb 220,90,90 / #DC5A5A): the diff "-" line's
	// foreground. Captured before ` 1 -function add(a,b){ return a `:
	// `\x1b[38;2;220;90;90m\x1b[48;2;61;1;0m 1 -function...`. The capture
	// also carries a tinted background (rgb 61,1,0 / #3D0100) behind the
	// whole line, which is NOT implemented here — RenderDiffLines colours
	// only the foreground, matching how the rest of this package renders
	// (no background painting anywhere else in transcript.go); the
	// background tint is a real, observed gap, not an oversight.
	TranscriptRed = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#DC5A5A")))

	// TranscriptUserMark (#505050): the "❯" marker on a user/command echo
	// row, distinct from TranscriptDim. Confirmed directly against
	// testdata/reference/claude-code/turn-edit.styles.txt (harness-drive
	// --styles), which landed in the tree after the .rec archaeology
	// above and is exact rather than inferred:
	// `[fg=#505050 bg=#373737]❯[/]`. The row also carries a #373737
	// background tint (echoed text itself is #ffffff on that same tint),
	// which is NOT implemented — see TranscriptRed's note on the same
	// gap for diff rows; no background painting exists anywhere in this
	// package.
	TranscriptUserMark = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#505050")))

	// TranscriptDiffAdded (#50C850): the "+" diff row's number+sign
	// prefix — NOT the same green as TranscriptGreen (#4EBA65), confirmed
	// side by side in the same styles dump: the settled ⏺ marker for
	// Update/Read is #4eba65, the diff "+" prefix is #50c850.
	TranscriptDiffAdded = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#50C850")))

	// TranscriptDiffText (#F8F8F2): a diff row's own content, after the
	// number+sign prefix — off-white, NOT the same red/green as the
	// prefix. Claude Code further applies full chroma syntax highlighting
	// to the added line's content (visible in the styles dump as
	// per-token colours within the "+" row) and a single-character
	// highlight marking the changed word within both rows; neither is
	// implemented here — RenderDiffLines renders the whole content run
	// in this one flat colour, which is a real, observed gap, not an
	// oversight.
	TranscriptDiffText = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#F8F8F2")))

	// TranscriptWhite (rgb 255,255,255 / #FFFFFF): the user echo's own
	// text and the "⏺" marker Claude Code uses for assistant prose
	// (`⏺ Done.`) — plain foreground, not colour-coded like a tool
	// marker. Captured before `⏺\x1b[3G\x1b[39mDone.` (`\x1b[38;2;255;255;255m⏺`)
	// and before the echoed prompt text in several frames (though the
	// same string also appears in dim (153,153,153) in one frame, and
	// always inside the input box's own `48;2;55;55;55` background tint —
	// see the report for what that ambiguity is and is not resolved).
	TranscriptWhite = style(lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")))
)
