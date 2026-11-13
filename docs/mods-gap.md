# Mods v1 gap: kiln vs Claude Code

What kiln's mod runtime (`internal/mods`) implements, measured against Claude Code's
mod API surface. The authoritative Claude Code surface is its type declarations
(the `.d.ts`, Claude Code 2.1.302, marked EARLY ACCESS, vendored in the recall
plugin's `types/claude-code/index.d.ts`). kiln's side is `internal/mods/api.go`
(the `implemented` map and the `documented` reject-list) and
`internal/mods/events.go` (the events it raises). Captured 2026-11-09.

**Legend**
- ✅ implemented — kiln answers it.
- ⚠️ partial — answered, but narrower than Claude Code.
- 🚫 recognized → rejects — kiln knows the name and rejects cleanly (`… is not
  available in kiln (mods v1)`, logged once per plugin+call).
- ❌ absent → rejects — not in kiln's lists at all; still rejects cleanly via the
  same path, just unnamed.
- ➖ not raised — (events) kiln never fires this event, so a hook for it is dead.

kiln's rejections never crash a hook, so an unsupported call degrades to "no
result," not a failure. The practical bar is: **do the mods Andre runs (squad,
savvy-progress) work?** — not full parity.

---

## Events (what a mod can `on(...)`)

Claude Code's mod-native events. kiln raises **8**. (Claude Code's `classic.*`
events — `PreToolUse`, `PostToolUse`, … — are the settings.json hooks; kiln
runs those through its own Claude Code-hooks path, not the mods engine, so they
are out of scope here.)

