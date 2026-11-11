# kiln

kiln is a Go coding-agent TUI (`cmd/kiln`) that reads Claude Code's own configuration and runs on
self-hosted models. Module `github.com/andrepato/harness`.

## Read first
- `docs/architecture.md`: package layout. `docs/testing.md`: test layers.
- `docs/kiln-design-handoff/Terminal.dc.html` before changing anything on screen (it is the normative design).
- `.claude/rules/*.md` (design-source, goldens, real-terminal-drives, scenario-failures) apply to all work here.

## Layout
- `cmd/kiln` (the TUI), `cmd/kiln-drive`, `cmd/faux`, `cmd/mcpfixture`, `cmd/vtreplay`, `cmd/covercheck`.
- `internal/tui` (screens, `theme.go`), `internal/cli`, `internal/agent`, `internal/claude` (reads Claude
  Code settings, hooks, skills, rules, plugins), `internal/mcp`, `internal/provider`.
- `test/e2e` (PTY tests, `e2e` build tag), `qa/` (real-terminal scenarios), `eval/` (eval scenarios).

## Commands
- `make check`: gofmt, vet, staticcheck, unit tests, vet of the e2e package. Run before every commit.
- `make e2e`: PTY tests against a freshly built binary. Run before every commit that touches `internal/tui`,
  `internal/cli` or rendering.
- `make build`: builds `bin/kiln` and installs it to `$GOBIN` or `$GOPATH/bin` (`Makefile:23-31`).
- Also in the Makefile: `qa`, `qa-lint`, `qa-headless`, `eval`, `eval-live`, `cover`, `covercheck`, `race`.

## Working rules
- Verify UI changes in a real terminal, not only with goldens.
- Every fix ships with a regression test that fails without it.
- kiln must behave like Claude Code wherever it reads Claude Code's files (settings, hooks, skills, rules,
  plugins, MCP). Check Claude Code's behaviour before diverging from it.

## Done means
- `make check` passes; `make e2e` too when `internal/tui`, `internal/cli` or rendering changed.
- UI changes also checked in a real terminal; every fix has a regression test that fails without it.
- Ships via PR to `main` (CI `go.yml`); releases are git tags (`release.yml`).
