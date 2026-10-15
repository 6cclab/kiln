package tui

// Rendering a mod's element tree (mods.Element) to terminal rows.
//
// This is a small, deliberately simplified flexbox: enough to draw what
// Andre's mods (squad, savvy-progress) actually build — rows and columns of
// Box/Text/Button/Raster, padding, gaps, a border, justify/align basics —
// not a full CSS/Yoga implementation. Gaps from the full spec are called out
// in each function's doc comment rather than silently approximated.
//
// Every renderer here takes an `avail` column budget and returns a modBox
// whose lines are already clipped/padded to its own width, and whose
// buttons carry positions relative to the box's own top-left corner (row 0,
// col 0) — composing two boxes is then just offsetting the child's button
// coordinates by where it landed, which is what layoutRow/layoutColumn and
// renderBox's padding/border/margin wrapping do as they go. Nothing here
// panics: malformed or missing props fall back to a default via the
// propXxx helpers (a nil Props map reads back zero values in Go, so a nil
// map never needs a special case), and every dimension that could come from
// an untrusted mod (width, columns, rows, gap, padding...) is clamped
// before it reaches strings.Repeat or a slice allocation.
import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/andrepato/harness/internal/mods"
)

// unboundedTextAvail is the width budget used to measure an element whose
// real available width is not known yet (a non-text child found inside a
// Text's children, which the mod API does not really allow but which must
// not panic here) — bounded, unlike a true "unbounded" measurement, so a
// pathological tree cannot force a huge allocation.
const unboundedTextAvail = 4096

// ModRendered is RenderModTree's result.
type ModRendered struct {
	// Lines are the rendered rows, ANSI-styled, each at most `width` visible
	// columns wide (TestModRender_NeverWiderThanWidth asserts this).
	Lines []string
	// Buttons are every Button found in the tree, with absolute row/col
	// coordinates into Lines.
	Buttons []ModButton
}

// ModButton is one Button element's rendered position, for hit-testing a
// key press or a click against RenderModTree's Lines.
type ModButton struct {
	Callback mods.Callback
	// Hotkey is the button's normalized hotkey ("" if it has none or the
	// prop was not one digit or one lowercase letter — normalizeHotkey).
	Hotkey          string
	Row, Col, Width int
	Label           string
}

// modBox is one rendered element: lines already at `width` visible columns
// (padded or clipped — every renderXxx function below returns lines of
// exactly this width, never wider), and this element's buttons with
// coordinates relative to its own [0,0].
type modBox struct {
	lines   []string
	width   int
	buttons []ModButton
}

// RenderModTree renders el to fit within width columns.
func RenderModTree(el mods.Element, width int) ModRendered {
	if width < 0 {
		width = 0
	}
	b := renderNode(el, width)
	lines := make([]string, len(b.lines))
	for i, l := range b.lines {
		lines[i] = clipWidth(l, width)
	}
	return ModRendered{Lines: lines, Buttons: b.buttons}
}

// renderNode dispatches by element type, tolerating an unknown type by
// rendering its children stacked in a column (the "tolerate unknown
// element types by rendering their children" requirement).
func renderNode(el mods.Element, avail int) modBox {
	if avail < 0 {
		avail = 0
	}
	switch el.Type {
	case "Box":
		return renderBox(el, avail)
	case "Text", "":
		return renderText(el, avail)
	case "Button":
		return renderButton(el, avail)
	case "Raster":
		return renderRaster(el, avail)
	case "Image":
		return renderImage(el, avail)
	case "Link":
		return renderLink(el, avail)
	case "Markdown":
		return renderMarkdown(el, avail)
	default:
		return renderChildrenColumn(el.Children, avail)
	}
}

// ---- Box ----

// borderChars is one borderStyle's glyph set: corners, horizontal and
// vertical fill.
type borderChars struct {
	tl, tr, bl, br, h, v string
}

