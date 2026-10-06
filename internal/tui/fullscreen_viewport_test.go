package tui

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// TestFullscreenViewport_MatchesFullContent holds the windowed fullscreen
// viewport to the direct one (a viewport given the whole transcript plus
// tail) across transcript and tail sizes, rows wider than the screen,
// styled and empty rows, every scroll position and viewport height.
func TestFullscreenViewport_MatchesFullContent(t *testing.T) {
	withRenderEnv(t, 80)
	rowKinds := []func(i int) string{
		func(i int) string { return fmt.Sprintf("row %d", i) },
		func(i int) string { return "" },
		func(i int) string { return strings.Repeat("w", 70+i%30) }, // some wider than 80
		func(i int) string { return KilnAmber(fmt.Sprintf("styled %d 日本語", i)) },
		func(i int) string { return strings.Repeat("日", 41) },
		func(i int) string { return "   " },
	}
	rng := rand.New(rand.NewSource(7))
	row := func(i int, wide bool) string {
		k := rng.Intn(len(rowKinds))
		if !wide && (k == 2 || k == 4) {
			k = 0
		}
		return rowKinds[k](i)
	}
	cases := 0
	for _, wide := range []bool{false, true} {
		for _, transcriptRows := range []int{0, 1, 3, 25, 60} {
			for _, tailRows := range []int{0, 1, 4, 30} {
				for _, height := range []int{1, 2, 10, 39} {
					m := newTestModel()
					m.width, m.height = 80, height+5
					m.fullscreen = true
					var rows []string
					for i := 0; i < transcriptRows; i++ {
						rows = append(rows, row(i, wide))
					}
					if len(rows) > 0 {
						m = m.appendTranscript(rows)
					}
					m.viewport.SetWidth(80)
					m.viewport.SetHeight(height)
					m.viewport.GotoBottom()
					var tail []string
					for i := 0; i < tailRows; i++ {
						tail = append(tail, row(1000+i, wide))
					}
					if tailRows == 4 {
						tail[1] = "two\nlines" // SetContent splits a tail row on "\n"
					}
					// Every scroll position, from the bottom (following) up
					// past the top.
					for up := 0; up <= transcriptRows+2; up++ {
						vm := m
						vm.viewport.ScrollUp(up)
						got, gotTop := vm.fullscreenViewport(append([]string(nil), tail...))
						want, wantTop := vm.fullscreenViewportFull(append([]string(nil), tail...))
						cases++
						if gotTop != wantTop || !slices.Equal(got, want) {
							t.Fatalf("wide=%v transcript=%d tail=%d height=%d up=%d:\n got  top=%d %q\n want top=%d %q",
								wide, transcriptRows, tailRows, height, up, gotTop, got, wantTop, want)
						}
					}
				}
			}
		}
	}
	t.Logf("%d viewport states compared", cases)
}

// TestFullscreenViewport_ClearResetsWidest checks the transcript's widest
// row is forgotten with the transcript: a wide row from before a clear
// must not keep cutting the rows committed after it.
func TestFullscreenViewport_ClearResetsWidest(t *testing.T) {
	withRenderEnv(t, 80)
	m := newTestModel()
	m.width, m.height = 40, 20
	m.fullscreen = true
	m = m.appendTranscript([]string{strings.Repeat("w", 200)})
	m = m.clearTranscript()
	m.viewport.SetContent("")
	m = m.appendTranscript([]string{"short \x1b[1mbold\x1b[0m row"})
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)
	got, _ := m.fullscreenViewport(nil)
	want, _ := m.fullscreenViewportFull(nil)
	if !slices.Equal(got, want) {
		t.Fatalf("after clear:\n got  %q\n want %q", got, want)
	}
}
