package tui

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// The status line, ported from status.ts:102-138.
//
// Claude Code's is a dashboard, not a label: context used against the
// window with a meter, git branch and whether the tree is dirty, spend so
// far, session age, and the permission mode on its own line with the key
// that changes it. Reading it answers "can I keep going, and what will it
// do if I do" without running a command.
//
// Every segment here either changes as you work or tells you something you
// cannot otherwise see. A segment with nothing to say is omitted rather
// than shown empty, because a dashboard of blanks is worse than a short
// one.

// GitStatus is the git segment of the status line.
type GitStatus struct {
	Branch string
	Dirty  bool
}

// StatusState is everything RenderStatus needs.
type StatusState struct {
	ModelLabel    string
	ContextWindow int
	// ContextUsed is nil when there is nothing to report yet.
	ContextUsed *int
	// Cost is cumulative spend, in dollars. Omitted (rendered as nothing)
	// when zero, matching status.ts.
	Cost float64
	Git  *GitStatus
	// Mode is the permission mode, shown on the second line with the key
	// that cycles it.
	Mode string
	// Thinking is set while the model is reasoning.
	Thinking bool
	// StartedAt is session start, for the elapsed clock.
	StartedAt time.Time
	// Now is injected for tests; the zero value means "use time.Now()".
	Now time.Time
}

const (
	filledCell        = "█"
	emptyCell         = "░"
	meterWidthDefault = 6
)

// Meter renders a small bar. Fractional fills round down, so a full bar
// means genuinely full.
func Meter(fraction float64, width ...int) string {
	w := meterWidthDefault
	if len(width) > 0 {
		w = width[0]
	}
	clamped := math.Max(0, math.Min(1, fraction))
	filled := int(math.Floor(clamped * float64(w)))
	return strings.Repeat(filledCell, filled) + strings.Repeat(emptyCell, w-filled)
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

// Elapsed renders a duration the way the status line's session clock does.
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

// pressure colours by how much room is left, not by an absolute number.
// 80% of a 1M window and 80% of a 32k window are the same problem to the
// person reading it, and the point of the colour is to say "start thinking
// about compaction" at the moment that becomes true.
func pressure(fraction float64) func(string) string {
	if fraction >= 0.9 {
		return Red
	}
	if fraction >= 0.7 {
		return Yellow
	}
	return Green
}

func clock(at time.Time) string {
	h := at.Hour()
	m := fmt.Sprintf("%02d", at.Minute())
	suffix := "am"
	if h >= 12 {
		suffix = "pm"
	}
	hour12 := h % 12
	if hour12 == 0 {
		hour12 = 12
	}
	return fmt.Sprintf("%d:%s%s", hour12, m, suffix)
}

// RenderStatus renders the status line as two rows: segments, then the
// mode row. The mode goes on its own row because it is the one thing here
// that changes what the agent will *do* rather than reporting what it has
// done, and it is the one thing with a key that changes it.
func RenderStatus(s StatusState) [2]string {
	now := s.Now
	if now.IsZero() {
		now = time.Now()
	}
	p := IsPlain()
	sep := Dim(" │ ")
	var segments []string

	segments = append(segments, fmt.Sprintf("%s %s", Bold(s.ModelLabel), Dim(fmt.Sprintf("(%s ctx)", Compact(s.ContextWindow)))))

	if s.Git != nil {
		// A dirty tree is the thing you forget and then discover during a
		// rebase, so it gets a glyph rather than being implied by
		// absence.
		mark := Green("✓")
		if s.Git.Dirty {
			mark = Yellow("●")
		}
		label := "⎇"
		if p {
			label = "git"
		}
		segments = append(segments, fmt.Sprintf("%s %s %s", Dim(label), Cyan(s.Git.Branch), mark))
	}

	if s.ContextUsed != nil && s.ContextWindow > 0 {
		fraction := float64(*s.ContextUsed) / float64(s.ContextWindow)
		colour := pressure(fraction)
		percent := int(math.Round(fraction * 100))
		segments = append(segments, fmt.Sprintf("%s %s%s%s %s",
			colour(Meter(fraction)),
			Compact(*s.ContextUsed), Dim("/"), Compact(s.ContextWindow),
			Dim(fmt.Sprintf("%d%%", percent)),
		))
	}

	if s.Thinking {
		label := "◇ thinking"
		if p {
			label = "thinking"
		}
		segments = append(segments, Dim(label))
	}

	// Omitted rather than shown as $0.00: a self-hosted model has no
	// marginal cost, and a permanent zero is a segment that never earns
	// its width.
	if s.Cost > 0 {
		segments = append(segments, fmt.Sprintf("$%.2f", s.Cost))
	}

	dot := "· "
	if p {
		dot = ""
	}
	segments = append(segments, Dim(fmt.Sprintf("%s %s%s", Elapsed(now.Sub(s.StartedAt)), dot, clock(now))))

	modeColour := Green
	switch s.Mode {
	case "bypassPermissions":
		modeColour = Red
	case "plan":
		modeColour = Cyan
	}
	arrow := "▶▶"
	if p {
		arrow = ">>"
	}
	modeLine := fmt.Sprintf("%s %s %s", Dim(arrow), modeColour(fmt.Sprintf("%s mode", s.Mode)), Dim("(shift+tab to cycle)"))

	return [2]string{strings.Join(segments, sep), modeLine}
}