// borderStyleChars maps a BoxProps.borderStyle name to its glyphs, or nil
// for "" or a name this version does not know (tolerated: no border is
// drawn rather than refusing the tree).
func borderStyleChars(name string) *borderChars {
	switch name {
	case "round":
		return &borderChars{"╭", "╮", "╰", "╯", "─", "│"}
	case "single":
		return &borderChars{"┌", "┐", "└", "┘", "─", "│"}
	case "double":
		return &borderChars{"╔", "╗", "╚", "╝", "═", "║"}
	case "bold":
		return &borderChars{"┏", "┓", "┗", "┛", "━", "┃"}
	case "classic":
		return &borderChars{"+", "+", "+", "+", "-", "|"}
	default:
		return nil
	}
}

// renderBox lays out a Box's children (flexDirection row/column, Ink's own
// default — a Box stacks its children vertically unless flexDirection:
// "row" is set, unlike CSS's row default) then wraps the result in its own
// padding, border, background and margin, in that order outward — matching
// a CSS border-box: an explicit width/height is the box's own outer size,
// not just its content.
//
// Not implemented (documented rather than guessed at): position
// (absolute/hover), minWidth/minHeight, flexShrink/flexWrap, alignSelf,
// overflow, borderDimColor, and justifyContent/alignItems on a column
// Box's main/cross axis (column children always stretch to the content
// width; justify only applies to a row Box's horizontal main axis, which is
// the only axis squad/savvy-progress actually justify).
func renderBox(el mods.Element, avail int) modBox {
	props := el.Props
	if avail < 0 {
		avail = 0
	}

	if propStringDefault(props, "display", "flex") == "none" {
		return modBox{}
	}

	marginTop := clampSpace(spacing(props, "marginTop", "marginY", "margin"))
	marginBottom := clampSpace(spacing(props, "marginBottom", "marginY", "margin"))
	marginLeft := clampSpace(spacing(props, "marginLeft", "marginX", "margin"))
	marginRight := clampSpace(spacing(props, "marginRight", "marginX", "margin"))

	paddingTop := clampSpace(spacing(props, "paddingTop", "paddingY", "padding"))
	paddingBottom := clampSpace(spacing(props, "paddingBottom", "paddingY", "padding"))
	paddingLeft := clampSpace(spacing(props, "paddingLeft", "paddingX", "padding"))
	paddingRight := clampSpace(spacing(props, "paddingRight", "paddingX", "padding"))

	bch := borderStyleChars(propStringDefault(props, "borderStyle", ""))
	borderW := 0
	if bch != nil {
		borderW = 1
	}

	outerAvail := max(0, avail-marginLeft-marginRight)
	outerWidth := outerAvail
	if wv, ok := resolveDimension(props, "width", outerAvail); ok {
		outerWidth = clamp(wv, 0, outerAvail)
	}

	contentAvail := max(0, outerWidth-2*borderW-paddingLeft-paddingRight)

	direction := propStringDefault(props, "flexDirection", "column")
	isRow := strings.HasPrefix(direction, "row")
	reverse := strings.HasSuffix(direction, "-reverse")
	children := el.Children
	if reverse {
		children = reverseElements(children)
	}

	gap := clampSpace(propIntDefault(props, "gap", 0))
	colGap := clampSpace(propIntDefault(props, "columnGap", gap))
	rowGap := clampSpace(propIntDefault(props, "rowGap", gap))

	var contentLines []string
	var buttons []ModButton
	if isRow {
		contentLines, buttons, _ = layoutRow(children, contentAvail, colGap, props)
	} else {
		contentLines, buttons, _ = layoutColumn(children, contentAvail, rowGap)
	}

	if hv, ok := resolveHeight(props, "height"); ok {
		target := max(0, hv-2*borderW-paddingTop-paddingBottom)
		contentLines = fitHeight(contentLines, target, contentAvail)
	}
	for i, l := range contentLines {
		contentLines[i] = padOrClip(l, contentAvail)
	}

	blankContent := strings.Repeat(" ", contentAvail)
	padded := make([]string, 0, len(contentLines)+paddingTop+paddingBottom)
	for i := 0; i < paddingTop; i++ {
		padded = append(padded, blankContent)
	}
	padded = append(padded, contentLines...)
	for i := 0; i < paddingBottom; i++ {
		padded = append(padded, blankContent)
	}
	for i := range buttons {
		buttons[i].Row += paddingTop
		buttons[i].Col += paddingLeft
	}
	for i, l := range padded {
		padded[i] = strings.Repeat(" ", paddingLeft) + l + strings.Repeat(" ", paddingRight)
	}
	innerWidth := contentAvail + paddingLeft + paddingRight

	if bg := propStringDefault(props, "backgroundColor", ""); bg != "" {
		if st := bgColorStyle(bg); st != nil {
			for i, l := range padded {
				padded[i] = st(padOrClip(l, innerWidth))
			}
		}
	}

	lines := padded
	width := innerWidth
	if bch != nil {
		bStyle := fgColorStyle(propStringDefault(props, "borderColor", ""))
		if bStyle == nil {
			bStyle = func(s string) string { return s }
		}
		top := bStyle(bch.tl + strings.Repeat(bch.h, innerWidth) + bch.tr)
		bottom := bStyle(bch.bl + strings.Repeat(bch.h, innerWidth) + bch.br)
		side := bStyle(bch.v)
		out := make([]string, 0, len(padded)+2)
		out = append(out, top)
		for _, l := range padded {
			out = append(out, side+l+side)
		}
		out = append(out, bottom)
		lines = out
		width = innerWidth + 2
		for i := range buttons {
			buttons[i].Row++
			buttons[i].Col++
		}
	}
	for i := range lines {
		lines[i] = padOrClip(lines[i], width)
	}

	blankOuter := strings.Repeat(" ", width)
	final := make([]string, 0, len(lines)+marginTop+marginBottom)
	for i := 0; i < marginTop; i++ {
		final = append(final, blankOuter)
	}
	final = append(final, lines...)
	for i := 0; i < marginBottom; i++ {
		final = append(final, blankOuter)
	}
	for i := range buttons {
		buttons[i].Row += marginTop
		buttons[i].Col += marginLeft
	}
	for i, l := range final {
		final[i] = strings.Repeat(" ", marginLeft) + l + strings.Repeat(" ", marginRight)
	}

	return modBox{lines: final, width: width + marginLeft + marginRight, buttons: buttons}
}

