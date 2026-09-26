package tui

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// The status line — one row, ported from the kiln design handoff's "Status
// line" section (docs/kiln-design-handoff/README.md "Screen anatomy"):
//
//	● auto-edit  ⇧⇥                      ~/src/relay-api · main*   ctx ━━━━──────  38%  $0.42
//
// mode segment (dot + label + the mode-cycle key) on the left, cwd/branch
// next to it, a flexible spacer, then the context meter and cumulative
// spend on the right — a dashboard read left-to-right: what mode am I in,
// where am I, how much room and spend is left.

// GitStatus is the git segment of the status line.
type GitStatus struct {
	Branch string
	Dirty  bool
}

// StatusState is everything RenderStatusLine needs.
type StatusState struct {
	ModelLabel    string
	ContextWindow int
	// ContextUsed is nil when there is nothing to report yet — the meter
	// still renders, empty, at 0%.
	ContextUsed *int
	// Cost is cumulative spend, in dollars. Omitted entirely when zero.
	Cost float64
	Git  *GitStatus
	// Cwd is the working directory shown in the location segment,
	// home-abbreviated by the caller's choosing (RenderStatusLine does not
	// abbreviate it itself, so a caller that wants "~/…" passes it already
	// abbreviated — see AbbrevHome).
	Cwd string
	// Mode is the permission mode driving the dot/label colour and text.
	Mode string
	// Thinking is set while the model is reasoning. RenderStatusLine does
	// not use this itself (the busy line owns "thinking" now); kept on the
	// state because callers (MsgThinking) still set it and other code may
	// read it back via FooterState.State().
	Thinking bool
	// StartedAt is session start; RenderStatusLine no longer shows an
	// elapsed clock (kept for callers that still read it).
	StartedAt time.Time
	// Now is injected for tests; the zero value means "use time.Now()".
	Now time.Time
}

// meterWidthDefault is Meter's width when the caller does not specify one.
const meterWidthDefault = 6

// Meter renders a small bar using the active glyph table's fill/empty
// cells (G().MeterFull/MeterEmpty: "━"/"─", ASCII "="/"-" in plain mode) —
// unstyled; callers colour the filled and empty runs separately. Fractional
// fills round down, so a full bar means genuinely full.
func Meter(fraction float64, width ...int) string {
	w := meterWidthDefault
	if len(width) > 0 {
		w = width[0]
	}
	clamped := math.Max(0, math.Min(1, fraction))
	filled := int(math.Floor(clamped * float64(w)))
	gl := G()
	return strings.Repeat(gl.MeterFull, filled) + strings.Repeat(gl.MeterEmpty, w-filled)
}

// Compact renders 450k, 1.0m, 21.1k — the compact forms the meter sits
// next to.
func Compact(n int) string {
	if n >= 1_000_000 {
		m := float64(n) / 1_000_000
		if m >= 10 {
			return fmt.Sprintf("%.0fm", math.Round(m))
		}
		return fmt.Sprintf("%.1fm", m)
	}
	if n >= 1_000 {
		k := float64(n) / 1_000
		if k >= 100 {
			return fmt.Sprintf("%.0fk", math.Round(k))
		}
		if k >= 10 {
			return fmt.Sprintf("%.0fk", k)
		}
		return fmt.Sprintf("%.1fk", k)
	}
	return fmt.Sprintf("%d", n)
}

// Elapsed renders a duration the way the status line's session clock used
// to; kept for callers that still measure session age.
func Elapsed(d time.Duration) string {
	seconds := int(d / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours := minutes / 60
	if hours < 24 {
		if minutes%60 > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes%60)
		}
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dd", hours/24)
}

// modeLabel returns the status line's mode label text for the dot+label
// segment, and its colour — manual/default/ask before edits (dim), the
// auto-edit family (green), plan (blue). Unrecognised modes fall back to
// "ask before edits" dim, the safest default.
func modeLabel(mode string) (label string, colour func(string) string) {
	switch mode {
	case "acceptEdits", "auto":
		return "auto-edit", KilnGreen
	case "bypassPermissions":
		return "bypass permissions", KilnGreen
	case "dontAsk":
		return "don't ask", KilnGreen
	case "plan":
		return "plan only", KilnBlue
	default: // "manual", "", any unrecognised mode
		return "ask before edits", Muted
	}
}

// contextPressure colours the context meter's filled cells: amber below
// 70% used, red above — the point at which "start thinking about
// compaction" becomes true.
func contextPressure(fraction float64) func(string) string {
	if fraction > 0.70 {
		return KilnRed
	}
	return KilnAmber
}

// AbbrevHome replaces the user's home directory prefix in path with "~".
func AbbrevHome(path, home string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

// RenderStatusLine renders the one-row status line: mode segment, location
// segment, a flexible spacer, then the context meter and cost — fitted so
// the right side ends at width-1. When the row does not fit, segments drop
// in order: location first, then cost, then the row is truncated outright
// with FitStatus.
func RenderStatusLine(s StatusState, width int) string {
	p := IsPlain()

	// --- mode segment ---
	label, colour := modeLabel(s.Mode)
	dot := "●"
	shiftTab := "⇧⇥"
	if p {
		dot = "*"
		shiftTab = "shift+tab"
	}
	modeSeg := colour(dot) + " " + colour(label) + "  " + Muted(shiftTab)

	// --- location segment ---
	var locSeg string
	if s.Cwd != "" {
		locSeg = Muted(s.Cwd)
		if s.Git != nil {
			branch := s.Git.Branch
			if s.Git.Dirty {
				branch += "*"
			}
			locSeg += Muted(" · " + branch)
		}
	}

	// --- right side: ctx meter + cost ---
	fraction := 0.0
	percent := 0
	if s.ContextUsed != nil && s.ContextWindow > 0 {
		fraction = float64(*s.ContextUsed) / float64(s.ContextWindow)
		percent = int(math.Round(fraction * 100))
	}
	meter := Meter(fraction, 10)
	filledN := int(math.Floor(math.Max(0, math.Min(1, fraction)) * 10))
	// Meter emits filled cells then empty cells with no separator, so split
	// by rune count rather than byte offset since glyphs may be multi-byte.
	runes := []rune(meter)
	if filledN > len(runes) {
		filledN = len(runes)
	}
	filledRun := string(runes[:filledN])
	emptyRun := string(runes[filledN:])
	ctxSeg := "ctx " + contextPressure(fraction)(filledRun) + Rule(emptyRun) + Muted(fmt.Sprintf("  %d%%", percent))

	var costSeg string
	if s.Cost > 0 {
		costSeg = Muted(fmt.Sprintf("$%.2f", s.Cost))
	}

	build := func(includeLoc, includeCost bool) string {
		r := ctxSeg
		if includeCost && costSeg != "" {
			r += "  " + costSeg
		}
		left := modeSeg
		if includeLoc && locSeg != "" {
			left += "  " + locSeg
		}
		lw := VisibleWidth(left)
		rw := VisibleWidth(r)
		spacer := width - 1 - lw - rw
		if spacer < 1 {
			return ""
		}
		return left + strings.Repeat(" ", spacer) + r
	}

	if out := build(true, true); out != "" {
		return out
	}
	if out := build(false, true); out != "" {
		return out
	}
	if out := build(false, false); out != "" {
		return out
	}
	return FitStatus(modeSeg, width)
}
