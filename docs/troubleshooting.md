# Troubleshooting

Symptom-first. Every message quoted here is copied from the source that prints
it, with a `file:line` to confirm it still says that.

## First steps

```bash
kiln doctor      # model, tier, tools, MCP, hooks, agents, settings, logs, problems
kiln --debug     # debug-level log; path is announced at startup / on stderr
```

`/doctor` does the same inside a session. Logs go to `~/.harness/logs`
(`HARNESS_LOG_DIR` overrides), one file per run, last 30 kept
(`internal/diag/diag.go`). `--debug`/`HARNESS_DEBUG=1` adds every
turn, tool call, retry and compaction pass, not just phase timings
(`internal/diag/diag.go`). `kiln doctor`'s `logs` line names the latest file —
attach that to a bug report, plus a `kiln-drive --record` capture for anything
visual (see "Reporting a bug").

## Startup and models

- **`kiln: invalid model %q: expected provider/model`** (`internal/cli/chat.go`).
  `--model`, settings.json's `model` (only honored with a `/` in it),
  `HARNESS_MODEL`, or the default resolved to something not `provider/model`
  shaped. Fix: `kiln --model anthropic/claude-opus-4-6`; `kiln models` lists pairs.

- **`unknown model "%s" on provider "%s": run a refresh first if the provider is
  dynamic`** (`internal/provider/registry.go`). Typo, or (Ollama) the model
  isn't pulled / the refresh failed (a `kiln: refreshing %s: %v` line precedes it,
  `internal/cli/chat.go`). Fix: `kiln models <provider>`; `ollama pull <model>`.

- **`Context window of %d tokens is too small: the %s tier floor (system prompt
  %d + tools %d + reserve %d) exceeds it by %d tokens. Raise num_ctx, or pick a
  model with a larger window.`** (`internal/budget/tier.go`,
  `ContextTooSmallError`). Usable budget fell below the 4,096-token floor
  (`MinUsableTokens`, `internal/budget/tier.go`) — checked at startup so
  it doesn't surface as a silently truncated first turn. Fix: raise `num_ctx` /
  `OLLAMA_CONTEXT_LENGTH`, or pick a bigger model (`kiln models` shows budgets).

- **`kiln: role %s`** warnings, e.g. `heavy: "opus" is not provider/model` or
  `heavy: anthropic/claude-opus-4-6 is not among the available models`
  (`internal/claude/agents/agents.go`, printed at `internal/cli/chat.go`).
  A `modelRoles` entry is malformed or names an unavailable model. Not fatal — a
  bad role falls back to the parent model at dispatch. Fix: correct
  `.claude/settings.json`'s `modelRoles`; check with `kiln models`.

- **`roles %s and %s both use ollama with different models; loading a second
  model may stall the host`** (`internal/cli/subcommands.go`, also in `kiln
  doctor`). Ollama keeps one model resident; two roles on different Ollama
  models can evict each other. Fix: point every Ollama role at the same model.

- **`CLAUDE.md files use ~Nk tokens, over this model's Mk memory budget;
  loaded anyway`** (`internal/cli/chat.go`). The CLAUDE.md files alone
  exceed the tier's memory budget (10% of the window). They still load;
  every rule is then listed by description rather than loaded
  (`internal/claude/memory/memory.go`). Fix: trim CLAUDE.md, move detail
  into `.claude/rules/*.md` with a `description`, or use a bigger-window
  model.

- **`kiln: interactive mode requires a TTY on stdin; use -p`**
  (`internal/cli/chat.go`). stdin isn't a terminal and no `-p` was given.
  Fix: `kiln -p "..."`, or run from a real terminal.

- **No "no provider configured" message at startup.** `resolveAuth` treats
  "nothing stored, nothing in env" as an empty key, not an error
  (`internal/provider/builtin/builtin.go`); auth is only checked when a
  request actually goes out, so a misconfigured provider fails at first turn
  (a 401/HTTP error), not at launch. Fix: check first with `kiln providers` /
  `kiln doctor`, and `kiln login <provider>`.