// layoutColumn stacks children vertically, each stretched to the full
// `avail` width (Ink's default cross-axis stretch), separated by `gap`
// blank rows.
func layoutColumn(children []mods.Element, avail, gap int) ([]string, []ModButton, int) {
	if len(children) == 0 {
		return nil, nil, avail
	}
	var lines []string
	var buttons []ModButton
	row := 0
	for i, c := range children {
		b := renderNode(c, avail)
		for _, bt := range b.buttons {
			bt.Row += row
			buttons = append(buttons, bt)
		}
		for _, l := range b.lines {
			lines = append(lines, padOrClip(l, avail))
		}
		row += len(b.lines)
		if i < len(children)-1 && gap > 0 {
			blank := strings.Repeat(" ", avail)
			for g := 0; g < gap; g++ {
				lines = append(lines, blank)
			}
			row += gap
		}
	}
	return lines, buttons, avail
}

// layoutRow lays children out horizontally: a child with no flexGrow is
// measured at its natural (shrink-to-fit) width, capped by what is left of
// `avail` after earlier siblings; the children with flexGrow then share
// whatever width is left over, proportionally. Unused width (flexGrow == 0
// for every child) is handled by justifyContent (flex-start/center/
// flex-end/space-between/space-around/space-evenly); alignItems
// (flex-start/center/flex-end) positions each child vertically within the
// row's height, which is the tallest child.
func layoutRow(children []mods.Element, avail, gap int, boxProps map[string]any) ([]string, []ModButton, int) {
	n := len(children)
	if n == 0 {
		return nil, nil, avail
	}
	gaps := make([]int, max(0, n-1))
	for i := range gaps {
		gaps[i] = gap
	}
	remaining := max(0, avail-gap*len(gaps))

	grows := make([]float64, n)
	growTotal := 0.0
	for i, c := range children {
		g := propFloatDefault(c.Props, "flexGrow", 0)
		if g > 0 {
			grows[i] = g
			growTotal += g
		}
	}

	boxes := make([]modBox, n)
	widths := make([]int, n)
	usedFixed := 0
	for i, c := range children {
		if grows[i] > 0 {
			continue
		}
		a := max(0, remaining-usedFixed)
		b := renderNode(c, a)
		w := min(b.width, a)
		boxes[i] = b
		widths[i] = w
		usedFixed += w
	}
	leftover := max(0, remaining-usedFixed)

	if growTotal > 0 {
		var growIdx []int
		for i := range children {
			if grows[i] > 0 {
				growIdx = append(growIdx, i)
			}
		}
		dist := 0
		for k, i := range growIdx {
			var w int
			if k == len(growIdx)-1 {
				w = leftover - dist
			} else {
				w = int(float64(leftover) * grows[i] / growTotal)
			}
			w = max(0, w)
			dist += w
			boxes[i] = renderNode(children[i], w)
			widths[i] = w
		}
		leftover = 0
	}

	leadPad := 0
	if leftover > 0 {
		switch propStringDefault(boxProps, "justifyContent", "flex-start") {
		case "center":
			leadPad = leftover / 2
		case "flex-end":
			leadPad = leftover
		case "space-between":
			if n > 1 {
				extra, rem := leftover/(n-1), leftover%(n-1)
				for i := range gaps {
					gaps[i] += extra
					if i < rem {
						gaps[i]++
					}
				}
			} else {
				leadPad = leftover
			}
		case "space-around", "space-evenly":
			unit := leftover / (n + 1)
			leadPad = unit
			for i := range gaps {
				gaps[i] += unit
			}
		}
	}

	alignItems := propStringDefault(boxProps, "alignItems", "flex-start")
	height := 1
	for i := range boxes {
		if h := len(boxes[i].lines); h > height {
			height = h
		}
	}

	rows := make([]string, height)
	for r := range rows {
		rows[r] = strings.Repeat(" ", leadPad)
	}
	var buttons []ModButton
	col := leadPad

	for i := range children {
		w := widths[i]
		bl := boxes[i].lines
		voffset := 0
		switch alignItems {
		case "center":
			voffset = (height - len(bl)) / 2
		case "flex-end":
			voffset = height - len(bl)
		}
		voffset = max(0, voffset)
		for r := 0; r < height; r++ {
			seg := ""
			if br := r - voffset; br >= 0 && br < len(bl) {
				seg = bl[br]
			}
			rows[r] += padOrClip(seg, w)
		}
		for _, bt := range boxes[i].buttons {
			bt.Row += voffset
			bt.Col += col
			buttons = append(buttons, bt)
		}
		col += w
		if i < n-1 {
			g := gaps[i]
			for r := range rows {
				rows[r] += strings.Repeat(" ", g)
			}
			col += g
		}
	}
	return rows, buttons, col
}

