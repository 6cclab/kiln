# Handoff: kiln — terminal UI for a coding agent (Layout 1b "Ruled")

## Overview
kiln is a full-screen terminal UI (TUI) for an agentic coding assistant. It has one scrolling transcript with a fixed input and status line at the bottom. The chosen direction is **1b "Ruled"**: every transcript block has a small label on a hairline rule above it, with right-aligned metadata such as timing. The design covers the welcome screen, plans, parallel subagents, streaming text, tool calls, diffs, permission prompts, errors and retries, the slash-command palette, and the context/cost meter.

## About the design files
The files in this bundle are **design references built in HTML**. They show the intended look and behavior and are not production code. The task is to **recreate them as a real terminal app** in the target codebase's environment. If there is no existing environment, recommended options are:
- **Ink** (React for the terminal, Node/TypeScript). This maps most directly to the component and state model in the prototype.
- **Bubble Tea + Lip Gloss** (Go).
- **Textual** (Python).

The HTML version is rendered in a browser, so values are given in px. Convert them to terminal cells with these rules:
- 1 line = 14px font × 1.6 line-height ≈ 22px, which is one terminal row.
- 1ch = one column.
- A 1px hairline becomes a row of `─` (U+2500) in the rule color.
- Vertical gaps of 14px become one blank row between blocks.
- 4–8px gaps inside a block become zero rows.
- Horizontal padding of 20px becomes a 2-column margin.
- Anything with `border-radius` becomes square. Diff and permission boxes use `─` rules; box-drawing corners are optional.
- Backgrounds such as `#241f18` become a truecolor background SGR (`\x1b[48;2;36;31;24m`). If the terminal lacks truecolor, fall back to 256-color approximations.

## Fidelity
**High fidelity** for colors, glyphs, copy, block anatomy and interaction model. The font is **Fira Code**, but in production it is whatever the user's terminal uses, so do not ship a font. Window chrome (the title bar with three dots) is presentation only. The real app draws everything inside the terminal's own window.

## Screen anatomy (top → bottom)
1. **Transcript**: scrollable, auto-scrolls to the bottom on new content. Margin: 2 columns left and right, 1 row top.
2. **Busy line** (only while the agent is working): `{spinner} {status}…  {elapsed}s · {N}k tokens` on the left and `esc to stop` on the right.
   - Spinner, status text: amber `#e9a64b`.
   - Elapsed/tokens and `esc to stop`: dim `#a39781`.
   - Spinner frames are `◐ ◓ ◑ ◒` at 140ms per frame.
3. **Slash palette** (only while the input starts with `/` and contains no space): a list directly above the input with a `─` rule above it.
   - Each row: command (10 columns wide), then its description.
   - Selected row: background `#241f18`, command in amber. Other rows: command in ink `#ece4d4`, description dim.
4. **Input**: a `─` rule in `#3a3228` above and below it.
   - Prompt glyph `›` in amber, then 1 space, then the text. Caret is amber.
   - Placeholder text (dim):
     - idle: `describe a task · / for commands · @ to add files`
     - busy: `queue a follow-up, or esc to stop`
     - waiting for approval: `press 1, 2 or 3`
5. **Status line** (12px ≈ same row, dim `#a39781`), laid out left to right:
   - mode, e.g. `● auto-edit  ⇧⇥`
   - `~/src/relay-api · main*`
   - the active model, e.g. `kiln-large`, in ink `#ece4d4`
   - flexible spacer
   - `ctx ` + 10-cell meter + ` 38%`
   - `$0.42`

   Mode colors: `● ask before edits` dim, `● auto-edit` green `#9bc46e`, `● plan only` blue `#86b4d4`. Cycle with Shift+Tab.

   Meter: filled cells in amber (red `#e5765d` above 70%), empty cells in `#2f2920`. In a terminal, use `━` or `█` for filled cells and `─` for empty ones.

## Block anatomy (Layout 1b)
Every block except the banner has a **label rule**:
```
{label} ────────────────────────────────────────── {meta}
{body}
```
- The label is 12px. It is dim by default, or colored by block type (see below). The rule is `#2f2920`. Meta is dim and right-aligned.
- There is one blank row between blocks.