- **`keybindings: %s`** at startup — a parse error (`could not parse <path>:
  <err>`, `<path> must contain a JSON object mapping actions to keys`,
  `internal/claude/keybindings/keybindings.go`) or a conflict (`<key> is
  bound to <actions>`, `internal/claude/keybindings/keybindings.go`), printed
  before the TUI starts (`internal/cli/chat.go`; editor-binding
  conflicts separately at `internal/cli/tui.go`). Fix: fix or remove the
  entry in `~/.claude/keybindings.json`.

## Authentication

Credentials live in `~/.harness/credentials.json`, one entry per provider,
written atomically (temp file + rename), mode `0600`
(`internal/auth/file_store.go`). A **stored credential always wins
over an environment variable** for the same provider — env is consulted only
when nothing is stored (`internal/provider/builtin/builtin.go`).

```bash
kiln login anthropic      # OAuth
kiln logout anthropic
kiln providers            # kind + configured/- per provider
```

- **`Logging in to %s...`** then **`Logged in to %s.`** or **`Login failed: %s`**
  (`internal/auth/login/login.go`) — normal login output; the failure
  wraps whatever the OAuth exchange or key prompt returned.

- **`no OAuth login flow implemented for provider "%s"`**
  (`internal/auth/login/login.go`). That provider has no OAuth flow
  wired in. Fix: use its API-key path, or check `kiln providers` for its kind.

- **`%s OAuth refresh failed: %w`** (`internal/provider/builtin/builtin.go`)
  or **`%s OAuth credential was removed during refresh`** (same file:280). The
  stored token's `Expires` (already reduced by a 5-minute margin at store time,
  `internal/auth/oauth/anthropic.go`) passed and the refresh call itself
  failed (network error or revoked token); the second means a concurrent logout
  raced the refresh. Fix: `kiln login <provider>` again.

- **`HTTP request failed. status=%d; url=%s; body=%s`** / **`token response
  returned invalid JSON. url=%s; body=%s; err=%w`**
  (`internal/auth/oauth/anthropic.go`) — the token endpoint rejected the
  exchange/refresh, or returned an unexpected body. Fix: `kiln login <provider>`
  again; a persistent failure means the OAuth grant was revoked.

- Ollama auths via the `OLLAMA_HOST` env var, not a stored credential — there is
  no `kiln login ollama` flow (`internal/provider/ollama/ollama.go`). A
  gateway in front of Ollama takes its bearer token from `Options.APIKey`, not
  the credential store.

## Ollama

No login needed for a bare local install (`internal/provider/ollama/ollama.go:
271-273`). **Context window resolution**, most authoritative first
(`internal/provider/ollama/ollama.go`, `ResolveContextWindow`):
`/api/ps`'s `context_length` for the *currently loaded* instance (the only
source of the real serving window, and only while resident) → `num_ctx`
pinned in the Modelfile, from `/api/show`'s `parameters` → the server-wide
`OLLAMA_CONTEXT_LENGTH` → a conservative 8,192-token fallback. The model's
*training* context (`model_info.*.context_length`) is deliberately never
used — "wildly larger than what gets served" (same file, comment above
`ResolveContextWindow`). Consequence: **a model's reported window can shrink
the moment it's unloaded**, since only `/api/ps` reports the true served
window.

