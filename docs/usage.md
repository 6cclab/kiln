# Using kiln

A session guide for the `kiln` binary. For install and the one-paragraph pitch,
see `README.md`. For the visual contract behind everything described here —
palette, block layout, glyphs — see `docs/kiln-design.md`.

## 1. Starting a session

```bash
kiln                       # new interactive session, new session file
kiln -c                    # continue the most recent session in this directory
kiln -r [id]                # resume a session, by id or unique id prefix
kiln --resume               # resume with no id: the latest session
kiln --fork-session -r <id> # resume into a NEW copy of that session's full tree
kiln --session-id <uuid>    # name a new session's id explicitly
kiln -n "name"               # name the session
kiln -p "fix the bug"       # one prompt, print the result, exit
```

Plain `kiln` always creates a new session file. `-c`/`--continue` and
`-r`/`--resume` open an **existing** file and keep appending to it in place.
Add `--fork-session` to instead copy the whole session tree into a brand-new
file (a fresh id, timestamped at fork time) and continue from the copy,
leaving the original untouched. `--resume <id>` matches a full id or a unique
prefix; an ambiguous prefix is a hard error, and an id that matches nothing
falls through to starting a new session rather than failing.
Source: `internal/agent/session.go`.

**Print mode** (`-p`) runs one prompt non-interactively and exits.
`--output-format` controls what comes out:

| format | output |
|---|---|
| `text` (default) | the final assistant text only |
| `json` | one object: `ok`, `text`, `toolCalls`, `blocked`, plus `usage` (input, output, cache_read, cache_write tokens), `total_cost_usd`, `duration_ms`, `num_turns`, `num_tool_calls`, and `reason` when the run was cut short |
| `stream-json` | one NDJSON line per event while it runs (`tool_start`, `tool_end`, `assistant`, `result`); nothing printed at the end. The `result` line carries the same totals as `json` |

`--max-turns n` stops a print-mode run before it starts turn `n+1`; a run that
finishes on turn `n` is not cut. When it trips, stderr says `kiln: stopped
after n turns (--max-turns)`, `ok` is false, `reason` is
`max-turns-exceeded`, and the exit code is 1.

There is no one to answer a permission prompt in print mode, so any tool call
that would otherwise ask is auto-denied and recorded in `blocked`. Exit code
is `0` on completion, `1` on an empty prompt, a bad slash command, a
`UserPromptSubmit` hook block, or a run that didn't complete.
Source: `internal/cli/print.go`.

`--effort low|medium|high|xhigh|max` sets the reasoning effort sent to the
model on every turn of the session (`internal/harness/turn.go`). Full-screen
(alt-screen) is the default (§10); `--inline` opts out, keeping the
transcript in native scrollback (`--fullscreen` is still accepted, as a
no-op, for compatibility). `--ax-screen-reader` always runs inline (§11).

`kiln session inspect <path>` is the only `session` subcommand: it opens a
session file directly and prints a JSON report (entry counts, branch tips,
usage totals). There is no `session list`. Source: `cmd/kiln/main.go`.

**Trust prompt.** The first time kiln runs in a directory it has not seen
before, it asks:

> Quick safety check: Is this a project you created or one you trust? (Like
> your own code, a well-known open source project, or work from your team).
> If not, take a moment to review what's in this folder first.
> kiln will be able to read, edit, and execute files here.

Choose "No, exit" or "Yes, I trust this folder" (Enter confirms, Esc declines).
Trusting a directory also trusts everything under it, so trusting a repo root
covers its worktrees. Until you answer, no hook runs and the project's allow
rules wait; "Yes" applies them without a restart (`docs/configuration.md`,
Trust). Recorded in `~/.harness/trusted.json`; set
`HARNESS_TRUST_ALL=1` to skip the prompt entirely. Source: `internal/tui/dialog_trust.go`, `internal/claude/trust/trust.go`.

## 2. The screen

Startup shows a banner (KILN wordmark, version, `<cwd> · branch <b> · model
<m>`, a shortcut tips row), then the transcript: each turn, tool call, diff
and system note commits as its own labelled block (`you`, `kiln`, a tool
name, `edit`, `system`, `error`, ...) under a hairline rule. By default
(full-screen/alt-screen, §10) that transcript scrolls in its own viewport,
banner at the top, growing down until it fills the screen and then
scrolling; under `--inline` it goes straight into the terminal's own
scrollback instead. Below that, a small live region redraws every
frame: a spinner row while the model is working (gerund + elapsed/token
count, `esc to stop` right-aligned), the permission/plan prompt when one is
open, the input box, and one status-line row below it — a mode dot and label
(e.g. `● auto-edit  ⇧⇥`), the cwd/branch, and a context-usage meter plus
cumulative cost, right-aligned. A configured `statusLine` command's own
output renders as extra dim rows below that one, refreshed every 2s. Full
visual detail — colours, glyphs, exact block anatomy — is
`docs/kiln-design.md`, not this file.

