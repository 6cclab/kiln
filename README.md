# harness

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

This harness keeps the same interface and slash commands, and spends context
deliberately: tools are indexed rather than resident, skills load their bodies
only on invocation, and memory is budgeted against the model's actual window.

Measured result on the same catalog: **1,514 tokens, 5% of the window** — a 26x
reduction, leaving the window for the conversation.

## What it is built on

[`@earendil-works/pi`](https://github.com/earendil-works/pi) (MIT) provides the
agent loop, ~45 model providers, compaction, and the TUI primitives. This
project adds the context budgeting, the `.claude/` compatibility layer, MCP tool
gating, and session search.

Model-agnostic by construction: local Ollama, an API key, or a Claude Pro/Max
or ChatGPT Plus/Pro subscription. No code path branches on provider — only the
budget tier differs.

## Install

Requires Node 24 (for native TypeScript and `node:sqlite`).

```bash
npm install
npm link          # puts `harness` on PATH
```

## Use

```bash
harness                      # interactive session
harness -c                   # continue the most recent session here
harness --resume <id>        # resume a specific session
harness providers            # who you can talk to, and auth status
harness models               # models, their tiers, and usable budget
harness login anthropic      # log in with a Claude Pro/Max plan
```

Inside a session: `/` for commands, `@` to reference a file, `!` to run a shell
command directly, `#` to add a memory. `Ctrl+R` expands truncated tool output
and reasoning blocks, `Shift+Tab` cycles permission mode, `Esc` interrupts.

`@path` inlines the file so the model has it without spending a turn on a read,
capped at the tier's per-result budget. `@screenshot.png` attaches the image
instead.

`/doctor` is the first thing to run when something looks wrong: it reports the
model, tier, tool strategy, MCP status, hooks and agents on one screen, with
problems last.

## How context is kept small

- **Tiers.** `tierFor(model.contextWindow)` is the single place model choice
  becomes behavior. Everything downstream reads a tier and never asks which
  provider it is on, so the same code relaxes on a 200k model instead of
  branching.
- **Tool gating.** MCP tools are registered but inactive. The model sees a
  one-line index and calls `tool_search` to load the schemas it needs.
  Registration is free; activation is what costs tokens.
- **Postures.** A `coding` posture does not index UniFi or Grafana at all. This
  does more work than dynamic gating: indexing everything still costs 18% of a
  32k window, while a posture index costs 5%.
- **Skills.** Only `name` + `description` stay resident; bodies load on
  invocation. Measured on a real `~/.claude/skills`: 841 tokens resident versus
  8,316 if bodies were loaded.

## `.claude/` compatibility

Reads your existing configuration; writes nothing into it.

| Asset | Behavior |
|---|---|
| `~/.claude/skills`, `.claude/skills` | loaded, user-invocable ones become slash commands |
| `.claude/commands/*.md` | slash commands; nested dirs become `namespace:command` |
| `CLAUDE.md`, `@imports`, `.claude/rules/*.md` | system prompt, budgeted against the tier |
| `.claude/settings.json` | permissions, merged `user` → `project` → `local` |
| `~/.claude.json` | MCP servers |
| `.claude/agents/*.md` | subagents, dispatched with the `task` tool |
| `.claude/settings.json` `hooks` | `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart`, `SessionEnd` |

Subagents get their own context window: a search that reads thirty files spends
those tokens in the subagent's window, and the parent pays for one paragraph.
On a small model that is the difference between a task completing and not.

A `PreToolUse` hook may rewrite a command before it runs. Hooks are applied
**before** the permission gate, so the gate judges what will actually execute
rather than what the model proposed.

## Safety

Tool calls pass through a permission gate before running. Beyond the rules in
`settings.json`, any path **outside the workspace** requires confirmation even
when a rule would allow the tool — `allow: [Read]` means "reading is fine here",
not "read anything on this machine". Widen the workspace with `/add-dir`.

## Development

```bash
npm run check     # typecheck + tests
npm test
```

Measurement scripts in `scripts/` are the reproducible record behind the numbers
above — `gating-check.ts` re-measures the catalog, `check-thinking.ts` re-tests
reasoning suppression after an Ollama upgrade.

`docs/claude-code-parity.md` is the interface checklist. Items are marked
`[obs]` (directly observed) or `[chk]` (from familiarity, verify before
trusting).