- Ollama unreachable: `DiscoverModels` calls `/api/tags` first and fails whole
  on error (`internal/provider/ollama/ollama.go`) — surfaces as
  `kiln: refreshing ollama: %v` wrapping Go's raw dial error (e.g. `connection
  refused`). Fix: `ollama serve`; check `OLLAMA_HOST`.

- A model in `ollama list` missing from `kiln models ollama`: models without
  the `"completion"` capability are skipped outright, and without `"tools"`
  when `RequireTools` is true (the default) — the agent loop needs tool calls
  (`internal/provider/ollama/ollama.go`). Intentional filtering, not a
  bug.

- Window smaller than expected / early compaction: pin `num_ctx` in the
  Modelfile or set `OLLAMA_CONTEXT_LENGTH`, then reload the model so `/api/ps`
  reports the new window.

## MCP servers

Read from `~/.claude.json` and `.mcp.json` - the same files `claude mcp add`
writes, and `kiln mcp add` writes them too (`internal/mcp/config.go`; scopes
and precedence in `docs/configuration.md`).
Servers connect sequentially, name-sorted; one failing doesn't block the rest
(`internal/mcp/hub.go`).

```bash
kiln mcp                                    # connect for real, print status
HARNESS_MCP_CONNECT_TIMEOUT=90s kiln        # default 30s (internal/mcp/hub.go)
kiln --strict-mcp-config --mcp-config ./project-mcp.json
```

- **`No MCP servers configured. Add one with kiln mcp add — it writes
  ~/.claude.json/.mcp.json, the same files claude mcp add uses`**
  (`internal/mcp/status.go`; `kiln mcp list`'s own empty message in
  `internal/cli/mcp_cmd.go` is similar: `Add one with: kiln mcp add <name>
  -- <command> [args...]`). Missing, empty, or unparsable `mcpServers`
  (silently treated as empty, `internal/mcp/config.go`).

- **`⚠ 1 MCP server unavailable · run /mcp`** / **`⚠ %d MCP servers unavailable
  · run /mcp`** (interactive, `internal/cli/tui.go`); print mode writes
  **`mcp: %s unavailable — %s`** per server to stderr, truncated to 100 runes
  (`internal/cli/mcp.go`). Run `/mcp` or `kiln mcp` for the per-server
  reason.

- Per-server reasons (`internal/mcp/hub.go`, `describeConnectError`):
  `command not found: <path>` (stdio command missing/not on PATH) ·
  `no response within <timeout>` (connect or tools/list timed out) ·
  `connection refused at <url>` · `host not found at <url>` (DNS) ·
  `unauthorized (check its token) at <url>` (401) · `forbidden at <url>` (403) ·
  `the server exited before it answered` (stdio process died mid-handshake) ·
  `connected, but listing its tools failed: <reason>` (handshake OK,
  `tools/list` itself errored, `internal/mcp/hub.go` — a server-side
  bug, not a connectivity issue).

- **`--strict-mcp-config`** with no `--mcp-config` connects to **nothing** — it
  does not fall back to `~/.claude.json` (`internal/mcp/config.go`).

- A connected server's tools are invisible to the model, not even offered by
  `tool_search`: **posture gating**. `HARNESS_POSTURE` (default `coding`)
  restricts which *servers* are indexed. A server named by some built-in
  posture but not the active one is never indexed, `tool_search` or not
  (`InPosture`, `internal/mcp/gating.go`). A server no posture names at all
  is in scope for every posture, so this only affects the servers on the
  built-in lists. Fix: `/posture all` (every server; expensive),
  `/posture ops`, or add the server to a posture's list.

- **`tool "<name>" is not available to this agent: it is not in the active
  tool set`** as a tool result: the model called a tool that is registered
  but not active for this lane (`internal/harness/turn.go`). Either a
  subagent definition restricts its tools and the model guessed a name
  outside that list, or a gated MCP tool was called before `tool_search`
  admitted it. Fix: widen the agent's `tools:` allowlist, or let the model
  call `tool_search` first; the run log records `tool refused: not active`
  with the active set.

- An in-posture tool still isn't directly callable (only `tool_search`,
  `bash`, `read`, `edit`, `write`, `todo_write`, etc. are resident): on the
  `posture-index`/`full-index` tool strategies (small/medium tiers) MCP tools
  are indexed but not *active* — only `tool_search`'s admissions run
  (`internal/mcp/gating.go`). This is expected; the model calls
  `tool_search` itself. Check the strategy via `kiln doctor`'s `tools` line.

## Permissions

Modes (`internal/claude/settings/settings.go`): `manual` (default,
read-only free else ask), `acceptEdits` (edit/write + read-only free else
ask), `auto` (a classifier model reviews what rules leave allowed; see configuration.md "Auto mode classifier"), `dontAsk` (read-only free, allow rules
apply, everything else denied without asking), `bypassPermissions` (allow
everything else), `plan` (read-only allowed, everything else denied
outright, no prompt). An explicit `deny` rule wins over every mode,
`bypassPermissions` included — see Precedence below.

**Precedence** (`internal/claude/settings/settings.go`, `Decide`): `deny`
match wins, checked first → `bypassPermissions` allows the rest → `allow` match
→ `ask` match → the mode's own default.

- **`blocked by permission rules.`** (`internal/claude/permission/permission.go:
  294`) — a `deny` rule matched. Fix: check `/permissions`, adjust
  `.claude/settings.json`.

- **`plan mode is read-only, so <tool> is not available. Describe the change
  instead of making it.`** (same file:296) — `plan` mode, tool not on the
  read-only allowlist. Fix: exit plan mode (`Shift+Tab`).

- **`<path> is outside the workspace and cannot be confirmed.`** (same
  file:314, headless) — any path outside the gate's roots always needs
  confirmation, even under an `allow` rule; no prompter (print mode) refuses
  outright. Fix: `/add-dir <path>`.

- **`the user declined access to a path outside the workspace.`** (file:317) —
  the outside-workspace dialog was declined.

- **`requires confirmation and no prompt is available.`** (file:349) — verdict
  is `ask` with no prompter, i.e. print mode; `ask` is a refusal there by
  design. Fix: add an explicit `allow` rule, or run interactively.

- **`the user declined. Ask what they would prefer before trying again.`** /
  **`the user declined and said: <feedback>`** (file:369-371) — the
  confirmation dialog was declined, with or without typed feedback.

- **`dontAsk`** is a blanket allow (file:306-307) still subject to an explicit
  `deny`, and does **not** bypass the outside-workspace check (enforced
  separately, guarded by `mode != bypassPermissions`, file:305).

- `/permissions` shows merged deny/allow/ask, active mode, workspace roots, and
  this session's "don't ask again" grants for tools other than bash (a bash
  "don't ask again" saves allow rules to `.kiln/settings.local.json`). Its
  delete key removes only rules kiln saved; a rule from a Claude Code
  `.claude` file is refused with that file's name: edit the file yourself.
  (`internal/commands/inspect_commands.go`).

## Hooks

Read from `.claude/settings.json`'s `hooks`. `PreToolUse` runs **before** the
permission gate, and a rewrite it makes is what the gate judges — not the
model's original call (`internal/claude/hooks/runner.go`). Default
timeout: **60s** (`DefaultTimeoutSeconds`, `internal/claude/hooks/hooks.go`),
overridable per-hook via `timeout` in settings.json.

- **`hook timed out: <label>`** (`internal/claude/hooks/runner.go`) —
  didn't exit in time; its process group is SIGKILLed. Fix: raise `timeout`.

- **`blocked by hook: <label>`** (default reason) or the hook's stderr as the
  reason (same file:150-157) — exit code 2, Claude Code's "block this call"
  convention.

- **`hook failed (<code>): <stderr>`** (file:161-166) — non-zero, non-2 exit
  with stderr text; surfaced as a notice, not swallowed.

- **`stopped by hook: <label>`** (file:188-192) — JSON stdout set
  `"continue": false` with no `stopReason`.

- **`denied by hook: <label>`** (file:200-204) — JSON stdout set
  `hookSpecificOutput.permissionDecision: "deny"` with no reason.

- Non-JSON stdout (or JSON that isn't an object) becomes context appended for
  the model, not a user notice — no hard error for malformed JSON, it just
  falls through to this path (file:167-183).

- **`rewrote <tool>: <primary arg>`** notice (file:325-331) — a `PreToolUse`
  hook returned `updatedInput`; the gate then checks the merged args.

- **`Stop hook asked to continue: <reason>`** (`internal/cli/chat.go`) — a
  `Stop` hook blocked at run end. Reported to you; kiln does **not** re-prompt
  the model on it (confirmed: `internal/cli/chat.go` only calls
  `notice(...)`, never re-drives the turn).

## Context and compaction

- `CLAUDE.md files use ~Nk tokens, over this model's … memory budget` — see
  "Startup and models" (`internal/claude/memory/memory.go`).

