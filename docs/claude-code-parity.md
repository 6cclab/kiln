# Claude Code parity spec

The harness must look and operate identically to Claude Code. This file turns
that into a checklist so "identical" is testable rather than a matter of taste.

**Confidence is marked per item.** `[obs]` means directly observable in a running
Claude Code session and safe to treat as settled. `[chk]` means stated from
familiarity and **must be confirmed against the real thing before implementing** —
Claude Code ships frequently, so memory goes stale. Do not implement a `[chk]`
item without looking at it first.

---

## 1. Input line

| Behavior | Conf | Notes |
|---|---|---|
| Two full-width rules around the input (top and bottom), no sides | `[obs]` | matches CC's `borderLeft/right: false` box |
| `❯` prompt glyph at the left inside the box | `[obs]` | `figures.pointer` |
| Placeholder hint when empty | done | `Try "fix typecheck errors"`; hidden once text exists |
| Multiline: newline inserts, Enter submits | `[obs]` | `Editor` handles |
| `\` + Enter, and Option/Shift+Enter, insert a newline | `[chk]` | which combos are bound |
| Up/Down recalls history at the first/last line | `[obs]` | `navigateHistory` exists |
| Paste of multiline text stays one block, no premature submit | `[obs]` | |
| Paste of an image renders an attachment chip | `[chk]` | |

## 2. Input modes (prefix characters)

| Prefix | Meaning | Conf |
|---|---|---|
| `/` | slash command; opens the command palette | `[obs]` |
| `@` | file mention; fuzzy path completion inline, contents inlined into the prompt on submit | `[obs]` |
| `!` | run a bash command directly, output into transcript | `[obs]` |
| `#` | write to memory / CLAUDE.md | `[chk]` exact target file |

All four are token-boundary triggers, matching `AutocompleteProvider.triggerCharacters`.

## 3. Slash command palette

| Behavior | Conf |
|---|---|
| Typing `/` opens a popup **above** the input | `[obs]` |
| Filters as you type, fuzzy not just prefix | `[obs]` |
| Each row shows `name` + dimmed `description` | `[obs]` |
| Selected row hinted with the `suggestion` colour; Up/Down moves; Tab/Enter accepts; Esc dismisses | `[obs]` |
| Commands taking arguments show an `argument-hint` after the name | `[obs]` |
| Argument values can themselves complete (e.g. a model name) | `[obs]` |
| User commands from `.claude/commands/*.md` appear beside built-ins | `[obs]` |
| Project and personal commands are distinguishable in the list | `[chk]` how |

### One namespace, four sources

**This is the key structural fact.** Slash commands are not a fixed built-in
list with extras bolted on — built-ins, project commands, plugin commands and
*skills* all resolve in a single flat namespace. It is why
`--disable-slash-commands` is documented as "Disable all skills".

| Source | Origin | Example |
|---|---|---|
| Built-in | compiled in | `/compact` |
| Project / personal | `.claude/commands/*.md` | `/track-work` |
| Plugin | installed plugin | `/explain-error` |
| Skill | `.claude/skills/*/SKILL.md` | `/simplify` |

Namespaced entries use `plugin:command`, e.g.
`/chrome-devtools-mcp:a11y-debugging`.

Design consequence: build **one `CommandRegistry`** in Phase 3 with pluggable
sources, rather than a built-in list that Phase 4 later patches. pi's
`SlashCommand` is already the right shape for every source, and `loadSkills`
returns `name` + `description` — exactly the two fields the palette renders.

### Built-in commands

Two independent recollections (this project's author-model and a separate
`claude -p` invocation) agree on the list below, which is better than one — but
neither is filesystem-verifiable, so it stays `[chk]`. Confirm against a live
`/help` before shipping.

**Done** (read from a live `/help`, not recalled — a grep of `src/commands/`
misses `/bashes` and `/posture`, which register inline in `cli.ts`):

`/add-dir` `/agents` `/bashes` `/clear` `/compact` `/config` `/context` `/cost`
`/doctor` `/exit` `/export` `/help` `/hooks` `/init` `/login` `/logout` `/mcp`
`/memory` `/model` `/permissions` `/posture` `/resume` `/rewind` `/status`
`/statusline` `/terminal-setup` `/todos` `/tools` `/usage` `/vim` — 30 commands,
plus every user-invocable skill in `~/.claude/skills`, which appear in the same
palette as Claude Code puts them in.

`/statusline` and `/vim` are honest no-ops: they say what is not implemented
rather than appearing to work. `/login` and `/logout` report status and point at
the CLI subcommand — the OAuth flow opens a browser and binds a local port, so
it must not start mid-turn. `/usage` reports the context budget, since no plan
limit applies to a self-hosted model.