| event | Claude Code fires when | kiln |
|---|---|---|
| `session.start` | once per plugin load, before first prompt | ✅ |
| `session.end` | session ends (exit, clear, resume, logout) | ⚠️ raised on kiln's exit (`prompt_input_exit`) and when a `-p` run finishes (`other`), within a 2 s bound; no clear/resume/logout reasons (kiln has no such session transitions) |
| `session.receive` | an inbound delivery reaches the session | ⚠️ raised for an MCP channel event (origin `task-notification`) before it is queued; `{consumed}` drops it and a rewritten `text` is what is queued. kiln has no peers, bridge client or agent mailbox, so no other origin ever arrives |
| `session.append` | a conversation row is about to be stored | ⚠️ raised right after each row is committed (observe-only: a rewrite of the row is not applied); doors prompt, hook-context, delivery, response, tool-result, compaction |
| `session.compact` | the conversation is about to be compacted | ⚠️ raised before every compaction (trigger manual/auto/plugin); `{skip}` and rewritten `instructions` are honoured; `{messages}` of a hook's own are not (kiln summarises the conversation it holds) |
| `session.send` | a message is about to leave for another agent or session | ⚠️ raised only by a mod's `$.session.send` (kiln's model has no SendMessage tool); `origin` is pinned; nothing is ever delivered |
| `session.attach` / `session.detach` | a remote client joins / leaves the surface roster | ➖ never raised: kiln has one local terminal and no remote attach. A hook on them registers and logs "kiln does not raise …" |
| `session.measure` | session usage measured, a unit moved | ⚠️ raised after each main-loop response when the context fill (the figure `/context` and the footer read) differs from the last one raised; `changed` is `["context"]`, `rateLimits` is `[]`, `cost` is absent (kiln reads no rate-limit windows and keeps no priced ledger for the event) |
| `turn.start` | a model turn begins | ✅ |
| `turn.step` | a model request is about to be sent (streams) | ✅ chunks (text, thinking, tool, input, stop) stream through the hook chain in order, then the result |
| `turn.complete` | a model turn ended | ✅ |
| `tool.call` | before the engine runs a tool | ✅ |
| `tool.check` | before a tool's permission verdict settles | ⚠️ raised before the permission gate on every real call (core's answer is the gate's query: allow, ask or deny); only a hook's `deny` is applied, an `allow`/`ask` does not override the gate |
| `tool.describe` | a tool's schema first rendered in a session | ⚠️ raised once per tool of the main conversation when mods start; a rewritten `description` replaces the tool's; `isDeferred` is not applied (no ToolSearch placement); subagents' tools are not described |
| `command.run` | a slash command is about to run | ✅ |
| `command.describe` | a command listed for typeahead / help | ⚠️ raised for every command each time the registry list is rebuilt (500 ms bound); `description` and `argumentHint` apply; `isHidden` does not (kiln's typeahead has no hidden commands); `immediate` is always false |
| `config.set` | a /config row is about to change | ⚠️ raised only by a mod's `$.config.set` (origin `{kind:'plugin', name}`; the caller's own hooks skipped). kiln's `/config` panel is a read-only report with no menu write, so no person-origin `composer` event ever fires. `key`, `previous`, `provider`, `origin` pinned. See the `$.config.set` row for what core answers |
| `config.describe` | a /config row is listed | ⚠️ raised once per row for `$.config.list` (not cached: every list asks the hooks again); a relabel, `description` and `isHidden` apply. kiln's own `/config` panel (a report of model, mode, roots, hooks, …, none of them rows `$.config.list` names) does not raise it |
| `telemetry.log` | a record is about to be logged | ⚠️ raised only by a mod's `$.telemetry.log` (`to` filled in as `anthropic`, pinned; a collector record's `loggedAt` filled from the clock when left out). kiln raises none of its own and has no analytics sink or collector beneath the hooks: core resolves and nothing is sent. A hook's `{deny}` rejects the caller. `on("telemetry.*")` hooks both names |
| `telemetry.mark` | a feature use is marked | ⚠️ as `telemetry.log`: raised only by `$.telemetry.mark`, no `to`, core sends nothing; `{deny}` rejects the caller |
| `agent.spawn` | before the Agent tool starts a subagent | ✅ |
| `agent.offer` | an agent type is offered to the model | ⚠️ raised at dispatch (the model naming a `subagent_type`); `isOffered: false` refuses the dispatch with a reason; the task tool's static listing of agent types is built before mods start and is not filtered; a plugin's own `$.agent.spawn` is no offer |
| `prompt.submit` | a prompt is submitted, before the turn | ⚠️ raised in the TUI at Enter (busy: at Enter with the running turn's id) and by `$.prompt.submit`, before mentions and UserPromptSubmit hooks; under `-p` once, with origin `{kind:'sdk'}`; a rewrite of `text`, added `context` and `drop` apply; a slash command's expansion gets `drop` and `context` only (its text is not rewritten); `wait` is always false (kiln has no queue-submit chord); image attachments are served through `$.prompt.media` |
| `prompt.fill` | text is about to enter the prompt box | ⚠️ raised for `$.prompt.fill`; a rewritten `text` is what the box takes; `decorations` are passed on but not drawn; under `-p` the answer is refusal `no_composer` |
| `prompt.suggest` | text proposed as a prompt suggestion | ⚠️ raised for `$.prompt.suggest` and for kiln's own next-prompt guess (origin `{kind:'suggestion'}`); a rewritten `text` is shown, `isShown:false` drops it; under `-p` `isShown` is false |
| `prompt.edit` | the person edits the prompt box | ⚠️ raised once per key or paste that changes the text, with `{start, end, inputText}` as a splice in UTF-16 units and the box before; observe-only (a hook's rewrite is not applied); carries no `key`; a pure cursor move raises nothing; never fires under `-p` (no input line) |
| `prompt.change` | the prompt box text changed | ⚠️ raised when something other than a key changed the text (history recall, clear/submit, kiln's own edit, a plugin's fill: origin `history`, `clear`, `engine`, `plugin`); observe-only; never fires under `-p` |
| `prompt.autocomplete` | typeahead for the token at the cursor | ⚠️ raised on each popup refresh for the token before the cursor; the hooks' `suggestions` rows (`text`, `label`, `description`) join the popup beneath kiln's own, and accepting one replaces the token; never fires under `-p` |
| `prompt.mention` | an `@path` is read | ⚠️ raised in `ResolveMentions` (TUI and `-p`) after the path resolves to an existing non-directory file; `deny` refuses it, a redirected `path` is re-checked against the roots, `context` blocks follow the files; not raised for a missing file or a directory; no line-range `offset`/`limit` |
| `prompt.compose` | a system prompt is rendered | ⚠️ raised each time kiln builds the system prompt (start, `/model`, tier change, MCP index, scratchpad, `$.ui.invalidate('prompt.compose')`) over kiln's sections; a rewritten list (valid ids, shared before session) replaces it, a bad list is refused and the prompt stands; mods start after the first build, so the prompt is rebuilt once they are up |
| `prompt.section` | a named system-prompt section is assembled | ⚠️ raised per section: `base` (shared), `environment`, `append`, `review`, `scratchpad`, `memory`, `auto_memory`, `skills`, `mcp_index` (session); `text:null` drops one; kiln has no output-style, git-status or tools sections of its own, so those ids do not appear |
| `prompt.context` | first message's context blocks computed | ➖ never raised: kiln sends no first-user-message context blocks (CLAUDE.md, env and date live in the system prompt; use `prompt.section` `memory` / `environment`) |
| `prompt.attachment` | an injected model message is carried | ⚠️ raised for types `plan_mode`, `plan_mode_exit` (mode reminders, no detail), `hook_additional_context` (hook or plugin context) and `file` (an `@mention`'s content); `text:null` leaves the message out; other types (queued commands, todo/task reminders, diagnostics, …) are not raised |
| `skill.prompt` | a skill's prompt is expanded for the model | ⚠️ raised where the `skill` tool returns a skill's body (skills, plus the custom commands the tool also serves) and where a `/skill` command builds its prompt, with the text after args are applied; a hook's `{text}` is what the model reads, a failed chain leaves it. `frontmatter` is every key the SKILL.md declares (`__proto__`, `constructor`, `prototype` left out, named in `frontmatterProblem`); `source` is `userSettings`, `projectSettings` or `plugin` (a command served by the tool has none, so reads `built-in`); `skill`, `source`, `pluginName`, `path`, `frontmatterProblem` pinned. Not raised for a custom `.claude/commands` slash command, nor for skills preloaded into a subagent (kiln has no preload) |
| `attribution.text` | a commit / PR text is composed | ➖ never raised: kiln composes no commit trailer, PR footer, pre-ship mandate or commit-gate text (it has no `includeCoAuthoredBy` / attribution setting and no commit skills of its own), so there is nothing for the event to carry. A hook on it registers and logs "kiln does not raise …" |
| `plugin.register` | a hooks module joins the chain | ➖ |
| `ui.render` | before a component is drawn | ⚠️ `Pane`, `AbovePrompt` and `CommandOutput` (tree replacement only; prop rewrites are not applied); other components not started |
| `ui.resolve` | at load, per surface element table | ✅ |
| `ui.press` | a Button or Markdown link is pressed | ✅ raised with `{ plugin, element (the Button's key), component, requestId, surface }`, all pinned; core runs the element's own `onPress`. A hook that answers without `next` takes the press |
| `ui.input` / `ui.select` | an Input / Select changes | ➖ never raised: kiln draws no `Input` or `Select` element (only Box, Text, Button, Raster, Image, Link, Markdown), so nothing can change. A hook on them registers and logs "kiln does not raise …" |
| `ui.message` / `ui.fault` | a `Client` posts data / fails on a surface | ➖ never raised: kiln has no `Client` element and no surface module to load, so none can post or fail |
| `ui.scroll` / `ui.focus` | before a site's window / focus ring moves | ⚠️ raised for a `Pane` body (the person's Up/Down, Tab/Shift+Tab while it holds the keys; `$.ui.scroll` / `$.ui.focus` with origin `plugin`). Core moves to `e.offset` / lands on `e.element`; an answer without `next` leaves it. kiln's band above the prompt is not a scrollable window and has no focus ring, so nothing is raised for `AbovePrompt`; the mouse wheel does not scroll a pane, so no wheel event. Pinned: everything but `offset` / `element`; `pointer` is never set |
| `ui.status` / `ui.log` / `ui.notice` / `ui.notify` / `ui.copy` / `ui.selection` / `ui.blit` | op events of the `$.ui.*` calls | ✅ raised by the call, every hook but the calling one, then core |

---

## `$` methods (what a hook can call)

Grouped by namespace. "Signature" is Claude Code's.

### session
| call | signature | kiln |
|---|---|---|
| `session.id` | `() => Promise<string>` | ✅ |
| `session.cwd` | `() => Promise<string>` | ✅ |
| `session.usage` | `(args?) => Promise<SessionUsage>` | ⚠️ context fill only; no cost/limits |
| `session.messages` | `(args?) => Promise<…>` | ⚠️ main conversation as rows, or `{as:"api"}` as ApiMessage; newest 4096 (the API form opens on a user message). `{agentId}` resolves `{deny}`: kiln keeps a subagent's conversation to itself. No `toolResults` on user rows (a result is folded into its `toolUses` entry) |
| `session.root` | `() => Promise<string>` | ✅ repo root (gitfiles) |
| `session.model` | `() => Promise<string>` | ✅ live lane model |
| `session.turns` | `() => Promise<number>` | ✅ user-typed prompts (KilnTyped) |
| `session.repo` | `() => Promise<SessionRepo\|null>` | ✅ git root + push URL; internal:false, name:null |
| `session.version` | `() => Promise<SessionVersion>` | ⚠️ `{version}` only (no base/builtAt: kiln's version is a git sha) |
| `session.compact` | `(input?) => Promise<…>` | ✅ runs the session.compact chain (trigger `plugin`) then kiln's compaction; resolves `{messages, tokensBefore, tokensAfter}` or `{skip}` |
| `session.append` | `(args) => Promise<…>` | ⚠️ queues a user text row for the main conversation (string or text blocks); other types or blocks resolve `{deny}`; a subagent id resolves `{deny}` |
| `session.authorize` | `() => Promise<SessionAuthorization>` | ⚠️ always `null`, the d.ts answer for "no first-party credential". A handle would let a mod spend the person's Anthropic key at a host (`$.http.fetch` `auth`) with no approval from them, which is more than the permission gate granted the mod, so kiln lends none even when the session holds a key |
| `session.surfaces` / `session.surface` | `() => Promise<…>` | ⚠️ `["terminal"]` / `"terminal"` while the TUI runs; `[]` / `null` in a `-p` run. Never a remote surface (`surface` deprecated) |
| `session.identity` | `() => Promise<SessionIdentity>` | ⚠️ always `{deny}`: the d.ts answers an account only to a mod managed settings install, and kiln installs none that way (it holds no login either) |
| `session.send` | `({to, text}) => Promise<{isDelivered, reason?}>` | ⚠️ runs the `session.send` chain (origin `{kind:"plugin", name}`; `to` as a string or `{sessionId}`/`{agentId}`), then kiln's core: `{isDelivered:false}` with a reason, because kiln has no SendMessage tool and no inbox for another agent or session. A hook's own refusal reason wins |
| `session.receive` / `attach` / `detach` / `measure` / `end` / `start` | event-call forms | see the events table |

### fs
| call | signature | kiln |
|---|---|---|
| `fs.read` | `(path, {as?}) => Promise<string\|{base64}>` | ✅ (4 MiB cap) |
| `fs.exists` | `(path) => Promise<boolean>` | ✅ |
| `fs.list` | `(path?) => Promise<FsEntry[]>` | ✅ |
| `fs.write` | `(path, text) => Promise<void>` | 🚫 |
| `fs.stat` | `(path, options?) => Promise<FsStat>` | 🚫 |
| `fs.ancestors` | `(request) => Promise<FsAncestor[]>` | 🚫 |

### ui
| call | signature | kiln |
|---|---|---|
| `ui.open` / `ui.close` / `ui.panes` | pane lifecycle | ✅ |
| `ui.toast` | `(text, options?) => void` | ✅ |
| `ui.invalidate` | `(event) => void` | ✅ |
| `ui.resolve` | `(e) => Elements` | ✅ |
| `ui.status` | `(text?) => void` | ✅ one dim line per plugin under kiln's status row (first 2000 chars); `undefined` clears it |
| `ui.log` | `(text, options?) => void` | ✅ `transcript` appends a dim system-notice row; `debug` goes to kiln's debug log only; a hook may rewrite `e.to` |
| `ui.notice` | `(tool_use_id, text?) => void` | ✅ one dim line under the open permission dialog of that call; refused when no dialog is open for the id. Not refused for another plugin's `$.tool.call` run (kiln does not track who made it) |
| `ui.blit` | `(args) => Promise<UiBlitResult>` | ⚠️ repaints a keyed `Raster` the plugin drew in its pane (`cells`); an `Image` source answers `{ deny }`: kiln draws an Image as its alt text |
| `ui.ask` | `(question, options?) => Promise<string>` | ✅ a `tool.call` of `AskUserQuestion` through every hook but the calling one, answered by kiln's question dialog; the label chosen (multi-select comma-joined) or the text typed under Other. Rejects when dismissed and in a `-p` run. Option labels double as descriptions |
| `ui.scroll` | `(args) => Promise<{ deny? }>` | ⚠️ `to: 'start' \| 'end'` with `in`, or `{ key }` of a Button, in this plugin's pane; `block` honoured. `{ requestId }` of a transcript row answers `{ deny }`: kiln moves none. Only a Button can be located by `key` |
| `ui.focus` | `(args) => Promise<{ deny? }>` | ⚠️ lands the focus ring of a pane that holds the keys on a keyed Button; any other element is `{ deny }` (kiln's ring walks Buttons) |
| `ui.copy` | `(args) => Promise<UiCopyResult>` | ✅ written as the mouse selection is (OSC 52 plus the local clipboard tool); `no-clipboard` when that tool fails |
| `ui.selection` | `() => Promise<UiSelection \| undefined>` | ⚠️ the text of the last mouse selection (fullscreen only); no `requestId`, since kiln keeps no row identity for it. Cleared by the next click |
| `ui.notify` | `(text, options?) => Promise<UiNotifyResult>` | ⚠️ writes the escape sequence of `preferredNotifChannel` (`auto` picks iTerm2, Kitty, Ghostty, else the bell), once the person's `Notification` hooks have run (`notification_type` `plugin_notification`, `message` the text, `title` the heading; a matcher is tested against the type, so a `permission_prompt` hook does not run). They run only under the TUI (a `-p` run has no notification wiring) |
| `ui.view` | (kiln-only name) | ✅ always refuses — kiln has no per-agent transcript view |

### clock
| call | signature | kiln |
|---|---|---|
| `clock.now` | `() => Promise<number>` | ✅ |
| `clock.after` / `clock.every` | `(ms, fn) => Timer` | ✅ |
| `clock.sleep` | `(ms, options?) => Promise<void>` | ✅ |

### agent / command / tool
| call | signature | kiln |
|---|---|---|
| `agent.list` | `() => Promise<AgentInfo[]>` | ✅ |
| `command.register` | `(command) => Promise<…>` | ✅ |
| `tool.register` | `(tool) => Promise<…>` | ✅ |
| `agent.spawn` | `(input) => Promise<AgentSpawnResult>` | ⚠️ raises `agent.spawn` (every hook but the caller's), then starts the subagent in the background and resolves `{model, agentId}`; `name`, `cwd` and `background` are not applied (kiln has no teammates, and a subagent runs in the session's directory); the model is the one asked or the session's, not yet resolved |
| `agent.register` | `(spec) => Promise<…>` | ⚠️ raises `agent.register`, then makes the type dispatchable by name (`$.agent.spawn`, or the model naming it); refuses a name that exists; `disallowedTools`, `effort` and `permissionMode` are not applied; the task tool's listing does not include it (the model is not told of it) |
| `agent.offer` | `(input) => Promise<…>` | ➖ an event only: Claude Code has no `$` call of that name |
| `command.list` | `() => Promise<CommandInfo[]>` | ✅ `source` is builtin, user (project and personal files) or plugin (plugins and skills); `plugin` is the command's namespace; no `mcp` source |
| `command.run` | `(input) => Promise<CommandRunResult>` | ⚠️ raises `command.run` (every hook but the caller's), then runs the slash command and resolves `{text}`; a panel, an editor hand-over, a quit or a prompt expansion does only its printed part; a mod's own command is answered by its hook, so core runs nothing for it; no `exitCode` |
| `command.describe` | `(input) => Promise<…>` | ➖ an event only: Claude Code has no `$` call of that name |
| `tool.list` | `() => Promise<ToolInfo[]>` | ✅ the main conversation's tools by Claude Code's names (`bash` is `Bash`); `mcp` is true for `mcp__` tools |
| `tool.call` | `(input) => Promise<ToolCallResult>` | ⚠️ raises `tool.call` (every hook but the caller's), then runs the tool through the permission gate (which may prompt the person) and resolves `{result, text, isError?}` (`result` is the text: kiln has no structured per-tool results) or `{deny}`; the settings PreToolUse hooks do not run for it; `consent` and `requestMeta` are not applied; a mod's own tool is answered by its hook alone |
| `tool.check` | `(input) => Promise<ToolCheckResult>` | ✅ raises `tool.check` (every hook but the caller's); core is the permission gate asked without prompting or calling the auto-mode classifier (where the gate would ask, the answer is `ask`); a deny is recorded in the gate's blocked list like any refusal; no `rule`, `hook`, `setting` or `ceiling` |

### model / audio / mcp / http / process
| call | signature | kiln |
|---|---|---|
| `model.complete` | `(request) => Promise<ModelCompleteResult>` | ✅ via provider.Stream; resolves alias/id/"" model; block `cache` flag ignored |
| `model.classify` | `(text, labels, options?) => Promise<string?>` | ✅ via provider.Stream; options ignored |
| `model.fork` | `(request) => Promise<ModelForkResult>` | ✅ session model + branch transcript; `nothing-to-fork` when empty |
| `audio.play` / `audio.speak` | `(…) => Promise<…>` | 🚫 no audio in kiln (user decision) |
| `mcp.call` | `(server, tool, args?) => Promise<McpToolResult>` | ✅ via mcp hub |
| `http.fetch` | `(url, init?) => Promise<HttpResponse>` | ✅ text body; no auth handle |
| `process.run` | `(argv, init?) => Promise<ProcessRunResult>` | ✅ ungated (matches CC) |

### store / env / settings / config / prompt / turn
| call | signature | kiln |
|---|---|---|
| `env.get` | `(name) => Promise<string?>` | ✅ |
| `settings.read` | `(args?) => Promise<Settings>` | ✅ |
| `env.set` | `(name, value?) => Promise<void>` | 🚫 |
| `store.get` / `set` / `delete` / `keys` | plugin key-value store | 🚫 |
| `config.list` | `() => Promise<ConfigRow[]>` | ⚠️ kiln's rows are `model` (text) and `permissionMode` (choice), provider `engine/core`, plus one row per non-sensitive `userConfig` field of each loaded plugin (`<plugin>.<field>`, provider the plugin, tier `user`, value from its options, `isLocked: true`). Each row runs `config.describe` first; hidden rows are left out. kiln has no theme, verbose or other Claude Code panel rows |
| `config.set` | `({key, value}) => Promise<{value} \| {deny}>` | ⚠️ never writes: it raises `config.set` through the other mods' hooks and then core answers `{deny}`. What Claude Code allows here (the person's own menu write to user settings) kiln does not: kiln writes only its own `~/.kiln` / `.kiln` files, never Claude Code's settings, and its `/config` is read-only. So a `userConfig` row is denied as owned by Claude Code's settings (`isLocked`), `model` / `permissionMode` are denied as changed only by `/model` / `/plan`, and a value of the wrong kind is denied as not fitting. A hook that answers `{value}` without calling `next` is still reported as `{deny}`, never as written. An unknown key rejects |
| `telemetry.log` / `telemetry.mark` | `(entry) => Promise<void>` | ⚠️ validated (`to` anthropic or collector, props number / boolean / `{value, of}`, `mark` feature snake_case, kind ok / sad / bad), run through the hooks, then dropped: kiln has no first-party analytics and no collector. Resolves when the hooks answer; rejects on a hook's `{deny}` or a malformed entry |
| `prompt.read` | `() => Promise<PromptBox>` | ✅ the box as the TUI last published it, cursor in UTF-16 units; under `-p` `{text:'', cursor:0}` |
| `prompt.submit` | `(input) => Promise<…>` | ✅ raises `prompt.submit`, then starts a turn once idle (queued behind a running one); under `-p` it answers `{drop: "kiln -p runs the one prompt it was given: there is no idle session to submit into"}` |
| `prompt.fill` / `suggest` | | ✅ raise their events, then reach the box through the TUI; `fill` refuses with `no_composer` under `-p` and `dialog` while a dialog holds the keys; `suggest` shows only in an idle empty box |
| `prompt.compose` | `(args) => Promise<…>` | ✅ the sections kiln composes for the args (facts: model, promptModel, surfaces, tools, outputStyle:null, traits), after `prompt.section` |
| `prompt.media` | `({id, offset?}) => Promise<…>` | ✅ a 4 MiB slice of an image attached to a submission in flight; images only; answered for any mod (kiln has no managed-settings notion) |
| `turn.abort` | `(input) => Promise<void>` | ✅ raises `turn.abort`, then aborts the running main turn; rejects when `turnId` is not the running turn |

### state (kiln-only)
Claude Code backs reactive state with `atom`/`read`/`update` imported from
`claude-code`. kiln wires those to `$.state.get` / `$.state.set`, which have no
Claude Code `$` equivalent. ✅ for the atom use squad/savvy make.

---

## What actually matters

Most remaining 🚫/❌ rows are for features no mod Andre runs touches. The egress/
exec tier lands — **`http.fetch`**, **`process.run`** (ungated, matching Claude
Code — a loaded mod is trusted; user decision 2026-11-09), **`mcp.call`**,
**`model.classify`**, plus **`fs.write`/`fs.stat`/`fs.ancestors`**, **`store.*`**,
**`env.set`** — as do the model and session-meta tiers once the `.d.ts` was
recovered (CC 2.1.302): **`model.complete`** and **`model.fork`** on the
`provider.Stream` path (usage recorded to session cost), and the
**`session.root`/`session.model`/`session.version`/`session.repo`/`session.turns`**
getters (user decision 2026-11-09: build these, revisiting the earlier
classify-only call). The deliberate stops left:

1. **`session.end`** — landed (see the events table).
2. **`audio.play` / `audio.speak`** — no audio playback or capture in kiln (user
   decision 2026-11-09).
3. **`turn.step` streaming** — landed.
4. **Simplifications in the tiers that did land:** `model.complete`/`fork` ignore a
   text block's `cache` flag (kiln's `StreamOptions` has no per-block prompt-cache
   breakpoint) and report an API error as the honest `unknown` kind with a null
   status (kiln can't recover the provider's HTTP status or classify it);
   `session.version` returns `{version}` only (no `base`/`builtAt` — kiln's version
   is a git sha); `session.repo` reports `internal:false`, `name:null` (kiln has no
   own-org notion).
5. `config.*`, `telemetry.*`, `skill.prompt` — landed (see the tables; `config.set`
   never writes, kiln has no telemetry sink). `attribution.text` is never raised:
   kiln composes no git attribution text.

squad and savvy-progress run on the ✅ set as of this commit.
