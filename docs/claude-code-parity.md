# Claude Code feature parity spec

> Rendering contract: `kiln-design.md` — kiln's own palette, glyphs and block
> anatomy (Layout 1b "Ruled"). This file no longer tracks pixel/glyph parity
> with Claude Code's look; kiln has its own visual design, applied on top of
> the same underlying architecture. Where a row below still describes an
> exact glyph or layout, treat it as historical context for the *behavior*
> it documents, not as a rendering spec — see `kiln-design.md` for what the
> kiln actually draws today.

kiln (née harness) keeps Claude Code's *feature surface* — keybindings, CLI
flags, permission-mode semantics, hooks, MCP, slash commands, subagents — but
replaces its visual design language with kiln's own. This file turns the
feature surface into a checklist so "does the behavior match" is testable
rather than a matter of taste; it is not a visual-parity checklist any more.

**Confidence is marked per item.** `[obs]` means directly observable in a running
Claude Code session and safe to treat as settled. `[chk]` means stated from
familiarity and **must be confirmed against the real thing before implementing** —
Claude Code ships frequently, so memory goes stale. Do not implement a `[chk]`
item without looking at it first.

---

## 1. Input line

Exact rule/glyph rendering (the prompt glyph, rule colour) is kiln's own —
see `kiln-design.md`'s "Input" section, not the notes column below.

| Behavior | Conf | Notes |
|---|---|---|
| Two full-width rules around the input (top and bottom), no sides | `[obs]` | matches CC's `borderLeft/right: false` box |
| A prompt glyph at the left inside the box | `[obs]` | kiln uses `›`, not CC's `❯` — `kiln-design.md` |
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

All three are token-boundary triggers, matching `AutocompleteProvider.triggerCharacters`.
Claude Code's own prompt input has only these two live modes (bash and plain
prompt) - no `#` memory shortcut. kiln's earlier `#` mode was removed to
match; a line starting with `#` is now an ordinary prompt.

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
misses `/bashes` and `/posture`, which register inline in `internal/cli`):

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
kiln extends with instead (skills, agents, commands, MCP, hooks).

**Adapt:** `/login` `/logout` already exist in kiln's CLI and should also
be slash commands. `/usage` becomes budget/tier reporting rather than plan
limits. `/fast` has no meaning here; `/effort` (§9) replaces it.

**Out of scope** — Anthropic-account or Anthropic-infrastructure specific:
`/upgrade` `/feedback` `/privacy-settings` `/install-github-app`
`/release-notes` `/pr-comments` `/code-review` `/ultrareview` `/review`
`/security-review` `/workflows`

Omit these rather than stubbing them. A command that exists and does nothing is
worse than one that is absent.

## 4a. Transcript layout — superseded by kiln-design.md

This section used to pin the exact Claude-Code-shaped transcript layout
(`⏺ Name(arg)` / `⎿` result glyph / `☒`/`☐` todo checkboxes / a bare `❯`
prompt with no label). kiln replaced all of that with its own "label rule"
block anatomy (a `{label}──── {meta}` hairline above every block, block
colours, and its own glyph table) — see `kiln-design.md`'s "Block anatomy"
section for the current, exact layout of every block type (user, text,
tool, diff, permission, plan, subagents, error, context) and the banner and
input box. The *behavioral* facts this section used to carry — a tool call
collapses to one line with a status-coloured marker and a summarized
result, an assistant turn has no leading marker on its own, a todo list
shows which item is done/current/pending, the input box is two full-width
rules with no sides — are unchanged and are covered by §4/§5 below; only
the exact characters and colours moved to `kiln-design.md`.

## 4. Transcript rendering

Exact glyphs/colours are kiln's own (`kiln-design.md`'s block anatomy
table), not the ones implied below; the "Behavior" column describes what
still holds structurally.