`/plugin` reports that marketplaces are not supported and names what this
harness extends with instead (skills, agents, commands, MCP, hooks).

**Adapt:** `/login` `/logout` already exist in the harness CLI and should also
be slash commands. `/usage` becomes budget/tier reporting rather than plan
limits. `/fast` has no meaning here; `/effort` (§9) replaces it.

**Out of scope** — Anthropic-account or Anthropic-infrastructure specific:
`/upgrade` `/feedback` `/privacy-settings` `/install-github-app`
`/release-notes` `/pr-comments` `/code-review` `/ultrareview` `/review`
`/security-review` `/workflows`

Omit these rather than stubbing them. A command that exists and does nothing is
worse than one that is absent.

## 4a. Exact transcript layout

Written from direct operational familiarity. Glyphs are the highest-risk detail
in this document — verify them against a live session before committing, since a
wrong glyph is the single most noticeable parity failure.

A tool call and its result:

```
⏺ Read(src/provider/ollama.ts)
  ⎿  Read 240 lines (ctrl+r to expand)

⏺ Bash(npm run typecheck)
  ⎿  > tsc --noEmit
     (no output)
```

Key properties, each of which people get wrong:

- The call marker is a **filled circle** `⏺`, flush left, colored by status.
- The result is indented **two spaces**, then `⎿`, then **two more spaces**
  before content. The glyph appears on the *first* result line only; subsequent
  lines align under the content, not under the glyph.
- The call line is `ToolName(primaryArg)` — one argument, the identifying one.
  Not a serialized argument object.
- Results are summarized, not dumped: `Read 240 lines`, not 240 lines. The
  expansion hint is part of the line.
- A failed call colors the `⏺` red and the result carries the error text.

An assistant turn is plain markdown at the left margin with no marker of its
own — only tool calls get `⏺`. This matters: marking assistant prose the same
way makes the transcript unreadable.

Todo list:

```
⏺ Update Todos
  ⎿  ☒ Measure tool-schema cost
     ☒ Wire the provider registry
     ☐ Build the TUI shell
```

Completed items are `☒` and dimmed; pending are `☐`. The in-progress item is
shown distinctly from both.

Input box:

```
──────────────────────────────────────────────
❯ Try "fix typecheck errors"
──────────────────────────────────────────────
```

Two full-width rules (top and bottom), no sides — Claude Code's
`borderStyle="round"` with `borderLeft`/`borderRight` off. `❯` then a space
before the cursor; a dim example command fills the empty box and disappears
once you type.

A user message reads the same way, `❯` then the text, with no fill band.

Working indicator — a spinning glyph of Claude Code's own frame set, a
lowercase gerund, elapsed time and the live token count on one line:

```
✳ working (12s · ↓ 3.4k tokens)
```

The `esc to interrupt` hint moved to the footer: it rides the status line's
first row while a turn is loading, exactly where Claude Code puts it rather
than inside the still spinner. The gerund varies per turn; it is flavor, and
the harness should have its own vocabulary rather than copying a word list.

## 4. Transcript rendering

| Element | Behavior | Conf |
|---|---|---|
| User message | plain `❯` pointer then the text at the left margin, no fill — its own voice without a background band | `[obs]` |
| Assistant text | markdown rendered; code blocks syntax-highlighted | `[obs]` done |
| Tool call | one collapsed line: bullet, tool name, key argument | `[obs]` |
| Tool result | indented under the call with a `⎿` continuation glyph | `[obs]` |
| Long output | truncated with a "+N lines" affordance, Ctrl+R expands | `[obs]` done |
| File edits | unified diff, green additions / red deletions, line numbers | `[obs]` |
| Todo list | checkbox list, completed struck through or dimmed | `[obs]` done |
| Thinking | dimmed italic block, collapsed by default | `[obs]` done |
| Errors | red, visually distinct from normal tool output | `[obs]` |

## 5. Spinner / working indicator

