package uv

import (
	"bytes"
	"testing"
)

// TestRendererFullscreenGrowTracksEveryRow: entering the alternate screen
// from an inline frame shorter than the screen must leave the model equal to
// the frame just drawn, including the rows past the old inline height. They
// had no line in the model to be written into, so the model stayed blank
// over cells the terminal showed, and the next frame skipped clearing them
// (a stray panel row in kiln after Ctrl+F). See HARNESS-PATCH.md.
func TestRendererFullscreenGrowTracksEveryRow(t *testing.T) {
	var buf bytes.Buffer
	r := NewTerminalRenderer(&buf, []string{"TERM=xterm-256color"})
	r.SetRelativeCursor(true)
	small := NewRenderBuffer(20, 4)
	small.SetCell(0, 0, &Cell{Content: "a", Width: 1})
	r.Render(small)

	r.SaveCursor()
	r.SetFullscreen(true)
	r.SetRelativeCursor(false)
	r.Erase()
	big := NewRenderBuffer(20, 10)
	big.SetCell(0, 7, &Cell{Content: "X", Width: 1})
	r.Render(big)

	if got := r.curbuf.Line(7).At(0); got == nil || got.Content != "X" {
		t.Fatalf("model row 7 col 0 = %v, want X", got)
	}
}
