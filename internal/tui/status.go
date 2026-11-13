package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/money"
	"github.com/andrepato/harness/internal/plural"
)

// The status line — one row, ported from the kiln design handoff's "Status
// line" section (docs/kiln-design-handoff/README.md "Screen anatomy"):
//
//	● auto-edit  ⇧⇥  ~/src/relay-api · main*  kiln-large      ctx ━━━━──────  38%  $0.42
//
// mode segment (dot + label + the mode-cycle key) on the left, cwd/branch
// and the active model next to it, a flexible spacer, then the context
// meter and cumulative spend on the right — a dashboard read left-to-right:
// what mode am I in, where am I, which model, how much room and spend is
// left.

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
	// Shells is the number of background shells (bash_background) still
	// running. Zero hides the indicator; >0 shows "⟳ N shells" in amber,
	// the running colour, between the model and the context meter.
	Shells int
	// Thinking is set while the model is reasoning. RenderStatusLine does
	// not use this itself (the busy line owns "thinking" now); kept on the
	// state because callers (MsgThinking) still set it and other code may
	// read it back via FooterState.State().
	Thinking bool
	// RoutedModel/RoutedLevel report the latest EventRoute for a virtual
	// model (kiln/auto): RoutedModel is the physical model id the last
	// request actually went to, RoutedLevel its (possibly clamped)
	// thinking level, "" when the router reported none. Both are ""
	// before the first routed request, so a physical-model session's
	// render is untouched.
	RoutedModel string
	RoutedLevel string
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
// unstyled; callers colour the filled and empty runs separately. The fill
// is meterFilled's.
func Meter(fraction float64, width ...int) string {
	w := meterWidthDefault
	if len(width) > 0 {
		w = width[0]
	}
	filled := meterFilled(fraction, w)
	gl := G()
	return strings.Repeat(gl.MeterFull, filled) + strings.Repeat(gl.MeterEmpty, w-filled)
}

