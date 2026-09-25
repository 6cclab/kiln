# kiln: optional full-screen (alt-screen) TUI mode — plan

> Status: proposed / not yet built. kiln renders inline today (blocks commit to native
> scrollback, small live region at the bottom). This documents the plan for an opt-in
> full-screen mode. Inline stays the default.

## Context
Full-screen mode is an alt-screen TUI with an internal scrolling transcript viewport and a fixed
input + status bar at the bottom — the layout the kiln design mockup shows, and how Claude Code
behaves. It also resolves the inline model's "dynamic gap / bottom-pin" limitation. It is
**opt-in** (a `--fullscreen` flag plus a runtime toggle keybind); inline stays the default so
nothing regresses and the existing inline cursor/autowrap fixes still apply to the default path.

## Approach
Add a full-screen render path alongside the inline one, selected by a `fullscreen bool` on the
model. Reuse the existing renderers — only the *composition* and the *commit target* change.

1. **Frame = alt-screen.** Bubbletea already supports a per-frame `View.AltScreen` field
   (`third_party/bubbletea/cursed_renderer.go` handles enter/exit). In fullscreen, `View()`
   returns `AltScreen: true` and composes one full-terminal frame:
   ```
   [ transcript viewport            (fills height - bottomRows)      ]
   [ busy/spinner line (when busy)  ]
   [ prompt/dialog OR input box     ] fixed bottom region
   [ statusline (kiln-dim) + mode   ]
   ```
2. **Transcript viewport.** Use `charm.land/bubbles/v2/viewport` (v2.2.1, already available).
   Content = the rendered transcript via `RenderTranscriptEntries(entries, width, verbose, cwd,
   summary)` (`internal/tui/replay.go`) — the same function Ctrl+O uses — then
   `viewport.SetContent(strings.Join(...))`. The in-progress turn's live blocks (streaming
   assistant text, running tool group, thinking) are appended to the content on each re-render,
   as the inline live region shows them today, so streaming still updates in place.
3. **Commit target switch.** In fullscreen, finished blocks must NOT `tea.Println` into native
   scrollback (alt-screen has none). Add a mode to `Bridge` (`internal/tui/bridge.go`): when
   fullscreen, `Commit` appends the rendered lines to an in-memory `transcript []string` buffer
   (via a message) or triggers a re-render from the session log. The single ordered-commit
   goroutine stays; only its sink changes. `committedRows`/`tea.Println`/startup-filler logic is
   inline-only and skipped in fullscreen.
4. **Scroll + auto-follow.** The viewport handles PgUp/PgDn/mouse-wheel and arrow/`j`/`k` when
   the input is empty. Auto-scroll to bottom on new content **only when already at the bottom**
   (`viewport.AtBottom()` → `GotoBottom()`); if the user scrolled up, hold (scroll-to-pause).
   Route these keys in `keys.go`/`app.go` before the editor, guarded by fullscreen + empty input.
5. **Resize.** On `WindowSizeMsg`, recompute viewport height (`terminal height − bottomRows`) and
   re-render the transcript at the new width, then `SetContent`. History re-wraps to the new
   width (better than inline, which cannot).
6. **Bottom region.** Reuse `liveLines`' pieces (spinner `RenderSpinner`, prompts/dialogs via
   `m.prompt.Render`/`dialogRows`, editor `View`, recolored statusline + `renderModeLine`) placed
   as a fixed-height block below the viewport instead of after committed scrollback.
7. **Toggle.** `--fullscreen` flag (`internal/cli/args.go`, wired in `internal/cli/tui.go` →
   `Config`) and a runtime keybind (a new action in `internal/tui/keys.go`, e.g. `ctrl+f`) that
   flips `m.fullscreen`, enters/exits alt-screen on the next frame, and re-renders.

## Key files
- `internal/tui/app.go` — `fullscreen bool` + `viewport` on `Model`; `View()` branches to a
  `fullScreenView()` composer; `Update` handles toggle, scroll keys, fullscreen resize; gate the
  inline-only `committedRows`/filler code on `!m.fullscreen`.
- `internal/tui/bridge.go` — commit-sink mode (append-to-buffer vs `tea.Println`).
- `internal/tui/replay.go` — reuse `RenderTranscriptEntries` (verify it covers every block type
  shown live).
- `internal/tui/keys.go` — toggle action + viewport scroll routing.
- `internal/cli/args.go`, `internal/cli/tui.go` — `--fullscreen` flag → `Config.Fullscreen`.
- `third_party/bubbletea/*` — no change expected (AltScreen already supported); confirm
  enter/exit through the driver.
- Dep: `charm.land/bubbles/v2/viewport` (already in the module graph via bubbles v2.2.1).

## Risks / decisions to settle during build
- **Streaming vs viewport**: render the in-progress turn as the content tail and `GotoBottom`
  while streaming and at-bottom, so it doesn't fight auto-follow.
- **Re-render cost**: `RenderTranscriptEntries` over a long session on every event is O(n);
  acceptable to start (Ctrl+O already does it), optimize later by caching committed content and
  appending only new blocks.
- **Mouse**: wheel scroll needs mouse mode on in fullscreen; confirm it doesn't break paste.
- **Plain/screen-reader mode**: fullscreen falls back to inline under `--ax-screen-reader`.

## Verification
- Drive the built binary through `internal/testkit/screen` with `--fullscreen`: transcript fills
  the screen, input+statusline pinned at the bottom, a fix-bug turn streams and auto-follows,
  PgUp pauses auto-follow and PgDn resumes, resize re-wraps history, `/model` + permission prompt
  render in the bottom region, toggle key flips in/out of alt-screen cleanly.
- Inline mode (default, no flag) unchanged: existing `make e2e` PTY goldens still pass.
- `make check` + `make e2e` green; add fullscreen-specific PTY goldens and a scroll-pause
  assertion.