| Behavior | Conf |
|---|---|
| Animated glyph (Claude Code's frame set) plus a varying lowercase gerund | `[obs]` |
| Elapsed seconds counter | `[obs]` |
| Token count `↓ N`, updating live during streaming | `[obs]` |
| "esc to interrupt" hint on the footer, only while loading | `[obs]` |
| Replaced in-place by the result, leaving no spinner residue | `[obs]` |

## 6. Keybindings

| Key | Action | Conf |
|---|---|---|
| `Esc` | interrupt the current turn | done |
| `Esc` `Esc` | rewind (points at `/rewind`) | done |
| `Ctrl+C` | clear input or interrupt; twice within 1s exits | done |
| `Ctrl+D` | exit on empty input; passed to the editor otherwise | done |
| `Ctrl+L` | clear the rendered transcript, not the conversation | done |
| `Ctrl+R` | expand tool output and reasoning | done |
| `Shift+Tab` | cycle permission mode | done |
| `Tab` | accept completion (pi-tui's editor) | done |

Routing lives in `src/tui/keys.ts`, extracted from `runApp` so it can be tested
— while it was a closure over a dozen locals, the only way to check that Ctrl+C
twice exits was to run a session and press it, so three of these bindings were
never implemented and nothing noticed.

`~/.claude/keybindings.json` is read and applied through pi-tui's
`KeybindingsManager`. Conflicts are reported at startup rather than resolved: a
key bound to two actions is a mistake only the user can settle, and silently
dropping one produces a key that works until it doesn't.

## 7. Status / footer

| Element | Conf |
|---|---|
| Current working directory | `[obs]` |
| Active model name | `[obs]` |
| Permission mode when not default | `[obs]` |
| Context-remaining indicator | `[obs]` |
| "esc to interrupt" while a turn is loading | done |

Claude Code shows `? for shortcuts` when idle and `esc to interrupt` while
loading. The harness's status line replaces the idle hint (a status line always
present is the documented divergence), so only the loading hint is threaded
through — it rides the first status row while a turn runs.

**Harness addition, deliberately not in Claude Code:** live budget consumption
against the active tier. On a 32k local model this is the difference between a
working session and a mysterious truncation, and it is the one place the harness
should diverge — an addition, not a change to existing elements.

## 7a. Permission modes

Confirmed from `claude --help` (`--permission-mode`), so these strings are exact
rather than remembered. `Shift+Tab` cycles between them, and the active mode
shows in the footer when it is not the default.

| Mode | Behavior |
|---|---|
| `manual` | ask before every tool use |
| `acceptEdits` | file edits auto-approved, other tools still prompt |
| `auto` | read-only tools allowed, anything that can change something still asks |
| `dontAsk` | never prompt, but still enforce deny rules |
| `bypassPermissions` | skip all checks |
| `plan` | read-only; no edits or mutations, produce a plan |

Not every mode needs to ship in v1, but the **names must match** — muscle memory
and `.claude/settings.json` files both depend on them. `plan` and `acceptEdits`
are the two that carry most of the daily value.

Related, also confirmed from `--help`:

- **Effort levels** are `low | medium | high | xhigh | max` (`--effort`). Maps to
  pi's `ThinkingLevel`, which the harness already threads through
  `AgentHarnessOptions.thinkingLevel`.
- **Settings sources** are `user`, `project`, `local` (`--setting-sources`),
  confirming the three-tier hierarchy Phase 4 must merge.
- **Session names** are "shown in the prompt box, /resume picker, and terminal
  title" — so the input box renders a name when one is set, and the harness
  should set the terminal title too.
- **`--ax-screen-reader`** renders "flat text, no decorative borders or
  animations". Worth mirroring: the whole TUI must degrade to plain text, which
  is also what makes it usable over a poor SSH link.

## 8. Permission prompts

| Behavior | Conf |
|---|---|
| Inline block, not a modal | `[obs]` |
| Shows the exact command or diff to be applied | `[obs]` |
| Options: yes / yes-and-remember / no with feedback | `[chk]` exact wording |
| Number keys select | `[obs]` |
| Rejection returns control to the user, turn continues | `[obs]` |

---

## 9. CLI surface

From `claude --help`, so these are exact. Muscle memory lives here as much as in
the TUI — someone who types `claude -c` should get the same result from
`harness -c`.

Worth implementing, in rough priority order:

All parsed by `src/cli-args.ts`, which accepts both `--flag value` and
`--flag=value`. The earlier `process.argv.indexOf` lookups handled only the
space form and silently ignored the other.

| Flag | State |
|---|---|
| `-c, --continue` | done |
| `-r, --resume [id]` | done; bare `--resume` means "the latest" |
| `-p, --print` | done |
| `--model <model>` | done; accepts `provider/model` including local ids |
| `--permission-mode <mode>` | done, all six modes |
| `--effort <level>` | done -> pi `ThinkingLevel` |
| `--add-dir <dirs...>` | done, repeatable, enforced as a real boundary |
| `--allowed-tools` / `--disallowed-tools` | done; additive to settings, deny still wins |
| `--output-format` | done, all three; `stream-json` is NDJSON emitted as events happen |
| `--settings`, `--setting-sources` | done; `--settings` applies last, overriding the hierarchy |
| `--mcp-config`, `--strict-mcp-config` | done |
| `--system-prompt`, `--append-system-prompt` | done |
| `--session-id <uuid>` | done; creates with that id rather than only resuming it |
| `--fork-session` | done; copies the tree, leaving the source session intact |
| `-n, --name <name>` | done |
| `--ax-screen-reader` | done |
| `-v, --version`, `-h, --help` | done |

**A trap worth recording:** `.claude/settings.json` `model` holds a Claude Code
alias (`"opus[1m]"` on this machine). Honoring it literally made every session
fail with `Unknown model "" on provider "opus[1m]"`. It is now used only when it
is in `provider/model` form.

Subcommands worth mirroring: `doctor` (health check), `mcp` (manage servers),
`auth` (manage credentials — the harness already has `login`/`logout`).

Out of scope, Anthropic-infrastructure specific: `gateway`, `install`,
`update`, `ultrareview`, `setup-token`, `--from-pr`, `--remote-control`,
`--chrome`, `--betas`.

Notable for the budget work: `--max-budget-usd` caps spend per run. The harness
equivalent is the tier system, but a dollar cap still makes sense once a paid
provider is in use — local models are free, subscriptions are not metered per
call, but API keys are.

## Deliberate divergences

Three, all additive:

1. **Budget footer** (§7) — required by the 32k local case.
2. **Model picker spans providers** — `/model` must offer local Ollama models
   beside Claude, which Claude Code has no reason to do.
3. **`/tools`** — inspect which MCP tools are currently resident. Claude Code
   has no equivalent because it has no posture/index layering.

## Explicit non-goals

- Anthropic-account features (`/bug`, `/upgrade`, billing).
- Pixel-matching Claude Code's *current* build forever. It ships weekly; this
  spec is a snapshot. Re-verify `[chk]` items when they are implemented.


## Hooks

`.claude/settings.json` hooks run as shell commands with a JSON payload on
stdin. Implemented against this machine's real hooks (`rtk-rewrite.sh`,
`homelab-kb-capture.sh`, the relay inbox hook) rather than from documentation.