| Element | Behavior | Conf |
|---|---|---|
| User message | the message renders in its own block, no fill band shared with other blocks | `[obs]` |
| Assistant text | markdown rendered; code blocks syntax-highlighted | `[obs]` done |
| Tool call | one collapsed line: a status-coloured marker, tool name, key argument | `[obs]` |
| Tool result | indented under the call with a continuation glyph | `[obs]` |
| Long output | truncated with a "+N lines" affordance, Ctrl+O expands (Ctrl+R in old CC) | `[obs]` done |
| File edits | unified diff, green additions / red deletions, line numbers | `[obs]` |
| Todo list | list of items, completed struck through or dimmed, current item distinct | `[obs]` done |
| Thinking | dimmed italic block, collapsed by default | `[obs]` done |
| Errors | visually distinct from normal tool output | `[obs]` |

## 5. Spinner / working indicator

Exact spinner frames are kiln's own (`kiln-design.md`: `◐◓◑◒` at 140ms), not
Claude Code's frame set.

| Behavior | Conf |
|---|---|
| Animated glyph plus a varying lowercase gerund | `[obs]` |
| Elapsed seconds counter | `[obs]` |
| Token count `↓ N`, updating live during streaming | `[obs]` |
| "esc to stop" hint (was "esc to interrupt" in CC) on the footer, only while loading | `[obs]` |
| Replaced in-place by the result, leaving no spinner residue | `[obs]` |

## 6. Keybindings

| Key | Action | Conf |
|---|---|---|
| `Esc` | interrupt the current turn | done |
| `Esc` `Esc` | rewind (points at `/rewind`) | done |
| `Ctrl+C` | clear input or interrupt; twice within 1s exits | done |
| `Ctrl+D` | exit on empty input; passed to the editor otherwise | done |
| `Ctrl+L` | clear the rendered transcript, not the conversation | done |
| `Ctrl+O` | toggle detailed transcript (tool output and reasoning) | done |
| `Shift+Tab` | cycle permission mode | done |
| `Tab` | accept completion (pi-tui's editor) | done |

Routing lives in `internal/tui/keys.go`, separate from the app model so it can be tested
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
| "esc to stop" while a turn is loading (CC: "esc to interrupt") | done |

Claude Code shows `? for shortcuts` when idle and `esc to interrupt` while
loading. kiln's status line replaces the idle hint (a status line always
present is the documented divergence), so only the loading hint is threaded
through — it rides the first status row while a turn runs. Its exact wording
and colour theming are kiln's own — see `kiln-design.md`'s "Status line"
section.

**kiln addition, deliberately not in Claude Code:** live budget consumption
against the active tier. On a 32k local model this is the difference between a
working session and a mysterious truncation, and it is the one place kiln
should diverge — an addition, not a change to existing elements.

## 7a. Permission modes

Confirmed from `claude --help` (`--permission-mode`), so these strings are exact
rather than remembered. `Shift+Tab` cycles between them, and the active mode
shows in the footer when it is not the default.

| Mode | Behavior |
|---|---|
| `manual` | ask before every tool use |
| `acceptEdits` | file edits auto-approved, other tools still prompt; a write to a protected path (below) prompts |
| `auto` | done: deny/ask rules and the workspace boundary first; reads, workspace edits and read-only bash run; everything else goes past a classifier model that sees user messages, earlier non-read-only tool calls and CLAUDE.md, never tool results. A block goes back to the model; 3 in a row or 20 in a session ask instead; a failed check asks. `autoMode` settings from user settings and `--settings` only (code.claude.com/docs/en/permission-modes, /auto-mode-config). Details: `configuration.md` "Auto mode classifier" |
| `dontAsk` | never prompt: reads and allow-rule matches run, anything that would prompt is denied |
| `bypassPermissions` | skip all checks, protected paths included; deny rules and ask rules still apply |
| `plan` | read-only; no edits or mutations, produce a plan |