## 3. Typing

| Prefix | Meaning |
|---|---|
| `/` | slash command — opens the command palette |
| `@` | file (or image) mention — fuzzy path completion |
| `!` | run a shell command directly; output goes to the transcript, never to the model |

`@path` inlines the file's contents into your message so the model has it
without spending a turn on a read, split across however many `@mentions` are
on the line and capped per-mention at the tier's read budget (floor 256
tokens); a mention outside the workspace, or a directory, is skipped and
reported rather than inlined. `@screenshot.png` attaches the image instead of
inlining text. A `!command` line runs in your own shell/environment (capped
at 64KB / 2000 lines of captured output) and is never fed back to the model —
reference its output yourself in your next message if the model needs it.
kiln has no `#` memory-note shortcut: Claude Code's own prompt input has
none either (only a bash prefix and a plain prompt), so a line starting with
`#` is an ordinary message. Use `/memory` (§5) to open a CLAUDE.md in your
editor instead. Source: `internal/cli/mentions.go`, `internal/tui/inputmodes.go`.

**Autocomplete.** Typing `@` anywhere opens fuzzy file completion; typing `/`
at the start of the line opens command completion, then argument completion
once you've typed a space after a known command. `↑`/`↓` move the selection,
`Tab` or `Enter` accepts it (a slash command gets a trailing space so you can
keep typing arguments; a directory keeps its trailing `/` so you can keep
descending), `Esc` closes the popup without accepting.
Source: `internal/tui/autocomplete.go`.