| Event | Status |
|---|---|
| `PreToolUse` | `[obs]` matcher, rewrite via `updatedInput`, deny via exit 2 or `permissionDecision` |
| `PostToolUse` | `[obs]` fires with `tool_response` |
| `UserPromptSubmit` | `[obs]` stdout becomes turn context |
| `SessionStart` | `[obs]` stdout becomes first-turn context |
| `SessionEnd` | `[obs]` fires on exit with `transcript_path` |
| `Stop`, `SubagentStop`, `Notification`, `PreCompact` | `[chk]` parsed, not yet fired |

Three stdout shapes are accepted: empty, plain text (becomes context), and JSON
with `hookSpecificOutput`. Plain text is not a fallback — the relay inbox hook
depends on it.

**Ordering:** PreToolUse hooks run *before* the permission gate, so the gate
judges the rewritten command rather than the one the model proposed. See
`guardToolCall`.

**Known upstream quirk:** `rtk-rewrite.sh` stops rewriting whenever a project
`.claude/settings.local.json` exists, even an empty one. That is rtk's own
behavior, not the harness's, and it makes end-to-end tests of rewriting
unreliable in any repo that has one.


## Background shells

`bash_background` starts a command and returns a shell id immediately;
`bash_output` reads what is new since the last read; `kill_shell` stops it;
`/bashes` lists them. Everything still running is killed on exit.

A separate tool rather than a `run_in_background` flag on `bash`: pi's bash tool
owns its own schema and execution path, and wrapping it to add a parameter is
how the foreground case quietly breaks.

Reads are **incremental**. Returning the whole buffer on each poll would put the
same lines into context every time, which on a 32k window is a way to end a
conversation by checking on a build. The buffer holds the last 500 lines and
reports how many were dropped rather than silently skipping them.

The `onUpdate` protocol has four kinds and they are not all appends — `replace`
carries `output.text`, `append` carries `text`, `slide` drops `drop` characters
off the front, `metadata` carries nothing. Verified against a live `exec`.

## Images

`@screenshot.png` attaches the image as an image block on the prompt rather than
inlining it as text, for `png`/`jpg`/`jpeg`/`gif`/`webp`. A PNG read as UTF-8 is
megabytes of replacement characters: it ends a small-window conversation and
tells the model nothing.