**Protected paths** (Claude Code's docs, permission-modes "Protected paths";
kiln: `internal/claude/permission/protected.go`). A write to `.git/`,
`.claude/` (not `.claude/worktrees`), `.vscode/`, `.idea/`, shell startup
files, `.mcp.json`, `.claude.json` and the rest of Claude Code's list, plus
kiln's `.kiln`, `.harness`, `.ssh` and login-autostart directories, is a
safety check that runs after deny rules and before ask rules, allow rules
and `acceptEdits`: `manual` and `acceptEdits` prompt, `dontAsk` and print
mode refuse, `auto` classifies, `bypassPermissions` allows. It covers the
file tools and every file a bash command is seen to write, compared without
case, through symlinks and in the OS's own spelling. Done; details and the
parts not mirrored are in `configuration.md` "Protected paths".

Not every mode needs to ship in v1, but the **names must match** — muscle memory
and `.claude/settings.json` files both depend on them. `plan` and `acceptEdits`
are the two that carry most of the daily value.

Related, also confirmed from `--help`:

- **Effort levels** are `low | medium | high | xhigh | max` (`--effort`). Maps to
  pi's `ThinkingLevel`, which kiln already threads through
  `AgentHarnessOptions.thinkingLevel`.
- **Settings sources** are `user`, `project`, `local` (`--setting-sources`),
  confirming the three-tier hierarchy Phase 4 must merge.
- **Session names** are "shown in the prompt box, /resume picker, and terminal
  title" — so the input box renders a name when one is set, and kiln
  should set the terminal title too.
- **`--ax-screen-reader`** renders "flat text, no decorative borders or
  animations". Worth mirroring: the whole TUI must degrade to plain text, which
  is also what makes it usable over a poor SSH link.

## 8. Permission prompts

Exact framing/colours are kiln's own — an amber-ruled "perm" block with
"Allow kiln to …?" copy — see `kiln-design.md`'s "perm" block anatomy.

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
`kiln -c`.

Worth implementing, in rough priority order:

All parsed by `internal/cli/args.go`, which accepts both `--flag value` and
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
| `--output-format` | done, all three; `stream-json` is NDJSON emitted as events happen. The `json` format and the terminal `stream-json` `result` event both carry Claude Code's enriched result fields: `usage` (`input`/`output`/`cache_read`/`cache_write`), `total_cost_usd`, `duration_ms`, `num_turns`, `num_tool_calls`, and `reason` (omitted unless the run was cut short, e.g. `max-turns-exceeded`) |
| `--max-turns <n>` | done, print mode (`-p`) only; caps the run at `n` assistant turns. A run that finishes exactly at turn `n` with no further tool call is not cut off — only a run that would start turn `n+1` is cancelled, ending with `ok:false` and `reason:"max-turns-exceeded"` (exit code 1). Not wired into the interactive TUI: a human there can just stop typing |
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
`auth` (manage credentials — kiln already has `login`/`logout`).

Out of scope, Anthropic-infrastructure specific: `gateway`, `install`,
`update`, `ultrareview`, `setup-token`, `--from-pr`, `--remote-control`,
`--chrome`, `--betas`.

Notable for the budget work: `--max-budget-usd` caps spend per run. kiln
equivalent is the tier system, but a dollar cap still makes sense once a paid
provider is in use — local models are free, subscriptions are not metered per
call, but API keys are.

## Deliberate divergences

Seven; the first six are additive:

1. **Budget footer** (§7) — required by the 32k local case.
2. **Model picker spans providers** — `/model` must offer local Ollama models
   beside Claude, which Claude Code has no reason to do.
3. **`/tools`** — inspect which MCP tools are currently resident. Claude Code
   has no equivalent because it has no posture/index layering.
4. **Model roles + `task`'s `model` argument** — `settings.json`'s
   `modelRoles` (fast/structured/heavy → provider/model) names providers and
   models once; a `task` call names a role instead of a literal model, or
   `inherit`. Claude Code's `subagent_type` selection has no such role
   indirection `[chk]`.