// fitHeight clips lines to target rows (scrolling to the top — the extra
// rows at the bottom are dropped) or pads with blank rows to reach it.
func fitHeight(lines []string, target, width int) []string {
	target = max(0, target)
	if len(lines) >= target {
		return lines[:target]
	}
	out := append([]string{}, lines...)
	blank := strings.Repeat(" ", max(0, width))
	for len(out) < target {
		out = append(out, blank)
	}
	return out
}

func reverseElements(els []mods.Element) []mods.Element {
	out := make([]mods.Element, len(els))
	for i, e := range els {
		out[len(els)-1-i] = e
	}
	return out
}

// ---- Text ----

// renderText renders a Text element (or a bare text node, Type == ""): its
// own text plus any Text/text-node children, concatenated into one styled
// string (textRunString), then wrapped or truncated per its `wrap` prop.
//
// Nested Text styling is not inherited outward or inward — each Text's
// style applies only to its own direct text — which differs from Ink,
// where an unset style on a nested Text can inherit from its parent.
func renderText(el mods.Element, avail int) modBox {
	styled := textRunString(el)
	wrap := propStringDefault(el.Props, "wrap", "wrap")
	var lines []string
	switch wrap {
	case "truncate", "end", "truncate-end":
		lines = []string{ansi.Truncate(styled, max(avail, 0), "…")}
	case "truncate-start":
		lines = []string{modTruncateStart(styled, avail)}
	case "truncate-middle", "middle":
		lines = []string{modTruncateMiddle(styled, avail)}
	default: // "wrap"
		if avail <= 0 {
			lines = nil
		} else {
			lines = splitLines(ansi.Wrap(styled, avail, ""))
		}
	}
	width := 0
	for _, l := range lines {
		if vw := VisibleWidth(l); vw > width {
			width = vw
		}
	}
	return modBox{lines: lines, width: min(width, avail)}
}

