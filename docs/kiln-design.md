# kiln rendering contract

This is kiln's own rendering contract — the "Ruled" layout (Layout 1b) from
the design handoff (`docs/kiln-design-handoff/README.md`, copied into this
repo for reference; the original bundle also has an interactive HTML
prototype and screenshots that aren't reproduced here). It replaces
`docs/claude-code-reference.md`, which pinned kiln to Claude Code's
own look; kiln has its own palette, glyphs and block layout, applied on top
of the same inline-scrollback renderer (blocks commit to native terminal
scrollback via `Bridge.Commit → tea.Println`; a small live region — spinner,
input box, status line — redraws at the bottom).

Where this doc and the code (`internal/tui/theme.go`,
`internal/tui/theme_transcript.go`, `internal/tui/theme_dialog.go`,
`internal/tui/transcript.go`) disagree, the code wins — this is a snapshot,
not the source of truth. `docs/claude-code-parity.md` keeps the
*behavioral*/feature checklist (keybindings, CLI flags, permission-mode
semantics, hooks, MCP) that is unaffected by this visual change.

## Palette (truecolor)

| token | hex | use |
|---|---|---|
| ink | `#ece4d4` | primary text |
| dim | `#a39781` | secondary text, labels, meta, statusline |
| faint | `#7d7262` | line numbers, todo glyph, unselected keys |
| amber | `#e9a64b` | accent: prompt `›`, running, `you` label, approvals, KILN wordmark |
| green | `#9bc46e` | success, additions, done |
| red | `#e5765d` | errors, removals |
| blue | `#86b4d4` | edit label, plan mode |
| violet | `#c3a3d6` | context "tools" segment |
| rule | `#2f2920` | block label hairlines, empty meter, diff borders |
| rule-strong | `#3a3228` | input box rules |
| panel | `#1c1813` | diff header background |
| raise | `#241f18` | user-message bg, `$cmd` bg, selected-row bg |
| diff add bg | `#232619` | diff `+` line background |
| diff del bg | `#2f1c15` | diff `−` line background |

kiln does **not** set a global terminal background: inline mode does not own
the whole screen. Only the `raise`/`panel`/diff-line background tints apply,
as per-span SGR, where the design uses them.

## Glyphs

| glyph | meaning |
|---|---|
| `›` | prompt / user mark |
| `•` | text bullet |
| `·` | note |
| `±` | diff label |
| `?` | approval |
| `≡` | plan label |
| `∥` | subagents label |
| `!` | error label |
| `◧` | context label |
| `✓` | ok (green) |
| `✕` | error / declined (red) |
| `▸` | plan-current item (amber) |
| `○` | plan-todo item (faint) |
| `━` | meter-filled |
| `─` | meter-empty / hairline rule |
| `■` | interrupted / legend segment |
| `▍` | streaming caret (amber) |
| `◐ ◓ ◑ ◒` | spinner frames, 140ms per frame |
| `↺` | reconnect |
| `→` | tool output / subagent action |
| `⇧⇥` | cycle-mode hint |

`ASCIIGlyphs` (`internal/tui/theme.go`) is the plain-mode/screen-reader
fallback: every glyph above has an ASCII equivalent (e.g. `›` → `>`, the
spinner → `-\|/`, `✓`/`✕` → `+`/`x`), used under `--ax-screen-reader` and
`SetPlainMode(true)`.

## Banner

No label rule (it's not a transcript block); sits above a `─` rule, pinned
near the bottom of the screen on startup with blank filler above it:

```
K I L N  v0.9.2 · coding agent
~/src/relay-api · model kiln-large · medium effort
/ commands   @ add files   ⇧⇥ cycle mode   esc stop
```

- Row 0: `KILN` in amber, bold, letter-spaced (`K I L N`), then dim
  `v<version> · coding agent`.
- Row 1: dim — cwd, ` · model `, the model label, ` · `, the effort level,
  ` effort`.
- Row 2: shortcut hints, keys in amber, descriptions dim.
- A "Recent sessions" list may follow, shown only before the first message.

## Input box

A `─` rule in `rule-strong` (`#3a3228`) above and below the input:

```
────────────────────────────────────────────────
› Try "how do I log an error?"
────────────────────────────────────────────────
```

- Prompt glyph `›` in amber, then a space, then the text. The caret is
  amber.
- Placeholder text (dim), depending on state: idle
  (`describe a task · / for commands · @ to add files`), busy (`queue a
  follow-up, or esc to stop`), waiting for approval (`press 1, 2 or 3`).

## Mode line / status line

Below the input box, one row:

```
● auto mode on (shift+tab to cycle) · ← for agents
```

A filled dot `●` leads the line, coloured by mode: `ask`/`manual` dim, the
auto-edit family (`auto`, `acceptEdits`, `bypassPermissions`, `dontAsk`)
green, `plan` blue. The design's own statusline segments (`ctx` meter,
`$cost`, git branch) are kiln's pre-existing statusLine feature,
re-themed dim rather than replaced.

Meter: filled cells amber (red above 70%), empty cells in `rule`. Terminal
rendering uses `━`/`█` for filled cells, `─` for empty ones.

## Busy line (spinner)

Only while the agent is working, one row:

```
◐ Whirring… (4s · ↓ 1.2k tokens)
```

- Spinner and status text: amber.
- Elapsed/tokens and the trailing hint: dim.
- Spinner frames `◐ ◓ ◑ ◒` at 140ms per frame.
- Before thinking/tokens are available, no suffix — just `{spinner}
  {Label}…`.
- While thinking (no tokens yet): `(Ns · thinking with <effort> effort)`.
- Once tokens are known, the token suffix replaces the thinking one:
  `(Ns · ↓ N tokens)`.
- The hint at the right is "esc to stop" (not Claude Code's "esc to
  interrupt").

## Block anatomy — the label rule

The core new pattern: every transcript block (except the banner) has a
label on a hairline rule above it, with right-aligned metadata, and one
blank row between blocks:

```
{label}────────────────────────────────────  {meta}
{body}
```

`{label}` is coloured per block type below; the hairline `─` fills the
middle in `rule` (`#2f2920`); `{meta}` is dim and right-aligned.
`internal/tui/theme.go`'s `labelRule(label, labelColor, meta, width)` is the
shared helper every block-rendering function in `internal/tui/transcript.go`
calls.

| type | label (colour) | meta | body |
|---|---|---|---|
| user | `you` (amber) | – | Message on `raise` bg, 1 column padding, ink text |
| text | `kiln` (dim) | – | Ink text that wraps; a trailing `▍` (amber) while streaming |
| note / system | `system` (dim) | – | One dim line, e.g. `↺ Reconnected on attempt 2`, `■ Interrupted…`, `✕ Declined …` |
| tool | tool name lowercased (status colour) | `approved · 4.1s` | Line 1: `Name` (status colour) + arg (dim). Output lines: first prefixed `→ `, later ones indented 2 columns, dim (red after the first line for errors) |
| diff / edit | `edit` (blue) | filename | Header on `panel` bg (path, tag dim, `+N` green, `−N` red); rows: line number (4 columns, right-aligned, faint), sign (2 columns; `+` green / `−` red), code (ink; context lines dim); `+` rows get `diff add bg`, `−` rows `diff del bg`; long lines truncate, not wrap |
| perm | `approval needed` (amber) | – | Amber rules above/below; `Allow kiln to …?` (amber, bold); `$ cmd` on `raise`; a reason (dim); numbered options; a hint (dim) |
| plan | `plan` (dim) | `2/5` | `Plan · N of M` (dim); items: done `✓` (green) + dim strikethrough, current `▸` (amber) + ink, todo `○` (faint) + dim |
| agents / subagents | `subagents` (dim) | `2/3 done` | Header + rows: name (amber running / green done), task + last action (dim, `→`/`✓` prefixed), a 10-cell `━` bar, tokens (dim, right-aligned) |
| error | `error` (red) | – | Line 1 red; line 2 dim countdown |
| context | `context` (dim) | – | Header + stacked segment bar + legend; segment colours: system=blue, tools=violet, files=amber, conversation=green, free=rule |

Tool-call status colour: running amber, ok green, error red — the same
three colours the spinner and diff signs use throughout.

### Permission prompt

```
approval needed ────────────────────────────────────────────────
──────────────────────────────────────────────────────────────────
 Allow kiln to run this command?

 $ npm test -- upload
   Generate the upload bundle

──────────────────────────────────────────────────────────────────
 1  Yes
 2  Yes, and don't ask again for: npm test *
 3  Yes, and switch to auto mode · auto mode handles these prompts for you
 4  No

 ↑↓ select · enter confirm · esc decline · tab to amend
```

Selected row: raised background (`#241f18`) + amber key. Other rows: dim
text, key in `faint`. kiln does not draw a "❯" marker on these rows — the
raised background and amber key alone carry the selection, so a
colour-disabled (plain-text) capture of this prompt cannot distinguish
selected from unselected rows by content; that state is only observable
with colour on.

### Diff / edit permission prompt

Same amber-ruled frame, with `Allow kiln to edit <path>?` / `Allow kiln to
write to <path>?`, a `╌` dashed rule around the hunk rows (line number,
sign, code — no background tint here, unlike the "edit" transcript block),
then the same numbered-option list.

### Plan approval

```
plan ───────────────────────────────────────────────────────────
 Ready to code?

 Here is the plan:
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 <plan body, wrapped>
──────────────────────────────────────────────────────────────
 kiln has written up a plan and is ready to execute. Would you like to proceed?

 1  Yes, and use auto mode
 2  Yes, manually approve edits
 3  Tell kiln what to change
      shift+tab to approve with this feedback

 <plan path>
```

## Dialogs (`/model`, `/mcp`, rewind, trust, command palette)

Every list dialog (`/model`, `/mcp`, the rewind picker, the generic
command-option dialog) marks its selected row the same way: a `❯` before
the label plus the raised background spanning the row's full width, with
the label/marker in ink/amber; unselected rows are dim with a faint (or
absent) marker. The Trust dialog draws its own full-width `─` rule as its
first row (rather than sitting under the app-owned rule the numbered-option
dialogs share) and says `kiln will be able to read, edit, and execute files
here.` (kiln, not "Claude Code'll be able to…", and no "Security guide"
row — kiln has no such doc to link).

The `/model` dialog additionally shows a `◐ <Effort> effort ←/→ to adjust`
row using the spinner glyph as a static effort indicator, and legends like
`Enter to set as default · s to use this session only · Esc to cancel`.

## Autocomplete popup (slash / `@` / argument completion)

Selected row: raised background, command in amber. Unselected rows: command
in ink, description dim (not unstyled — both rows carry colour, only the
background tint differs).

## Rename note

The product is "kiln"; the on-disk data directory stays `~/.harness`
(credentials, sessions, history, trust, search db — renamed silently would
orphan existing users' data), the Go module path stays
`github.com/andrepato/harness`, and `HARNESS_*` env vars are unchanged. Only
the user-facing surface (banner, `--version`/`--help`, usage strings, the
terminal window title, `cmd/harness` → `cmd/kiln`, `cmd/harness-drive` →
`cmd/kiln-drive`) renamed.