- **Compaction trigger**: fires when estimated context tokens exceed
  `contextWindow - ReserveTokens` (`internal/compaction/estimate.go`,
  `ShouldCompact`), checked before an operation's first request and at the
  end of every turn (`internal/harness/compaction.go`). Before every request
  the turn loop also checks the request itself (system prompt, tools,
  conversation) against the window less room for the reply
  (`harness.RequestLimit`): over it, kiln compacts once; still over it, the
  turn fails with `the conversation (~N tokens …) does not fit <model>'s
  window, even after compacting it` instead of sending it. Ollama never
  refuses an oversized prompt, it truncates it silently, so this check is the
  only guard. While compaction runs, the busy line reads `Compacting
  conversation (part N of M) · <model> is reading ~Nk tokens` (the model
  streams nothing until it has read the whole prompt) or `… is writing the
  summary`; Esc cancels it and leaves the conversation as it was. The run log
  has one `compaction_part` line per summary request.

- **Summary requests never exceed the summarising model's window**
  (`internal/compaction/fit.go`): history too large for one request is
  summarised in parts, each carrying the summary so far. A model that streams
  nothing is given up on after 2 minutes plus 1 second per 20 prompt tokens
  (measured: a 27B model on an Ollama GPU host reads ~200 tokens/s), or 5
  minutes without a token once it has started.