// textRunString concatenates el's own text and its text-node/Text children
// into one ANSI-styled string, each run styled by its *own* element's props
// (color, backgroundColor, bold, italic, underline, strikethrough, inverse,
// dimColor). A child that is neither a text node nor a Text (not valid mod
// API input, but something this must not panic on) is rendered on its own
// and its first line spliced in, tolerating it rather than refusing the
// tree.
func textRunString(el mods.Element) string {
	var b strings.Builder
	if el.Text != "" {
		b.WriteString(applyTextStyle(el.Props, el.Text))
	}
	for _, c := range el.Children {
		switch c.Type {
		case "":
			b.WriteString(applyTextStyle(c.Props, c.Text))
		case "Text":
			b.WriteString(textRunString(c))
		default:
			cb := renderNode(c, unboundedTextAvail)
			if len(cb.lines) > 0 {
				b.WriteString(cb.lines[0])
			}
		}
	}
	return b.String()
}

// applyTextStyle renders text through a lipgloss style built from props,
// or returns it unchanged if no style prop applies or styling is disabled
// (plain mode / NO_COLOR — see theme.go's `enabled`).
func applyTextStyle(props map[string]any, text string) string {
	if text == "" || !enabled {
		return text
	}
	st := lipgloss.NewStyle()
	any := false
	if hex, ok := resolveColor(propStringDefault(props, "color", "")); ok {
		st = st.Foreground(lipgloss.Color(hex))
		any = true
	}
	if hex, ok := resolveColor(propStringDefault(props, "backgroundColor", "")); ok {
		st = st.Background(lipgloss.Color(hex))
		any = true
	}
	if propBool(props, "bold") {
		st = st.Bold(true)
		any = true
	}
	if propBool(props, "italic") {
		st = st.Italic(true)
		any = true
	}
	if propBool(props, "underline") {
		st = st.Underline(true)
		any = true
	}
	if propBool(props, "strikethrough") {
		st = st.Strikethrough(true)
		any = true
	}
	if propBool(props, "inverse") {
		st = st.Reverse(true)
		any = true
	}
	if propBool(props, "dimColor") {
		st = st.Faint(true)
		any = true
	}
	if !any {
		return text
	}
	return st.Render(text)
}

func modTruncateStart(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := VisibleWidth(s)
	if sw <= w {
		return s
	}
	const ell = "…"
	keep := max(0, w-VisibleWidth(ell))
	return ansi.TruncateLeft(s, sw-keep, ell)
}

func modTruncateMiddle(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := VisibleWidth(s)
	if sw <= w {
		return s
	}
	const ell = "…"
	remain := max(0, w-VisibleWidth(ell))
	leftW := (remain + 1) / 2
	rightW := remain - leftW
	left := ansi.Cut(s, 0, leftW)
	right := ansi.Cut(s, sw-rightW, sw)
	return left + ell + right
}

// clipWidth truncates s to at most w visible columns without an ellipsis
// (used where the mod API's own semantics clip silently — a Raster/Image
// wider than its site, or the outer RenderModTree clip — as opposed to
// Text's wrap modes, which choose their own tail marker).
func clipWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if VisibleWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "")
}

// padOrClip pads s with trailing spaces to exactly w visible columns, or
// clips it if it is already wider — every box composes its children with
// this so a line is never wider than the width it was allotted.
func padOrClip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	vw := VisibleWidth(s)
	if vw == w {
		return s
	}
	if vw < w {
		return s + strings.Repeat(" ", w-vw)
	}
	return ansi.Truncate(s, w, "")
}

