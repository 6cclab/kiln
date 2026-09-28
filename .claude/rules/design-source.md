# Design source

- `docs/kiln-design-handoff/Terminal.dc.html` is the normative design. The README there
  summarises it; where they disagree, the HTML wins.
- Take colours, glyphs, spacing and copy from the HTML. Do not invent tokens: add new ones to
  `internal/tui/theme.go` with the design's hex and name.
- When the user supplies a new handoff, use the `design-handoff` skill.
