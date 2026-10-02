# kiln

A coding agent with Claude Code's interface that runs on self-hosted models.

## Why

Claude Code's resident context floor is too large for a local model. Measured on
this machine, against a 32,768-token window:

| | tokens | % of window |
|---|---|---|
| Full MCP tool schemas (165 tools, 9 servers) | 31,897 | **97%** |
| Grafana alone (81 tools) | 19,992 | 61% |

The catalog consumes the window before a system prompt, a file read, or a single
user turn. That is an architecture problem, not a tuning problem.

kiln keeps the same interface and slash commands, and spends context
deliberately: tools are indexed rather than resident, skills load their bodies
only on invocation, and memory is budgeted against the model's actual window.

Measured result on the same catalog: **1,514 tokens, 5% of the window**, a 26x
reduction, leaving the window for the conversation.

## What it is built on

A single Go binary. The agent loop, session format, compaction and provider
catalog are ports of [`@earendil-works/pi`](https://github.com/earendil-works/pi)
(MIT): sessions are pi's JSONL v4 and open in either implementation, and the
41 providers over 10 API shapes are pi-ai's catalog, vendored under
`internal/provider/catalog`. The terminal UI is
[Bubble Tea v2](https://github.com/charmbracelet/bubbletea); finished output is
committed to the terminal's own scrollback, only the live region repaints.

The interface follows the kiln "Ruled" design language (`docs/kiln-design.md`):
a labelled hairline rule above every transcript block, a warm-dark truecolor
palette, ruled diffs and permission prompts, and a block-letter banner.

Model-agnostic by construction: local Ollama, an API key, or a Claude Pro/Max,
ChatGPT Plus/Pro, GitHub Copilot, Kimi or xAI subscription. No code path branches
on provider; only the budget tier differs.

## Install

Requires Go 1.26 and git.

```bash
curl -fsSL https://raw.githubusercontent.com/6cclab/kiln/main/scripts/install.sh | sh
```

`scripts/install.sh` builds kiln from source and installs it to `~/.local/bin`.
`--ref <branch|tag|commit>` picks the version, `--dir <path>` the install directory,
and `--source <checkout>` builds a local checkout instead of cloning. Running it again
updates kiln in place; a kiln that is already running keeps its old binary.

From a checkout:

```bash
make build                 # bin/kiln, installed to Go's bin dir (~/go/bin)
```

## Use

```bash
kiln                      # interactive session
kiln -c                   # continue the most recent session here
kiln --resume <id>        # resume a specific session
kiln -p "fix the bug"     # one prompt, then exit (see --output-format)
kiln providers            # who you can talk to, and auth status
kiln models [provider]    # models, their tiers, and usable budget
kiln login anthropic      # log in with a Claude Pro/Max plan
kiln doctor               # model, tier, tools, MCP, hooks, agents, logs, problems
kiln mcp                  # MCP servers and their state
kiln --debug              # debug-level run log, path announced at startup
kiln --fullscreen         # alt-screen TUI: scrolling transcript, input pinned at the bottom
```

MCP servers connect in the background after the prompt appears: the footer shows
progress, and a one-line notice counts any servers that failed (`1 MCP server
unavailable · run /mcp`). `/mcp` names each failed server with a reason you can
act on; in print mode the same name and reason go to stderr. The model can use a
server's tools as soon as that connect finishes.

Every run writes a log to `~/.harness/logs` (the last 30 are kept): startup phases
with elapsed time, each server's connect outcome and duration, and every turn, tool
call, retry and compaction. `kiln doctor` prints the directory and the latest
file; attach that file to a bug report.

Inside a session: `/` for commands, `@` to reference a file, `!` to run a shell
command directly, `#` to add a memory. `Ctrl+O` toggles the detailed transcript
(tool output and reasoning expanded), `Shift+Tab` cycles permission mode, `Esc`
interrupts, `Ctrl+C` twice exits. `Ctrl+F` toggles full-screen mode (also
`--fullscreen`): the transcript scrolls inside the terminal with `PgUp`/`PgDn`,
`Shift+Up`/`Shift+Down` or the mouse wheel, new output follows only while you are
at the bottom, and history re-wraps on resize. Toggling either way redraws the
transcript from the session log, so shell (`!`) output and hook notices that are
not part of the log do not survive the switch. Inline mode stays the default; the
screen-reader flag always uses inline.

`@path` inlines the file so the model has it without spending a turn on a read,
capped at the tier's per-result budget. `@screenshot.png` attaches the image
instead.

`/doctor` is the first thing to run when something looks wrong: it reports the
model, tier, tool strategy, MCP status, hooks and agents on one screen, with
problems last.

Credentials live in `~/.harness/credentials.json`, sessions in
`~/.harness/sessions`, prompt history in `~/.harness/history`. A stored
credential wins over an environment variable for the same provider.

## How context is kept small

- **Tiers.** The model's context window is the single place model choice becomes
  behavior. Everything downstream reads a tier and never asks which provider it
  is on, so the same code relaxes on a 200k model instead of branching.
- **Tool gating.** MCP tools are registered but inactive. The model sees a
  one-line index and calls `tool_search` to load the schemas it needs.
  Registration is free; activation is what costs tokens.
- **Postures.** A `coding` posture does not index UniFi or Grafana at all. This
  does more work than dynamic gating: indexing everything still costs 18% of a
  32k window, while a posture index costs 5%.
- **Skills.** Only `name` + `description` stay resident; bodies load on
  invocation. Measured on a real `~/.claude/skills`: 841 tokens resident versus
  8,316 if bodies were loaded.
- **Subagents.** A `task` runs in its own session and window; the parent pays
  for one paragraph.
- **Roles.** `settings.json`'s `modelRoles` (fast/structured/heavy →
  provider/model) is the one place model names appear; a `task` call picks a
  role instead of naming a provider and model itself. Concurrent `task` calls
  from one turn run in parallel, each in its own session. A subagent can
  dispatch subagents of its own, up to two levels deep; a role that crosses
  onto a different paid provider asks once per session per role, not once per
  dispatch.

## `.claude/` compatibility

kiln reads your existing Claude Code configuration exactly as Claude Code
does, but never writes any of `~/.claude`, `<repo>/.claude`, `~/.claude.json`
or `<repo>/.mcp.json`. Everything kiln itself saves goes under `.kiln`
instead - `~/.kiln` for user-scope state, `<repo>/.kiln` for project/local
state - and each kiln file joins the Claude Code hierarchy at the same scope,
winning for a single value (model) and adding to a list (permission rules,
MCP servers). A `.kiln` directory kiln creates gets a `.gitignore` that
ignores everything except `mcp.json`, which (like Claude Code's `.mcp.json`)
is meant to be committed.

| kiln writes | From | Mirrors |
|---|---|---|
| `<repo>/.kiln/settings.local.json` | `/permissions`, "don't ask again" | `.claude/settings.local.json` |
| `~/.kiln/settings.json` | `/model`'s default | `~/.claude/settings.json` |
| `~/.kiln/CLAUDE.md` | a `#note`, and `/memory user`, when the project has no `CLAUDE.md` | `~/.claude/CLAUDE.md` |
| `~/.kiln/mcp.json` | `kiln mcp add -s user\|local` | `~/.claude.json`'s top-level `mcpServers` / `projects[<dir>].mcpServers` |
| `<repo>/.kiln/mcp.json` | `kiln mcp add -s project` | `.mcp.json` |

A git-tracked or symlinked `<repo>/.kiln/settings.local.json` is held until
the folder is trusted - only its deny/ask rules apply until then.

| Asset read (never written) | Behavior |
|---|---|
| `~/.claude/skills`, `.claude/skills` | loaded, user-invocable ones become slash commands |
| `.claude/commands/*.md` | slash commands; nested dirs become `namespace:command` |
| `CLAUDE.md`, `@imports`, `.claude/rules/*.md` | system prompt, budgeted against the tier |
| `.claude/settings.json` | permissions, merged `user` → `project` → `local` |
| `~/.claude.json`, `.mcp.json` | MCP servers, merged with kiln's own `mcp.json` files (same-scope kiln entry wins) |
| `.claude/agents/*.md` | subagents, dispatched with the `task` tool |
| `.claude/settings.json` `modelRoles` | model roles for `task` dispatch (fast/structured/heavy → provider/model) |
| `~/.claude/keybindings.json` | editor key overrides; conflicts reported at startup |
| `.claude/settings.json` `hooks` | `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart`, `SessionEnd`, `Stop`, `SubagentStop`, `Notification`, `PreCompact` |

A `PreToolUse` hook may rewrite a command before it runs. Hooks are applied
**before** the permission gate, so the gate judges what will actually execute
rather than what the model proposed. A blocking `Stop` hook is reported to you;
kiln does not re-prompt the model on it.

## Safety

Tool calls pass through a permission gate before running. Beyond the rules in
`settings.json`, any path **outside the workspace** requires confirmation even
when a rule would allow the tool: `allow: [Read]` means "reading is fine here",
not "read anything on this machine". Widen the workspace with `/add-dir`. In
print mode there is nobody to ask, so `ask` is a refusal.

## Documentation

`docs/README.md` is the index. The short version:

- `docs/usage.md`: running a session, keys, slash commands, permissions, MCP, subagents.
- `docs/configuration.md`: flags, environment variables, `settings.json`, `.claude` assets, MCP config.
- `docs/troubleshooting.md`: `kiln doctor`, the run log, and the fix for each known symptom.
- `docs/architecture.md`: package map, lifecycle of a turn, sessions, budget, providers, tools.
- `docs/contributing.md`: building, testing, conventions, and how to add a tool, command or provider.

## Evaluation

`kiln eval` runs the built binary against a directory of scenarios
(`eval/scenarios/`, one prompt/fixture/check set per subdirectory) and grades
each run mechanically (`checks:` — file contents, tool calls, permission
blocks, usage, compaction, subagent dispatch, ...) and, where a scenario
declares a `judge:` rubric, by an LLM judge. Each run is written as one JSON
record to a timestamped file under `eval/results/`.

```bash
make eval                    # every scenario against the faux provider, then a report
bin/kiln eval run --only fix-bug -j 4
bin/kiln eval report --results eval/results --format md
```

`eval/results/baseline.jsonl` is the committed reference: `kiln eval report`
diffs the newest run against it and exits non-zero if any (scenario, model)
pair's score regresses past `--fail-on-regression`. Everything else under
`eval/results/` (timestamped run files, and `runs/` when `--keep` is passed)
is machine-local and gitignored.

Live runs (real providers, real network) are opt-in: `kiln eval run` refuses
any non-faux model unless `KILN_EVAL_LIVE=1` is set, so `make eval` never
leaves the faux provider. `make eval-live MODELS=ollama/qwen3.8` runs the
suite against real models instead.

## Development

```bash
make check        # vet, staticcheck, gofmt, go test ./...
make e2e          # the real binary through a PTY and a scripted model (faux)
make e2e-live     # the same against a real model; HARNESS_E2E_LIVE=1, HARNESS_LIVE_MODEL=...
make eval         # eval/scenarios against faux, reported against eval/results/baseline.jsonl
```

Every screen assertion runs against an emulated terminal, never against the
bytes written, because that is where every rendering bug in this project has
lived. `docs/testing.md` explains the driver, the faux model server and
`kiln-drive`, which turns a bug report into a replayable script.

Two upstream libraries are vendored under `third_party/`, each with patches
described in its `HARNESS-PATCH.md`: ultraviolet (inline-renderer shrink
handling) and bubbletea (ordered `Println` commits, a full repaint of the live
region, restoring the caret after a committed line, and guarding autowrap on a
full-width committed line).

`docs/claude-code-parity.md` is the interface checklist. Items are marked
`[obs]` (directly observed) or `[chk]` (from familiarity, verify before
trusting).