// ---- Button ----

// normalizeHotkey keeps hotkey only if it is one digit or one lowercase
// letter (ButtonProps.hotkey's contract); anything else is dropped rather
// than refusing the tree.
func normalizeHotkey(s string) string {
	if len(s) != 1 {
		return ""
	}
	c := s[0]
	if c >= '0' && c <= '9' {
		return s
	}
	if c >= 'a' && c <= 'z' {
		return s
	}
	return ""
}

// renderButton draws "[ label ]" normally, or (ButtonProps.plain) the
// hotkey, a colon and the label with no chrome, and records the button's
// position/label/callback for the caller's hit-testing.
func renderButton(el mods.Element, avail int) modBox {
	props := el.Props
	label := propStringDefault(props, "label", "")
	if label == "" {
		if len(el.Children) == 1 && el.Children[0].Type == "" {
			label = el.Children[0].Text
		} else if el.Text != "" {
			label = el.Text
		} else {
			label = textPlain(el)
		}
	}
	hotkey := normalizeHotkey(propStringDefault(props, "hotkey", ""))
	dim := propBool(props, "dimColor")

	var display string
	if propBool(props, "plain") {
		if hotkey != "" {
			display = hotkey + ": " + label
		} else {
			display = label
		}
	} else {
		display = "[ " + label + " ]"
	}
	display = clipWidth(display, avail)
	width := VisibleWidth(display)

	styled := display
	if enabled && dim {
		styled = lipgloss.NewStyle().Faint(true).Render(display)
	}

	var cb mods.Callback
	if c, ok := props["onPress"].(mods.Callback); ok {
		cb = c
	}

	return modBox{
		lines: []string{styled},
		width: width,
		buttons: []ModButton{{
			Callback: cb,
			Hotkey:   hotkey,
			Row:      0,
			Col:      0,
			Width:    width,
			Label:    label,
		}},
	}
}

// textPlain concatenates el's text and every descendant's text, with no
// styling — the fallback label source for a Button whose single child or
// own text is not a plain string, and for anything else that just needs
// the tree's plain text.
func textPlain(el mods.Element) string {
	var b strings.Builder
	b.WriteString(el.Text)
	for _, c := range el.Children {
		b.WriteString(textPlain(c))
	}
	return b.String()
}

// ---- Raster ----

// rasterDefaultColor is the bit RasterProps.cells reserves for "the
// terminal's default colour" (0x01000000) — such a cell's fg/bg is left
// unstyled rather than painted black.
const rasterDefaultColor = 0x01000000

// renderRaster decodes RasterProps.cells (standard padded base64 of
// columns*rows little-endian u32 [codePoint, fg, bg] triplets) into a grid
// of styled glyphs. Malformed input (wrong base64, too few bytes for the
// declared columns*rows) renders as an empty box rather than panicking.
func renderRaster(el mods.Element, avail int) modBox {
	props := el.Props
	cols := clamp(propIntDefault(props, "columns", 0), 0, 512)
	rows := clamp(propIntDefault(props, "rows", 0), 0, 256)
	if cols == 0 || rows == 0 {
		return modBox{}
	}
	raw, err := base64.StdEncoding.DecodeString(propStringDefault(props, "cells", ""))
	if err != nil || len(raw) < cols*rows*12 {
		return modBox{}
	}
	width := min(cols, avail)
	lines := make([]string, rows)
	for r := 0; r < rows; r++ {
		var sb strings.Builder
		for c := 0; c < cols; c++ {
			off := (r*cols + c) * 12
			cp := binary.LittleEndian.Uint32(raw[off : off+4])
			fg := binary.LittleEndian.Uint32(raw[off+4 : off+8])
			bg := binary.LittleEndian.Uint32(raw[off+8 : off+12])
			glyph := string(rune(cp))
			if enabled {
				st := lipgloss.NewStyle()
				any := false
				if fg != rasterDefaultColor {
					st = st.Foreground(lipgloss.Color(rasterHex(fg)))
					any = true
				}
				if bg != rasterDefaultColor {
					st = st.Background(lipgloss.Color(rasterHex(bg)))
					any = true
				}
				if any {
					glyph = st.Render(glyph)
				}
			}
			sb.WriteString(glyph)
		}
		lines[r] = clipWidth(sb.String(), width)
	}
	return modBox{lines: lines, width: width}
}

