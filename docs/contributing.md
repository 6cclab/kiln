# Contributing to kiln

This is the guide for building, testing and extending kiln itself. If you
just want to run the binary, read the repository [README](../README.md)
first.

## 1. Prerequisites and first build

kiln requires **Go 1.26** (`go.mod` pins `go 1.26.6`; CI's
`.github/workflows/go.yml` installs `go-version: "1.26.6"`).

Build the binary:

```bash
make build          # -> bin/kiln, also installed to ~/go/bin
```

`Makefile`'s `build` target runs `go build` with `-ldflags` setting
`internal/cli.Version` from `git describe --tags --always --dirty` (falling
back to `dev` outside a git checkout), and writes the binary to `bin/kiln`.
`bin/` is gitignored; nothing under it is committed.

You can also install `kiln` onto your `$GOBIN`/`~/go/bin`:

```bash
go install ./cmd/kiln
```

`go build ./...` (no ldflags, no special output path) is the quickest way to
check the whole module compiles; `make check` runs it as part of `go test`
compilation, gofmt and `go vet` (see below).

Two more binaries are dev/test tools, not part of `make build`, and are built
directly when you need them:

```bash
go build -o bin/kiln-drive ./cmd/kiln-drive   # PTY driver, see docs/testing.md
go build -o bin/faux ./cmd/faux               # scripted model server
```

`cmd/mcpfixture` (a scripted MCP server fixture) and `cmd/vtreplay` (replays a
`.rec` recording through the VT emulator, see `docs/testing.md`'s "Styles"
section) are the same story: build them directly with `go build -o bin/<name>
./cmd/<name>` when you need them.

