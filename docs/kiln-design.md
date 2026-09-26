# kiln rendering contract

This is kiln's own rendering contract — the "Ruled" layout (Layout 1b) from
the design handoff (`docs/kiln-design-handoff/README.md`, copied into this
repo for reference; the original bundle also has an interactive HTML
prototype and screenshots that aren't reproduced here). Kiln has its own
palette, glyphs and block layout, applied on top of an inline-scrollback
renderer (blocks commit to native terminal scrollback via `Bridge.Commit`
→ `Program.Println`; a small live region — spinner, prompts, input box,
status line — redraws at the bottom every frame).

Where this doc and the code (`internal/tui/theme.go`, `internal/tui/status.go`,
`internal/tui/spinner.go`, `internal/tui/transcript.go`, `internal/tui/note.go`,
`internal/tui/context.go`, `internal/tui/plan.go`, `internal/tui/subagents.go`,
`internal/tui/retry.go`, `internal/tui/permission_render.go`,
`internal/tui/autocomplete.go`, `internal/tui/replay.go`, `internal/cli/tui.go`)
disagree, the code wins — this is a snapshot, not the source of truth.
`docs/claude-code-parity.md` keeps the *behavioral*/feature checklist
(keybindings, CLI flags, permission-mode semantics, hooks, MCP) that is
unaffected by this visual change. This doc describes only what
`internal/tui`/`internal/cli` render today; the design handoff
(`docs/kiln-design-handoff/README.md`) is a wishlist and is not touched by
this doc or by this pass.

## Palette (truecolor)

Every hex value below is read directly from `internal/tui/theme.go`'s
`hex*` constants.

| token | hex | use |
|---|---|---|
| ink | `#ece4d4` | primary text |
| dim | `#a39781` | secondary text, labels, meta, statusline (`Muted`) |
| faint | `#7d7262` | line numbers, todo glyph, unselected keys |
| amber | `#e9a64b` | accent: prompt, running, `you` label, approvals, KILN wordmark |
| green | `#9bc46e` | success, additions, done |
| red | `#e5765d` | errors, removals |
| blue | `#86b4d4` | edit label, plan mode |
| violet | `#c3a3d6` | context "Tools" segment |
| rule | `#2f2920` | block label hairlines, empty meter, diff borders |
| rule-strong | `#3a3228` | input box rules |
| panel | `#1c1813` | diff header background |
| raise | `#241f18` | user-message bg, `$cmd` bg, selected-row bg |
| diff add bg | `#232619` | diff `+` line background |
| diff del bg | `#2f1c15` | diff `−` line background |
| bar-empty | `#3f372c` | subagents panel: progress-bar empty cell |

kiln does **not** set a global terminal background: inline mode does not own
the whole screen. Only the `raise`/`panel`/diff-line/bar-empty background
tints apply, as per-span SGR, where the design uses them.

## Glyphs

From `internal/tui/theme.go`'s `UnicodeGlyphs` table:

| glyph | meaning |
|---|---|
| `›` | prompt / user mark (`UserMark`) |
| `•` | text bullet (`Text`) |
| `·` | note (`Note`) |
| `±` | diff label (`Diff`) |
| `?` | approval (`Approval`) |
| `≡` | plan label (`Plan`) |
| `∥` | subagents label (`Subagents`) |
| `!` | error label (`Error`) |
| `◧` | context label (`Context`) |
| `✓` | ok (green, `OK`/`TodoDone`) |
| `✕` | error / declined (red, `Fail`) |
| `▸` | plan-current item (amber, `PlanCurrent`/`TodoActive`) |
| `○` | plan-todo item (faint, `PlanTodo`/`TodoPending`) |
| `━` | meter-filled (`MeterFull`) |
| `─` | meter-empty / hairline rule (`MeterEmpty`) |
| `■` | interrupted / legend segment (`Segment`) |
| `▍` | streaming caret, amber (`StreamCaret`) |
| `◐ ◓ ◑ ◒` | spinner frames, 140ms per frame (`Spinner`) |
| `↺` | reconnect (`Reconnect`) |
| `→` | tool output / subagent action (`Action`) |
| `⏺` | `Call` — the marker on a collapsed running-Bash-group row, and on the model-switch / subagent-dispatch notes `bridge.go` commits |
| `⎿` | `Result` — the continuation glyph under a multi-line slash-command result (`CommitCommandResult`) |
| `∴` | `Thinking` — verbose mode's `∴ Thinking`/`∴ Thinking…` row (collapsed mode shows nothing; the spinner's own "thinking with effort" suffix carries that state instead) |
| `✻` | `Summary` — carried over from the pre-kiln glyph set; not referenced by any renderer today |
| `⇧⇥` | cycle-mode hint (status.go literal, not a `Glyphs` field) |