// rasterHex formats a RasterProps colour (0x00RRGGBB) as "#rrggbb".
func rasterHex(v uint32) string {
	return fmt.Sprintf("#%06x", v&0x00ffffff)
}

// ---- Image ----

// renderImage draws ImageProps.alt (dim) — kiln has no terminal graphics
// protocol in v1, so an Image always falls back to its alt text, which the
// mod API requires every Image to have.
func renderImage(el mods.Element, avail int) modBox {
	alt := propStringDefault(el.Props, "alt", "")
	if alt == "" {
		alt = " "
	}
	styled := alt
	if enabled {
		styled = Dim(alt)
	}
	line := clipWidth(styled, avail)
	return modBox{lines: []string{line}, width: VisibleWidth(line)}
}

// ---- Link ----

// renderLink draws its label (or its one text child, or href if neither is
// set), underlined. kiln has no OSC 8 hyperlink helper yet (grepped for
// "\x1b]8" — only stream_wrap_test.go's fixture string uses that escape, no
// production helper emits it), so href is not wired up as a clickable
// hyperlink; this is the "else the label" fallback the brief calls for.
func renderLink(el mods.Element, avail int) modBox {
	label := propStringDefault(el.Props, "label", "")
	if label == "" {
		label = textPlain(el)
	}
	if label == "" {
		label = propStringDefault(el.Props, "href", "")
	}
	styled := label
	if enabled {
		styled = lipgloss.NewStyle().Underline(true).Render(label)
	}
	line := clipWidth(styled, avail)
	return modBox{lines: []string{line}, width: VisibleWidth(line)}
}

// ---- Markdown ----

// renderMarkdown reuses kiln's own markdown renderer (markdown.go), the
// same one an assistant reply's text goes through, rather than a second
// implementation.
func renderMarkdown(el mods.Element, avail int) modBox {
	if avail <= 0 {
		return modBox{}
	}
	text := propStringDefault(el.Props, "text", "")
	r := NewMarkdownRenderer(avail, !enabled)
	lines := r.Render(text)
	width := 0
	for i, l := range lines {
		lines[i] = clipWidth(l, avail)
		if vw := VisibleWidth(lines[i]); vw > width {
			width = vw
		}
	}
	return modBox{lines: lines, width: width}
}

// renderChildrenColumn is the fallback for an element type this version of
// kiln does not know: draw its children as if it were a column Box with no
// gap, rather than refusing the tree.
func renderChildrenColumn(children []mods.Element, avail int) modBox {
	lines, buttons, width := layoutColumn(children, avail, 0)
	return modBox{lines: lines, width: width, buttons: buttons}
}

// ---- prop helpers ----
//
// Every one of these tolerates a nil Props map (a plain Go nil-map read,
// never a panic), a missing key, or a value of the wrong JSON-decoded type
// (e.g. a bool where a number was expected) by falling back to its
// default/false/zero — the "malformed props by ignoring them" requirement.