| type | label (color) | meta | body |
|---|---|---|---|
| user | `you` (amber) | – | Message on a `#241f18` background, 1 column of horizontal padding, ink text |
| text | `kiln` (dim) | – | Ink text that wraps. While streaming, add a trailing `▍` in amber |
| note | `system` (dim) | – | One dim line, e.g. `↺ Reconnected on attempt 2`, `■ Interrupted…`, `✕ Declined …` |
| tool | tool name lowercased (status color) | `approved · 4.1s` | Line 1: `Name` (status color, weight 500) + space + arg (dim). Then output lines: first prefixed `→ `, later ones indented 2 columns. Output is dim; for errors, lines after the first are red |
| diff | `edit` (blue) | filename | Header row on `#1c1813`: path, tag (`new file`, dim), spacer, `+N` green, `−N` red. Each line: line number (4 columns, right-aligned, `#7d7262`), sign (2 columns; `+` green, `−` red), code (ink; context lines dim). Added lines have background `rgba(155,196,110,.10)` ≈ `#232619`; removed lines `rgba(229,118,93,.13)` ≈ `#2f1c15`. Long lines are truncated, not wrapped |
| perm | `approval needed` (amber) | – | Amber rules above and below. Contents: `Allow kiln to run this command?` (amber, bold); `$ npm test -- upload` on `#241f18`; a one-line reason (dim); 3 options (below); hint `↑↓ select · enter confirm · esc decline` (dim) |
| plan | `plan` (dim) | `2/5` | `Plan · 2 of 5` (dim), then items: done `✓` green with dim strikethrough text; current `▸` amber with ink text; todo `○` `#7d7262` with dim text |
| agents | `subagents` (dim) | `2/3 done` | Header `3 subagents running in parallel` (or `… finished`). Each row has 4 columns: name (8 columns; amber while running, green when done); task (ink, truncated) with the latest action below it (dim, 12px, prefixed `→ ` or `✓ `); a 10-cell `━` progress bar (filled cells amber/green, empty `#3f372c`); tokens (dim, right-aligned) |
| error | `error` (red) | – | Line 1 red, e.g. `Stream interrupted · 529 overloaded`. Line 2 dim: `Retrying in 3s · attempt 2 of 5 · r to retry now`, counting down once per second |
| context | `context` (dim) | – | Header `kiln-large · 76k of 200k tokens`. Then a stacked bar with one segment per source, widths proportional to tokens, 1-column gaps. Then legend rows: `■` in the segment color, label, tokens, and a percentage (5 columns, right-aligned) |

**Permission options.** Selected row: background `#241f18`, ink text, amber key. Other rows: dim text, key in `#7d7262`.
- `1  Yes`
- `2  Yes, and don't ask again for npm test`
- `3  No, and tell kiln what to do instead`

**Context segment colors.**
- System prompt: blue `#86b4d4`
- Tools: violet `#c3a3d6`
- Files read: amber
- Conversation: green
- Free: `#2f2920`

**Banner** (no label; the welcome block at the top of every session; sits above a `─` rule):
- ANSI-art kiln on the left: 9 rows, 19 columns, made of block characters. Copy it verbatim from `Terminal.dc.html`. Colors:
  - body `#b86a45`
  - smoke `#6f6555`
  - door lintel `#3a3228`
  - flames `#e9a64b`
  - glow `#f3c27f`
  - base `#5a4a3a`
- To the right, stacked: `KILN` in amber bold and letter-spaced, `v0.9.2 · coding agent` (dim), then the repo line.
- Dim line: `~/src/relay-api · branch main · model kiln-large`.
- Shortcut row, keys in amber and descriptions dim: `/ commands   @ add files   ⇧⇥ cycle mode   esc stop`.
- `Recent sessions` list, shown only before the first message: when (10 columns, dim) and title (ink).

Only layout 1b is in scope. `Terminal.dc.html` still accepts `gutter` and `rail`, but ignore them.

## Interactions & behavior
**Input**
- Enter sends the message. If the agent is busy, it becomes a queued follow-up; the prototype ignores these.
- Shift+Tab cycles the mode: ask → auto-edit → plan.

