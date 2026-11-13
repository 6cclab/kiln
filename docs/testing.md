# Testing kiln

Pick the cheapest layer that can observe the bug, and before writing an
assertion, check that it can fail. Every screen assertion runs against an
emulated terminal, never against the bytes written. Two byte streams can differ
and produce the same screen, and a write-log has no cursor, no scroll region and
no cell grid, so it cannot say what is on the glass. Every rendering bug in this
project has lived in that gap.

`make check` runs the fast layers; `make e2e` and `make e2e-live` run the slow
ones. The `Makefile` has the exact commands for each layer.

## Rules

- A test that cannot fail is not evidence. Reintroduce the bug and watch the test
  go red before trusting a green run.
- Assert at the layer where the user perceives the bug. A layout complaint is a
  screen assertion, not a byte assertion.
- `make check` is the fast layers; `make e2e` drives the real binary.
- A test must not be able to fail because the machine is slow. Wait on an event,
  never on a fixed sleep or an elapsed bound.
  - To keep a turn busy, script a faux `hold: <name>` step and release it from
    the test (`releaseFaux`, or `RELEASE` in kiln-drive and drive.py). Do not use
    a `delay`.
  - To wait for something to disappear, use `waitScreenGone` or `WAITGONE`.
  - e2e screens floor every wait at 20 s (`screen.WithMinWait`), `waitQuiescent`
    ignores the busy row, and golden comparisons retry a fresh capture until they
    match or 20 s pass (`goldenEventually`), so a half-painted frame cannot fail
    one.
  - Only a negative check ("nothing happens within N") may use a plain sleep:
    slow can only make it pass vacuously.

## 1. Unit — `go test ./...`

Ordinary Go unit tests next to the code they cover. Fast, no subprocess, no PTY.
Use them for parsing, formatting, session-store logic, hook decision logic and
tool-call plumbing: anything that needs neither a real terminal nor a real model.

## 2. Faux — a scripted model server

`internal/testkit/faux` (owned separately from this document) runs a small HTTP
server that speaks the Anthropic and OpenAI-compatible wire formats against a
scripted transcript. Tests can drive kiln against a fake model without the
network or a real API key. Tests that want it set:

- `HARNESS_FAUX_ADDR`: the faux server's listen address, so harness's model
  client points at it instead of a real provider endpoint.
- `HARNESS_FAUX_SCRIPT`: path to the script file describing the turn-by-turn
  responses (and any tool calls) the faux server plays back for that test.
- `HARNESS_MODEL=faux/faux-1`: selects the faux provider/model pair, so kiln's
  normal model-selection path runs unchanged rather than being special-cased for
  tests.

Use it for anything that needs a real request/response round trip (streaming,
tool-call turns, retries, token accounting) but not a real model or PTY.

## 1a. Render goldens — `internal/tui`

`assertRenderGolden(t, name, lines)` in `internal/tui/golden_test.go` compares a
renderer's output lines against two files under
`internal/tui/testdata/render/<name>{,.styles}.txt`. It makes a direct call into
a `Render*` function, with no PTY and no Bubbletea program.

- `<name>.txt` has ANSI stripped, for a human-legible layout diff.
- `<name>.styles.txt` keeps ANSI, for a colour/attribute diff.

`withRenderEnv(t, width)` first forces the renderer's global state (colour on,
plain mode off, a fixed width) so results are deterministic across machines.
`UPDATE=1 go test ./internal/tui -run <TestName>` (re)writes both files from the
current render. Without it, a mismatch fails with a diff against each file.

This is the fastest layer that can assert on styled output. It does not drive a
PTY, so it misses layout bugs that only show in a real terminal (wrapping,
resize, cursor placement). The PTY layer (`test/e2e`) stays the source of truth
for those.

Files using it today (`grep -rl assertRenderGolden internal/tui/*.go`):
`golden_test.go` itself, `blocks_golden_test.go`, `status_golden_test.go`,
`retry_test.go`, `stream_test.go`, `note_behaviour_test.go` and
`render_extra_golden_test.go`.

`internal/cli/banner_golden_test.go` covers `bannerRows`. Package `cli` cannot
import `internal/tui`'s unexported `assertRenderGolden`, so the test has its own
copy of the same ~20-line compare helper and goldens into
`internal/cli/testdata/banner/*.txt`.

