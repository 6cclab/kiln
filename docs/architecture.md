# Architecture

## Overview

`kiln` is a single Go binary (module `github.com/andrepato/harness`, command
`cmd/kiln`) that provides Claude Code's interface — slash commands, `@file`
mentions, `!shell`, permission modes, `.claude/` configuration — over any
model provider, local or hosted. The agent loop, session format, compaction
and provider catalog are Go ports of [`@earendil-works/pi`](https://github.com/earendil-works/pi):
sessions are written in pi's JSONL v4 format and are readable by either
implementation (`internal/session/jsonl/doc.go`), and the provider catalog
(41 providers, 10 API shapes) is pi-ai's generated catalog, vendored under
`internal/provider/catalog`. The terminal UI (`internal/tui`) is built on
Bubble Tea v2, with two patched upstream libraries vendored under
`third_party/` (`third_party/bubbletea`, `third_party/ultraviolet`; see each
directory's `HARNESS-PATCH.md`).

Everything downstream of model choice reads a `budget.Tier`
(`internal/budget/tier.go`) derived from the model's context window, never
the provider — the same code relaxes on a 200k-token subscription model
instead of branching per provider.

## Repository map

### `cmd/`

| Binary | Purpose |
|---|---|
| `cmd/kiln` | The product: dispatches to `internal/cli` for management subcommands and the chat/TUI paths; `eval.go` dispatches `kiln eval run` / `kiln eval report` to `internal/eval` before argument parsing, since the eval runner has its own flag set. |
| `cmd/kiln-drive` | Drives a real `kiln` binary through a PTY over a line protocol, for scripted end-to-end tests. |
| `cmd/faux` | Runs `internal/testkit/faux`'s scripted model server as a standalone process. |
| `cmd/mcpfixture` | A real MCP server over stdio, exercising the actual MCP client code path in tests. |
| `cmd/vtreplay` | Replays a `.rec` PTY recording through a VT emulator to produce styled golden screens. |
| `cmd/covercheck` | Aggregates a Go coverprofile per package and enforces the floors in `.coverage-floors` (`make covercheck`). |

### `internal/` by layer

| Layer | Packages |
|---|---|
| CLI / TUI | `internal/cli` (wires everything: `chat.go` print/interactive entry, `tui.go` `RunInteractive`, `mcp.go` hub+gating wiring, `commands.go` slash-command registry, `print.go` `-p` mode, `subcommands.go` `providers`/`models`/`login`/`doctor`/`mcp`); `internal/tui` (pure rendering: `app.go`, `bridge.go`, `transcript.go`, `theme.go`, `width.go`, `markdown.go`); `internal/tui/editor` (input editor). |
| Agent runtime | `internal/harness` (the operation state machine: `Harness`, `Lane`, turn loop `turn.go`, `hooks.go`, `retry.go`, `events.go`); `internal/agent` (`Start`/`SetModel` in `session.go`, subagent dispatch in `dispatch.go`/`subagent.go`); `internal/msg` (shared message/content types); `internal/budget` (`Tier`); `internal/compaction` (summarization); `internal/search` (SQLite FTS5 recall). |
| Providers | `internal/provider` (`Provider` interface, `Registry`, `Model`/`Api`); `internal/provider/builtin` (wires the catalog to streaming clients); `internal/provider/catalog` (embedded, vendored pi-ai model catalog); `internal/provider/api` (one streaming HTTP client per API shape); `internal/provider/ollama` (local discovery, served context window); `internal/provider/faux` (test provider); `internal/auth`, `internal/auth/oauth` (credential store, OAuth/device-code flows). |
| Session / storage | `internal/session` (storage-agnostic `Entry` tree, value/list namespaces, commit primitives, `Storage` interface); `internal/session/jsonl` (the JSONL-file-backed `Storage`: create/open/list/delete/fork, legacy v3 detection). |
| `.claude/` compatibility | `internal/claude/paths` (directory hierarchy, scope ordering); `internal/claude/settings` (load/merge `settings.json`, rule matching, `Decide`); `internal/claude/writesettings` (read-modify-write kiln's own `.kiln/settings.local.json` and `~/.kiln/settings.json`; kiln never writes a `.claude` file); `internal/claude/permission` (the gate, and auto mode's side of it in `classifier.go`); `internal/automode` (auto mode's classifier: the transcript boundary, its prompt and the model call); `internal/claude/hooks` (hook config + running hook commands); `internal/claude/memory` (CLAUDE.md/`@import`/rules); `internal/claude/skills`; `internal/claude/agents`; `internal/claude/commands`; `internal/claude/keybindings`; `internal/claude/statusline`; `internal/claude/trust` (the harness's own `~/.harness/trusted.json`). |
| Tools / MCP | `internal/tool` (the `tool.Tool` contract); `internal/tools` (built-ins plus harness-authored tools); `internal/mcp` (client hub: connection, adaptation, gating); `internal/execenv` (filesystem + shell tools run against); `internal/sandbox` (OS sandbox for the bash tools: Seatbelt / bubblewrap wrappers and the network proxy); `internal/commands` (slash-command registry). |
| Diagnostics / testkit | `internal/diag` (run log under `~/.harness/logs`); `internal/testkit/faux` (scripted model server); `internal/testkit/screen`, `sessiondata`, `stubtui`, `mcpfixture` (terminal-emulation driving, fixture sessions, a stub TUI, a fixture MCP server); `internal/testkit/fauxtest` (the one shared helper that starts a faux server in a test); `internal/testkit/scenario` (loads `scenario.yaml` bundles for the e2e behaviour suite and the eval runner); `internal/eval` (the `kiln eval` runner: drives the built binary against `eval/scenarios`, grades with mechanical checks or an LLM judge, writes one JSONL record per run). |

`third_party/` vendors two patched libraries (`bubbletea`, `ultraviolet`; see
each directory's `HARNESS-PATCH.md`). `testdata/` holds fixtures for `drive`
(PTY-driven scripts), `e2e`, `faux` scripts, `golden` screen goldens,
`hooks`, `behaviour` (fixtures for the e2e behaviour suite), and recorded
`sessions`. `eval/` holds the eval runner's scenarios, fixtures and results.

## Lifecycle of a turn

Starting from a user prompt in the TUI or `-p` mode:

1. **Prompt submit.** `Lane.Prompt` (`internal/harness/turn.go`) appends a
   `session.EntryMessage` user entry, commits
   `pi.branch.tip`/`pi.op.meta`/`pi.op.state`/`pi.lane.state` in one
   transaction, and emits `EventEntryAdded` then `EventRunStart`.
2. **`before_run` hooks.** `Lane.invokeBeforeRun` runs every
   `Hooks.OnBeforeRun` registration (`internal/harness/hooks.go`).
3. **Drive loop starts.** `Lane.drive` commits `op.state = checkpoint` on
   first entry, then emits `EventTurnStart`.
4. **System prompt / memory assembly.** The system prompt string handed to
   `Harness.Options` (CLAUDE.md and `@import`s via `internal/claude/memory`,
   budgeted against the tier's `SystemPromptTokens`) was assembled before
   the run; `buildStreamOptions` in `turn.go` carries it as
   `provider.StreamOptions.SystemPrompt` on every request.
5. **Transcript build + `transform_context` hooks.** `drive` scans the
   branch (`Storage.ScanBranch`), projects entries to `[]msg.Message`
   (`entriesToTranscript`), and runs `Hooks.OnTransformContext`.
6. **Provider stream.** `Lane.requestWithRetry` resolves the provider/model
   and calls `Provider.Stream`, retrying retriable errors (429/529/5xx/
   network) per `RetryPolicy` (`internal/harness/retry.go`). Every stream
   event is recorded as a `pi.pending.assistant_frame` append and emitted as
   `EventMessageUpdate`.
7. **Assistant entry committed.** `drive` writes the assistant
   `session.Entry` and a `UsageWrite`, advances the branch tip, and emits
   `EventEntryAdded`/`EventUsage`. No tool calls ends the operation here
   (`finishCompleted`).
8. **Tool call → `before_tool` hook → permission gate → execution → `after_tool`.**
   `Lane.beginTool` first runs `Hooks.OnBeforeTool` (the CLI wires this to
   `hooks.GuardToolCall`, running `.claude/settings.json` `PreToolUse` hooks
   *before* `permission.Gate.Check`, so the gate judges what will actually
   run — `internal/claude/hooks/doc.go`). A hook may rewrite arguments or
   block the call (`ToolBlock`); otherwise `tool.Tool.Execute` runs and the
   result becomes a `msg.ToolResultMessage`. `Lane.commitToolResult` writes
   the result entry, runs `Hooks.OnAfterTool` (wired to `PostToolUse`), and
   emits `EventToolEnd`. Calls whose `tool.Tool.Concurrent` flag is set run
   as a batch on separate goroutines via `Lane.executeConcurrentRun`, with
   all `Storage.Commit` calls still serialized on the driving goroutine.
9. **Next model call.** `drive` loops back, re-scanning the branch (now
   including the tool results), until an assistant message carries no tool
   calls.
10. **`before_run_end` hooks and `EventRunEnd`.** `Lane.Prompt` runs
    `Hooks.OnBeforeRunEnd` and emits `EventRunEnd`; `Lane.finishOperation`
    writes the terminal transaction (deletes `pi.op.meta`/`pi.op.state`,
    writes `pi.result`, idles `pi.lane.state`).

Events reach the TUI through `internal/harness/events.go`'s synchronous
`Events` bus: `internal/tui/bridge.go`'s `Bridge` subscribes to harness
events (plus user echo, `!`/`#` output and command output) and serializes
every commit onto one background goroutine that owns `Program.Println`,
because concurrent `tea.Println` calls from different goroutines are
documented to land out of order. Every session entry is committed to disk
inside the transactions listed above — there is no separate "flush" step;
`Storage.Commit` (`internal/session/jsonl`) is the durable write.

In print mode (`-p`), there is no `Prompter`: `internal/claude/permission`'s
`Gate.Check` refuses (rather than asking) any call whose verdict is `ask`.

## Harness and lanes

A `Lane` (`internal/harness/lane.go`) drives one branch of one session
through the 13-leaf operation state machine pi defines (`AtStarting`,
`AtCheckpoint`, `AtAssistantReady`, `AtAssistantEffectPending`, `AtTools`,
...). `Harness` (`internal/harness/harness.go`) owns the `Options`
(storage, provider registry, tools, compaction/retry settings) that every
lane on it shares; a lane's own configuration (model, active tools,
thinking level) lives in `session.LaneConfiguration`, read/written via
`pi.lane.config`.

Retries (`internal/harness/retry.go`) use `RetryPolicy` (default: 1s base
delay, capped at 60s, 4 attempts) with full jitter exponential backoff,
triggered by `isRetriable`: HTTP 429/529/5xx (via a `RetriableError`
carrying a status code), a `net.Error`, `context.DeadlineExceeded`, or a
string match on common transient-failure text.

A model request has no total deadline: provider HTTP clients bound only
connecting (`api.NewStreamingClient`). `requestWithRetry` instead ends a
request that goes quiet (`internal/harness/stall.go`, a retriable
`StallError`): no first token within compaction's allowance for the
prompt's size (two minutes plus 20 tokens a second), or no further token
for five minutes.

Interruption: `Lane.Abort` cancels the lane's `context.CancelFunc` and
emits `EventOperationAbort`; the turn loop itself notices `ctx.Err() != nil`
at the top of its next iteration or mid-request and calls `finishAborted`,
which still writes the terminal `pi.result` transaction with
`StatusAborted`. A reply interrupted mid-stream keeps its text
(`Lane.commitInterrupted`, `internal/harness/interrupt.go`): it is committed
as an aborted assistant entry, shown, sent with the next request
(`compaction.ContextMessages` keeps an aborted reply's text), and its
usage, with output estimated from what streamed, is counted. `Lane.Steer` queues text into `pi.lane.state.inbox` for
injection at the next checkpoint (a best-effort approximation of pi's
mid-stream steering).

Resume replays no partial streaming state: an interrupted operation's
`pi.pending.assistant_frame` list exists on disk (written for
storage-format parity with pi) but this port's `Resume` re-requests a fresh
assistant response from scratch rather than reconstructing a partial one
from frames (`internal/harness/turn.go`'s `Frame` doc comment).

## Sessions

Sessions are pi's JSONL v4 format (`internal/session/doc.go`,
`internal/session/jsonl/doc.go`). Line 1 is a header
(`{"v":4,"kind":"header","id":...,"storageVersion":1,"createdAt":...,"cwd":...}`,
plus `parentSessionId`/`legacyParentSessionPath`/`nextSeq` when the file was
created by a fork or a legacy-v3 upgrade). Every subsequent line is one
`kind: "entry" | "value" | "list" | "usage"` write, assigned a sequence
number from one global, monotonically increasing counter shared across all
four kinds — sequence order is commit order.

A session is a tree of `Entry` nodes linked by `ParentID`
(`internal/session/types.go`): `EntryMessage` (a verbatim `msg.Message`),
`EntryCompaction` (a summary replacing a run of prior entries),
`EntryBranchSummary` (anchored to a `FromID`, or the branch root if nil),
and `EntryCustom`. Branching/forking is navigating `pi.branch.tip` to a
different node and starting a new lineage from there; `Lane.NavigateTree`
moves a lane's tip (refusing while an operation is running).

The flat key/value store is partitioned into namespaces — `pi.branch.tip`,
`pi.lane.config`, `pi.lane.state`, `pi.op.meta`, `pi.op.state`,
`pi.op.tool_args`, `pi.pending.entry`, `pi.pending.tool_output`,
`pi.pending.assistant_frame` (a list), `pi.result`, `pi.session.name`,
`pi.entry.label` — addressed by `(namespace, key)`.

Session files live under `~/.harness/sessions/<encoded-cwd>/`, one directory
per working directory (`DirectoryName`, `internal/session/jsonl/repo.go`), one
file per session (`FileName`: ISO-8601 `createdAt` + the session id,
`.jsonl`).

**Legacy v3 detection**: a line-1 header shaped
`{"type":"session","version":3,...}` identifies a legacy session
(`internal/session/jsonl/codec.go`'s `isLegacyV3Header`); `Open` upgrades it
to v4 as a side effect before any other code (including `Fork`) reads it —
see `internal/session/jsonl/legacy_v3.go` for what the upgrade does and does
not yet carry over (the full v3 record model — compaction tails, branch
summaries, per-field change records — is deferred).

**Session search** (`internal/search`) indexes both formats over SQLite
FTS5 (`modernc.org/sqlite`, CGO-free, FTS5 compiled in) in one standalone
FTS5 virtual table (`entries`) plus a plain `indexed_files` table tracking
which files have been synced. A hit returns a short snippet (12 tokens,
`snippetTokens`), never full entry text — the design constraint carried
over from pi's `sqlite.ts` is that recall must cost tens of tokens, not
thousands.

## Context budget

`budget.Tier` (`internal/budget/tier.go`) is resolved from a model's
context window via `TierForWindow`. The tier **name** is a step function
(`small` ≤ 32,768, `medium` ≤ 131,072, else `large`) because it selects the
tool-catalog strategy, which has a fixed measured cost that either fits or
doesn't; every other budget scales continuously with the window as a
fraction, not a per-tier constant — an earlier constant-per-tier design let
a 49,152-token window fall into "medium" and inherit budgets sized for
128k, ending up with *less* usable context than a 32,768-token window.

| Budget | Share of window | Floor | Ceiling |
|---|---|---|---|
| System prompt (`SystemPromptTokens`) | 10% | 2,048 | 32,768 |
| Compaction reserve (`Compaction.ReserveTokens`) | 10% | 2,048 | 32,768 |
| Compaction keep-recent (`Compaction.KeepRecentTokens`) | 25% | 4,096 | 100,000 |
| Per-tool-result output (`ToolOutputTokens`) | 12% | 2,048 | 49,152 |

Each floor is itself capped at 1.25x its own share of the window, so a tiny
window cannot have three generous floors eat most of it before a single
message exists.

**Tool strategy** (`ToolStrategy`, `toolBudgetShare = 0.2` of the window):
`StrategyFullSchemas` (every tool's full JSON Schema resident, measured
31,897 tokens on the reference 165-tool/9-server catalog), `StrategyFullIndex`
(one `name: description` line per tool across every server, 7,597 tokens),
`StrategyPostureIndex` (one line per tool, only in-posture servers, 1,286
tokens). `StrategyForWindow` picks the most generous strategy whose
measured cost fits `toolBudgetShare`, falling back to the cheapest.

**Postures** (`internal/mcp/gating.go`) are a static, free filter over which
MCP servers may be indexed *at all*, independent of the tool strategy: the
built-ins are `coding` (the default: infisical, argocd-mcp, personal-kb,
homelab-kb, claude-relay, sentry, github), `ops` (grafana, proxmox,
argocd-mcp, unifi-mcp, pocket-id, infisical), and `all` (`*`, every server).
Under `StrategyFullSchemas`, every in-posture tool is active immediately
(`ActiveToolNames`); under the two index strategies, only `tool_search` plus
whatever a `GateState` has already admitted is active — the model must call
`tool_search` (`internal/tools/toolsearch.go`) to activate a schema. Search
scores a tool by keyword against its name (10x for an exact match, 4x for a
substring) and description (1x), admits the top matches permanently for the
session (`GateState.Admit`), and returns their full schemas.

**Compaction** (`internal/compaction`) decides via `ShouldCompact` when
estimated context usage nears the window, then `FindCutPoint` (turn-aware:
never splits a tool call from its result except for a deliberately
"split turn" case) picks the boundary between what gets summarized and what
is kept verbatim (bounded by `Settings.KeepRecentTokens`). `Compact` calls
the lane's own provider/model (`Lane.CompactWith` may name another: `/compact`
right after a switch to a smaller window uses the outgoing model) with a
fixed structured-summary prompt (`SummarizationSystemPrompt` /
`summarizationPrompt`, or `updateSummarizationPrompt` when extending a prior
summary). History too large for one request to that model is summarised in
parts that each fit its window, each carrying the summary so far through
`updateSummarizationPrompt` (`fit.go`); every request has a stall watchdog.
It appends a read-files/modified-files tag block, and returns a
`Result` stored as a new `session.EntryCompaction` carrying `Summary`,
`RetainedTail` and `TokensBefore`. On the next request the lane projects the
branch through `compaction.ContextMessages` (`entriesToTranscript` in
`internal/harness/lane.go`): everything before the last compaction entry is
replaced by that entry's summary and retained tail, so compaction shrinks
the request itself, not only the session's bookkeeping. Before sending, the
turn loop checks the request against the window (`Lane.fitRequest`):
compacting once if it does not fit and refusing with a
`ContextOverflowError` if it still does not, since Ollama truncates an
oversized prompt silently instead of refusing it. A provider that does
refuse one as too long gets one compaction and one retry. `/compact` runs as
a background command (`commands.Result.Background`), off the TUI's event
loop, so Esc cancels it.
`SummarizeBranch`/`PrepareBranchSummary` are the sibling path used when
navigation abandons a branch.

**Skills**: only `name` + `description` stay resident (`internal/claude/skills`);
a skill's body loads only on invocation.

**`@file` inlining**: `@path` inlines a file's content directly rather than
spending a tool-call turn on a read, capped at the tier's per-result budget
(`README.md`; `@screenshot.png` attaches an image instead).

## Providers

`provider.Provider` (`internal/provider/types.go`) is the interface every
model backend implements; `Stream` returns a channel of `msg.StreamEvent`
plus a `wait()` func yielding the final `*msg.AssistantMessage`. `Api` is
one of ten known shapes (`ApiAnthropicMessages`, `ApiOpenAICompletions`,
`ApiOpenAIResponses`, `ApiOpenAICodexResponses`, `ApiAzureOpenAIResponses`,
`ApiGoogleGenerativeAI`, `ApiGoogleVertex`, `ApiBedrockConverseStream`,
`ApiMistralConversations`, `ApiPiMessages`) but is a plain string type, not
a closed enum, since pi's own `Api` type is `KnownApi | string`.

`provider.Registry` (`internal/provider/registry.go`) composes providers by
id (`Register`/`Provider`/`GetModel`), backed by an `auth.CredentialStore`.
`internal/provider/builtin` wires the vendored catalog
(`internal/provider/catalog`, 41 provider files / ~1,495 models, embedded
and refreshed via `go generate` from a checked-out pi-ai package —
`catalog.go`'s `//go:generate` directive and `generate.go`) to whichever API
shapes have a streaming client implemented (`implementedAPIs`); a model
whose `api` is catalog-known but not yet streamable reports
`ErrNotYetStreamable` from `Stream` rather than being silently omitted from
`Models()`.

`internal/provider/api` implements one HTTP streaming client per shape
(`anthropic_messages.go`, `openai_completions.go`, `openai_responses.go`,
`openai_codex_responses.go`, `azure_openai_responses.go`,
`google_generative_ai.go`, `google_vertex.go`, `bedrock_converse_stream.go`,
`mistral_conversations.go`, `pi_messages.go`) plus shared SSE parsing
(`sse.go`) and auth (`auth.go`). Every client mutates the same
`*msg.AssistantMessage` in place across stream events
(`internal/provider/api/sse.go`).

`internal/provider/ollama` discovers local models over `/api/tags`,
`/api/ps` and `/api/show`, resolving the context window a model is
*actually being served with* — often smaller than its training context —
which is what `TierForWindow` is fed for Ollama models.

Authentication (`internal/auth`, `internal/auth/oauth`) stores credentials
in `~/.harness/credentials.json` (a stored credential wins over an
environment variable for the same provider: `resolveAuth` in
`internal/provider/builtin/builtin.go` reads the store first and falls
through to the provider's env vars only when nothing is stored); per-provider
OAuth/device-code flows exist for Anthropic, GitHub Copilot, Kimi (Kimi
Code subscription), OpenAI Codex (ChatGPT Plus/Pro), OpenRouter, a
`radius` pi-messages gateway, and xAI, sharing PKCE (`pkce.go`) and an RFC
8628 device-authorization polling loop (`device_code.go`).

`internal/provider/faux` registers a `faux` provider against
`internal/testkit/faux`'s scripted server (only when `HARNESS_FAUX_ADDR` is
set; never part of the default provider list), giving tests deterministic
models (`faux-1`, `faux-2`) to stream against without a network call.

## Tools

`tool.Tool` (`internal/tool/tool.go`) is the contract every tool
implements: a `Name`, JSON Schema `Parameters`, and an `Execute` that
streams partial results through an `Update` callback and returns a final
`Result` (content blocks the model sees, optional `Details` kept in the
session but not sent to the model, and an `IsError` flag).

**Built-ins** (`internal/tools/builtins.go`, `tools.Builtins`): `bash`,
`read`, `edit`, `write` — the four every session gets for free, in pi's
order, all operating against an `execenv.Env`.

**Harness-authored tools** (`internal/tools`): `tool_search`
(`toolsearch.go`, described above), `task` (`task.go`, subagent dispatch,
described below), plus `todo_write`, `exit_plan_mode`, `session_search`,
`bash_background`, `bash_output`, `kill_shell`.

**MCP adapters** (`internal/mcp`): each configured server's tools are
adapted into `tool.Tool` values; whether an adapted tool is *active* in a
session is governed by the gating described in Context budget, above —
registration is free, activation is what costs tokens (`README.md`). The
active set is enforced at execution, not only in the schema offered to the
model: `beginTool` in `internal/harness/turn.go` refuses a call whose name is
not in the lane's recorded active set (`tool "x" is not available to this
agent: it is not in the active tool set`), so a subagent restricted to `read`
cannot run `bash` by guessing the name, and a gated MCP tool must be admitted
through `tool_search` first. A lane with no recorded active set (an older
session) is not filtered.

**Execution environment** (`internal/execenv`): a filesystem rooted at a
working directory plus shell execution, ported from pi-agent-core's
`NodeExecutionEnv`. `Exec` runs a command under a resolved shell
(`resolveShell`: an explicit path, else `/bin/bash`, else the first `bash`
on `PATH`, else `sh`) in its own process group, so cancelling the context or
crossing `ExecOptions.Timeout` kills the whole group, including background
jobs the command spawned. Output is captured and truncated to
`DefaultMaxLines` (2000) / `DefaultMaxBytes` (50 KiB) unless a larger
`Capture` is requested; `Spill: true` preserves the complete output on disk
regardless, recording the path in `ExecResult.SpillPath`.

**OS sandbox** (`internal/sandbox`): with `sandbox.enabled` in settings,
`internal/cli/sandbox.go` builds a `sandbox.Manager` and sets it as
`execenv.Env.Sandbox`. The bash tools ask it per command
(`ForCommand`: not when `excludedCommands` covers the call, or the model
asked for `dangerouslyDisableSandbox` and that is allowed) and pass the
result as `ExecOptions.Sandbox`; `Exec` then runs the wrapped argv
(`execenv/wrap.go`). The manager resolves a `Plan` per command from the
settings and the gate's current roots (`plan.go`: writable roots, protected
paths, read rules, proxy ports; `gitdir.go` limits git directories to what
git writes) and renders it as a deny-default Seatbelt
profile for `/usr/bin/sandbox-exec` on macOS (`seatbelt.go`) or a bubblewrap
argv with a private network namespace on Linux (`bwrap.go`, with socat
relaying the loopback proxy port in through a Unix socket). Network access
goes through `Proxy` (`proxy.go`), which serves only requests carrying the
session's credential (in the proxy URLs the plan sets), enforces the domain
lists and asks the gate (`Gate.ApproveNetwork`) about other hosts. The gate auto-allows a
call that will run sandboxed (`permission/sandbox.go`), computing
"sandboxed" with the same `WillSandbox` the tool uses. The user's `!`
commands, hooks, MCP servers and the status line never go through it; kiln's
own `git status` for the status line does (`cli/gitstatus.go`, through
`Manager.Always`), since it reads a repository sandboxed commands write.

## Permissions and hooks

`permission.Gate` (`internal/claude/permission/permission.go`) decides
whether a tool call proceeds, given: the merged `.claude/settings.json`
permission rules (`settings.Permissions`), the active `PermissionMode`, a
workspace boundary (`Roots`, widened via `/add-dir`), and — for anything
`ask` or allow-outside-workspace — a `Prompter` (absent in headless/print
runs). A denial is not a Go error: `Check` returns a `*BlockResult` whose
`Reason` is written for the model, so a refused call ends the tool call, not
the session.

**The six modes** (`internal/claude/settings/settings.go`): `manual`,
`acceptEdits`, `auto`, `dontAsk`, `bypassPermissions`, `plan`. `Decide`
checks deny rules first (absolute, never overridable), then, in `plan`,
refuses edit tools whatever allow or ask rules say (Claude Code keeps edits
blocked until the plan is approved), then ask rules (they ask even in
`bypassPermissions`, and even when an allow rule matches too), then
`bypassPermissions` (allow everything else), then allow rules — Claude
Code's deny, ask, allow — then falls through to mode defaults: `plan`
allows `settings.ReadOnly` tools and read-only bash, asks about any other
shell command (the regular flow, as in Claude Code) and denies everything
else outright; `acceptEdits`
allows `edit`/`write`/read-only tools and asks for the rest; `dontAsk`
answers like `manual`, and the gate then denies whatever `manual` would have
asked about; `auto` allows as far as rules go, and the gate then sends
what the workspace boundary leaves past the auto mode classifier
(`permission/classifier.go`, the model call in `internal/automode`;
`docs/configuration.md` "Auto mode classifier").

**Rule syntax** (`MatchesRule`): a bare rule is an exact tool-name match
(`mcp__`-prefixed rules match by prefix); a parenthesized rule
`Tool(pattern)` matches the tool's primary argument against a glob (`*`),
with a `cmd:*` colon suffix normalized to `cmd *`. `Read(path)` and
`Edit(path)` are path rules (`internal/claude/settings/pathrules.go`), matched
as Claude Code matches them: the tool's path argument is resolved to an
absolute path the way the tool resolves it (`execenv.ResolveToolPath`), and the
rule is a gitignore pattern under its anchor (`//` root, `~/` home, `/` the
settings source's directory, otherwise the current directory). Each rule
carries its source (`settings.RuleSource`, in `Permissions.AllowFrom`/
`DenyFrom`/`AskFrom`) through the merge, so a user-settings `/path` anchors at
`~/.claude` and a project one at the project. Deny rules also reach the files a
bash command names (`bash_paths.go`).

**Bash analysis** (`bash_parse.go`): a command line is parsed with
`mvdan.cc/sh/v3/syntax` (bash dialect) into one `bashAnalysis`: every simple
command anywhere in it (lists, pipelines, subshells, control-flow and function
bodies, substitutions, heredocs fed to a shell, `sh -c` and `eval` strings,
parsed recursively), its words evaluated statically (`evalWord`: quote removal,
`$'…'`, brace expansion; an expansion makes a word non-literal), what runs once
wrappers are stripped, and whether anything runs that kiln cannot name. Bash
rules (`bash_rules.go`), the read-only classification (`readonly_bash.go`) and
the file-operand checks (`bash_paths.go`) all read it. `TestBashDifferential`
runs generated command lines in real bash with logging stub commands and fails
when bash runs a command the analysis did not collect.

**Outside-workspace rule**: a path argument outside `Gate.Roots` always
warrants a question — even when a rule would otherwise `Allow` — unless
mode is `bypassPermissions`; with no prompter available, it is refused. This
is the concrete mechanism behind `README.md`'s "`allow: [Read]` means
reading is fine here, not read anything on this machine."

**Hook events** (`internal/claude/hooks`): `PreToolUse`, `PostToolUse`,
`UserPromptSubmit`, `SessionStart`, `SessionEnd`, `Stop`, `SubagentStop`,
`Notification`, `PreCompact`. All nine fire: `SessionStart`, `SessionEnd`,
`UserPromptSubmit`, `Stop` (run end), `PreCompact` (compaction start) and
`SubagentStop` (once per dispatched subagent) are wired in
`internal/cli/chat.go`; `Notification` fires on the TUI's permission prompt
(`internal/cli/tui.go`). `hooks.GuardToolCall` composes `PreToolUse`
with the permission gate by running hooks *before* `Check`: a hook may
rewrite the command's arguments, and the gate then judges what will
actually execute, not what the model originally proposed
(`internal/claude/hooks/doc.go`).

**Print-mode behaviour**: with no `Prompter` wired, `Gate.Check` refuses
(rather than blocking indefinitely) any call whose verdict is `ask` —
"in print mode there is nobody to ask, so `ask` is a refusal" (`README.md`).

## Subagents and model roles

The `task` tool (`internal/tools/task.go`, `TaskTool`) dispatches to a
named agent from `.claude/agents/*.md` (`agents.Definition`), passing a
self-contained prompt (the subagent sees none of the parent conversation).
It is `Concurrent: true` — several `task` calls in one assistant message run
in parallel, each starting its own session with its own storage, sharing no
mutable state.

`agent.Dispatcher` (`internal/agent/dispatch.go`) resolves the requested
agent, resolves its model (the task call's own `model` argument — a role
name, `"inherit"`, or empty — wins over the agent definition's `model:`
field, via `agents.ResolveModel` against the registry's available
candidates and `settings.json`'s `modelRoles`), and starts a **fresh**
`agent.Start` session — a full harness with its own JSONL file, not a lane
on the parent's — because the system prompt is set per-harness: a subagent
whose system prompt is its parent's would not be a specialized subagent at
all.

**Depth limit**: `Dispatcher.Depth` names how many dispatches deep a
dispatcher sits (0 = the process-level dispatcher). A dispatch at
`Depth < 2` gives its subagent its own `task` tool wired to a child
`Dispatcher` at `Depth+1`; at `Depth 2`, no `task` tool is built at all, so
the model at the bottom of the tree never sees the option — root → subagent
→ subagent's-subagent, then no further fan-out.

**Shared gate**: every `Dispatcher` in the tree shares the same
`*permission.Gate` — one permission ledger, so an "allow always" granted
three levels down is remembered everywhere — installed on each subagent
harness's `before_tool` hook.

**Paid-provider prompt**: a dispatch that crosses onto a *different*,
metered provider than the parent's current model, where that model's input
cost rate is nonzero, goes through `Gate.Check` with tool name `task` and
primary arg `"role:" + requested` before starting: a dispatch onto a paid
provider the user did not already choose for this turn is a spending
decision, not just a routing one (`internal/agent/dispatch.go`).
Same-provider switches and free models never hit this check.

Concurrent dispatches, tool-call counts and final usage are reported to the
UI via `Dispatcher.OnEvent` (`SubagentEvent`), never to the model; the
parent only ever receives the subagent's final assistant text
(`EventMessageEnd`'s last non-empty chunk, replaced not accumulated).

## TUI

`internal/tui` is the pure rendering layer (`theme.go`, `width.go`,
`transcript.go`, `markdown.go`, `changepreview.go`, `permission_render.go`)
plus the Bubble Tea model in `app.go`. Full-screen (alt-screen) is the
default: a scrolling `viewport` transcript, banner at the top, with the
input pinned at the bottom, re-wrapped on resize. `--inline`/`Ctrl+F`
switches to inline mode, which commits finished output to the terminal's
own scrollback via `Program.Println` instead and repaints only the live
region below it; toggling either way redraws the transcript from the
session log, so unlogged output (`!` shell output, hook notices) does not
survive the switch (`README.md`). `--ax-screen-reader` always runs inline.

`internal/tui/bridge.go`'s `Bridge` is the agent side of the TUI: it
serializes every commit-worthy event — harness events, user echo, `!`/`#`
output, command output, turn summaries — from whichever goroutine produced
it (the lane's `Prompt` goroutine, Bubble Tea's `Update`, one-off `Cmd`
goroutines) onto one background goroutine that owns `Program.Println`, so
commit order matches enqueue order regardless of which goroutine produced
each item.

Every frame splits into two regions with different lifetimes. The
committed transcript (native scrollback, written once via `Program.Println`
and never redrawn) holds everything finished: user echoes, completed tool
calls, assistant text, notes. The live region (`app.go`'s `liveLines`,
rebuilt from scratch on every `Update`) holds only what is still in
flight: the spinner, a streaming reply's last few rows with its trailing
caret, a pending permission/plan prompt, the live "plan" checklist and
subagents panel, the input box and the status line. A block that finishes
(a completed plan, a subagents dispatch, a fully streamed reply) commits
once as an ordinary transcript block and then drops out of the live
region entirely, rather than the live region growing without bound.
Ctrl+O, Ctrl+F and Rewind all redraw the transcript from the session log
rather than from anything the live region held, so
`internal/tui/replay.go`'s `RenderTranscriptEntries` reconstructs it: it
walks the session's logged entries and, in the same pass, splices back in
every synthetic (non-entry) block that was recorded via
`Bridge.CommitSynthetic` — the committed plan checklist, subagent dispatch
lines, system notes, the `/context` block — immediately after the entry
it was originally committed alongside, since none of those exist in the
session log itself and a replay built purely from the log would silently
drop them.

Design language, per-row layout and style details are covered in
`docs/kiln-design.md`; the terminal-emulation test methodology (every
screen assertion runs against an emulated terminal, never raw bytes) is
covered in `docs/testing.md`.

## Diagnostics

`internal/diag` is the process-global run logger: one file per run under
`~/.harness/logs` (or `$HARNESS_LOG_DIR`), written at Info level always and
Debug level with `--debug`/`HARNESS_DEBUG=1`. It logs startup phases with
elapsed time, every MCP server's connect outcome and duration, every
harness event (turns, tool calls, retries, compaction), and the exit code.
The last 30 run logs are kept (`keepFiles`); before `Start` is called the
logger discards everything, so tests and subcommands that never start a
full run pay nothing.

`kiln doctor` (`internal/cli/subcommands.go`'s `Doctor`, also `/doctor`
inside a session) prints model, tier, tool strategy, MCP status, hooks and
agents on one screen, problems last.
