# Claude Code reference screens

Captured from the real `claude` binary (v2.1.281) through `harness-drive` on
2026-09-24, 100x40 unless the cursor line says otherwise. These are the goldens the
harness TUI is held to: same glyphs, same columns, same wording, same row layout.

- `*.txt`: the emulated screen (`SCREEN` dumps; trailing spaces trimmed). The last line
  is the driver's cursor/occupied summary.
- `manual-session.rec`: raw bytes of the manual-mode session, with `\x00IN:` markers for
  keystrokes; colours live here.
- `capture-manual.txt`: the harness-drive script that produced `manual-session.*`.
- `startup.txt`, `autocomplete-*.txt`, `dialog-*.txt`, `turn-*.txt`, `verbose-*.txt`,
  `spinner.txt`, `permission-edit.txt`, `shortcuts.txt`, `help.txt`, `resize-60.txt`,
  `ctrl-c-hint.txt`, `mode-cycle.txt`, `dialog-trust.txt`.

Machine specifics in these screens (the user's own `statusLine` row beginning
`Opus 5 (1M context) │ ⎇ main`, plugin tips, the auto-mode teach dialog, account
names) are Claude Code reacting to this machine's config, not part of the target.
`startup-default-home.txt` was captured with a HOME that has no statusLine: the default
bottom area is the input box and the mode line only.

Recapture: `env -u CLAUDE_CODE_CHILD_SESSION -u CLAUDECODE bin/harness-drive --bin $(which claude)
--cols 100 --rows 40 --env HOME=$HOME --record manual-session.rec < capture-manual.txt`
inside a trusted scratch git repo containing `math.js` with `return a - b`.
