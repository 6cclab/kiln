---
name: kiln-qa
description: >-
  Drive kiln through its real-terminal QA scenarios (qa/scenarios/*.steps) in iTerm2,
  Terminal.app or Warp, triage the failures, and re-verify fixes. Use when asked to test kiln
  "for real", run the QA campaign, check a UI change in a real terminal, or add a scenario.
---

# kiln real-terminal QA

Read first: the docstring of `scripts/qa/drive.py` (every directive and step verb) and
`docs/testing.md` "Real-terminal QA". Follow `.claude/rules/real-terminal-drives.md` and
`.claude/rules/scenario-failures.md`.

## Run

1. `make build` (builds `bin/kiln`, installs `~/go/bin/kiln`) and `go build -o bin/faux ./cmd/faux`.
2. Tell the user a drive is starting and not to type or move the pointer until it ends.
3. One or a few scenarios:
   `python3 scripts/qa/drive.py --terminal iterm-dark --out qa/runs/<label>-<UTC stamp> <steps...>`
   Everything: `make qa` (`TERMINAL=iterm-light|terminal|warp`, `SCENARIO=<area>/<name>`).
   A full run of every faux scenario takes about 25 minutes: run it in the background and
   do not rebuild until it ends.
4. Results: `grep -E "^(PASS|FAIL)" qa/runs/<run>.log`. Each scenario directory holds
   `run.log`, and a `.png` plus `.txt` per SHOT (and per timeout).

## Triage a failure

1. `grep -B1 -- "-> FAIL\|ERROR" <scenario dir>/run.log` for the failing step.
2. Read the last `.txt` (screen text) and look at its `.png`. The screenshot is the evidence:
   spacing, colour and glyph defects never show in text.
3. Classify it (rules/scenario-failures.md). Stale expectations get the `.steps` file fixed;
   defects get a finding (`scripts/qa/findings.py new ...`) and a fix with a regression test.
4. Look past the failing check: read the screens of passing scenarios too, for duplicated
   blocks, raw model-facing text, misaligned columns and inconsistent number formats.

## Verify a fix

1. `make check`, then `make e2e`; update goldens per `.claude/rules/goldens.md`.
2. `make build`, re-drive every scenario that failed plus any the change touches, and read the
   new screenshots.
3. Flip findings with `scripts/qa/findings.py set <id> --status fixed --fix-test ... --after ...`.

## Terminals

- `iterm-dark` is the default. `iterm-light` uses a dynamic profile the driver installs and
  removes. `terminal` (Terminal.app) exercises the 256-colour fallback.
- `warp` reads the screen by OCR, so its EXPECT checks are advisory: confirm against the PNG.
- SCROLL moves the pointer to the window centre and back; wheel direction is compensated for
  macOS natural scrolling inside `wheel.swift`.

## Debug terminal input

If kiln seems to mishandle a key or wheel event, find out what the terminal actually sends:
run a small raw-mode python program in a fresh terminal window that enables the same modes
(for mouse: `\e[?1003h\e[?1006h`) and logs the bytes it reads, then replay the input. Decide
between a terminal, driver or kiln problem from those bytes, not from kiln's behaviour.
