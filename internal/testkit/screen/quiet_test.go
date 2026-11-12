package screen

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeClock moves only when the code under test sleeps, so these tests
// script what the screen shows at each moment instead of racing a real
// process against the wall clock.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(0, 0)} }

func (c *fakeClock) elapsed() time.Duration { return c.now.Sub(time.Unix(0, 0)) }

// WaitQuiet must not return in the gap between two paints separated by a
// delay (kiln's resize: an immediate reflow, then a debounced re-wrap),
// only once the screen has stayed unchanged for the whole quiet window.
func TestWaitQuiet_WaitsOutADelayedSecondPaint(t *testing.T) {
	c := newFakeClock()
	view := func() string {
		if c.elapsed() < 1400*time.Millisecond {
			return "first"
		}
		return "second"
	}
	if _, ok := waitQuiet(c, view, 1500*time.Millisecond, 30*time.Second); !ok {
		t.Fatal("WaitQuiet timed out on a screen that went quiet")
	}
	if got := view(); got != "second" {
		t.Fatalf("WaitQuiet returned before the second paint: %q", got)
	}
	if c.elapsed() < 2900*time.Millisecond {
		t.Fatalf("WaitQuiet returned %s in, before the screen had been quiet for 1.5s after the 1.4s paint", c.elapsed())
	}
}

func TestWaitQuiet_TimesOutOnAScreenThatKeepsChanging(t *testing.T) {
	c := newFakeClock()
	view := func() string { return fmt.Sprintf("tick %d", c.elapsed()/(50*time.Millisecond)) }
	changed, ok := waitQuiet(c, view, 2*time.Second, 4*time.Second)
	if ok {
		t.Fatal("WaitQuiet returned ok on a screen that never stopped changing")
	}
	if !strings.Contains(changed, "tick") {
		t.Errorf("last change = %q, want the ticking row", changed)
	}
	if c.elapsed() < 4*time.Second || c.elapsed() > 4*time.Second+pollEvery {
		t.Errorf("gave up after %s, want the 4s timeout", c.elapsed())
	}
}

// A row the caller ignores may keep changing without holding WaitQuiet up.
func TestWaitQuiet_IgnoresMaskedRows(t *testing.T) {
	c := newFakeClock()
	rows := func() []string {
		return []string{"steady", fmt.Sprintf("tick %d", c.elapsed()/(50*time.Millisecond))}
	}
	dropTicks := func(rows []string) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			if !strings.HasPrefix(r, "tick") {
				out[i] = r
			}
		}
		return out
	}
	if _, ok := waitQuiet(c, maskedView(rows), 300*time.Millisecond, 30*time.Second); ok {
		t.Fatal("unmasked, the ticking row should keep the screen from going quiet")
	}
	c = newFakeClock()
	if _, ok := waitQuiet(c, maskedView(rows, dropTicks), 300*time.Millisecond, 30*time.Second); !ok {
		t.Fatal("WaitQuiet with the ticking row ignored timed out")
	}
	if c.elapsed() > 300*time.Millisecond+pollEvery {
		t.Errorf("returned after %s, want right after the 300ms quiet window", c.elapsed())
	}
}

// settle is short: on a screen that keeps changing (a reply streaming in,
// a spinner) WaitFor returns soon after the pattern appears, not once the
// screen goes quiet or the timeout runs out. Settling until the deadline
// held TestTUI_Esc_InterruptsAContinuedTurn's WaitFor for the whole slow
// reply, so its Esc landed after the reply had finished.
func TestSettle_BoundedOnAScreenThatKeepsChanging(t *testing.T) {
	c := newFakeClock()
	view := func() string { return fmt.Sprintf("tick %d", c.elapsed()/(20*time.Millisecond)) }
	settleView(c, view, c.Now().Add(30*time.Second))
	if c.elapsed() < settleMax || c.elapsed() > settleMax+pollEvery {
		t.Errorf("settled after %s, want settleMax (%s)", c.elapsed(), settleMax)
	}
}

// On a screen that repaints faster than settleQuiet, then stops, settle
// returns once it has been quiet for settleQuiet, well before settleMax.
func TestSettle_ReturnsOnceQuiet(t *testing.T) {
	c := newFakeClock()
	view := func() string {
		if c.elapsed() < 100*time.Millisecond {
			return fmt.Sprintf("painting %d", c.elapsed()/(30*time.Millisecond))
		}
		return "done"
	}
	settleView(c, view, c.Now().Add(30*time.Second))
	if got := view(); !strings.HasPrefix(got, "done") {
		t.Fatalf("settled on %q, before the last paint", got)
	}
	if want := 100*time.Millisecond + settleQuiet; c.elapsed() < want || c.elapsed() > want+2*pollEvery {
		t.Errorf("settled after %s, want about %s", c.elapsed(), want)
	}
}