`ASCIIGlyphs` (`internal/tui/theme.go`) is the plain-mode/screen-reader
fallback: every glyph above has an ASCII equivalent (e.g. `›` → `>`, the
spinner → `-\|/`, `✓`/`✕` → `+`/`x`), used under `--ax-screen-reader` and
`SetPlainMode(true)`.

## Banner

No label rule (it's not a transcript block, `internal/cli/tui.go`'s
`bannerRows`); sits above a `─` rule, pinned near the bottom of the screen
on startup with blank filler above it:

```
K I L N  v0.9.2 · coding agent

~/src/relay-api · branch main · model kiln-large

/ commands   @ add files   ⇧⇥ cycle mode   esc stop

Recent sessions
  2h ago   Fix the null pointer in the upload handler
```

- Row 0: `K I L N` (spaced letters) in amber bold, then dim
  `v<version> · coding agent` (`v` prefix only when the version string
  starts with a digit; a non-numeric build string like `dev` renders bare).
- A blank row, then row 2: dim — `<cwd, ~-abbreviated>`, ` · branch `,
  the branch name (only when git status was read successfully), ` · model
  `, the model label. There is no separate "effort" segment on this row —
  `bannerRows` builds exactly `loc + " · model " + modelLabel`.
- A blank row, then the shortcut-tips row: keys in amber, descriptions
  dim (`/ commands`, `@ add files`, `⇧⇥ cycle mode`, `esc stop`).
- Unless this run resumed a session (`--resume`/`--continue`), a blank row,
  `Recent sessions` (dim), then up to 3 rows of `"<age> ago"` (dim,
  10-column padded) + the session's first user message, single line,
  truncated to 60 columns (`firstUserMessageTitle`). Rows are read from
  `internal/session/jsonl`'s repo for this cwd, newest-modified first, with
  a 300ms timeout — on timeout, on any read error, or when there are no
  sessions in this folder, the whole "Recent sessions" block is omitted
  silently (`recentSessionRows`/`buildRecentSessionRows`).
- A closing blank row; the caller pins a full-width `─` divider and the
  input box below that.

## Input box