## 3. Screen — PTY driver + `kiln-drive`

`internal/testkit/screen` (owned separately) drives a real PTY running the
compiled `kiln` binary and gives tests a real terminal emulator's view of it. It
is the Go equivalent of the TypeScript `FakeTerminal` → `@xterm/headless`
pipeline described above, but against the actual binary rather than an
in-process render loop. `cmd/kiln-drive` is the CLI front end for that driver. It
launches `kiln` inside a PTY and accepts a line-oriented protocol on its own
stdin to control the session and inspect the screen:

- `SEND <text>`: write text to the PTY as if typed, without a trailing key.
- `KEY <name>`: send a named key (`enter`, `esc`, `ctrl+c`, arrow keys, etc.) as
  its terminal escape sequence.
- `WAIT <pattern>`: block until the screen matches a pattern (or a timeout
  elapses), for tests that wait on async model/tool output before asserting.
- `SCREEN`: print the current screen contents (rows, cursor position) to the
  driver's own stdout, for the test to assert on.
- `RESIZE <cols> <rows>`: resize the PTY, for layout-over-time tests (overlays,
  frames that grow or shrink, scroll).
- `EXIT`: end the session and terminate the driven process. `Screen.Exit`
  (`internal/testkit/screen/screen.go`) first gives the process a short grace
  period to exit on its own (for a script that sent a quit key just before
  `EXIT`). It then sends `SIGINT`, the same signal a real terminal's Ctrl+C
  delivers. `RunInteractive` in `internal/cli/tui.go` funnels that signal
  straight to `program.Quit()` whatever the TUI's key router is doing (a prompt
  open, mid-feedback-capture, a dialog up, anything). Finally it waits up to the
  screen's own timeout for the process to exit, and returns an error if it does
  not.

Use it for anything that must exercise process startup, real PTY resize
semantics, or real terminal escape-sequence handling: the things the in-process
TypeScript layers cannot see.

## 4. Live e2e — `test/e2e`, gated on `HARNESS_E2E_LIVE=1`

`test/e2e` holds tests that drive the real, compiled binaries end to end. It is
build-tagged `e2e`, so `go build ./...`, `go test ./...` and `make check` never
pull it in.

Most of it runs against the faux server or a scripted MCP fixture
(`internal/testkit/mcpfixture`), so it is hermetic; `make e2e` runs that subset.
Tests that need a real model, real network access or real API keys are named to
match `-run Live` and check `HARNESS_E2E_LIVE=1` before anything network-bound.
A plain `make e2e` (and CI) therefore never touches the network; only
`make e2e-live` does.

## Go TUI e2e — `test/e2e/tui_test.go`

Drives the real, compiled `kiln` binary's interactive TUI through
`internal/testkit/screen`. Every assertion is against the emulated terminal grid
(`Rows`/`Viewport`/`CursorRow`/`OccupiedHeight`), never against raw escape bytes,
matching the screen-layer rule above. It covers:

- the startup frame at several widths, the fix-bug flow, and the busy/spinner
  state;
- the inline permission prompt (allow and deny-with-feedback);
- the `/model`, `/permissions`, `/mcp`, `/agents` and `/config` panels;
- Shift+Tab's permission-mode cycle, the Ctrl+O verbose-transcript toggle, a
  resize sweep, `--ax-screen-reader`, and the `!`/`#` input modes;
- Ctrl+C/Ctrl+D exit.

Run it as part of `make e2e`, or on its own:

    go test -tags e2e ./test/e2e/... -run TestTUI_ -v

To update a golden after an intentional UI change:

    UPDATE=1 go test -tags e2e ./test/e2e/... -run TestTUI_FixBug -v

Goldens live under `testdata/golden/tui-*.txt`. `testdata/faux/slow.yaml` (a
response behind a `hold: turn` step the test releases) gives the busy-state test
something to observe while a turn is still running.

A few of these tests carry comments describing real, reproduced bugs found while
writing them, rather than routing around them: a modal panel whose key-hint and
status rows were silently clipped by an undersized height budget, and a model
selection that hung the whole program. See the comments at each call site, and
the suite's own report, for the repro.