- **Switching to a smaller model** (`/model`): if the conversation is larger
  than the new window, the switch says so with the numbers. The next message
  compacts with the new model, in parts; `/compact` right after the switch
  summarises with the outgoing model instead, which still holds all of it.

- **`@file`**: capped at the tier's `ToolOutputTokens`, split evenly across
  mentions (`internal/cli/mentions.go`). Unresolvable mentions show
  as **`· @<path> - <reason>`** in print mode (`internal/cli/chat.go`),
  `<reason>` one of `not found`, `outside the workspace`, `is a directory`, or
  the raw read error (`internal/cli/mentions.go`). A file that resolves
  but is too large is silently cut at the budget on a line boundary — no
  truncation marker, unlike tool output below.

- **Tool output truncation markers** (`internal/tools/bash.go`,
  `internal/tools/read.go`): bash line-limit
  `[Showing lines %d-%d of %d. Full output: %s]`; bash byte-limit `[Showing
  lines %d-%d of %d (%s limit). Full output: %s]`; bash oversized single line
  `[Showing last %s of line %d (line is %s). Full output: %s]`; read line-limit
  `[Showing lines %d-%d of %d. Use offset=%d to continue.]`; read byte-limit
  `[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]`; read's
  own oversized-single-line case `[Line %d is %s, exceeds %s limit. Use bash:
  sed -n '%dp' %s | head -c %d]` (`internal/tools/read.go`). Defaults
  2,000 lines / 50KB, whichever hits first (`internal/execenv/truncate.go:
  9-11`); bash spills the full output to a temp file when truncated.

## Sessions

`~/.harness/sessions` by default, `HARNESS_SESSIONS_DIR` overrides
(`internal/session/jsonl/repo.go`).

