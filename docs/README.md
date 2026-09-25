# kiln documentation

Start with the repository [README](../README.md) for what kiln is, how to
install it, and the one-screen summary of how it keeps context small.

## Using kiln

| Document | Read it when |
|---|---|
| [usage.md](usage.md) | you are running a session: starting, resuming, typing, keys, slash commands, permissions, MCP, subagents, full-screen mode |
| [configuration.md](configuration.md) | you need the exact flag, environment variable, `settings.json` key, `.claude` asset shape, or MCP server config |
| [troubleshooting.md](troubleshooting.md) | something looks wrong: what `kiln doctor` and the run log tell you, and the fix for each known symptom |

## Working on kiln

| Document | Read it when |
|---|---|
| [architecture.md](architecture.md) | you need the package map, the lifecycle of a turn, sessions on disk, the context budget, providers, tools, permissions and hooks, subagents, the TUI |
| [contributing.md](contributing.md) | you are building, testing or changing kiln: prerequisites, make targets, CI, conventions, and recipes for adding a tool, command, provider, hook event or TUI block |
| [testing.md](testing.md) | you are writing or debugging a test: the unit, faux, screen, e2e and styles layers, the behaviour suite, `kiln eval`, coverage floors, `kiln-drive`, record/replay |
| [benchmarks.md](benchmarks.md) | you want the recorded `go test -bench` numbers; a log, nothing is asserted |
| [parity-verification.md](parity-verification.md) | you need to settle a `[chk]` parity row by hand against the real Claude Code binary |
| [kiln-design.md](kiln-design.md) | you are touching anything that renders: the palette, glyphs, block anatomy and dialogs; the code wins where they disagree |
| [claude-code-parity.md](claude-code-parity.md) | you need the behavioural checklist against Claude Code (keys, flags, modes, hooks, MCP), with `[obs]`/`[chk]` confidence marks |
| [kiln-fullscreen-plan.md](kiln-fullscreen-plan.md) | you are working on the alt-screen mode: the design it was built to and the decisions settled during the build |
| [kiln-design-handoff/README.md](kiln-design-handoff/README.md) | you want the original "Ruled" design handoff the rendering contract was derived from |

Other reference material lives next to what it describes:
`testdata/sessions/README.md` (session fixtures and scrubbing) and
`third_party/*/HARNESS-PATCH.md` (the patches carried on vendored libraries).