`assertGoldenStyles(t, s, name, opts)` (`test/e2e/tui_test.go`) is the styled
counterpart to `assertGoldenTail`/`assertGoldenNormalizedBanner`. It encodes
`s.Viewport()`/`s.Styles()` per row via `screen.EncodeStyledRow` (same encoding
as §5 below) and compares against `testdata/golden/<name>.styles.txt`, after
`opts` normalizes volatile rows (the banner's cwd/branch row, the live spinner's
cycling frame) the same way the plain-text goldens do. A masked row's *styling*,
not just its text, therefore never reaches the golden.

**Determinism knobs.** `startTUI` sets two environment variables on every driven
process so PTY goldens are reproducible:

- `HARNESS_RETRY_JITTER=0` (`internal/harness/retry.go`) disables retry backoff's
  random jitter. A scripted retry's countdown is then the deterministic base
  delay, not a race against a random near-zero one.
- `HARNESS_TEST_CLOCK` (RFC3339 timestamp; read by `clockOverride` in
  `internal/tui/footer.go`, `NewModel` in `internal/tui/app.go`, and `toolMeta`
  in `internal/tui/bridge.go`) freezes the footer's and model's `StartedAt` and
  forces every tool call's elapsed-time meta to read a fixed `1.0s`, instead of
  real wall-clock skew.

`testdata/drive/tui-smoke.txt` reproduces the fix-bug flow by hand through
`cmd/kiln-drive` instead of a Go test, for a person (or another agent) driving
the real binary interactively:

    go build -o bin/kiln ./cmd/kiln
    go build -o bin/faux ./cmd/faux
    ./bin/faux testdata/faux/fix-bug.yaml &   # prints its listen address, e.g. 127.0.0.1:54321
    go run ./cmd/kiln-drive --bin bin/kiln \
      --env HARNESS_FAUX_ADDR=127.0.0.1:54321 \
      --env HARNESS_FAUX_API=anthropic-messages \
      --env HARNESS_MODEL=faux/faux-1 \
      -- --permission-mode bypassPermissions \
      < testdata/drive/tui-smoke.txt

## 5. Styles

`Screen.Styles()` in `internal/testkit/screen` reads the x/vt emulator's cell
buffer (fg, bg, bold, dim, italic, underline, reverse per cell).
`screen.EncodeStyledRow`/`EncodeStyledScreen` render it as a compact golden
encoding: runs of cells sharing one style become one span,
`[fg=#d97757 b]text[/]`, and an unstyled span is plain text with no brackets.

