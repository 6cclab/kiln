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
| `session.receive` | an inbound delivery reaches the session | ➖ |
| `session.append` | a conversation row is about to be stored | ⚠️ raised right after each row is committed (observe-only: a rewrite of the row is not applied); doors prompt, hook-context, delivery, response, tool-result, compaction |
| `session.compact` | the conversation is about to be compacted | ⚠️ raised before every compaction (trigger manual/auto/plugin); `{skip}` and rewritten `instructions` are honoured; `{messages}` of a hook's own are not (kiln summarises the conversation it holds) |
| `session.attach` / `session.detach` | a remote client joins / leaves the surface roster | ➖ |
| `session.measure` | session usage measured, a unit moved | ➖ |
| `turn.start` | a model turn begins | ✅ |
| `turn.step` | a model request is about to be sent (streams) | ✅ chunks (text, thinking, tool, input, stop) stream through the hook chain in order, then the result |
| `turn.complete` | a model turn ended | ✅ |
| `tool.call` | before the engine runs a tool | ✅ |
| `tool.check` | before a tool's permission verdict settles | ➖ |
| `tool.describe` | a tool's schema first rendered in a session | ➖ |
| `command.run` | a slash command is about to run | ✅ |
| `command.describe` | a command listed for typeahead / help | ➖ |
| `config.set` | a /config row is about to change | ➖ |
| `config.describe` | a /config row is listed | ➖ |
| `agent.spawn` | before the Agent tool starts a subagent | ✅ |
| `agent.offer` | an agent type is offered to the model | ➖ |
| `prompt.submit` | a prompt is submitted, before the turn | ➖ (registers, never runs) |
| `prompt.fill` | text is about to enter the prompt box | ➖ |
| `prompt.suggest` | text proposed as a prompt suggestion | ➖ |
| `prompt.edit` | the person edits the prompt box | ➖ |
| `prompt.compose` | a system prompt is rendered | ➖ |
| `prompt.section` | a named system-prompt section is assembled | ➖ |
| `prompt.context` | first message's context blocks computed | ➖ |
| `prompt.attachment` | an injected model message is carried | ➖ |
| `skill.prompt` | a skill's prompt is expanded for the model | ➖ |
| `attribution.text` | a commit / PR text is composed | ➖ |
| `plugin.register` | a hooks module joins the chain | ➖ |
| `ui.render` | before a component is drawn | ⚠️ `Pane` + `AbovePrompt` only |
| `ui.resolve` | at load, per surface element table | ✅ |
| `ui.press` | a Button or Markdown link is pressed | ⚠️ callback invoked; no hook chain |
| `ui.input` / `ui.select` | an Input / Select changes | ➖ (no Input/Select elements) |
| `ui.message` | a Client surface module posts data | ➖ |
| `ui.scroll` / `ui.focus` | before a site's window / focus ring moves | ➖ |

---

## `$` methods (what a hook can call)

Grouped by namespace. "Signature" is Claude Code's.

### session
| call | signature | kiln |
|---|---|---|
| `session.id` | `() => Promise<string>` | ✅ |
| `session.cwd` | `() => Promise<string>` | ✅ |
| `session.usage` | `(args?) => Promise<SessionUsage>` | ⚠️ context fill only; no cost/limits |
| `session.messages` | `() => Promise<SessionMessage[]>` | 🚫 |
| `session.root` | `() => Promise<string>` | ✅ repo root (gitfiles) |
| `session.model` | `() => Promise<string>` | ✅ live lane model |
| `session.turns` | `() => Promise<number>` | ✅ user-typed prompts (KilnTyped) |
| `session.repo` | `() => Promise<SessionRepo\|null>` | ✅ git root + push URL; internal:false, name:null |
| `session.version` | `() => Promise<SessionVersion>` | ⚠️ `{version}` only (no base/builtAt: kiln's version is a git sha) |
| `session.compact` | `(input?) => Promise<…>` | ✅ runs the session.compact chain (trigger `plugin`) then kiln's compaction; resolves `{messages, tokensBefore, tokensAfter}` or `{skip}` |
| `session.append` | `(args) => Promise<…>` | ⚠️ queues a user text row for the main conversation (string or text blocks); other types or blocks resolve `{deny}`; a subagent id resolves `{deny}` |
| `session.authorize` | `() => Promise<SessionAuthorization>` | 🚫 |
| `session.surfaces` / `session.surface` | `() => Promise<…>` | 🚫 (`surface` deprecated) |
| `session.identity` | `() => Promise<SessionIdentity>` | ❌ |
| `session.receive` / `attach` / `detach` / `measure` / `end` / `start` | event-call forms | ❌ |

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
| `ui.status` | `(text?) => void` | 🚫 |
| `ui.log` | `(text, options?) => void` | 🚫 |
| `ui.notice` | `(tool_use_id, text?) => void` | 🚫 |
| `ui.blit` | `(args) => Promise<UiBlitResult>` | 🚫 |
| `ui.ask` | `(question, options?) => Promise<string>` | ❌ |
| `ui.scroll` / `ui.focus` | `(args) => Promise<…>` | ❌ |
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
| `agent.spawn` | `(input) => Promise<AgentSpawnResult>` | 🚫 |
| `agent.register` | `(spec) => Promise<…>` | 🚫 |
| `agent.offer` | `(input) => Promise<…>` | ❌ |
| `command.list` | `() => Promise<CommandInfo[]>` | 🚫 |
| `command.run` | `(input) => Promise<CommandRunResult>` | ❌ (event raised; not a `$` call) |
| `command.describe` | `(input) => Promise<…>` | ❌ |
| `tool.list` | `() => Promise<ToolInfo[]>` | 🚫 |
| `tool.call` | `(input) => Promise<ToolCallResult>` | 🚫 |
| `tool.check` | `(input) => Promise<ToolCheckResult>` | ❌ |

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
| `config.list` | `() => Promise<ConfigRow[]>` | 🚫 |
| `config.set` / `config.describe` | | ❌ |
| `prompt.read` | `() => Promise<PromptBox>` | 🚫 |
| `prompt.submit` | `(input) => Promise<…>` | 🚫 |
| `prompt.fill` / `suggest` / `compose` | | ❌ |
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
5. Everything under `prompt.*`, `config.*`, `agent.offer`, `tool.check`,
   `skill.prompt`, `attribution.text` — deep integration points; no consumer, low
   priority.

squad and savvy-progress run on the ✅ set as of this commit.
