# Mods v1 gap: kiln vs Claude Code

What kiln's mod runtime (`internal/mods`) implements, measured against Claude Code's
mod API surface. The authoritative Claude Code surface is its type declarations,
`mods/types/claude-code.d.ts` (header: "Written by Claude Code 2.1.277",
marked EARLY ACCESS). kiln's side is `internal/mods/api.go` (the `implemented`
map and the `documented` reject-list) and `internal/mods/events.go` (the events it
raises). Captured 2026-11-09.

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
| `session.end` | session ends (exit, clear, resume, logout) | ➖ |
| `session.receive` | an inbound delivery reaches the session | ➖ |
| `session.append` | a conversation row is about to be stored | ➖ |
| `session.compact` | the conversation is about to be compacted | ➖ |
| `session.attach` / `session.detach` | a remote client joins / leaves the surface roster | ➖ |
| `session.measure` | session usage measured, a unit moved | ➖ |
| `turn.start` | a model turn begins | ✅ |
| `turn.step` | a model request is about to be sent (streams) | ⚠️ fired, but chunks not streamed |
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
| `session.turns` | `() => Promise<number>` | 🚫 |
| `session.repo` | `() => Promise<SessionRepo\|null>` | 🚫 |
| `session.version` | `() => Promise<SessionVersion>` | 🚫 SessionVersion shape unverified |
| `session.compact` | `(input?) => Promise<…>` | 🚫 |
| `session.append` | `(args) => Promise<…>` | 🚫 |
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
| `model.complete` | `(request) => Promise<string>` | 🚫 request shape unverified |
| `model.classify` | `(text, labels, options?) => Promise<string?>` | ✅ via provider.Stream; options ignored |
| `model.fork` | `(request) => Promise<…>` | 🚫 no one-shot primitive |
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
| `turn.abort` | `(input) => Promise<void>` | 🚫 |

### state (kiln-only)
Claude Code backs reactive state with `atom`/`read`/`update` imported from
`claude-code`. kiln wires those to `$.state.get` / `$.state.set`, which have no
Claude Code `$` equivalent. ✅ for the atom use squad/savvy make.

---

## Stale entries in kiln's reject-list

`internal/mods/api.go`'s `documented` still names **`session.send`**, which Claude
Code's 2.1.277 `.d.ts` does not (no such method). Harmless — it just rejects — but
it should be dropped or corrected when this list is next touched. (`process.spawn`
was dropped when `process.run` landed.)

---

## What actually matters

Most remaining 🚫/❌ rows are for features no mod Andre runs touches. The whole
egress/exec tier now lands: **`http.fetch`**, **`process.run`** (ungated, matching
Claude Code — a loaded mod is trusted; user decision 2026-11-09), **`mcp.call`**,
**`model.classify`**, plus **`fs.write`/`fs.stat`/`fs.ancestors`**, **`store.*`**,
**`env.set`**, and the **`session.root`/`session.model`** getters. The deliberate
stops left:

1. **`session.end`** (➖ event) — the only lifecycle hook for a mod to flush or
   clean up on exit. Nothing kiln runs needs it yet, but it is the most common
   "real mod" expectation after `session.start`. Needs a shutdown event kiln does
   not raise.
2. **`model.complete` / `model.fork`** — `complete`'s request/response shape lives
   only in the `.d.ts` (unrecoverable this session), and `fork` has no one-shot
   primitive in kiln. `model.classify` ships; these two reject by name.
3. **`audio.play` / `audio.speak`** — no audio playback or capture in kiln (user
   decision 2026-11-09).
4. **`turn.step` streaming** (⚠️) — kiln fires the event but not the chunk stream,
   so a live-token mod sees only the final step.
5. **`session.version` / `session.repo` / `session.turns`** — their return types
   (`SessionVersion`, `SessionRepo`) are object shapes the `.d.ts` defines and this
   session cannot verify, so they reject rather than return a guessed shape.
6. Everything under `prompt.*`, `config.*`, `agent.offer`, `tool.check`,
   `skill.prompt`, `attribution.text` — deep integration points; no consumer, low
   priority.

squad and savvy-progress run on the ✅ set as of this commit.