Token vocabulary: `fg=#rrggbb`, `bg=#rrggbb`, `b` bold, `d` dim/faint, `i`
italic, `u` underline, `r` reverse. `[`, `]` and `\` in text are escaped as
`\[`, `\]` and `\\`.

`Screen.GoldenStyles(t, name)` compares against `testdata/golden/<name>.styles.txt`
(`UPDATE=1` to write). `kiln-drive` exposes the same encoding over the line
protocol as `SCREEN --styles`.

`cmd/vtreplay` replays a `.rec` recording (see "Record/replay" below) through the
same VT emulator, sized at `--cols`/`--rows`, and prints the final screen in the
same styles encoding. `--frames` also prints the screen immediately before each
`\x00IN:` marker is applied. A NUL byte followed by `IN:` marks a keystroke. The
marker's text always ends at the next ESC (`0x1b`) byte, because every observed
PTY response opens with one; this is a parsing assumption, not a documented
format guarantee.

kiln's own colour palette and glyph table are documented in
`docs/kiln-design.md`, the rendering contract this suite's TUI goldens back. Use
that doc, not a recording-derived colour table. kiln has no upstream reference
recordings of its own: the ones this project used to diff against, captured from
Claude Code, were retired along with the visual-parity suite below.

## Visual drive — `scripts/visual-drive.sh`

Emulator goldens cannot show what a real terminal does with the palette, the
font, or the user's background. `scripts/visual-drive.sh` runs the real
`bin/kiln` in a new iTerm2 window (the default profile, so the user's own colours
and font), drives it from a step file, and screenshots named scenes:

    scripts/visual-drive.sh --faux testdata/faux/design-session.yaml \
        --fixture testdata/behaviour/design \
        --steps testdata/drive/design-visual.txt --out /tmp/shots

It is safe by construction: kiln runs with an empty `HOME` (no
`~/.claude/settings.json`, nothing written to `~/.harness`), `HARNESS_OFFLINE=1`
(any provider other than faux or ollama is refused), `--model faux/faux-1`, and
the deterministic `HARNESS_TEST_CLOCK` / `HARNESS_RETRY_JITTER=0` knobs.

The step file takes `SEND`, `TYPE`, `KEY`, `WAIT`, `SHOT` and `RELEASE <hold>`
lines; `testdata/drive/design-visual.txt` walks the design handoff's session. The
script closes its window and stops faux when done. It needs macOS with iTerm2 and
Screen Recording permission for the terminal that runs it.

## Real-terminal QA — `make qa`

The emulator cannot show fonts, glyph widths, colour on the actual background, or
how a terminal encodes keys. `make qa` drives the real `bin/kiln` through
`qa/scenarios/<area>/<name>.steps` files in a real terminal window and saves a
screenshot and the screen text for every `SHOT`:

    make qa                                   # the @smoke set, iTerm2 dark
    make qa ALL=1                             # every scenario but qa/scenarios/real
    make qa REAL=1                            # every scenario, real model included
    make qa SCENARIO=permissions/bash-deny-feedback-tab
    make qa TERMINAL=iterm-light              # iterm-dark, iterm-light, terminal, warp
    make qa-lint                              # check steps and faux scripts only

`@smoke` marks the scenarios that depend on the terminal itself (key encoding,
wheel, resize, background detection, fonts). Headless CI covers the rest on every
PR.

Requirements: macOS, the terminal under test, Orca (its `orca computer` actions
deliver every key, click and capture; AppleScript only creates, sizes, activates
and closes windows), and `yq` for `qa-lint`.

Output goes to `qa/runs/<timestamp>/<terminal>/<scenario>/` (gitignored):
`NN-<shot>.png`, `.txt` (visible screen), `.buffer.txt` (terminal scrollback),
`run.log` and `result.json`. A run is safe by construction: faux model,
`HARNESS_OFFLINE=1`, a scratch `HOME` and project per scenario.

Step files:

- Directives: `@faux`, `@fixture`, `@size`, `@args`, `@env`, `@untrusted`,
  `@nogit`, `@pre`.
- Verbs: `TYPE`, `KEY`, `CLICK`, `RESIZE`, `WAIT`, `WAITFOR /re/ [s]`,
  `WAITGONE /re/ [s]`, `SUBMIT_UNTIL /re/ s text` (resubmit a snapshot view such
  as `/mcp` until it matches), `RELEASE name`, `SHOT`, `EXPECT /re/`,
  `EXPECT_NOT /re/`, `NOTE`.
- `@real` scenarios add `SEND`, `IDLE`, `TURN`, `RUN`, `RELAUNCH` and
  `EXPECT_PROMPTS max`. `EXPECT_PROMPTS max` fails when IDLE answered more
  permission prompts than `max`, and lists each prompt's question and shot
  (`real/wf-auto-mode` asserts 0).

The full reference is the docstring of `scripts/qa/drive.py`. Faux scripts for
scenarios live in `testdata/faux/qa/`; `end_turn: true` closes a turn and
`chunk_delay` paces a streamed reply (`internal/testkit/faux/doc.go`). To keep a
turn busy while a scenario acts, use a `hold: <name>` step and release it with
`RELEASE <name>` instead of a long `delay:`.

Review every screenshot, not just `result.json`: an `EXPECT` sees text, while the
screenshot shows colour, spacing and glyphs. Compare against the design scenes in
`qa/reference/` (rendered from `docs/kiln-design-handoff/Terminal.dc.html`).
Record each defect as one file with `scripts/qa/findings.py new` (see its
`--help`). `findings.py list --status open` shows what is left, and
`findings.py set <id> --status fixed` records the fix, its regression test and
the after-shot.

Limits: Orca's `scroll` does not deliver wheel events, so `SCROLL` steps fail.
Under kiln's kitty keyboard protocol, Orca's synthetic Option/Ctrl+arrow events
arrive without their modifier (a physical keypress works). Warp has no screen
text, so its checks run on OCR and are advisory.

## Headless QA — `make qa-headless`

Most of `qa/scenarios/` (everything except `qa/scenarios/real/`, which drives a
real model interactively) also runs against `--terminal xterm-dark` or
`xterm-light`. That is a real PTY running `bin/kiln`, rendered by xterm.js in
headless Chromium via Playwright instead of a real terminal window
(`scripts/qa/headless.py`; the static page is `scripts/qa/xterm/`). It needs no
window manager, no Orca and no macOS, so it runs in CI
(`.github/workflows/qa-headless.yml`) and locally on a Mac or Linux box.

Input still goes through a real key encoder (xterm.js's own, driven by
Playwright's `keyboard`/`mouse` API), never bytes written straight to the PTY.
`.claude/rules/real-terminal-drives.md` states the same rule for the Orca path.

**What it covers**: startup, commands, dialogs, content rendering, input, modes,
palette, permissions, queueing, resize, scroll (xterm.js's own mouse wheel
reporting, matching kiln's SGR mouse mode) and tools. The `.steps`/`EXPECT`/`SHOT`
scenario format is unchanged from the macOS driver.

- xterm.js has no kitty keyboard protocol, so shift+enter is plain enter. Mark a
  scenario that depends on it `@requires kitty-keyboard`; headless runs print
  `SKIP` for it.
- Input returns once kiln's output has been quiet for 120ms (at most 1s),
  standing in for a real terminal's input latency. Still use WAITFOR, not
  EXPECT, for anything kiln draws after a delay.

**What it cannot cover** (needs a real terminal: `make qa`):

- A real terminal's own key encoder quirks. Terminal.app's, iTerm2's and Warp's
  can each differ from xterm.js's for a given key combo; the kitty keyboard
  protocol limitation above is one of these.
- 256-colour rounding specific to Terminal.app's palette.
- Natural-scroll direction on macOS.
- Real window chrome (titlebar, traffic lights, Dock interaction).
- Font rendering exactly as the user's own terminal and font would draw it.
  Headless Chromium uses `@fontsource/jetbrains-mono`, pinned in
  `scripts/qa/xterm/package.json`, plus the OS's own fallback font for glyphs
  outside that font's subsets (box-drawing and some arrow glyphs are outside
  it). This was verified visually against iTerm2's rendering rather than assumed;
  see this feature's own delivery report for the comparison.
- Programming ligatures ("===", "!==", ...). They are disabled on purpose
  (`font-variant-ligatures: none` in `scripts/qa/xterm/index.html`). The
  reference iTerm2 screenshots this backend was checked against render them fused
  into one wide glyph (the user's own iTerm2 font has ligatures on), which reads
  as a rendering defect in a screenshot meant to show the actual characters. A
  real terminal's own ligature setting is still worth checking separately if it
  ever matters to a scenario.

**OSC 11 (background colour detection)**: kiln asks the terminal for its
background colour (`tea.RequestBackgroundColor` in `internal/tui/app.go`) and
adapts its surface tokens to it (`SetTerminalBackground` in
`internal/tui/theme.go`) within a 30ms grace window (`bannerBackgroundGrace`).
xterm.js does not answer this query on its own (checked against its source: it
fires an internal event, not a wire reply). So the bridge in
`scripts/qa/headless.py` answers it directly against the raw PTY byte stream,
before Chromium has even launched. Answering from the page would add a WebSocket
and browser round trip that risks missing the 30ms window entirely.

**Local setup** (gitignored, run once per checkout). Use Python 3.12, the version
CI uses: the pinned Playwright 1.48 depends on greenlet, which has no Python 3.14
wheel and fails to build from source there.

```bash
python3.12 -m venv .venv-qa
.venv-qa/bin/pip install -r scripts/qa/requirements.txt
.venv-qa/bin/python -m playwright install chromium   # add --with-deps on a fresh Linux box
cd scripts/qa/xterm && npm ci && cd -
```

Then, with `bin/kiln` and `bin/faux` built (`make build && go build -o
bin/faux ./cmd/faux`):

```bash
.venv-qa/bin/python scripts/qa/drive.py --terminal xterm-dark qa/scenarios/startup/smoke.steps
make qa-headless                          # every non-real scenario, xterm-dark, -j 4
make qa-headless TERMINAL_HEADLESS=xterm-light J=1
```

`-j N` (headless terminals only) spawns one `drive.py` subprocess per scenario, up
to `N` at a time. Playwright's sync API is not thread-safe, so this is real
process parallelism, not threads sharing one Chromium. `--shard i/n` runs only the
i-th of n deterministic shards of the given scenario list (the CI matrix uses it
to split the suite across jobs).

## Record/replay via `kiln-drive --record`

Like the TypeScript layer's `HARNESS_RECORD_TTY`/`npm run replay`, `kiln-drive`
supports `--record <path>`. It captures to `<path>` the exact byte stream the PTY
produced, plus input markers for injected `SEND`/`KEY` commands. A bug seen once
on a real run can then be replayed deterministically, without the original model
conversation. It can be replayed at a different terminal size too, which is often
when layout bugs reproduce.

## Design-scene suite — `test/e2e/tui_design_test.go`

`test/e2e/tui_design_test.go` drives every scene the design handoff describes
(welcome, plan, agents, streaming, diff, permission, error, palette, context,
done) from `testdata/faux/design-session.yaml` through the real, PTY-attached
binary. It reuses `tui_test.go`'s own helpers (`startTUI`, `waitReady`,
`waitTurnSettled`, `assertGoldenTail`, `assertGoldenStyles`,
`submitSlashCommand`, `loadFauxScript`).

Its own fixtures live under `testdata/behaviour/design/` (a scratch project:
`src/routes/upload.ts`, `test/helpers/redisMock.ts`, `package.json`,
`scripts/npm`, `.claude/agents/scout.md`), copied fresh per test by
`designProject`/`copyFixtureTree`. Goldens are `testdata/golden/design-*.txt` and
`.styles.txt`.

- `driveDesignTo(t, s, scene)` submits the design task and drives the session up
  to (and including) the named scene's own anchor text. It answers whatever
  prompts come up along the way (the bash permission prompt with `1`, the
  mid-stream disconnect with `r` to retry now). Scene anchors are plain
  substrings unique to each scene's first on-screen signal (e.g.
  `designAgentsAnchor = "subagents running in parallel"`,
  `designErrAnchor = "Retrying in"`). It does not wait for every scene
  independently: some intermediate scenes (`diff1`, the reconnect note) reliably
  scroll out of the visible viewport before a later scene's own wait would catch
  them (documented in its doc comment, confirmed by a timing probe). A test that
  needs one of those reads `s.Scrollback()` instead of the visible screen.
- `designSortSubagentPanel` sorts the two design-session scouts' panel row pairs
  into a fixed order before golden comparison. Two `task` dispatches fired from
  one assistant message really do start on two independent goroutines, so which
  scout's start event the panel sees first, and therefore which row it occupies,
  is a genuine, harmless race, not a bug worth pinning down inside `internal/tui`
  itself. `assertDesignGoldenTailSorted`/`assertDesignGoldenStylesSorted` apply
  this sort before diffing against the golden.
- `testdata/drive/design-session.txt` is the hand-driven `kiln-drive` script
  version of the same full session, for a person (or another agent) to replay
  interactively, as `testdata/drive/tui-smoke.txt` does for the fix-bug flow.
- `TestTUI_Design_ExitAfterFullSession` is a regression test for a real,
  reproduced shutdown deadlock: a duplicate `SIGINT` handler in `RunInteractive`
  racing bubbletea's own internal one. It was found when this script's
  `kiln-drive EXIT` timed out roughly 1 run in 3. The test drives the full "done"
  scene, then asserts `Exit()` returns well under the screen's timeout instead of
  hanging.

`test/e2e/crash_unix_test.go` builds its own kiln with `-tags kiln_crashtest`, the
only build in which `crash.TestPoint` panics (where `KILN_CRASHTEST_PANIC` names
it). It checks that a panic on kiln's own goroutine leaves the terminal restored,
a crash report in the scratch HOME and its path on stderr.

## 6. Behaviour suite — `test/e2e/*_behaviour_test.go`

`test/e2e/budget_behaviour_test.go`, `compaction_behaviour_test.go`,
`mcp_behaviour_test.go`, `permission_behaviour_test.go`,
`subagent_behaviour_test.go`, `tools_behaviour_test.go` and
`tui_behaviour_test.go` drive the real, compiled `kiln` binary end to end to prove
*wiring*, not the decision tables underneath it. Each file's header names the
package that already unit-tests the table itself. For example, permission's
deny/bypass/allow/ask ordering is unit-tested in `internal/claude/settings` and
`internal/claude/permission`; `permission_behaviour_test.go` only proves that
`settings.json`, `--permission-mode`, `--add-dir` and the TUI's allow-always
prompt reach that table correctly.

`tui_behaviour_test.go` covers retry/esc/queue/declined behaviour end to end
through the PTY, reusing `tui_test.go`'s helpers the same way. Its goldens carry
a `tui-behaviour-` prefix.

`internal/tui` also has package-local behaviour tests (`app_behaviour_test.go`,
`bridge_behaviour_test.go`, `note_behaviour_test.go`). They are unit-level, with
no PTY, driving `Model.Update`/`Bridge.handleEvent` directly against a fake commit
sink, one layer below the PTY suite above.

Fixtures for these tests live under `testdata/behaviour/`: `mcp/` (e.g.
`rewrite-echo-text.sh`, a hook script) and `subagents/` (`reader.md`,
`sonnetagent.md`, subagent definitions a scenario dispatches with `task`).

Scenario-shaped fixtures (this suite's and `eval/`'s) are loaded through
`internal/testkit/scenario`, a loader-only package. It reads `scenario.yaml` plus
whatever fixture, faux script and settings files sit beside it, and has no opinion
on how the scenario is run or graded. Both `test/e2e` and `internal/eval` depend
on it without pulling in faux, screen, or a build tag either side would rather not
share.

Tests that need a faux model server start one through `internal/testkit/fauxtest`,
the shared helper that replaced four near-identical copies across `internal/cli`,
`internal/provider/api` and `test/e2e`. `fauxtest.Start(t, scriptYAML)` starts a
scripted `internal/testkit/faux` server, registers a `t.Cleanup` to close it, and
returns its listen address plus the `*faux.Server` itself for
`Requests()`/`Reset()`/`LoadScriptYAML()`. It takes `testing.TB`, so benchmarks
can use it too.

Two fault-injection steps in a faux script are worth knowing when writing a test
against them:

- `disconnect_after`: on a text/thinking step or as its own step, ends the
  response mid-stream after a given number of response body bytes (an integer) or
  after a given delay (a duration string, e.g. `200ms`). Like a `tool_call` step,
  it always ends its own turn.
- `raw_args`: on a `tool_call` (or an entry inside `tool_calls`), splices a raw
  string into the tool call's arguments verbatim instead of a JSON object, for
  exercising malformed or partial tool-call-argument handling. `args` and
  `raw_args` are mutually exclusive; a script setting both fails to load.

`internal/testkit/mcpfixture`'s `Server` keeps an in-memory log of every tool call
it receives (in-process callers only, not the `cmd/mcpfixture` stdio binary, which
runs out of process). `Requests()` returns that log in order and `Reset()` clears
it, so a behaviour test can assert exactly which MCP tool calls a run made.

## 7. Eval runner — `kiln eval run` / `kiln eval report`

`kiln eval` (`cmd/kiln/eval.go`, logic in `internal/eval`) runs the built `kiln`
binary against a directory of scenarios and grades each run.

```bash
bin/kiln eval run --only fix-bug -j 4
bin/kiln eval report --results eval/results --format md
```

`eval run` flags worth knowing:

- `--scenarios` (default `eval/scenarios`), `--fixtures` (default
  `eval/fixtures`), `--results` (default `eval/results`).
- `--models`: comma-separated `provider/model` list, default `faux/faux-1`.
- `--roles`: a JSON file or `k=v,k=v` list; defaults to the machine's own
  `modelRoles`.
- `--repeat`, `-j` (parallel workers), `--keep` (keep per-run artifacts under
  `<results>/runs/`).
- `--judge`: provider/model to grade live runs against a scenario's `judge:`
  rubric.
- `--only`: comma-separated scenario name/tag filter.

`eval report` flags: `--results`, `--target` (default: newest results file),
`--baseline` (default `<results>/baseline.jsonl`), `--last`, `--format`
(`text`/`md`/`json`) and `--fail-on-regression`.

`eval/scenarios/` holds one subdirectory per scenario (`scenario.yaml` plus its
prompt, checks and optional `judge:` rubric). `eval/fixtures/` holds the starting
project state each scenario runs against. `eval/results/` receives one timestamped
JSON-records file per `eval run` plus the committed `baseline.jsonl` that
`eval report` diffs against. Everything else under `eval/results/` is
machine-local and gitignored.

Live runs (real providers, real network) are opt-in: `eval run` refuses any
non-faux model unless `KILN_EVAL_LIVE=1` is set in the environment. `make eval`
never leaves the faux provider, and `make eval-live` sets it explicitly.

**Judging.** A scenario that declares a `judge:` rubric is scored one of two ways,
depending on the model under test:

- **Faux runs** are always scored against the scenario's own
  `judge.faux_verdict`, a canned `{"score": ..., "reasons": [...]}` written
  directly in the scenario's YAML, regardless of any `--judge` flag. This keeps
  `make eval` fully offline and deterministic.
- **Live runs** are scored by asking a real judge model (`RunJudge` in
  `internal/eval/judge.go`) to grade the run's prompt, final text, diff and tool
  calls against the rubric in one completion, parsed as the first balanced JSON
  object in its response. If `--judge` is not given, `kiln eval run` defaults the
  judge to the resolved `fast` model role (`roles["fast"]`). If that role is also
  unset, live runs go unjudged (mechanical `checks:` still run either way).

## 8. Coverage — `make cover`, `make covercheck`, `.coverage-floors`

`make cover` runs the whole module's tests under a coverage profile
(`go test -coverprofile=coverage.out -covermode=atomic ./...`), prints the
module-wide total, then prints `cmd/covercheck`'s per-package table
(`covercheck -profile coverage.out -report`).

`cmd/covercheck` (`covercheck -profile <coverprofile> [-report] [-floors <path>]`)
parses a Go coverprofile and aggregates statement coverage per package. `-report`
just prints the table. `-floors <path>` instead enforces a `.coverage-floors` file,
a `<package>\t<floor>` list: a listed package whose measured coverage falls below
its floor fails the check (exit 1), while a package not listed is reported but
never fails. The floor file is an opt-in ratchet, not a blanket requirement.

`make covercheck` runs `make cover` and then enforces `.coverage-floors` this way
(`go run ./cmd/covercheck -profile coverage.out -floors .coverage-floors`).

## 9. `make race` vs `make race-full`

`make race` is the fast race layer run in CI on every push/PR:
`go test -race -short ./...`. `-short` skips the one test known to take minutes
under `-race` (the sqlite FTS test in `internal/search`). `make race-full` runs
the same suite without `-short`, including that test, and is reserved for a
nightly run rather than every push.

## 10. Fuzz targets

Run any fuzz target with:

```bash
go test -run '^$' -fuzz <Name> -fuzztime 30s ./pkg/
```

Targets in the tree as of this writing:

| Target | Package |
|---|---|
| `FuzzParseAgent` | `internal/claude/agents` |
| `FuzzSplitFrontmatter` | `internal/claude/commands` |
| `FuzzApplyArguments` | `internal/claude/commands` |
| `FuzzParseSkill` | `internal/claude/skills` |
| `FuzzParseScriptYAML` | `internal/testkit/faux` |
| `FuzzOpen` | `internal/session/jsonl` |
| `FuzzNativeStream` | `internal/provider/ollama` |
| `FuzzParseSequence` | `third_party/ultraviolet` (vendored, not this project's own code) |

This list is a snapshot. Fuzz targets are added over time, so check
`grep -rl 'func Fuzz' --include=*_test.go .` for the current set before relying on
it.

`TestBashDifferential` (`internal/claude/settings`) is a seeded differential test,
not a `Fuzz` target. It generates bash command lines, runs each in real bash with
logging stub commands on `PATH`, and fails when bash runs a command kiln's bash
analysis did not collect. 300 cases run in `make check`; run more with
`KILN_BASH_FUZZ=<n>` and another seed with `KILN_BASH_FUZZ_SEED=<s>`. It skips
when `bash` is missing.

## 11. Benchmarks

Recorded results and methodology live in `docs/benchmarks.md`. Run any benchmark
directly with:

```bash
go test -bench . -benchmem -run '^$' ./pkg/
```

The `request_tokens` mechanical eval check accepts `max` and estimates the first
faux request's complete serialized body, including tool declarations. Unlike
`system_prompt_tokens`, it catches schema and description overhead. It is a
bytes/4 wire-size estimate, not provider tokenization or billed usage, and is
skipped for live providers.

Session cost totals now include task-worker usage, recursively, committed with
each tool result. Auxiliary usage does not replace the latest conversational
request's context-occupancy measurement.