A `─` rule in `rule-strong` (`#3a3228`) above and below the input
(`internal/tui/editor/view.go`'s `View`/`rule`):

```
────────────────────────────────────────────────
❯ describe a task · / for commands · @ to add files
────────────────────────────────────────────────
```

- Marker glyph `❯` (`editor.Styles.Marker`) in its own colour
  (`MarkerStyle`, amber), then one space, then the content. Wrapped/typed
  continuation rows (a second logical line, or one long line wrapped by
  the textarea) get a plain two-space indent, no repeated marker.
- Placeholder text (dim), set by `app.go`'s `syncPromptPlaceholder`,
  depending on state:
  - idle: `describe a task · / for commands · @ to add files`
    (`editor.DefaultPlaceholder`).
  - busy (a turn is running, no prompt open): `queue a follow-up, or esc
    to stop`.
  - a permission or plan prompt is open, not currently capturing feedback
    text: `press N, N or N` for the prompt's actual option count — `press
    1, 2 or 3` for a generic/edit/write/plan prompt (3 options), `press 1,
    2, 3 or 4` for the Bash prompt's extra "switch to auto mode" option
    (4 options) — built by `placeholderForOptionCount`, so any future
    option-count change is covered without a hardcoded per-tool check.
  - a prompt is open and capturing free-text feedback (option 3, "tell
    kiln what to do instead"): `press 1, 2 or 3` as well (the feedback
    line itself is the prompt block's own input row, not the editor).
- A scroll hint (`↑ N more` / `↓ N more`) is spliced into the top/bottom
  rule when the textarea's content overflows its viewport
  (`editor/view.go`'s `scrollHints`).

## Status line

Below the input box, one row, always rendered (`internal/tui/status.go`'s
`RenderStatusLine`), unless the shortcuts panel, a dialog, a prompt, or the
verbose-transcript notice is occupying that slot instead (`app.go`'s
`liveLines`/`renderStatusRow`):

```
● auto-edit  ⇧⇥                      ~/src/relay-api · main*   ctx ━━━━──────  38%  $0.42
```

Segments, left to right:

- **Mode segment**: a filled dot `●` and a label, both coloured by
  `modeLabel`, then two spaces and the `⇧⇥` cycle-mode hint (dim):

  | mode string | label | colour |
  |---|---|---|
  | `acceptEdits`, `auto` | `auto-edit` | green |
  | `bypassPermissions` | `bypass permissions` | green |
  | `dontAsk` | `don't ask` | green |
  | `plan` | `plan only` | blue |
  | `manual`, `""`, anything else | `ask before edits` | dim |

- **Location segment** (two spaces after the mode segment, only when
  `Cwd` is set): the cwd (dim), then, if a `GitStatus` is present, ` · `
  plus the branch name, with a trailing `*` when dirty.
- A flexible spacer, then, right-aligned so the row ends at `width-1`:
- **Context meter**: `ctx ` (plain), a 10-cell bar (`Meter`, filled cells
  first) coloured amber below 70% used / red above 70% (`contextPressure`),
  empty cells in the `rule` colour, then two spaces and the percentage
  (dim). The meter renders even with no usage yet (0%, all-empty).
- **Cost**: two spaces then `$N.NN` (dim), included only when cumulative
  cost is greater than zero.

When the row does not fit at the terminal's width, segments drop in this
order: location first, then cost, then (if still too wide) the whole row
is truncated with `FitStatus`.

A configured `statusLine` command's output renders as extra dim rows
*after* this one (its own colours stripped and re-tinted dim), refreshed
on its own schedule — it does not replace the status line above.

## Busy line (spinner)

Only while a turn is running, one row, above the input box
(`internal/tui/spinner.go`'s `Render`, `internal/tui/transcript.go`'s
`RenderSpinnerLeft`):

```
◐ Whirring…  4s · 1.2k tokens                              esc to stop
◐ Computing…  1s · thinking with medium effort              esc to stop
```

- Spinner glyph (amber, cycling `◐ ◓ ◑ ◒` at 140ms/frame) and the label —
  a gerund picked deterministically from a per-turn seed
  (`PickLabel`/`spinnerVerbs`: Brewing, Cooking, Crunching, Baking,
  Churning, Whirring, Simmering, Working) — amber, with a trailing `…`.
- A suffix (dim), chosen in this priority order:
  1. once a live token count is known: `Ns · N tokens` (`FormatTokens`:
     `450`, `3.4k`, `1.0m` — no `↓` prefix, no parentheses);
  2. while `Thinking` is set and no tokens have arrived yet: `Ns ·
     thinking with <effort> effort` (`effort` defaults to `medium` when
     unset);
  3. otherwise, once elapsed seconds is nonzero: `Ns` alone;
  4. before any of the above (the very first frame): no suffix at all —
     just `{spinner} {Label}…`.
  If a follow-up is queued via `Lane.Steer` while this turn runs, `
  N queued` is appended to whatever suffix is showing (or becomes the
  whole suffix if there was none).
- `esc to stop` (dim) is right-aligned to the row's own width (not
  Claude Code's "esc to interrupt").

There is no separate "effort hint" row and no post-turn "turn summary"
row: the busy line simply disappears when the turn ends
(`app.go`'s `finishTurn`); `RenderTranscriptEntries`'s replay does not
re-append one either (see "Replay" below).

## Block anatomy — the label rule

The core pattern: every transcript block (except the banner) has a label
on a hairline rule above it, with right-aligned metadata, and one blank
row between blocks — `internal/tui/theme.go`'s `labelRule(label,
labelColor, meta, width)`, the shared helper every block-rendering
function calls:

```
{label}────────────────────────────────────  {meta}
{body}
```

`{label}` is coloured per block type below; the hairline `─` fills the
middle in `rule` (`#2f2920`); `{meta}` is dim and right-aligned. In plain
mode the fill uses ASCII `-` and no colour.

| type | label (colour) | meta | body |
|---|---|---|---|
| user | `you` (amber) | – | Message on `raise` bg, ink text, wrapped to width |
| text | `kiln` (dim) | – | Already-rendered markdown lines under the rule; no streaming-caret variant of this specific block (the live caret is a separate live-region block, see "Streaming" below) |
| note / system | `system` (dim) | – | One dim line, truncated (not wrapped) to width — e.g. `↺ Reconnected on attempt 2`, `■ Interrupted. Tell kiln what to do instead.`, `✕ Declined npm test -- upload`, or a single-line slash-command result (`/cost`, `/compact`, `/model`, `/agents`, `/help`) |
| tool | tool name lowercased (status colour) | `approved · 4.1s` / `auto-approved · 4.1s` (only when set) | Line 1: tool name (status colour) + primary arg (dim). Result: first line prefixed `→ ` (dim, or red after the first line on error), later lines indented 2 columns; a `… +N lines (ctrl+o to expand)` tail when more lines exist than shown |
| diff / edit | `edit` (blue) | filename (`path.Base`) | Header row on `panel` bg (path, `new file` tag when set, `+N` green, `−N` red); rows: line number (4 columns, right-aligned, faint), sign (`+`green/`−`red/blank), code (ink; context lines dim); `+` rows get `diff add bg`, `−` rows `diff del bg`; long lines truncate, never wrap |
| perm | `approval needed` (amber) | – | Amber rules above/below; `Allow kiln to <verb> <arg>?` (amber, bold); `$ cmd` on `raise` when there's a primary arg; a reason line (dim) when outside the workspace or a change preview; numbered options; a hint row (dim) |
| plan | `plan` (dim) | `d/n` | `Plan · d of n` (dim); items: done `✓` (green) + dim strikethrough, current `▸` (amber) + ink, todo `○` (faint) + dim |
| agents / subagents | `subagents` (dim) | `d/n done` | Header (`N subagents running in parallel` / `N subagents finished`) + up to 6 dispatch rows (name amber-running/green-done/red-error, description ink, a 10-cell meter tracking tool-call count, tokens right-aligned once done) each followed by an indented action row (dim); a `+K more` row beyond 6 |
| error | `error` (red) | – | Line 1: the failure message, red. Line 2: `Retrying in Ns · attempt A of M · r to retry now` (dim) — the live variant only; a final failure commits as a plain `RenderError` block instead |
| context | `context` (dim) | – | Header (`<model> · Nk of Mk tokens`, dim) + a stacked segment bar + legend rows (swatch, label, tokens, right-aligned percent); segment colours: System prompt=blue, Tools=violet, Conversation=green, Free=rule |

Tool-call status colour: running amber, ok green, error red — the same
three colours the spinner and diff signs use throughout.

### Permission prompt

Two distinct renderers cover this block, and they are **not** styled
identically:

**Generic prompt** (`RenderPermissionPrompt`, any tool other than Bash/
Edit/Write):

```
approval needed ──────────────────────────────────────────
──────────────────────────────────────────────────────────

  Allow kiln to use <tool>?
  $ <arg, on raise bg>

──────────────────────────────────────────────────────────
  1  Yes
  2  Yes, and don't ask again for this
  3  No, and tell kiln what to do instead

  1-3, y/n, or esc to decline
```

Selected row: raised background (`#241f18`) + amber key + ink label; the
other rows show a faint key and a dim label. ↑/↓ move the selection, the
same convention as the Bash and Edit/Write prompts below.

**Bash prompt** (`RenderBashPermissionPrompt`, 4 options):

```
approval needed ──────────────────────────────────────────
──────────────────────────────────────────────────────────
 Allow kiln to run this command?

 $ npm test -- upload
   Generate the upload bundle

──────────────────────────────────────────────────────────
 1  Yes
 2  Yes, and don't ask again for: npm test *
 3  Yes, and switch to auto mode · auto mode handles these prompts for you
 4  No

 ↑↓ select · enter confirm · esc decline · tab to amend
```

Selected row: raised background (`#241f18`) + amber key. Option 2's gate
expression is the command's first two words plus ` *` (`bashDontAskRule`
— e.g. `openssl rand -hex 4` → `openssl rand *`).

