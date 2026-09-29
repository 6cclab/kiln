# kiln

kiln is a Go coding-agent TUI (`cmd/kiln`) that reads Claude Code's own configuration.

## Read first

- `docs/architecture.md` for the package layout, `docs/testing.md` for the test layers.
- `docs/kiln-design-handoff/Terminal.dc.html` before changing anything on screen.
- `.claude/rules/` applies to all work here.

## Commands

- `make check`: gofmt, vet, staticcheck, unit tests. Run before every commit.
- `make e2e`: PTY tests against a freshly built binary. Run before every commit that touches
  `internal/tui`, `internal/cli` or rendering.
- `make build`: builds `bin/kiln` and installs `~/go/bin/kiln`.
- Real-terminal checks: use the `kiln-qa` skill. New design handoffs: the `design-handoff` skill.

## Working rules

- Verify UI changes in a real terminal (the `kiln-qa` skill), not only with goldens.
- Every fix ships with a regression test that fails without it.
- kiln must behave like Claude Code wherever it reads Claude Code's files (settings, hooks,
  skills, rules, plugins, MCP). Check Claude Code's behaviour before diverging from it.