staticcheck is not vendored; `make check` looks for `$HOME/go/bin/staticcheck`
first and falls back to `go run honnef.co/go/tools/cmd/staticcheck@latest` if
it isn't installed. Installing it once locally (`go install
honnef.co/go/tools/cmd/staticcheck@latest`) makes `make check` faster.

## 2. Repository layout

kiln is one Go module (`github.com/andrepato/harness`, binary name `kiln`).
The package map — what lives under `internal/`, the lifecycle of a turn,
sessions on disk, the context budget, providers, tools, permissions, hooks,
subagents and the TUI — is `docs/architecture.md`. Read that before making a
structural change; this document only covers how to build, test and extend.

Two vendored libraries live under `third_party/`, each `replace`d in
`go.mod`: `third_party/bubbletea` and `third_party/ultraviolet`. See
"Upgrade a vendored library" below.

## 3. Make targets and CI

All targets are defined in `Makefile`:

| Target | What it does |
|---|---|
| `make build` | Builds `bin/kiln` with the version ldflags described above and installs it to `$GOBIN`, else `$GOPATH/bin` (normally `~/go/bin`, on your `PATH`). `make build INSTALL_DIR=` skips the install. |
| `make check` | `gofmt -l .` (fails on any unformatted file), `go vet ./...`, staticcheck, `go test ./...`, then `go vet -tags e2e ./test/e2e/...` so a compile break in the e2e package fails the fast layer. |
| `make e2e` | `go test -tags e2e ./test/e2e/... -count=1 -timeout 10m`. Gated behind the `e2e` build tag so it never runs as part of `go build ./...` or `make check`. |
| `make e2e-live` | Same as `make e2e` plus `HARNESS_E2E_LIVE=1`, restricted to `-run Live`: the only target that talks to a real model/network. |
| `make race` | `go test -race -short ./...`; `-short` skips the one sqlite FTS test that takes minutes under the race detector. |
| `make race-full` | `go test -race ./...`, including that test; run nightly, not on every push. |
| `make cover` | `go test -coverprofile=coverage.out -covermode=atomic ./...`, prints the module total and `cmd/covercheck`'s per-package table. |
| `make covercheck` | `make cover`, then fails if any package listed in `.coverage-floors` is below its floor. Unlisted packages are reported but never fail. |
| `make eval` | `make build`, then `kiln eval run` over `eval/scenarios` against the faux provider (`EVAL_REPEAT`, default 1) and `kiln eval report --fail-on-regression 0.1` against `eval/results/baseline.jsonl`. Never touches the network. |
| `make eval-live` | `kiln eval run` against real models: `MODELS` (comma-separated `provider/model`, default `faux/faux-1`) and `REPEAT`, with `KILN_EVAL_LIVE=1` set for you. |
| `make generate` | `go generate ./...` — currently just the provider catalog (see below). |
| `make clean` | `rm -rf bin`. |

`.github/workflows/go.yml` runs on push and PR to `main` (and `go-port`), on
`ubuntu-latest` (chosen because it has `/dev/ptmx`, so the PTY screen driver
works without a self-hosted runner). It runs five jobs on Go 1.26: `make
check`, `make e2e`, `make race`, `make covercheck` (uploads `coverage.out` as
an artifact) and `make eval` (uploads `eval/results/*.jsonl`).

`.github/workflows/nightly.yml` runs at 03:00 UTC and on manual dispatch:
the TUI e2e tests three times over for flake detection, `make race-full`,
and `make eval EVAL_REPEAT=3` for score variance.

`.coverage-floors` is an opt-in ratchet: each listed package's floor was set
at measured coverage minus two points. When you add tests that raise a
package's coverage, raise its floor; when you add a package, list it (at
`0.0` if it has no tests yet) so `covercheck` reports it.

## 4. Conventions

- **Package doc comments name the port origin.** Most packages under
  `internal/` are ports of a TypeScript original (harness's own `src/` or
  pi-agent-core/pi-ai), and the package doc comment says so explicitly, e.g.
  `internal/agent/doc.go`: "Package agent is the Go port of
  harness/src/agent/session.ts", or `internal/tools/doc.go`: "ported from
  pi-agent-core's tools". When you add a file to an existing package, match
  its existing doc comment's framing rather than writing a new one.
- **Render functions are pure — no terminal.** `internal/tui`'s block and
  dialog renderers take state and a width, and return strings; they never
  touch a real terminal. That is what makes them testable without a PTY (see
  layer 1 in `docs/testing.md`).
- **Screen assertions, not byte assertions.** Every layout/rendering test
  asserts against an emulated terminal's cell grid (`internal/testkit/screen`
  or the in-process stub), never against raw bytes written. Two byte streams
  can differ and still produce the same screen; a byte assertion also can't
  see cursor position or scroll region. `docs/testing.md`'s "Rules" section
  states this outright.
- **A test that cannot fail is not evidence.** Before trusting a new test,
  reintroduce the bug it's supposed to catch and confirm the test goes red.
- **`test/e2e` is gated behind the `e2e` build tag** so `go build ./...`,
  `go test ./...` and `make check` never pull it in; only `make e2e`/`make
  e2e-live` do. Tests that need a real model or network are additionally
  named to match `-run Live` and check `HARNESS_E2E_LIVE=1` before doing
  anything network-bound.
- **Tests live next to the code they cover**, `_test.go` beside the `.go`
  file it exercises, not in a parallel test tree.
- **Shared test helpers live under `internal/testkit/`**: `faux` (scripted
  model server), `screen` (PTY driver), `sessiondata` (session-fixture
  scrubbing), `mcpfixture` (scripted MCP server), `stubtui` (in-process TUI
  stub for fast render tests). Reach for one of these before writing a new
  helper.
- **Every run writes a diag log** to `~/.harness/logs` for observability
  (startup phases, MCP connect outcomes, every turn/tool call/retry/
  compaction) — see the README's "Every run writes a log" paragraph. When
  adding a subsystem that can fail in a way a user would need to debug,
  log through the same mechanism rather than inventing a second channel.
- **`gofmt` and `staticcheck` are enforced, not advisory.** `make check`
  fails the build on any gofmt diff or staticcheck finding; run `gofmt -w`
  on files you touch before committing.

## 5. Testing

`docs/testing.md` is the authority on the test layers (unit, faux,
screen/`kiln-drive`, live e2e, styles, the e2e behaviour suite, the eval
runner, coverage, fuzz targets) and record/replay. Read it before writing a
test. Start a faux server in a test through `internal/testkit/fauxtest`
rather than copying setup code; load scenario bundles through
`internal/testkit/scenario`.

Minimum before opening a PR:

```bash
make check                 # gofmt, vet, staticcheck, go test ./...
```

If your change touches anything the TUI renders (a block, a dialog, a
keybinding, layout at any width), also run:

```bash
make e2e                   # drives the real binary through a PTY
```

and update goldens deliberately with `UPDATE=1` (see `docs/testing.md`'s
"Go TUI e2e" and "Styles" sections for the exact commands) — never regenerate
a golden to make a red test pass without confirming the new output is
actually correct.

## 6. How-to recipes

### Add a built-in or harness tool

1. Implement it in `internal/tools/<name>.go`, building a `*tool.Tool` (the
   contract is `internal/tool/tool.go`: `Name`, `Label`, `Description`, a
   JSON Schema `Parameters`, and an `Execute` function that streams partial
   results through an `Update` callback and returns a `tool.Result`). Look at
   `internal/tools/todo.go` (`TodoTool`) for a complete, simple example:
   schema as a `json.RawMessage` literal, argument struct, validation, and a
   short summary text response rather than echoing input back.
2. If the tool is one of the four resident built-ins (bash/read/edit/write),
   add it to `Builtins` in `internal/tools/builtins.go`. Anything else
   (`todo_write`, `task`, `tool_search`, an MCP adapter, ...) is registered
   as an `ExtraTool` where the session is built —
   `internal/agent/session.go`'s `Start` adds `opts.ExtraTools` to the
   builtin set, and only names present in `ActiveToolNames` are actually
   sent to the model (registration is free; activation costs schema
   tokens — see the README's "Tool gating" section).
3. Set `Concurrent: true` only if `Execute` does not touch the calling
   session's files, branch, or other process-wide state — see the doc
   comment on `tool.Tool.Concurrent` in `internal/tool/tool.go`; `task` is
   the only current example, because a dispatched subagent gets its own
   session and storage.
4. If the tool should be governable by a permission rule, make sure its
   `Name` matches how a rule will reference it: `internal/claude/settings`'s
   `MatchesRule` (`internal/claude/settings/settings.go`) lower-cases both
   the tool name and the rule before comparing, and lets an `mcp__server`
   rule cover every tool of exactly that server — so a plain tool name
   like `bash` or `todo_write` must match exactly (case-insensitively).

### Add a slash command

1. Commands are built with `commands.Command` (`internal/commands/
   registry.go`): `Name`, `Description`, optional `ArgumentHint` and
   `ArgumentCompletions`, and a `Run` function returning a `commands.Result`
   (text `Output`, an optional `Prompt` to send to the model, an optional
   `Modal` panel spec, or `Exit`).
2. Group related commands into a `Source` — see any of the `*Commands`
   constructors in `internal/commands/builtins.go` (e.g. `/help`, `/clear`,
   `/compact`, `/model`) for the pattern: a `Deps` struct of callbacks so the
   `commands` package never imports `internal/cli` or `internal/agent`
   directly (see `BuiltinDeps`'s doc comment), and a function returning
   `commands.StaticSource(commands.OriginBuiltin, []commands.Command{...})`.
3. Wire the new source into `buildCommandRegistry` in
   `internal/cli/commands.go`, in the position that matches how it should
   rank against existing commands: sources are added least-specific first,
   and a later source's command of the same name wins on a collision (see
   that file's own doc comment for the one documented deviation from
   cli.ts's exact registration order).

### Add a provider or API shape

1. `internal/provider/types.go`'s `Provider` interface is the contract:
   `ID`, `Name`, `Auth` (an `AuthSpec` — API key env vars, OAuth, or none),
   `Models`, `RefreshModels`, and `Stream` (returns a channel of
   `msg.StreamEvent` plus a wait function).
2. A **catalog-backed** provider (one already in the vendored pi-ai catalog
   under `internal/provider/catalog/data/*.json`) is wired up in
   `internal/provider/builtin/builtin.go`: it maps a catalog provider id to
   auth config and, if the provider's `api` value isn't already one of the
   ten implemented shapes (`implementedAPIs` in that file), needs a new
   streaming client under `internal/provider/api/` — one file per API shape,
   e.g. `anthropic_messages.go`, `openai_responses.go`.
3. A **non-catalog** provider (one with its own model discovery, not sourced
   from the vendored catalog) is the pattern in `internal/provider/ollama`:
   its own `Models`/`RefreshModels` implementation (`/api/tags`, `/api/ps`,
   `/api/show`) instead of reading `internal/provider/catalog`.
4. New API-shape clients need golden request/response fixtures under
   `internal/provider/api/testdata/<shape>/*.sse`: recorded once from a live
   `internal/testkit/faux` server (`go test ./internal/provider/api/... -run
   TestRecordFixtures -record`, per `fixture_helpers_test.go`'s doc comment)
   and replayed by default so the conformance suite is fast and
   deterministic. A response shape faux's script engine can't produce in one
   turn (e.g. multiple tool-call blocks in one assistant message, a stream
   truncated mid-event) is hand-authored instead — see
   `internal/provider/api/fixtures_test.go` for which fixtures are
   hand-authored and why.
5. Auth flows (API key lookup, OAuth login/refresh) live under
   `internal/auth` (`internal/auth/credential.go`, `internal/auth/oauth/`,
   `internal/auth/login/`); wire a new OAuth refresh flow into
   `oauthRefreshers` in `internal/provider/builtin/builtin.go` if the
   provider supports it.

### Add a hook event

kiln recognizes the same nine Claude Code hook events. `Event` and its nine
constants (`PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart`,
`SessionEnd`, `Stop`, `SubagentStop`, `Notification`, `PreCompact`) live in
`internal/claude/hooks/hooks.go`, along with `Config` (event -> matcher
groups), `MatchesHook` (anchored, case-insensitive tool-name regex match) and
`HooksFor` (which commands apply to a given tool call). Hooks accumulate
across settings scopes rather than overriding — a project hook never
silently disables a user hook.

The harness side that actually *fires* hooks at the right point in a turn is
`internal/harness/hooks.go`: `Hooks` holds one `On*` registration function
per lifecycle point (`OnBeforeRun`, `OnBeforeTool`, `OnAfterTool`,
`OnBeforeCompaction`, `OnTransformContext`, ...), and each has a matching
`invoke*` method on `Lane` that runs registered handlers, converts a panic or
error into a fault-labelled `handler_error` event rather than crashing the
turn, and (for `OnBeforeTool`) can return a `BeforeToolResult` that rewrites
or blocks the call. To wire a new Claude Code hook event through to a
harness-level effect, add the event handling in `internal/claude/hooks` (if
it's a new event) or find the matching `On*`/`invoke*` pair in
`internal/harness/hooks.go` and call it from the turn-loop point that
corresponds to when Claude Code fires that event.

### Change a TUI block or dialog and update goldens

1. Rendering conventions live in `internal/tui/theme.go` — e.g. `labelRule`
   builds the "labelled hairline rule above every transcript block" the
   README describes: `label + " " + fill + "  " + meta`, filled to the
   terminal width, with a plain-mode ASCII fallback (`IsPlain()`) for the
   fill character.
2. Transcript block rendering lives in `internal/tui/transcript.go`; dialog
   scaffolding (the `Dialog` interface, title/description rendering shared
   across dialogs) lives in `internal/tui/dialog.go`. Both are pure render
   functions — no terminal I/O — per the conventions above.
3. Goldens live under `testdata/golden/` (plain-text screens like
   `tui-empty-117.txt`, and styled encodings like `*.styles.txt`). After an
   intentional visual change, regenerate the affected goldens deliberately:

   ```bash
   UPDATE=1 go test -tags e2e ./test/e2e/... -run TestTUI_<Name> -v
   ```

   Read the diff before committing — `UPDATE=1` overwrites the golden with
   whatever the current build renders, bug included.
4. `docs/kiln-design.md` is the rendering contract (palette, glyphs, block
   anatomy) these goldens back; update it too if the change is a deliberate
   design change rather than a bug fix.

### Refresh the provider catalog

The vendored catalog (`internal/provider/catalog/data/*.json`, `internal/
provider/catalog/VERSION`) is copied from a checked-out `pi-ai` package, not
fetched at build or run time. To refresh it:

```bash
PI_AI_DIR=/path/to/pi-ai/package go generate ./...
```

`internal/provider/catalog/generate.go` (built with the `ignore` tag, so it
is never part of `go build ./...`) copies every `dist/providers/data/*.json`
file from `$PI_AI_DIR` into `internal/provider/catalog/data/`, removes any
vendored file no longer present upstream (so a dropped provider doesn't
linger), and writes `internal/provider/catalog/VERSION` from the checked-out
package's `package.json` version field. Requires a local checkout of `pi-ai`
with `dist/` already built.

### Upgrade a vendored library

`third_party/bubbletea` and `third_party/ultraviolet` are vendored copies of
`charm.land/bubbletea/v2` and `github.com/charmbracelet/ultraviolet`, pulled
in through `replace` directives in `go.mod`. Each carries kiln-specific
patches, documented in that directory's `HARNESS-PATCH.md`:

- **bubbletea** (`third_party/bubbletea/HARNESS-PATCH.md`): four patches to
  `cursed_renderer.go` around `tea.Println`/`insertAbove` — flushing a
  pending frame before scrolling for an inserted line, a full repaint of the
  live region instead of an incremental diff, repainting after `insertAbove`
  so the cursor lands on the caret, and not dropping the last cell of a
  full-width committed line (autowrap guard).
- **ultraviolet** (`third_party/ultraviolet/HARNESS-PATCH.md`): one patch to
  `terminal_renderer.go`'s `Render` — erasing against the old terminal model
  before resizing the tracked buffer, so a shrinking inline frame doesn't
  leave stale rows on screen.

To re-vendor after an upstream fix: pull the new upstream source into the
`third_party/<lib>` directory, re-apply (or drop, if upstream now fixes it)
each patch described in that `HARNESS-PATCH.md`, update the version comment
at the top of the file, and — if a patch is no longer needed — remove both
the patch's section from `HARNESS-PATCH.md` and, once every patch is gone,
the `replace` directive in `go.mod`. Verify each patch's own "Verified
through" note still holds (most point at a specific
`internal/testkit/screen` scenario) before dropping it.

## 7. Documentation map

| Doc | Owns |
|---|---|
| [README.md](../README.md) | What kiln is, install, everyday usage, the context-budget design, `.claude/` compatibility, safety |
| `docs/architecture.md` | Package map, turn lifecycle, sessions on disk, context budget, providers, tools, permissions, hooks, subagents, TUI |
| `docs/configuration.md` | Exact flags, environment variables, `settings.json` keys, `.claude` asset shapes, MCP server config |
| `docs/usage.md` | Running a session: starting, resuming, typing, keys, slash commands, permissions, MCP, subagents, full-screen mode |
| `docs/troubleshooting.md` | What `kiln doctor` and the run log tell you, and the fix for known symptoms |
| `docs/contributing.md` (this file) | Prerequisites, make targets, CI, conventions, and recipes for adding a tool, command, provider, hook event, or TUI block |
| `docs/testing.md` | The unit/faux/screen/e2e/styles test layers, the behaviour suite, `kiln eval`, coverage floors, fuzz targets, `kiln-drive`, record/replay |
| `docs/benchmarks.md` | A log of recorded `go test -bench` measurements, one row per run; nothing is asserted |
| `docs/parity-verification.md` | The manual procedure for settling `[chk]` rows in the parity checklist against the real Claude Code binary; never run in CI |
| `docs/kiln-design.md` | The palette, glyphs, block anatomy and dialogs — the rendering contract; the code wins where they disagree |
| `docs/claude-code-parity.md` | Behavioral checklist against Claude Code (keys, flags, modes, hooks, MCP), with `[obs]`/`[chk]` confidence marks |
| `docs/kiln-fullscreen-plan.md` | The alt-screen mode's design and the decisions settled during the build |
| `docs/kiln-design-handoff/README.md` | The original "Ruled" design handoff the rendering contract was derived from |

Reference material that lives next to what it describes, rather than under
`docs/`: `testdata/sessions/README.md` (session fixtures and scrubbing) and
`third_party/*/HARNESS-PATCH.md` (patches carried on vendored libraries).