**Multi-line and history.** `Enter` submits; `Shift+Enter`, `Alt+Enter`,
`Ctrl+J`, or a trailing `\` before `Enter` all insert a newline instead.
`↑`/`↓` recall prompt history only when the cursor sits on the input's first
or last line (otherwise they move the cursor); the first `↑` stashes your
in-progress draft so `↓` past the newest history entry restores it. History
lives at `~/.harness/history` (outside any project, since a prompt can hold
anything), capped at 500 entries. A paste is inserted as one atomic block, so
a multi-line paste never submits early. Source: `internal/tui/editor`.

## 4. Keybindings

| Key | Action |
|---|---|
| `Enter` | submit the input |
| `Shift+Enter` / `Alt+Enter` / `Ctrl+J` / `\`+`Enter` | insert a newline |
| `↑` / `↓` | recall history at the buffer's edges, else move the cursor |
| `Ctrl+K` | kill to end of line |
| `Ctrl+U` | kill to start of line |
| `Ctrl+W` / `Alt+Backspace` | kill the previous word |
| `Ctrl+Y` | yank back the last kill |
| `Alt+←`/`→`, `Ctrl+←`/`→` | move by word |
| `Home`/`Ctrl+A`, `End`/`Ctrl+E` | move to line start / end |
| `Ctrl+O` | toggle the detailed transcript (expand tool output and reasoning) |
| `Shift+Tab` | cycle permission mode: auto → manual → accept edits → plan → auto |
| `Ctrl+L` | clear the screen |
| `Ctrl+F` | toggle full-screen mode |
| `Ctrl+C` | interrupt a running turn, or clear the input line; twice within 1s exits |
| `Ctrl+D` | exit, only when the input line is empty |
| `Esc` | interrupt a running turn; twice within 1s on an idle, empty input opens Rewind |
| `?` | open the shortcuts panel (idle, empty input only) |
| `PgUp`/`PgDn`, `Shift+↑`/`↓`, mouse wheel | scroll the transcript (full-screen mode only) |
| drag with the mouse | select transcript text and copy it to the clipboard on release (full-screen mode; inline mode leaves selection to the terminal) |

While a permission or plan prompt is open, it owns every key until answered
(§6). Source: `internal/tui/keys.go`, `internal/tui/editor`.

**Overriding.** `~/.claude/keybindings.json` remaps the editor's own actions
(submit, newline, history, word/line motion, kill/yank) — a flat JSON object
mapping an action id like `"tui.input.submit"` to a single key string. It
does not reach the global bindings above (`Ctrl+O`, `Shift+Tab`, `Ctrl+L`,
`Ctrl+F`, `Ctrl+C`, `Ctrl+D`, `Esc`), which are fixed. A key bound twice, or
a file that isn't a JSON object, is reported to stderr at startup rather than
silently resolved. Source: `internal/claude/keybindings`, `internal/tui/keybindings.go`.

## 5. Slash commands

Grouped by area, in registration order (a name defined in more than one area
is won by the later one — see precedence below).

**Session**

| Command | Args | Effect |
|---|---|---|
| `/resume` | `[id]` | switch to a past session here: type `/resume ` to pick one by its first prompt, or give an id prefix; with no argument, list recent sessions |
| `/rewind` | `[entry-id]` | list recent turns, or move the conversation's branch tip back to one |
| `/export` | `[path]` | write this conversation to a Markdown file |
| `/memory` | `[user\|project\|auto]` | no args: list the available CLAUDE.md files and the auto-memory folder; a name: open that CLAUDE.md in `$EDITOR`/`$VISUAL` (or print its path with none set), or name the auto-memory folder |
| `/add-dir` | `<path>` | let tools touch another directory without prompting; no args lists current roots |
| `/init` | — | ask the model to write/refresh this project's `CLAUDE.md` |
| `/config` | — | list the settings files in effect and their scope |

**Model & context**

| Command | Args | Effect |
|---|---|---|
| `/model` | `[provider/model]`, `roles` | show or switch the active model; `roles` prints the configured model roles |
| `/context` | — | context window, tier, tool/system-prompt/reserve budget breakdown |
| `/cost` | — | token pricing for this session, and a by-model breakdown if tracked |
| `/compact` | — | summarize and compact the current context |
| `/usage` | — | context budget used this session (there's no billing plan to report against) |

**Inspect**

| Command | Args | Effect |
|---|---|---|
| `/status` | — | model, provider, auth and connection status |
| `/tools` | — | which tools are currently resident |
| `/hooks` | — | configured hooks and the events they fire on |
| `/doctor` | — | one screen: model, tier, tool strategy, MCP, hooks, agents, problems last |

**Manage** (these shadow same-named report-only commands above)

| Command | Args | Effect |
|---|---|---|
| `/permissions` | — | view and edit permission rules (persists — see §6) |
| `/mcp` | — | MCP servers and their tools, in a dialog |
| `/agents` | — | subagents available for `task` dispatch |

**Account**

| Command | Args | Effect |
|---|---|---|
| `/login` | `[provider]` | auth status, or how to log in |
| `/logout` | `<provider>` | how to log out |
| `/terminal-setup` | — | check terminal capabilities |

**Inline**

| Command | Args | Effect |
|---|---|---|
| `/posture` | `[coding\|ops\|all]` | show or switch which MCP servers are searchable |
| `/bashes` | — | list background shells and their status (read-only; kill via the model's `kill_shell` tool) |
| `/todos` | — | show the current todo list |

**Always available**: `/help`, `/clear`, `/exit` (`/quit`).

**Precedence.** Sources register in this order — builtin, skills, session,
account, inspect, manage, inline, personal (`~/.claude/commands`), project
(`.claude/commands`) — and a later source's command wins a name collision.
So a project command can shadow any built-in, `/permissions`/`/mcp`/`/agents`
in Manage shadow their Inspect/builtin report-only versions, and Inline's
`/todos` shadows Account's. Source: `internal/commands/registry.go`,
`internal/cli/commands.go`.

**Project and personal commands.** Every `.md` file under `.claude/commands`
(project) or `~/.claude/commands` (personal) becomes a slash command named
after the file; files under a subdirectory get namespaced, so
`commands/frontend/component.md` becomes `/frontend:component`. Its
frontmatter (`description`, `argument-hint`, `allowed-tools`, `model`) drives
the palette entry; `$ARGUMENTS`/`$1`/`$2`... in the body are substituted from
what you typed, and running the command sends the rendered body to the model
as a prompt. Source: `internal/claude/commands`.

**Skills.** A skill under `.claude/skills` or `~/.claude/skills` appears as a
slash command named after it as long as its frontmatter doesn't set
`user-invocable: false` (the default is invocable). It carries no namespace;
running it sends the skill's body — plus anything you typed after it — to
the model as a prompt. Source: `internal/claude/skills`, `internal/commands/sources.go`.

## 6. Permissions

| Mode | What it allows without asking | Everything else |
|---|---|---|
| `manual` (default) | read-only tools | asks |
| `acceptEdits` | read-only tools, edits and writes | asks |
| `plan` | read-only tools | denied outright, not asked |
| `auto` | reads, edits in the workspace, read-only commands; anything else a classifier model approves (deny and ask rules and the workspace boundary still apply) | a classifier block goes back to the model; the 3rd block in a row, or a failed check, asks |
| `dontAsk` | everything | — |
| `bypassPermissions` | everything, including the outside-workspace check | deny rules still apply |

An explicit `deny` rule always wins regardless of mode. `Shift+Tab` cycles
`auto → manual → acceptEdits → plan → auto`; `dontAsk` and `bypassPermissions`
sit outside that ring and cycling out of either lands on `auto`. Start in a
given mode with `--permission-mode`. Source: `internal/claude/settings/settings.go` (`Decide`).

**The prompt**, when one is needed, offers different options by tool kind:

- A generic tool: **1 Yes** · **2 Yes, and don't ask again for this** · **3
  No, and tell kiln what to do instead** (opens a feedback line) — `Esc`
  declines outright.
- A shell command: **1 Yes** · **2 Yes, and don't ask again for: `<rules>`**
  · **3 Yes, and switch to auto mode** · **4 No** (`↑`/`↓` to move, `Tab` to
  amend).
- An edit or write: **1 Yes** · **2 Yes, and switch to accept edits for this
  session** · **3 No**.

On a shell command, "don't ask again" saves one allow rule per command in
the line that still needs approval, as Claude Code does: approving `git
status && npm test && make build` saves `Bash(npm test *)` and `Bash(make
build *)` (git status is read-only), so a later `npm test` runs unasked. A
command whose second word names a subcommand gets a `<command> <subcommand>
*` rule; anything else, and a shell or wrapper (`sh`, `env`, `sudo`, `xargs`,
`timeout`, …), gets an exact rule. The option names every rule it saves
(those that do not fit the row are counted, "+2 more"). The rules go to the
project's `.kiln/settings.local.json`, the file `/permissions` writes, so
they last across sessions. kiln reads Claude Code's `.claude` settings but
never writes them: everything it saves goes under `.kiln` (a `.kiln/.gitignore`
keeps it out of git). On any other tool, "don't ask again" is a
session-only grant for that exact call.

The option is left out (and the options below it move up a number) when the
grant would not be honoured: a deny or ask rule matched, the command names a
file kiln cannot resolve while Read/Edit path rules exist, the line cannot
be parsed or runs something kiln cannot name, a command in it has a
non-literal word, or it would need more than 5 rules. Source:
`internal/claude/settings/bash_suggest.go`, `internal/tui/permission_render.go`, `internal/claude/permission/permission.go`, `internal/claude/writesettings`.

**Outside the workspace.** A tool call targeting a path outside the current
workspace roots always asks, even in a mode that would otherwise allow it
silently — except `bypassPermissions`, which skips this too. Widen the
workspace with `/add-dir` (or list its current roots with no argument).

**Plan mode.** Enter it with `Shift+Tab` or `--permission-mode plan`. The
model is told it may read and search but not change anything; every tool
that isn't read-only is refused, not merely asked. When it has a plan, it
calls `exit_plan_mode`, which opens an approval dialog: **1 Yes, and use auto
mode** · **2 Yes, manually approve edits** · **3 Tell kiln what to change**
(feedback — the session stays in plan mode for a revision). Source:
`internal/agent/planmode.go`, `internal/tools/planmode.go`, `internal/tui/permission_render.go`.

## 7. MCP servers and postures

MCP servers are read from `~/.claude.json`/`.mcp.json` (the same files
`kiln mcp add` writes - see `docs/configuration.md`) or `--mcp-config`, and
connect in the background after the prompt appears; the footer reports progress, and a
failed server is named once with a reason. `/mcp` opens a dialog: a
sectioned server list with a status glyph, `↑`/`↓` to move, `Enter` for a
server's tool list and connect latency, `Esc` back or to close.
`kiln mcp` (a top-level subcommand, not the dialog) connects to every server
and prints a one-shot report: `N/M connected, M tools`, then each server's
tool count and connect time, or its failure reason.

A server's tools are registered but not resident: the model sees a one-line
index and calls `tool_search` (a tool, not a slash command) to load the
schemas it actually needs — activation, not registration, is what costs
context. A **posture** additionally limits which servers are indexed at all:

| Posture | Scope |
|---|---|
| `coding` (default) | infisical, argocd-mcp, personal-kb, homelab-kb, claude-relay, sentry, github |
| `ops` | grafana, proxmox, argocd-mcp, unifi-mcp, pocket-id, infisical |
| `all` | every configured server — expensive on a small context window |

A server that none of these lists name is searchable under every posture, so
a newly configured server works without switching posture; the lists only
keep the known noisy servers out of a coding session.

Set the default with `HARNESS_POSTURE`; switch at runtime with `/posture
[coding|ops|all]` (no argument lists postures, tool counts, and the active
one). Switching clears any tools already admitted under the old posture.
Source: `internal/mcp/gating.go`, `internal/cli/mcp.go`, `internal/commands/inline.go`.

## 8. Subagents and model roles

The model's `task` tool dispatches a subagent with its own context window;
only its final report re-enters the parent's context. Each call names a
`subagent_type` (from `.claude/agents/*.md`, or the built-in
`general-purpose`), a short `description`, a self-contained `prompt` (the
subagent sees nothing else of the conversation), and optionally a **model
role** — `fast` for scripts and lookups, `structured` for well-specified
changes, `heavy` for open-ended work — mapped to a provider/model by
`settings.json`'s `modelRoles`; omitting it inherits the current model.
Concurrent `task` calls in one turn run in parallel. A subagent can dispatch
subagents of its own, but only one level: at two levels deep the `task` tool
isn't offered at all. Source: `internal/agent/subagent.go`, `internal/agent/dispatch.go`.

While a dispatch runs, a **subagents panel** appears above the input: each
row shows the agent's name (amber running, green done, red error), its task
description, a last-action indicator, a 10-cell progress meter, and — once
done — a token total; nested dispatches are indented under their parent.
Source: `internal/tui/subagents.go`.

If a role's dispatch would cross onto a different, metered provider than the
one the parent turn is running on, it goes through the ordinary permission
gate first — the same "Allow kiln to use task?" prompt as any other tool,
not a special dialog. Choosing "don't ask again" there is remembered for
that role for the rest of the session only (never written to disk); plain
"Yes" asks again next time. Source: `internal/agent/dispatch.go`.

## 9. Sessions on disk

Sessions live at `~/.harness/sessions` (override with `HARNESS_SESSIONS_DIR`),
one JSONL file per session. `/resume` lists up to 15 recent sessions in the
current directory with their first prompts; `/resume <id>` ends the current
session (hooks, shells and MCP servers shut down as on exit) and restarts
kiln with the same flags plus `--resume <id>`, so the chosen conversation
continues in the same terminal. `kiln --resume <id>` does the same from the
shell. `/rewind` lists the last 10 user turns and moves the
conversation's branch tip back to one you pick (also reachable by pressing
`Esc` twice on an idle, empty input). `/export [path]` writes a Markdown
transcript (`# Session transcript`, then `## user`/`## assistant` sections),
defaulting to `transcript-<timestamp>.md` in the current directory.
Prompt history (not the session itself) is separate, at `~/.harness/history`.
Source: `internal/agent/session.go`, `internal/session/jsonl`, `internal/commands/session_commands.go`.

## 10. Full-screen mode

Full-screen (alt-screen) is the default layout: the transcript scrolls in
its own viewport, banner at the top, with the input pinned at the bottom,
navigable with `PgUp`/`PgDn`, `Shift+↑`/`↓`, or the mouse wheel; new output
only auto-follows while you're already at the bottom, and history re-wraps
on a terminal resize. `--inline` opts out at startup, keeping the transcript
in native scrollback instead; `Ctrl+F` toggles either direction at any time.
Toggling redraws the transcript from the session log, so anything not part
of that log — `!`-command output, hook notices — doesn't survive the
switch. `--fullscreen` is still accepted (a no-op — full-screen is already
the default) for compatibility with scripts/muscle memory from before it
was. Source: `internal/tui/app.go`, `internal/cli/tui.go`,
`internal/cli/args.go`.

## 11. Screen reader / plain mode

`--ax-screen-reader` always runs inline (full-screen is unavailable and
`Ctrl+F` is a no-op — screen readers read scrollback, not a redrawn
viewport) and switches every glyph to a plain-ASCII equivalent — no
box-drawing rules, no animated spinner frames — so the transcript reads as
flat text end to end.
Source: `internal/cli/args.go`, `internal/cli/tui.go`, `internal/tui/doc.go`.