5. **Concurrent `task` calls in one turn** — several `task` invocations in
   one assistant message run in parallel, each getting its own session and
   storage. Claude Code dispatches subagents from one turn serially `[chk]`.
6. **Nested dispatch, depth 2** — a subagent's own `task` tool can dispatch
   subagents of its own, up to two levels deep from the root session before
   the tool stops being offered at all. Claude Code's subagents cannot
   dispatch subagents of their own `[chk]`.
7. **kiln reads `.claude`, writes `.kiln`** — kiln reads Claude Code's
   settings, memory and rules exactly as Claude Code does, but never writes
   them. What kiln saves for itself goes to `<repo>/.kiln/settings.local.json`
   (approved rules, `/permissions` edits) and `~/.kiln/settings.json` (`/model`
   default). Each kiln file joins its Claude Code scope and wins for single
   values; deny rules win everywhere. A git-tracked or symlinked
   `.kiln/settings.local.json` is held until the folder is trusted.

   MCP is the deliberate exception: kiln has no MCP config file of its own.
   `kiln mcp add`/`add-json`/`remove` write the same files `claude mcp add`
   does (`~/.claude.json` for `local`/`user` scope, `.mcp.json` for
   `project` scope), so a server either tool adds is one set, usable by
   both - not a kiln-owned mirror to keep in sync.

   kiln also has no writable memory or auto-memory store of its own: it
   reads `CLAUDE.md` (user and project) and Claude Code's own
   auto-memory directory read-only, and has dropped its earlier `#`
   memory-note input mode and `~/.kiln/CLAUDE.md` file (see §2) rather than
   invent a CLAUDE.md of its own. Open item: a kiln-native, writable memory
   store (auto or otherwise) does not exist yet.

## Explicit non-goals

- Anthropic-account features (`/bug`, `/upgrade`, billing).
- Pixel-matching Claude Code's look at all. kiln has its own visual design
  (`kiln-design.md`) — this file tracks *feature* parity, not rendering
  parity, and re-verifying `[chk]` items is about behavior, not appearance.


## Bash sandbox