**Slash palette**
- Filtering is by prefix.
- ↑/↓ move the selection and wrap around.
- Enter or Tab runs the selected command. Esc clears the input.
- Commands: `/context`, `/cost`, `/compact`, `/clear`, `/model`, `/agents`, `/help`.
  - Running a command first echoes it as a `you` block, then adds the result block.
  - `/clear` resets to the banner.
  - `/compact` lowers the context meter.

**Permission**
- While a prompt is shown and the input is empty:
  - `1`, `2` or `3` choose that option directly.
  - ↑/↓ and Enter also work. Esc means option 3.
- Option 2 whitelists the command. Later runs of it show the meta `auto-approved`.
- Option 3 removes the prompt, adds a `✕ Declined …` note plus the text `Okay, I won't run it. What should I do instead?`, and ends the busy state.

**Esc while busy** cancels everything in flight:
- Streaming stops.
- Running tools are marked as errors.
- The prompt is removed.
- A `■ Interrupted. Tell kiln what to do instead.` note is added.

**Streaming**
- The prototype reveals about 3 characters every 18ms. A real app should render tokens as they arrive.
- The cursor `▍` shows only while streaming.

**Blocks update in place**, not by appending:
- the plan checklist
- subagent progress
- tool status (run → ok/err)
- the error countdown, which is replaced by a note on reconnect

**Tool status glyph and color**
- running: spinner, amber
- ok: `✓`, green
- error: `✕`, red

**Auto-scroll** to the bottom on every update. A real app should pause auto-scroll when the user scrolls up and resume at the bottom.

## State model
```
Session {
  blocks: Block[]            // discriminated union by `type` (see table above), each with optional stable `id` for in-place updates
  busy: boolean; status: string; startedAt: number
  waiting: boolean           // permission pending
  mode: 0|1|2                // ask | auto-edit | plan
  ctxPct: number; costUsd: number
  alwaysAllow: Set<string>   // commands approved with option 2
}
UI { input: string; paletteIndex: number; permSelection: 0|1|2; spinnerFrame: 0..3 }
```
The scripted session in `Terminal.dc.html` (`SCRIPT` array) is example data only. It shows a realistic event order: user → text → plan → agents → text → diff ×2 → perm → tool(err) → text → error/retry → diff → tool(ok) → text → idle.

## Design tokens
| token | hex | use |
|---|---|---|
| bg | `#14110d` | terminal background |
| panel | `#1c1813` | diff header, (1c) top status bar |
| raise | `#241f18` | user message, command line, selected rows |
| rule | `#2f2920` | hairlines, empty meter |
| rule-strong | `#3a3228` | input rules |
| ink | `#ece4d4` | primary text |
| dim | `#a39781` | secondary text, labels, meta |
| faint | `#7d7262` | line numbers, todo glyph, unselected keys (decorative only) |
| amber | `#e9a64b` | accent: prompt, running, user label, approvals |
| green | `#9bc46e` | success, additions, done |
| red | `#e5765d` | errors, removals |
| blue | `#86b4d4` | edits, plan mode |
| violet | `#c3a3d6` | context "tools" segment |

Type sizes: body 14px, meta and labels 12px, diff 13px, banner wordmark 22px. In a terminal, render every size as the terminal's own size. Weights: 400 body, 500 tool names, 600 headings and wordmark (bold SGR).

## Assets
None. All glyphs are Unicode: `› • · ✓ ✕ ± ? ≡ ∥ ! ◧ ▸ ○ ━ ─ ■ ▍ ◐◓◑◒ ↺ → ⇧⇥`. The traffic-light dots in the window chrome are presentation only.

## Files
- `Kiln TUI.dc.html`: overview page with the interactive prototype and 9 key-state screens, all in layout 1b (`ruled`). Open it in a browser.
- `Terminal.dc.html`: the terminal component. Its props are `layout` (`gutter` | `ruled` | `rail`) and `scene` (`live` | `welcome` | `plan` | `agents` | `streaming` | `diff` | `permission` | `error` | `palette` | `context` | `done`). The logic class contains all copy, the scripted session, block styling rules (`deco()`), and keyboard handling (`onKey`).
- `support.js`: runtime needed to open the `.dc.html` files.
