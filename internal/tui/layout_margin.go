package tui

import "strings"

// The 2-column side margin (finding no-side-margin): every block currently
// starts at column 0 against the terminal edge, where the design pads the
// whole body 20px left/right (Terminal.dc.html:26 transcript "padding:18px
// 20px 10px", :125 busy line "padding:4px 20px", :128/:132 palette/input
// "margin:0 20px", :142 status row "padding:7px 20px 10px"; the banner rows
// at :12/:18 are the mockup's own OS-chrome title bar, not something kiln
// draws itself, so they are not part of this). At 14px Fira Code
// (~8.4px/col) 20px is ~2 columns, which is where contentMarginCols below
// comes from.
//
// This file defines the margin once — the width it comes out of
// (ContentWidth, mirrored by Model.contentWidth) and the one line-padding
// helper (padMargin) — rather than each block reinventing its own indent.
// Every renderer already wraps/fits its content to contentWidth()
// (app.go), so redefining that one function to already exclude the margin
// gets every wrap width and every hairline/label rule (ruleWidth(),
// transcript.go) drawn at content width instead of the full terminal for
// free; padMargin is then the one place that actually shifts rendered rows
// right by the margin, applied at exactly the boundaries content leaves
// this package for the terminal:
//
//   - the live region (app.go's liveTail/chromeLines, covering the busy
//     line, the palette, the input box, the status row, dialogs and the
//     permission/plan prompt — everything below the committed transcript)
//   - the committed transcript (app.go's m.commit/m.commitSynthetic and
//     the note.go/bridge.go call sites listed in their own doc comments)
//   - the banner (app.go's Model.bannerRows)
//   - the frozen plan/subagents panel (live_freeze.go's newFreezeHook,
//     the one commit path that runs off the Update goroutine and so
//     cannot call an app.go helper)
//
// A narrow terminal (below marginMinWidth) drops the margin to 0 instead of
// crowding wrapped content further — the design has no equivalent case to
// match, so this port picks its own threshold and documents it here rather
// than in a scattered per-block comment.
const (
	contentMarginCols = 2
	marginMinWidth    = 40
)

// marginFor returns the margin columns budgeted from a raw terminal width
// (m.width, or bannerContentWidth's read of the real terminal before the
// program starts) — 0 below marginMinWidth, contentMarginCols otherwise.
func marginFor(width int) int {
	if width > 0 && width < marginMinWidth {
		return 0
	}
	return contentMarginCols
}

// ContentWidth is Model.contentWidth's pure form, usable without a Model:
// cli/tui.go's bannerContentWidth pre-fits the banner's row 1 to this same
// width so app.go's later re-fit (FitStatus, at the real contentWidth())
// doesn't have to truncate a second time.
func ContentWidth(width int) int {
	if width <= 0 {
		return 80
	}
	w := width - 2*marginFor(width)
	if w < 1 {
		w = 1
	}
	return w
}

// padMargin prepends cols spaces to every line — the left half of the
// margin; the right half is already accounted for by every caller wrapping
// its content to ContentWidth(rawWidth) rather than rawWidth itself, so a
// hairline rule or a raised block's own right-padding stops cols columns
// short of the real terminal edge without a trailing pad here too. cols<=0
// is a no-op (the narrow-terminal case), returning lines unchanged rather
// than a copy, so callers that don't need the copy (most of them, since the
// margin is >0 on any terminal wide enough to matter) don't pay for one.
func padMargin(lines []string, cols int) []string {
	if cols <= 0 {
		return lines
	}
	pad := strings.Repeat(" ", cols)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pad + l
	}
	return out
}
