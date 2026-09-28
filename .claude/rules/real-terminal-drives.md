# Real-terminal drives

- Drive kiln only through `scripts/qa/drive.py`. Input goes through Orca (TYPE, KEY, SEND) and
  wheel events through `scripts/qa/wheel.swift` (SCROLL). Never inject bytes with AppleScript
  `write text`: it skips the terminal's key encoder, so the drive tests input no user can produce.
- Build before a drive, never during one. The drive launches `bin/kiln` for every scenario.
- Tell the user before a drive that moves the pointer or takes focus, and ask them not to type
  until it finishes: stray keystrokes land in the driven window.
- Faux scenarios run in a scratch HOME. Never let a test or scenario write to the real
  `~/.claude`, `~/.claude.json` or `~/.harness`. `@real` scenarios on the real HOME stay read-only.
- A drive result is only valid for the binary it ran. After fixing something, `make build`,
  re-drive the failures, and read the screenshots, not only the PASS lines.
- `make build` also installs `~/go/bin/kiln`, which is what the user runs. Tell them to restart
  kiln after a fix they will try.
