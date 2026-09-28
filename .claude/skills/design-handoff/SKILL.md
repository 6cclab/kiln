---
name: design-handoff
description: >-
  Implement an updated kiln design handoff (a folder holding Terminal.dc.html, README.md,
  support.js and Kiln TUI.dc.html) in the Go TUI. Use when the user points at a new
  design_handoff_kiln_tui folder or asks to implement a redesign of a kiln screen.
---

# Implement a design handoff

Follow `.claude/rules/design-source.md`.

1. Diff each file against the repo copy:
   `diff docs/kiln-design-handoff/Terminal.dc.html "<handoff>/Terminal.dc.html"` (and README.md,
   support.js, `Kiln TUI.dc.html`). The HTML diff is the change; the README diff explains intent.
2. Copy the handoff files over `docs/kiln-design-handoff/` so the repo holds the normative copy.
3. Find the renderer for each changed block (banner: `internal/cli/tui.go` newBanner/styleBanner;
   transcript blocks: `internal/tui/transcript.go`, `bridge.go`; chrome: `app.go`, `status.go`;
   dialogs: `internal/tui/dialog*.go`). Take hex colours, glyphs and copy verbatim from the HTML.
4. Decide the narrow-terminal and `--ax-screen-reader` behaviour for anything wider or more
   decorative than before, and cover both with a test.
5. Update the render goldens for the tests the change touches (`.claude/rules/goldens.md`),
   then `make check` and `make e2e`.
6. `make build` and drive `qa/scenarios/startup/*.steps` (or the scenarios showing the changed
   block) with the `kiln-qa` skill. Compare the screenshot against the HTML rendered in a
   browser before calling it done.