// meterFilled is how many of w cells a fraction fills: rounded down, but
// any use at all shows at least one cell (the design's meter shows a
// sliver at 9%; rounding down left a 10-cell bar empty below 10%, so a
// session at 4% looked unused), and only a genuinely full context fills
// every cell. The sliver starts where the label reads 1%, so a bar never
// shows use beside "0%".
func meterFilled(fraction float64, w int) int {
	clamped := math.Max(0, math.Min(1, fraction))
	filled := int(math.Floor(clamped * float64(w)))
	if clamped >= 0.005 && filled == 0 {
		filled = 1
	}
	if clamped < 1 && filled == w && w > 1 {
		filled = w - 1
	}
	return filled
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
// segment, and its colour — manual/default/ask before edits (dim),
// acceptEdits/auto (green, but with distinct labels — see below), plan
// (blue). Unrecognised modes fall back to "ask before edits" dim, the
// safest default.
//
// acceptEdits and auto are both green but are NOT the same mode
// (settings.Decide, internal/claude/settings/settings.go): acceptEdits
// only auto-allows edit/write and read-only calls, asking for everything
// else, while auto is a blanket allow for every call not already denied —
// the same "auto mode" the Bash/plan permission prompts already offer to
// switch into (internal/tui/permission_render.go's "use auto mode").
// So they get distinct labels: "auto-edit" for acceptEdits, "auto mode"
// for auto, reusing that established name rather than inventing a new one.
func modeLabel(mode string) (label string, colour func(string) string) {
	switch mode {
	case "acceptEdits":
		return "auto-edit", KilnGreen
	case "auto":
		return "auto mode", KilnGreen
	case "bypassPermissions":
		// Red: every call runs unchecked, and the mode line is where that
		// has to be impossible to miss.
		return "bypass permissions", KilnRed
	case "dontAsk":
		return "don't ask", KilnGreen
	case "plan":
		return "plan only", KilnBlue
	default: // "manual", "", any unrecognised mode
		return "ask before edits", Muted
	}
}

// PermissionLabel is the footer's name for a permission mode ("manual" is
// "ask before edits"), so /context names the mode the way the footer does.
func PermissionLabel(mode string) string {
	label, _ := modeLabel(mode)
	return label
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

// ShortenPathLeft fits path into maxWidth by dropping leading path
// components (left-truncation), keeping the trailing, most specific
// components and marking the cut with a leading "…" — e.g.
// ShortenPathLeft("/private/tmp/scratchpad/real-proj", 22) is
// "…/scratchpad/real-proj". Used for the status line's location segment and
// the startup banner's cwd, both of which would rather lose distant
// ancestors than the branch/model that follow them on the same row.
//
// Home-dir abbreviation ("~/…") is the caller's job (AbbrevHome) and happens
// before this; ShortenPathLeft treats "~" as an ordinary leading component,
// so it is the first thing dropped once even the last path segment needs
// room.
func ShortenPathLeft(path string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if VisibleWidth(path) <= maxWidth {
		return path
	}
	if maxWidth == 1 {
		return "…"
	}
	parts := strings.Split(path, "/")
	kept := ""
	for i := len(parts) - 1; i >= 0; i-- {
		next := parts[i]
		if kept != "" {
			next = parts[i] + "/" + kept
		}
		if VisibleWidth("…/"+next) <= maxWidth {
			kept = next
		} else {
			break
		}
	}
	if kept != "" {
		return "…/" + kept
	}
	// Not even the last component fits alongside "…/" — hard-clip its tail
	// end, keeping the most specific (rightmost) characters.
	last := parts[len(parts)-1]
	room := maxWidth - VisibleWidth("…")
	r := []rune(last)
	if room >= len(r) {
		return "…" + last
	}
	if room <= 0 {
		return "…"
	}
	return "…" + string(r[len(r)-room:])
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
// segment, model, a flexible spacer, then the context meter and cost —
// fitted so the right side ends at width-1. When the row does not fit, the
// cwd shortens first, then segments drop in a fixed, monotonic order (the
// list at the end of this function): cost, the ⇧⇥ hint, the folder, the
// branch, then the narrow forms of the model and context meter; past that
// the row is truncated outright with FitStatus. Monotonic means never
// recombining: two widths one column apart show a consistent, predictable
// set of segments.
// design-studio: kiln / Terminal {"scene":"shells","layout":"ruled"}
// design-studio: kiln / Terminal {"scene":"routed","layout":"ruled"}
// design-studio: kiln / Terminal {"scene":"bypass","layout":"ruled"}
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
	// branchSuffix is fixed text that must survive alongside the cwd (per
	// the design, the branch is never optional once there is a cwd to show
	// it next to) — the cwd is what gets shortened when the row is tight,
	// never the branch.
	branchSuffix := ""
	if s.Git != nil {
		branch := s.Git.Branch
		if s.Git.Dirty {
			branch += "*"
		}
		branchSuffix = " · " + branch
	}
	// locSegAt renders the location segment with the cwd fitted to at most
	// cwdWidth columns (left-truncated with a leading "…", so the trailing,
	// most specific path components survive) — "" only when the cwd cannot
	// even be shown as an ellipsis.
	locSegAt := func(cwdWidth int) string {
		if s.Cwd == "" {
			return ""
		}
		cwd := s.Cwd
		if VisibleWidth(cwd) > cwdWidth {
			cwd = ShortenPathLeft(cwd, cwdWidth)
			// A cut inside the folder's own name ("…ay-api", "…01") names
			// nothing: drop the folder for the next step instead.
			base := s.Cwd[strings.LastIndex(s.Cwd, "/")+1:]
			if !strings.HasPrefix(cwd, "…/") && cwd != "…"+base {
				return ""
			}
		}
		if cwd == "" {
			return ""
		}
		// Two separate Muted() calls (not one Muted(cwd+branchSuffix)) to
		// match the pre-existing golden output's ANSI run boundaries, which
		// style the cwd and the " · branch" suffix as adjacent same-colour
		// spans rather than one merged span.
		out := Muted(cwd)
		if branchSuffix != "" {
			out += Muted(branchSuffix)
		}
		return out
	}

	// --- right side: ctx meter + cost ---
	fraction := 0.0
	percent := 0
	if s.ContextUsed != nil && s.ContextWindow > 0 {
		fraction = float64(*s.ContextUsed) / float64(s.ContextWindow)
		percent = int(math.Round(fraction * 100))
	}
	meter := Meter(fraction, 10)
	filledN := meterFilled(fraction, 10)
	// Meter emits filled cells then empty cells with no separator, so split
	// by rune count rather than byte offset since glyphs may be multi-byte.
	runes := []rune(meter)
	if filledN > len(runes) {
		filledN = len(runes)
	}
	filledRun := string(runes[:filledN])
	emptyRun := string(runes[filledN:])
	ctxSeg := "ctx " + contextPressure(fraction)(filledRun) + Rule(emptyRun) + Muted(fmt.Sprintf("  %d%%", percent))

	// Always shown, including $0.00 (design scene 01) — cost is part of the
	// dashboard even before anything has been spent, not hidden until it
	// is nonzero.
	costSeg := Muted(money.USD(s.Cost))

	// build tries, in order: the full cwd, a left-shortened cwd sized to
	// whatever room is left once the mode segment, branch and right side
	// are accounted for, then no location at all — location only drops
	// once even a minimal shortened form cannot fit (defect: a long cwd
	// used to blank the whole segment, branch included, instead of
	// shrinking the path first).
	// --- model segment ---
	// The active model, in ink after the location (design "Status line":
	// `<span style="color:#ece4d4">kiln-large</span>`), the same label the
	// banner's "· model <m>" shows. It outlives cost when the row is tight
	// (after a /model switch, which model is answering matters more than the
	// running spend) but gives way before cwd · branch.
	// routeSuffix renders " → <model>[ • <level>]" for the full model
	// segment, Muted on the arrow and separator, Ink on the names ("->"
	// in plain mode, per the design's Action glyph).
	arrow := "→"
	if p {
		arrow = "->"
	}
	levelSuffix := func() string {
		if s.RoutedLevel == "" {
			return ""
		}
		return Muted(" • ") + Muted(s.RoutedLevel)
	}
	modelSeg := ""
	if s.ModelLabel != "" {
		modelSeg = Ink(s.ModelLabel)
		if s.RoutedModel != "" {
			modelSeg += Muted(" "+arrow+" ") + Ink(s.RoutedModel) + levelSuffix()
		}
	}
	// modelSegCollapsed is just the routed model id, for a row too tight
	// for the full "auto → model" segment.
	modelSegCollapsed := modelSeg
	if s.RoutedModel != "" {
		modelSegCollapsed = Ink(s.RoutedModel) + levelSuffix()
	}

	// --- background-shell segment ---
	// "⟳ N shells" in amber (the running colour, same as the subagents
	// panel), shown only while at least one background shell is running. It
	// sits with the session state on the left, after the model and before
	// the flexible spacer; it drops after cost but before the model when the
	// row is tight (see the drop order below).
	shellsSeg := ""
	if s.Shells > 0 {
		shellsSeg = KilnAmber(G().Shells + " " + plural.Count(s.Shells, "shell"))
	}

	// The narrow forms: the mode without its shortcut hint (the shortcuts
	// panel still lists it), the context as "ctx N%" without its bar, and
	// the model without its provider prefix.
	modeSegBare := colour(dot) + " " + colour(label)
	ctxSegCompact := "ctx " + Muted(fmt.Sprintf("%d%%", percent))
	shortModel := func(m string) string {
		if i := strings.LastIndex(m, "/"); i >= 0 && i < len(m)-1 {
			return m[i+1:]
		}
		return m
	}
	modelShort := ""
	if name := s.RoutedModel; name != "" || s.ModelLabel != "" {
		if name == "" {
			name = s.ModelLabel
		}
		modelShort = shortModel(name)
	}

	type footer struct {
		cost, shells, hint, folder, branch, meter bool
		model                                     string // plain label; "" drops it
		styled                                    string // the model segment as drawn, when set
	}
	build := func(f footer) string {
		r := ctxSegCompact
		if f.meter {
			r = ctxSeg
		}
		if f.cost {
			r += "  " + costSeg
		}
		rw := VisibleWidth(r)
		model := ""
		if f.styled != "" {
			model = "  " + f.styled
		} else if f.model != "" {
			model = "  " + Ink(f.model)
		}
		shells := ""
		if f.shells && shellsSeg != "" {
			shells = "  " + shellsSeg
		}
		mode := modeSegBare
		if f.hint {
			mode = modeSeg
		}
		lwMode := VisibleWidth(mode) + VisibleWidth(model) + VisibleWidth(shells)

		tryLoc := func(cwdWidth int) string {
			left := mode
			switch {
			case f.folder && s.Cwd != "":
				loc := locSegAt(cwdWidth)
				if loc == "" {
					return ""
				}
				left += "  " + loc
			case f.branch && s.Git != nil:
				left += "  " + Muted(strings.TrimPrefix(branchSuffix, " · "))
			}
			left += model + shells
			spacer := width - 1 - VisibleWidth(left) - rw
			if spacer < 1 {
				return ""
			}
			return left + strings.Repeat(" ", spacer) + r
		}

		if !f.folder || s.Cwd == "" {
			return tryLoc(0)
		}
		if out := tryLoc(VisibleWidth(s.Cwd)); out != "" {
			return out
		}
		available := width - 1 - lwMode - 2 - VisibleWidth(branchSuffix) - rw - 1
		if available >= 1 {
			return tryLoc(available)
		}
		return ""
	}

	// Drop order as the row narrows, fixed and monotonic: shorten the cwd
	// (inside build) first, then drop cost, then the shells indicator, then
	// the ⇧⇥ hint, then the folder (the branch stays), then the branch,
	// then collapse "auto → model" to the routed model, then the context
	// bar (the percentage stays), then the provider prefix of the model,
	// and last cut the model itself. Mode, model and context percentage
	// are what a narrow window keeps: which model answers and how full the
	// context is matter more than where the session runs. Each step is
	// strictly narrower than the last, so two widths one column apart
	// never show a different combination of segments (qa/findings/
	// 20261004T204953Z-narrow-footer-and-panel-clipping.json). When no
	// shell runs, shellsSeg is "" and its step changes nothing.
	for _, f := range []footer{
		{cost: true, shells: true, hint: true, folder: true, branch: true, meter: true, styled: modelSeg},
		{shells: true, hint: true, folder: true, branch: true, meter: true, styled: modelSeg},
		{hint: true, folder: true, branch: true, meter: true, styled: modelSeg},
		{folder: true, branch: true, meter: true, styled: modelSeg},
		{branch: true, meter: true, styled: modelSeg},
		{meter: true, styled: modelSeg},
		{meter: true, styled: modelSegCollapsed},
		{styled: modelSegCollapsed},
		{model: modelShort},
	} {
		if out := build(f); out != "" {
			return out
		}
	}
	// The model, cut to whatever room the mode and "ctx N%" leave.
	if room := width - 1 - VisibleWidth(modeSegBare) - 2 - 2 - VisibleWidth(ctxSegCompact); modelShort != "" && room >= 4 {
		if out := build(footer{model: FitStatus(modelShort, room)}); out != "" {
			return out
		}
	}
	if out := build(footer{}); out != "" {
		return out
	}
	return FitStatus(modeSegBare, width)
}