The `sandbox` settings object, as Claude Code documents it in
[Sandboxing](https://code.claude.com/docs/en/sandboxing) and the
[settings reference](https://code.claude.com/docs/en/settings-reference#sandbox-settings).
Code: `internal/claude/settings/sandbox.go` (parsing and merge),
`internal/sandbox` (mechanism, proxy), `internal/claude/permission/sandbox.go`
(gate), `internal/cli/sandbox.go` (startup, doctor).

| Behavior | Status | Source / test |
|---|---|---|
| `sandbox.enabled` turns it on; off by default | done | settings reference; `TestLoadSettingsSandboxDefaults` |
| Booleans: later scope wins; arrays combine across scopes; unknown keys ignored, wrong types skipped with a warning | done | settings reference "Sandbox settings"; `TestLoadSettingsSandboxMerge`, `TestLoadSettingsSandboxBadTypes` |
| Path prefixes: `/` and `//` absolute, `~/` home, `./` or none = project root (project settings) or the settings file's directory (user settings); trailing `/` and `/**` stripped | done | settings reference "Sandbox path prefixes" |
| `filesystem.disabled`, `allowAppleEvents`, `network.strictAllowlist`, credential `mask` entries honoured only from user settings and `--settings` | done | settings reference; `TestLoadSettingsSandboxTrustedOnlyKeys` |
| `allowUnsandboxedCommands: false` in user settings holds against a project's `true` | done | settings reference; `TestLoadSettingsSandboxUnsandboxedHold` |
| macOS: Seatbelt (`sandbox-exec`); Linux: bubblewrap + socat; Windows: unsupported | done | sandboxing "OS-level enforcement"; `TestRealSandbox*` (macOS, and Linux under bwrap in a container), `TestManagerDetect` |
| Writes: working directory, added directories (`/add-dir`, `--add-dir`), a per-user temp dir (`$TMPDIR` set to it), `allowWrite`, `Edit(...)` allow rules; minus `denyWrite` and `Edit(...)` deny rules | done | sandboxing "Filesystem isolation"; `TestRealSandboxWrites` |
| Protected paths stay unwritable inside writable roots: `.claude` settings/skills/agents/commands/hooks/workflows and `.mcp.json` in the cwd and its parents; shell startup files, `.gitconfig`, `.vscode`, `.idea`, `.git/hooks`, `.git/config` in the cwd; bare-repo files; `~/.claude`, `~/.claude.json`. kiln adds: `.kiln`, `~/.kiln`, `~/.harness`; the same `.claude` entries, `.mcp.json` and `.git` entries in nested directories under every writable root (macOS; Linux holds the ones that exist); and, when home or a directory holding it is writable, the shell startup files, `~/Library/LaunchAgents`, `~/.config/autostart` and `~/.config/systemd` | done | sandboxing "Protected paths"; `TestRealSandboxDefaultsHomeAndGit`, `TestProtectedPaths`, `TestRealSandboxNestedProjectConfig`, `TestRealSandboxHomeAsRoot` |
| Git directories in writable roots: only what git writes (objects, refs, logs, the index, `*_HEAD` and message files, rebase/sequencer state, `packed-refs`, `shallow`, gc files and their locks) may be written, so `commondir`, `gitdir`, `info/attributes`, `config*` and `hooks` cannot be planted or redirected (macOS, by pattern, including module and worktree dirs created later; Linux: existing entries bound read-only, new ones removed when the command ends). Commit, checkout, stash, rebase, fetch and gc work; adding a submodule or worktree does not | done (kiln; Claude Code protects `.git/hooks` and `.git/config`) | `TestRealSandboxGitDirRedirect`, `TestRealSandboxGitWorkflow`, `TestRealSandboxSubmoduleGitDir` |
| `git init` in a workspace root without a `.git`: allowed, and `git init && git add && git commit` works in one command. The new directory may hold only what git writes plus git init's own files (macOS profile); after the command kiln removes non-sample hooks, redirect files and a `.git` file or link, and keeps only git init's, identity, remote and branch keys in its config; from the next command on its config and hooks are unwritable; it is cleaned again after every later command and on exit (a background process can outlive its command) | done (kiln) | `TestRealSandboxGitInit` |
| Tool caches: cache variables (`GOCACHE`, `GOMODCACHE`, `npm_config_cache`, pnpm's store, Yarn, Bun, Deno, pip, uv, Poetry, pre-commit, Composer, ccache, `XDG_CACHE_HOME`; `GOTMPDIR` to `$TMPDIR`) point at `<temp root>/cache/<tool>` unless the user set them (environment, settings `env`, Go's env file); the real cache directories stay unwritable | done (divergence: Claude Code leaves tool caches unwritable) | `TestRealSandboxToolCaches`, `TestCacheEnvRespectsUserSettings` |
| kiln's own `git status` (status line) runs inside the sandbox with `--no-optional-locks`, `core.fsmonitor=false` and `core.hooksPath=/dev/null`, so a repository a sandboxed command wrote cannot run code through it | done (kiln) | `TestKilnGitRunsInSandbox`, `TestReadGitStatusIgnoresFsmonitor` |
| A repository's settings (`.claude/settings.json`, `.claude/settings.local.json`) cannot switch the sandbox off over a user `enabled: true`, nor widen it: catch-all `excludedCommands`, `allowWrite` or `Edit(...)` allows covering home, `allowedDomains: ["*"]` or `WebFetch(domain:*)`, `allowUnixSockets`, `allowMachLookup: ["*"]`, `allowAllUnixSockets`/`allowLocalBinding: true` and proxy ports are ignored from them with a startup warning | done (kiln is stricter than the documented merge) | settings reference; `TestLoadSettingsSandboxRepositoryCannotWiden`, `TestSandboxRulesFromRepositoryDoNotWiden` |
| A linked worktree may write the shared `.git` dir except its `hooks` and `config` | done | sandboxing "Filesystem isolation"; `TestWorktreeGitDirs` |
| Reads: everything except `denyRead`, `Read(...)` deny rules and `credentials.files`; `allowRead` re-opens a narrower path; the narrower rule wins | done | sandboxing "Configure sandboxing"; `TestRealSandboxReadRules` |
| Symlinks cannot widen access (the kernel-resolved path is judged) | done | `TestRealSandboxSymlinkEscape` |
| Network: no direct route out; a local proxy checks each host against `allowedDomains` / `deniedDomains` (plus `WebFetch(domain:...)` rules); `HTTP(S)_PROXY` and `ALL_PROXY` set; `NO_PROXY` removed, since a sandboxed command has no direct route to loopback either | done | sandboxing "Network isolation"; `TestRealSandboxNetwork`, `TestProxyDecisions` |
| The proxy serves only the session: each session's proxy has a random credential carried in the proxy URLs it hands sandboxed commands; a request without it gets 407 before any decision or prompt. curl, git, npm, pip and Go programs send it | done (kiln) | `TestProxyRequiresCredential`, `TestRealSandboxProxyClients` |
| Domain syntax: `*.x` subdomains, bare `*`, `:port`, bracketed IPv6, trailing dot; ambiguous IPv6 read strictly | done | settings reference `allowedDomains`; `TestHostRules` |
| Hosts outside the lists: bypass allows, manual/acceptEdits/plan ask, auto/dontAsk refuse, print mode refuses; `strictAllowlist` always refuses; "Yes" lasts the session, "don't ask again" saves `WebFetch(domain:host)` (to `.kiln/settings.local.json`) | done | sandboxing "Hosts outside your allowed domains"; `TestApproveNetwork` |
| A hostname resolving only to local addresses is refused unless the IP is allowlisted; `localhost`/`*.localhost` may resolve to loopback | done | sandboxing "Hostnames that resolve to local addresses"; `TestProxyLocalAddressCheck` |
| Local targets (loopback, private, link-local and unspecified addresses, IP literals and `localhost` alike) need an exact `allowedDomains` entry; `*`, `*.x` and bypass-mode approval never reach them | done (kiln is stricter) | `TestProxyLocalTargets` |
| Hosts are compared in one form: lower case, no trailing dot, IPv4-mapped IPv6 unmapped, internationalised names in punycode (entries and requests) | done | `TestHostCanonicalForms` |
| Deny and ask rules see the command behind git global options (`git -C dir -c k=v push`) and package runners (`npx`, `npm exec`, `pnpm dlx`, `yarn dlx`, `bunx`, `uvx`, `pipx run`, `uv run`, `poetry run`, `bundle exec`); a command whose name the gate cannot read (`$x`, `$(...)`) is not auto-allowed | done | `TestDenyRuleSeesGitGlobalOptions`, `TestSandboxAutoAllowHiddenNames` |
| `autoAllowBashIfSandboxed` (default true): sandboxed commands run without a prompt; deny rules, content ask rules and critical `rm`/`rmdir` targets still apply; a bare `Bash` ask rule is skipped except in plan mode; plan mode does not widen. A command that writes a protected path kiln can name (`permission/protected.go`, the same list as for file tools, wider than the sandbox's own, which must leave git and builds working) is not auto-allowed: it asks in manual and acceptEdits, is refused where nobody can be asked, and in auto mode is classified. Otherwise, in auto mode it is approved before the classifier, which reviews commands outside the sandbox (excluded ones, unsandboxed retries) and, as for any allowed command, one whose writes kiln cannot name or that changes git's configuration | done | sandboxing "Sandbox modes", permission-modes "How the classifier evaluates actions"; `TestSandboxAutoAllow*`, `TestSandboxAutoAllowSkipsClassifierInAutoMode`, `TestSandboxAutoAllowYieldsToProtectedPaths`, e2e `TestSandbox_EscapeFailsAndTranscriptSaysSo` |
| `excludedCommands`: Bash-rule syntax, every command in the call must match, the text is matched, sudo/eval/xargs/cd/substitutions/subshells/control flow/redirects/variable names/escaping `git clone` stay sandboxed; excluded commands take the regular flow | done | settings reference `excludedCommands`; `TestExcluded` |
| `dangerouslyDisableSandbox` retry (offered only when `allowUnsandboxedCommands` is true): regular flow, prompt marked "runs outside the sandbox"; bypass runs it; dontAsk refuses it unless an allow rule matches; `Bash(dangerouslyDisableSandbox:true)` ask rule prompts in every mode | done | sandboxing "The unsandboxed retry escape hatch"; `TestSandboxUnsandboxedRetry` |
| A failed sandboxed command's result names the blocked hosts and how to retry | done | sandboxing "The unsandboxed retry escape hatch"; e2e test |
| `failIfUnavailable`: refuse to start; otherwise run unsandboxed with one startup warning | done | settings reference `failIfUnavailable`; `TestRun_Sandbox_Unavailable` |
| `credentials.envVars` deny entries unset in sandboxed commands | done | sandboxing "Protect credentials"; `TestRealSandboxDenyEnv` |
| `enableWeakerNestedSandbox`, `enableWeakerNetworkIsolation`, `allowLocalBinding`, `allowUnixSockets`, `allowAllUnixSockets`, `allowMachLookup`, `httpProxyPort`, `socksProxyPort` | done | settings reference; `TestSeatbeltProfileShape`, `TestBwrapArgs` |
| `allowLocalBinding` (macOS): commands may listen and accept connections on local addresses. Divergence: it opens no direct outbound route to loopback ports (Claude Code's docs say it lets commands connect to any localhost port); a local service is reached through the proxy with an exact entry. As with Claude Code, a listener on `0.0.0.0` accepts connections from other machines: Seatbelt's `local ip` filter accepts only `*` or `localhost` and `localhost` matches every local address | done (divergence) | sandboxing "A command fails to reach a server on localhost"; `TestRealSandboxLocalBinding` |
| `allowUnixSockets` entries open each socket, under each spelling of its path | done | `TestRealSandboxHidesAgentSockets` |
| Linux: without a seccomp filter kiln hides well-known Unix sockets instead (session and system bus, systemd user socket, docker/podman/containerd, gpg agent, `$SSH_AUTH_SOCK`, `$DOCKER_HOST`) by binding `/dev/null` over them, unless `allowAllUnixSockets` or an `allowUnixSockets` entry opens them | done (partial: other sockets stay reachable) | sandboxing "Set up Linux and WSL2"; `TestHiddenSockets`, `TestRealSandboxHidesAgentSockets` |
| macOS runs `/usr/bin/sandbox-exec` only; `PATH` is never searched | done | `TestDetectUsesSystemSandboxExec` |
| A process a sandboxed command leaves running (`nohup … & disown`) is not killed when the command returns, as Claude Code leaves it; it stays in the sandbox and keeps the proxy (and its allowlist and prompts) until kiln exits. On Linux it ends with bwrap's PID namespace | done | tools reference "Background commands"; `TestRealSandboxLeftoverStaysConfined` |
| Outside the sandbox: file tools, hooks, MCP servers, the status line, `!` commands | done (checked: they never go through `execenv.Sandbox`) | sandboxing "What runs outside the sandbox" |
| Subagents use the parent's sandbox | done (shared `execenv.Env` and gate) | sandboxing "Scope" |
| `kiln doctor` / `/doctor` show status, mechanism, mode, or why it cannot run | done | `TestDoctor_Sandbox` |

Open, not matched yet:

- **SOCKS proxy.** kiln runs an HTTP/CONNECT proxy only; `ALL_PROXY` points at it.
  Tools that need SOCKS (git over SSH on Linux) cannot connect.
- **TLS termination and credential masking** (`network.tlsTerminate`, `mask`
  entries, `awsPairs`, `sigv4`). A `mask` entry is enforced as `deny`, the way
  Claude Code treats mask files on macOS.
- **Upstream corporate proxy.** kiln's proxy dials hosts directly; it does not
  chain to `HTTPS_PROXY`.
- **Per-command allowed domains in auto mode** and server-side classifier
  review of sandboxed commands. In auto mode an unlisted host is refused.
- **Managed settings and their locks** (`allowManagedDomainsOnly`,
  `allowManagedReadPathsOnly`, admin-required repository locks, `bwrapPath`,
  `socatPath`): kiln has no managed tier.
- **`permissions.blockReadsOutsideWorkingDirectories`** and
  `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB`.
- **Violation reporting.** kiln does not monitor the kernel's violation log;
  a failure is annotated when its output looks like a sandbox refusal.
  `ignoreViolations` is accepted and has no effect.
- **Linux:** a protected path that does not exist yet is held by an empty
  placeholder, and a new entry in a git directory is removed when the command
  ends; nested `.claude`/`.mcp.json` entries that do not exist are not held
  (macOS holds them by pattern); wildcard `denyRead`/`allowRead` entries are
  skipped, not expanded; no seccomp filter, so only the well-known sockets
  above are hidden (doctor says so).
- **Submodules and worktrees inside the sandbox.** `git submodule update --init`
  and `git worktree add` write a `config` or `gitdir` file into a new git
  directory, which the sandbox refuses; run them outside it. `git init` in a
  subdirectory of a workspace root is refused on macOS (only a root's own `.git`
  may be created); on Linux it succeeds and that repository is not cleaned, as
  Linux does not hold nested git directories.
- **Live reload.** Sandbox settings and rules added mid-session (an `Edit`
  allow from "don't ask again") apply from the next start.
- **`/sandbox`** panel; `bash_background` has no `dangerouslyDisableSandbox`
  (an `excludedCommands` entry is how a background command leaves the sandbox).
- **macOS limits of Seatbelt itself:** setuid binaries (`ps`, `sudo`) cannot run
  inside it, and `mktemp` without a template uses the system temp directory,
  which is outside the sandbox; `mktemp "$TMPDIR/x.XXXXXX"` works.

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
| `Stop` | `[obs]` fires at run end (`internal/cli/chat.go:684`) — `TestHooks_Stop_FiresOnRunEnd`, `TestHooks_StopBlock_ReportedOnceNotReprompted` |
| `SubagentStop` | `[obs]` fires once per dispatched subagent (`internal/cli/chat.go:718`) — `TestHooks_SubagentStop_FiresPerSubagent`, `TestHooks_SubagentStop_PerDepth` |
| `Notification` | `[obs]` fires on the TUI's permission prompt (`internal/cli/tui.go:110`) — `TestHooks_Notification_Payload` |
| `PreCompact` | `[obs]` fires when compaction starts (`internal/cli/chat.go:700`); covered by `TestCompaction_PreCompactHookFires` in `test/e2e/compaction_behaviour_test.go` |

Three stdout shapes are accepted: empty, plain text (becomes context), and JSON
with `hookSpecificOutput`. Plain text is not a fallback — the relay inbox hook
depends on it.

**Ordering:** PreToolUse hooks run *before* the permission gate, so the gate
judges the rewritten command rather than the one the model proposed. See
`guardToolCall`.

**Known upstream quirk:** `rtk-rewrite.sh` stops rewriting whenever a project
`.claude/settings.local.json` exists, even an empty one. That is rtk's own
behavior, not kiln's, and it makes end-to-end tests of rewriting
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
