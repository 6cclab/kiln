package tui

// The shared band above the prompt: every mod with an AbovePrompt hook gets
// one Site (BandSite) and may draw a one-line strip there (squad's
// register.tsx AbovePrompt hook: a spinner, a status line, an "open"
// Button). Several mods drawing at once are stacked one row per mod,
// joined onto one row only when they all fit together.
import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/andrepato/harness/internal/mods"
)

// BandSite is the Site every mod's AbovePrompt hook renders for.
func BandSite(width int) mods.Site {
	return mods.Site{
		Component:   "AbovePrompt",
		Surface:     "terminal",
		BodyColumns: max(0, width),
		BodyRows:    1,
	}
}

// RenderModBand renders every mod's AbovePrompt drawing: one row per mod
// that actually drew something there (Drawing.Tree == nil means it called
// next or has no hook for this site, so it contributes nothing), each
// clipped to width. When every drawing's natural width fits on one line
// together (separated by two spaces, the same gap the design's own
// multi-segment status rows use), they join onto that single row instead
// of stacking — so two or three small mod strips (a spinner's "squad · 2
// running", a token meter) do not each cost their own row.
func RenderModBand(drawings []mods.Drawing, width int) []string {
	if width <= 0 {
		return nil
	}
	var rows []string
	for _, d := range drawings {
		if d.Tree == nil {
			continue
		}
		rendered := RenderModTree(*d.Tree, width)
		for _, l := range rendered.Lines {
			if strings.TrimSpace(ansi.Strip(l)) == "" {
				continue
			}
			rows = append(rows, l)
		}
	}
	if len(rows) <= 1 {
		return rows
	}

	const sep = "  "
	joined := rows[0]
	fits := true
	for _, r := range rows[1:] {
		candidate := joined + sep + r
		if VisibleWidth(candidate) > width {
			fits = false
			break
		}
		joined = candidate
	}
	if fits {
		return []string{clipWidth(joined, width)}
	}
	for i, r := range rows {
		rows[i] = clipWidth(r, width)
	}
	return rows
}