**Edit/Write prompt** (`RenderEditPermissionPrompt`, 3 options): amber
rule frame, `Allow kiln to edit <path>?` / `Allow kiln to write to
<path>?`, a `╌` dashed rule around numbered hunk rows (line number, sign,
code — no background tint, unlike the committed "edit" transcript
block), a closing `╌` rule, then the numbered options (option 2's second
line, "Yes, and switch to accept edits …", wraps onto its own row).
Selected row: raised background + amber key, same convention as Bash's.

Feedback capture (option "tell kiln what to do instead" / "no, and tell
kiln…") replaces the option list with a live text row (`> <feedback>▌`)
and `enter to send · esc to decline without a reason`, on all three
prompt variants.

### Plan approval

```
plan ───────────────────────────────────────────────────────────
   Ready to code?

   Here is the plan:
   ╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
   <plan body, wrapped, ink>
   ──────────────────────────────────────────────────────────
   kiln has written up a plan and is ready to execute. Would you like to
   proceed?

   1  Yes, and use auto mode
   2  Yes, manually approve edits
   3  Tell kiln what to change
        shift+tab to approve with this feedback

   <plan path>
```

A body taller than the available frame height clips, with a trailing `↓`
one column short of the right edge on the last visible row
(`RenderPlanApproval`'s `height`/scroll handling). `selected` always
arrives as 0 from the live key path today — 1/2/3 commit immediately
rather than arrow-navigating first — so a non-zero selected state is only
exercised by unit tests, not by any interactive key sequence.

## Dialogs (`/model`, `/mcp`, rewind, trust, command palette)

Every list dialog marks its selected row with the raised background
spanning the row's full width; unselected rows are dim. The Trust dialog
draws its own full-width `─` rule as its first row and says `kiln will be
able to read, edit, and execute files here.` (`internal/tui/dialog_trust.go`).

## Autocomplete popup (slash / `@` / argument completion)

`internal/tui/autocomplete.go`'s `Popup.Render`:

- Selected row: raised background spanning the full row width, command/
  value in amber. Unselected rows: command/value in ink, description dim
  (both rows carry colour — only the background tint differs).
- Slash-command rows: two-space indent, the command padded to a
  10-column column (`slashCommandColumn`), then the description,
  truncated (not wrapped) to the remaining width.
- Argument-completion rows: two-space indent, the value padded to a
  42-column column (`popupValueColumn`), then a description that may wrap
  onto a second row (`popupDescRows`), the second row's tail clipped with
  `…` if it still overflows.
- File (`@`) rows: `+ <path>`, the path itself middle-truncated (keeping
  the last path segment) when too wide for the row.
- Up to 5 rows show at once (`app.go`'s `renderPopup`), windowed around
  the current selection (`visibleRange`); the popup with no matches shows
  a single `  No matching commands` row.

## Streaming

While the assistant's text is still arriving, the live region (above the
input box, below any retry/plan/subagents rows) shows a `kiln ───` label
rule (dim) and the last 8 wrapped rows of the in-flight text
(`maxStreamRows`, `internal/tui/stream.go`'s `RenderStreamLive`), plain-
wrapped (no markdown rendering — that only happens once, on commit), with
a trailing amber `▍` caret appended to the last visible row. This live
block is dropped, not committed, once the message finishes — the
committed `kiln` text block (this doc's block-anatomy table) replaces it
with the fully markdown-rendered text.

## Replay (Ctrl+O / Ctrl+F / Rewind)

`internal/tui/replay.go`'s `RenderTranscriptEntries` redraws a session
branch from its logged entries plus any synthetic blocks recorded via
`Bridge.CommitSynthetic` (the committed plan checklist, subagent dispatch
lines, system notes, the `/context` block — none of which are session log
entries, so a replay built purely from the log would silently drop them).
Read-only tool calls (Read/Glob/Grep/Bash-without-shown-output/web
fetch/search) group into a collapsed `Read N files`/`Ran N shell
commands` row in non-verbose mode; Edit and Write never group. There is
no turn-summary row appended at the end of a replay — the busy line's
disappearance at turn end is the whole story (see "Busy line" above).

## Esc semantics (abort order)

`app.go`'s `finishTurn`, on `harness.StatusAborted`: every tool call that
started but never reached its own end event commits first, as a red
tool-call error block (from `Bridge.InFlightTools`); then the live
`plan`/`subagents` panels commit their final state as ordinary blocks (the
same commit every normal turn end makes); then, last, a `system` note —
`■ Interrupted. Tell kiln what to do instead.` — so the note reads as the
final word on what happened.

## Queued follow-up

Typing a line while a turn is running (and no prompt is capturing the
keystroke) does not start a second turn: the `you` block commits
immediately with a `queued` meta on its label rule
(`RenderUserMessageMeta`), and `Lane.Steer` (`internal/harness/lane.go`)
appends the raw text to the lane's inbox for the next turn boundary to
drain (`turn.go`'s `drainInbox`), emitting `EventQueueUpdate` with the new
queue length — which the busy line's ` · N queued` suffix reads (see
"Busy line" above).

## Rename note

The product is "kiln"; the on-disk data directory stays `~/.harness`
(credentials, sessions, history, trust, search db — renamed silently would
orphan existing users' data), the Go module path stays
`github.com/andrepato/harness`, and `HARNESS_*` env vars are unchanged. Only
the user-facing surface (banner, `--version`/`--help`, usage strings, the
terminal window title, `cmd/harness` → `cmd/kiln`, `cmd/harness-drive` →
`cmd/kiln-drive`) renamed.