func propString(props map[string]any, key string) (string, bool) {
	v, ok := props[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func propStringDefault(props map[string]any, key, def string) string {
	if s, ok := propString(props, key); ok {
		return s
	}
	return def
}

func propBool(props map[string]any, key string) bool {
	v, ok := props[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// clampInt bounds a JSON number to a range comfortably inside what any
// slice allocation or strings.Repeat in this file will do with it — the
// "huge numbers" fuzz case.
func clampInt(f float64) int {
	if f != f { // NaN
		return 0
	}
	const bound = 1_000_000
	if f > bound {
		return bound
	}
	if f < -bound {
		return -bound
	}
	return int(f)
}

func propInt(props map[string]any, key string) (int, bool) {
	v, ok := props[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return clampInt(n), true
	case int:
		return clampInt(float64(n)), true
	}
	return 0, false
}

func propIntDefault(props map[string]any, key string, def int) int {
	if v, ok := propInt(props, key); ok {
		return v
	}
	return def
}

func propFloatDefault(props map[string]any, key string, def float64) float64 {
	v, ok := props[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return def
}

// spacing reads the first of a BoxProps per-side/axis/all spacing prop
// family that is set ("marginTop", then "marginY", then "margin", for
// example), so an all-sides `margin` is the fallback for a side that was
// not overridden individually.
func spacing(props map[string]any, side, axis, all string) int {
	if v, ok := propInt(props, side); ok {
		return v
	}
	if v, ok := propInt(props, axis); ok {
		return v
	}
	return propIntDefault(props, all, 0)
}

// clampSpace bounds a padding/margin/gap value so it cannot blow up a
// strings.Repeat call even if a mod passes something absurd.
func clampSpace(v int) int {
	return clamp(v, 0, 300)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// resolveDimension reads a width/height-shaped prop: a number of columns
// (clamped to [0, avail]), or a percentage string ("100%", clamped to
// [0%, 1000%] of avail) — BoxProps.width/height's "number or 100%" shape. A
// plain (non-percent) string, or anything else, is not a dimension this
// renders and returns ok == false.
func resolveDimension(props map[string]any, key string, avail int) (int, bool) {
	v, ok := props[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return clamp(int(n), 0, max(0, avail)), true
	case int:
		return clamp(n, 0, max(0, avail)), true
	case string:
		s := strings.TrimSpace(n)
		if !strings.HasSuffix(s, "%") {
			return 0, false
		}
		pct, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			return 0, false
		}
		pct = clampFloat(pct, 0, 1000)
		return clamp(int(float64(avail)*pct/100.0), 0, max(0, avail)), true
	}
	return 0, false
}

// resolveHeight reads a height prop as a plain row count: only a number is
// supported (unlike width, a percentage has no vertical budget to be a
// percentage of in this layout engine — there is no incoming row
// constraint to measure against — so a percentage height is left
// unimplemented rather than guessed at).
func resolveHeight(props map[string]any, key string) (int, bool) {
	v, ok := propInt(props, key)
	if !ok {
		return 0, false
	}
	return clamp(v, 0, 2000), true
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ---- colour ----

// namedColors maps the CSS/Ink colour names mods commonly pass (beyond a
// literal "#rrggbb") to a hex value. Not exhaustive — an unrecognised name
// is simply not styled (resolveColor returns ok == false), which is safer
// than guessing a colour the mod did not ask for.
var namedColors = map[string]string{
	"black":   "#000000",
	"red":     "#e06c75",
	"green":   "#98c379",
	"yellow":  "#e5c07b",
	"blue":    "#61afef",
	"magenta": "#c678dd",
	"cyan":    "#56b6c2",
	"white":   "#ffffff",
	"gray":    "#808080",
	"grey":    "#808080",

	"blackBright":   "#5c6370",
	"redBright":     "#ff6b6b",
	"greenBright":   "#a0e6a0",
	"yellowBright":  "#ffe08a",
	"blueBright":    "#8ab4f8",
	"magentaBright": "#e6a0e6",
	"cyanBright":    "#a0e6e6",
	"whiteBright":   "#ffffff",
}

// resolveColor accepts a literal "#rrggbb" hex or one of namedColors; any
// other string (an unknown name, a malformed hex) is not a colour this
// renders.
func resolveColor(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if strings.HasPrefix(s, "#") {
		return s, true
	}
	if hex, ok := namedColors[s]; ok {
		return hex, true
	}
	return "", false
}

func fgColorStyle(s string) func(string) string {
	hex, ok := resolveColor(s)
	if !ok || !enabled {
		return nil
	}
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
	return func(t string) string { return st.Render(t) }
}

func bgColorStyle(s string) func(string) string {
	hex, ok := resolveColor(s)
	if !ok || !enabled {
		return nil
	}
	st := lipgloss.NewStyle().Background(lipgloss.Color(hex))
	return func(t string) string { return st.Render(t) }
}