- **`jsonl: failed to read <path>: <err>`** (`internal/session/jsonl/storage.go:
  73,81,119`) — file missing/unreadable. (A deleted/moved session resumed by id
  with no match just falls through to creating fresh —
  `internal/agent/session.go` — this error is for a path that exists
  but can't be read.)

- **`jsonl: invalid storage <path>: missing header`** / `invalid header: <err>`
  / `line <n>: <err>` (`internal/session/jsonl/storage.go`) — a
  corrupt or malformed line. Resume does **not** skip and continue; `Open`
  fails whole, wrapped as `agent: open session <id>: <err>`
  (`internal/agent/session.go`). No automatic repair — hand-edit or fix the
  line, or start fresh.

- Legacy v3 files are auto-upgraded in place to v4 on `Open`, not rejected
  (`internal/session/jsonl/storage.go`); the original is left untouched
  on any upgrade error.

- **`agent: session id prefix "%s" is ambiguous: matches %d sessions`**
  (`internal/agent/session.go`) — `--resume <prefix>` matched more than one
  session. Fix: use a longer prefix.

- Search index: `~/.harness/search.db` (SQLite FTS5, WAL). Synced automatically
  by the `session_search` tool at call time; no dedicated rebuild command. To
  force a full rebuild, delete it and let the next call reindex:

  ```bash
  rm ~/.harness/search.db ~/.harness/search.db-wal ~/.harness/search.db-shm
  ```

  If it fails to open, `session_search` is silently dropped from the resident
  tool list rather than failing the session (`internal/cli/chat.go`);
  print mode also writes `kiln: session search unavailable: %v` to stderr.

## Terminal and rendering

- **Ctrl+C**: first press shows `Press Ctrl-C again to exit` for one second
  (`internal/tui/keys.go`); second press within that window exits.

- **Truecolor**: the palette is 24-bit throughout with no fallback for a
  non-truecolor terminal (`internal/tui/theme.go`) — colors are
  approximated by the terminal, not by kiln.

- **`--ax-screen-reader`**: forces plain, linear, always-inline output —
  full-screen is unavailable and `Ctrl+F` is a no-op (`internal/cli/tui.go`).

- **Full-screen** (the default; `--inline` / `Ctrl+F` toggle it): scrolls via
  `PgUp`/`PgDn`, `Shift+Up/Down`, or the mouse wheel. Toggling either way
  redraws the transcript from the session log, so shell (`!`) output and
  hook notices not in that log don't survive the switch. Mouse text
  selection needs the terminal's own passthrough modifier (Shift or Option)
  since the app owns mouse events otherwise.

- **`/bashes`** lists background shells; reports `no background shells` if none
  are running or none are wired in (`internal/commands/inline.go`).

- Known renderer bugs already patched in the vendored forks (see
  `third_party/*/HARNESS-PATCH.md`): stale rows after a frame shrinks
  (ultraviolet); scroll desync/blank rows when a turn ends and lines commit in
  the same frame, a stale cell after an incremental diff, a cursor stuck after
  closing a dialog, and the last character of a full-width committed line
  dropped by autowrap (bubbletea). Seeing any of these on an un-vendored
  upstream build is that regression, not a new bug.

## Subagents and roles

- **`%s: %q is not provider/model`** (`internal/claude/agents/agents.go`) —
  a `modelRoles` value isn't `provider/model` shaped.

- **`%s: %s is not among the available models`** (same file:375) — the role
  names a real-looking pair that isn't in any available provider's catalog
  (wrong id, or that provider isn't authenticated). Fix: `kiln models`.

- **`Dispatch to %s/%s was not allowed: %s`** (`internal/agent/dispatch.go:
  199-200`) — a `task` dispatch crosses onto a different, metered provider
  than the parent's (only when the target model has a non-zero input cost)
  and the permission gate's `task`/`role:<name>` check was declined. The
  gate's session-grant cache (keyed on tool + primary arg,
  `internal/claude/permission/permission.go`) is what makes an "allow
  always" answer stick for the rest of the session per role, not per
  dispatch. Fix: answer "allow always" once, or pre-approve via
  `.claude/settings.json`.

## Print mode

- **`kiln: --max-turns expects a positive integer`**, exit 1 before any
  model call: the value was missing, zero, negative or not a number
  (`internal/cli/args.go`). `--max-turns` is print-mode only.

- **`kiln: stopped after N turns (--max-turns)`**, exit 1, `"ok": false` and
  `"reason": "max-turns-exceeded"` in JSON output: the run was about to
  start turn N+1 (`internal/cli/chat.go`). A run that finishes on turn N is
  never cut. Raise the cap or split the prompt.

- After switching to Anthropic from another provider with `/model`, earlier
  reasoning from the other provider is not replayed: the Anthropic API only
  accepts thinking blocks it signed, so unsigned ones are dropped from the
  request rather than failing it with `thinking.thinking: Field required`
  (`internal/provider/api/anthropic_messages.go`). Conversation text is
  unaffected.

## Reporting a bug

1. Reproduce with `--debug` running; attach the log file `kiln doctor` names.
2. For a visual or interaction bug, capture a replayable recording:

   ```bash
   go run ./cmd/kiln-drive --bin bin/kiln --record repro.rec ...
   ```

   This captures the exact PTY byte stream plus input markers, replayable
   later (including at a different terminal size) without the original model
   conversation.
3. If a scripted model conversation is needed to reproduce it, write a faux
   script and drive `kiln-drive` against it — see `docs/testing.md` for the
   script format and worked invocations; that document owns this, not this one.
4. Include: the debug log, the recording (if any), the exact command line, and
   `kiln doctor`'s output.
