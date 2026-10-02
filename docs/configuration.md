# Configuration reference

Every entry below was verified against the source at the cited `file:line`.
Where a default could not be confirmed in code, it says so rather than
guessing.

## 1. Command line

`internal/cli/args.go` (`Parse`, `Args`, `Help`), `internal/cli/subcommands.go`, `cmd/kiln/main.go`

### Subcommands

| Command | Handler | Behavior |
|---|---|---|
| `kiln` | `cli.Run` (default) | interactive session, or `-p` for one prompt |
| `kiln providers` | `cli.Providers` | every registered provider, auth kind, configured or not |
| `kiln models [provider]` | `cli.Models` | models, context window, tier, tool strategy, usable-token budget; also a `roles` table if `modelRoles` is configured |
| `kiln login <provider>` | `cli.LoginCmd` | OAuth flow if the provider has one, else prompts for an API key |
| `kiln logout <provider>` | `cli.LogoutCmd` | drops the stored credential |
| `kiln doctor` | `cli.Doctor` | model, tier, roles, MCP (connects for real), tools, hooks, agents, settings, logs, problems |
| `kiln mcp` | `cli.MCP` | connects to every configured server for real, prints connected/failed |
| `kiln session inspect <path>` | `main.sessionCommand` | dumps a session JSONL file's header/stats as JSON |
| `kiln eval run` | `main.evalCommand` → `internal/eval` | runs `eval/scenarios` against the built binary; flags `--kiln-bin`, `--scenarios`, `--fixtures`, `--results`, `--models`, `--roles`, `--repeat`, `-j`, `--keep`, `--judge`, `--only` |
| `kiln eval report` | `main.evalCommand` → `internal/eval` | reports a results file against a baseline; flags `--results`, `--target`, `--baseline`, `--last`, `--format text\|md\|json`, `--fail-on-regression` |

Recognized as a subcommand only when it's the first non-flag token (`internal/cli/args.go`, `commands` map: `providers`, `models`, `login`, `logout`, `doctor`, `mcp`, `session`). `eval` is dispatched in `cmd/kiln/main.go` before `cli.Parse` runs, because its flag set is its own (`cmd/kiln/eval.go`). `--version`/`-v` and `--help`/`-h` are answered before anything else, even ahead of unknown-flag rejection (`cmd/kiln/main.go`).

### Session flags

| Flag | Semantics |
|---|---|
| `-c`, `--continue` | continue the most recent session in this cwd |
| `-r`, `--resume [id]` | resume by id; **with no id**, resumes the latest (`args.go`: `ResumeSet=true`, `ResumeLatest=true` when the flag has no value) |
| `--session-id <uuid>` | use a specific session id |
| `--fork-session` | branch instead of continuing in place |
| `-n`, `--name <name>` | name the session |

### Model flags

| Flag | Semantics |
|---|---|
| `--model <provider/model>` | e.g. `ollama/qwen3.8:latest` |
| `--effort <level>` | one of `low`, `medium`, `high`, `xhigh`, `max` (`args.go`); any other value is silently ignored, leaving `Effort` unset |

### Permission flags

| Flag | Semantics |
|---|---|
| `--permission-mode <mode>` | `manual \| acceptEdits \| auto \| plan \| dontAsk \| bypassPermissions` |
| `--add-dir <dirs>` | repeatable; a single value also splits on whitespace or commas (`args.go`, `wsOrComma` regex) |
| `--allowed-tools "Bash(git *)"` | additive to `settings.json`'s `permissions.allow`, not a replacement (`internal/cli/chat.go`) |
| `--disallowed-tools "Write"` | additive to `permissions.deny`; deny is still checked first in `Decide` (see §5) |
| `--max-turns <n>` | print mode only; caps the run at `n` assistant turns. Must be a positive integer, else `kiln: --max-turns expects a positive integer` and exit 1. Interactive mode ignores it. |

Both tool-list flags accept `Read,Write,Edit` (commas) and `Bash(git *) Edit` (whitespace, with parens tracked so a space-containing rule stays one token) — `cli.SplitToolList` (`args.go`).

### Config flags

| Flag | Semantics |
|---|---|
| `--settings <file>` | extra settings file, read last, highest precedence for scalars (see §5) |
| `--setting-sources <list>` | `user,project,local`; restricts which settings-file scopes are read |
| `--mcp-config <file>` | MCP servers from this file instead of the usual scopes (see §8) |
| `--strict-mcp-config` | use only `--mcp-config`; with no `--mcp-config` given, connects to nothing (see §8) |
| `--system-prompt <text>` | replaces the base prompt (default: `agent.BasePrompt`, `internal/agent/prompt.go`) |
| `--append-system-prompt <text>` | appended after the base/replaced persona |

### Output flags

| Flag | Semantics |
|---|---|
| `-p`, `--print` | non-interactive: run one prompt, print, exit |
| `--output-format <fmt>` | `text \| json \| stream-json`; invalid values are dropped, falling back to `text` (`args.go`, `print.go`) |
| `--verbose` | echoes each tool call (`name(arg)`) to stderr as it starts |
| `--debug` | debug-level run log; path is printed under `-p` (`chat.go`) and always reported by `kiln doctor` |
| `--ax-screen-reader` | flat text, no borders/animation; always inline, never full-screen |
| `--inline` | opt out of the default full-screen (alt-screen) TUI, keeping native scrollback; toggled either way at runtime with `ctrl+f` |
| `--fullscreen` | accepted for compatibility; full-screen is already the default |

`-v`/`--version` and `-h`/`--help` take no value. An unrecognized flag is collected into `Unknown` and the whole run refuses with `unknown flag(s): ...` rather than being silently ignored (`args.go`, `main.go`).

### `-p` / print-mode specifics

`internal/cli/chat.go`, `internal/cli/print.go`

- The prompt is the joined positional args, or stdin if none were given (`readStdin`, `chat.go`) — `git diff | kiln -p "review this"` works.
- A prompt starting with `/` runs as a slash command first; a command with a `Prompt` result feeds that text into the turn (mentions resolved against it, not the original text), a command with no `Prompt` just prints its `Output` and exits without a model turn.
- `@path` mentions are resolved the same as interactive mode, capped at the tier's per-result budget.
- `--output-format json` prints `{ok, text, toolCalls, blocked, usage, total_cost_usd, duration_ms, num_turns, num_tool_calls, reason?}` where `usage` is `{input, output, cache_read, cache_write}`, `duration_ms` is wall-clock for the whole run, `num_turns` counts assistant turns, and `reason` is present only when the run was cut short (`max-turns-exceeded`). `stream-json` streams NDJSON lines of shape `tool_start`/`tool_end`/`assistant`/`result` as they happen and prints nothing at the end; its terminal `result` event carries the same enriched fields. Plain `text` prints the trimmed final assistant text.
- `--max-turns n` cancels the run when the harness is about to start turn `n+1`; a run that ends exactly at turn `n` is not cut off. When it trips: stderr `kiln: stopped after n turns (--max-turns)`, `ok` false, `reason` `max-turns-exceeded`, exit 1.
- `PrintResult.Blocked` combines every `before_tool` refusal (hook or permission gate), not just ones that reached `gate.Check` (`chat.go`).
- print mode has nobody to ask: a permission mode that would otherwise prompt (`ask`) is a refusal instead (README, confirmed by the gate design in §5).

## 2. Environment variables

### Runtime

Source: `grep -rn 'os.Getenv\|os.LookupEnv' --include="*.go" cmd internal | grep -v _test`, read at each site.

| Variable | File:line | Controls | Default |
|---|---|---|---|
| `HARNESS_MODEL` | `internal/cli/chat.go`, `subcommands.go` | model when `--model` and settings' `model` are both unset | falls through to `ollama/qwen3.8:latest` (`chat.go`) |
| `HARNESS_PERMISSION_MODE` | `internal/cli/chat.go` | permission mode, below `--permission-mode`, above `settings.permissions.defaultMode` | `manual` |
| `HARNESS_POSTURE` | `internal/cli/mcp.go` | starting MCP posture for the session | `coding` (also the fallback for an unknown name) |
| `HARNESS_MCP_CONNECT_TIMEOUT` | `internal/mcp/hub.go` | per-server MCP connect + list-tools timeout (`time.ParseDuration`) | 30s; invalid or non-positive falls back to 30s |
| `HARNESS_SESSIONS_DIR` | `internal/agent/session.go`, `internal/cli/chat.go` | root directory for session storage | `~/.harness/sessions` (via `jsonl.NewRepo("")`) |
| `HARNESS_LOG_DIR` | `internal/diag/diag.go` | directory run-logs are written to | `~/.harness/logs` (or `$TMPDIR/harness-logs` if the home dir can't be resolved) |
| `HARNESS_DEBUG` | `internal/diag/diag.go` | forces the run log to Debug level | Info level (same effect as `--debug`) |
| `HARNESS_TRUST_ALL` | `internal/cli/tui.go` | `"1"` skips the folder-trust dialog | dialog shown for an untrusted cwd |
| `KILN_EVAL_LIVE` | `internal/eval/runner.go` | `"1"` lets `kiln eval run` use non-faux models (`eval: live model(s) refused (set KILN_EVAL_LIVE=1 to allow)` otherwise) | refused |
| `OLLAMA_HOST` | `internal/cli/chat.go` | Ollama base URL | checked first |
| `OLLAMA_BASE_URL` | `internal/cli/chat.go` | Ollama base URL fallback | used only if `OLLAMA_HOST` unset |
| `OLLAMA_CONTEXT_LENGTH` | `internal/cli/chat.go` | server-side default `num_ctx` for models that pin none | 0 (unset) |
| `NO_COLOR` | `internal/tui/theme.go` | disables ANSI styling (any value) | checked first |
| `FORCE_COLOR` | `internal/tui/theme.go` | forces color on (any value) | checked after `NO_COLOR` |
| `SSH_TTY`, `SSH_CONNECTION` | `internal/auth/interaction.go` | treat the session as headless for OAuth login (prints URL instead of opening a browser) | falls back to stdout TTY check |
| `PI_OAUTH_CALLBACK_HOST` | `internal/auth/oauth/anthropic.go`, `openrouter.go` | local OAuth callback host | `127.0.0.1` |
| `KIMI_CODE_OAUTH_HOST`, `KIMI_OAUTH_HOST` | `internal/auth/oauth/kimi.go` | OAuth host for Kimi device-flow login | `KIMI_CODE_OAUTH_HOST` > `KIMI_OAUTH_HOST` > `https://auth.kimi.com` |
| `AWS_REGION`, `AWS_DEFAULT_REGION` | `internal/provider/api/bedrock_converse_stream.go` | Bedrock region | model ARN region > override param > `AWS_REGION` > `AWS_DEFAULT_REGION` > `us-east-1` |
| `AWS_BEARER_TOKEN_BEDROCK`, `AWS_PROFILE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` | `bedrock_converse_stream.go` | Bedrock credential chain | stored API key > bearer token > named profile (`~/.aws/credentials`) > access key/secret/session token |
| `GOOGLE_CLOUD_PROJECT`, `GCLOUD_PROJECT` | `internal/provider/api/google_vertex.go` | Vertex project | explicit field > `GOOGLE_CLOUD_PROJECT` > `GCLOUD_PROJECT` |
| `GOOGLE_CLOUD_LOCATION` | `google_vertex.go` | Vertex region | explicit field, then this var |
| `GOOGLE_APPLICATION_CREDENTIALS` | `google_vertex.go` | path to ADC JSON for Vertex auth | explicit `keyFilename` > this var > well-known gcloud ADC path |
| `APPDATA` | `google_vertex.go` | Windows-only relocation of the default ADC path | POSIX `~/.config/gcloud/...` otherwise |
| `TERM`, `TERM_PROGRAM`, `COLORTERM` | `internal/commands/account_commands.go` | terminal-capability diagnostics (e.g. warns on `TERM=dumb`) | `"unknown"`, `"unknown"`, `"not set"` |
| `VISUAL`, `EDITOR` | `internal/commands/session_commands.go` | editor launched to open `CLAUDE.md` from a `/config`-style command | `VISUAL` > `EDITOR`; prints the path if both unset |

`HARNESS_TEST_CLOCK` (`internal/tui/footer.go`) is compiled into the shipped binary (not behind a test build tag) but is inert unless explicitly set — it overrides wall-clock time in the footer for deterministic golden-file tests. Included here for completeness rather than under §test/dev.

### Provider credentials

`internal/provider/catalog/providers.go`, `internal/provider/builtin/builtin.go`

Precedence: **a stored credential (`~/.harness/credentials.json`) always wins over an environment variable** for the same provider — env vars are consulted only when nothing is stored (`builtin.go`, explicit in the source comment: "opposite of 'env wins'"). Within "nothing stored," `envKeys` are tried in order and the first non-empty one is used; for Anthropic, `ANTHROPIC_AUTH_TOKEN` (if that's the one used) is sent as a Bearer token.

| Provider | Env var(s), in order |
|---|---|
| anthropic | `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN`, `ANTHROPIC_API_KEY` |
| openai | `OPENAI_API_KEY` |
| github-copilot | `COPILOT_GITHUB_TOKEN` |
| kimi-coding | `KIMI_API_KEY` |
| openrouter | `OPENROUTER_API_KEY` |
| xai | `XAI_API_KEY` |
| radius | `RADIUS_API_KEY` |
| meta | `META_API_KEY` |
| vercel-ai-gateway | `AI_GATEWAY_API_KEY` |
| cloudflare-workers-ai / cloudflare-ai-gateway | `CLOUDFLARE_API_KEY` |
| opencode / opencode-go | `OPENCODE_API_KEY` |
| google | `GEMINI_API_KEY` |
| google-vertex | `GOOGLE_CLOUD_API_KEY` (plus the GCP vars above for project/region/ADC) |
| azure-openai-responses | `AZURE_OPENAI_API_KEY` |
| mistral | `MISTRAL_API_KEY` |
| deepseek | `DEEPSEEK_API_KEY` |
| groq | `GROQ_API_KEY` |
| cerebras | `CEREBRAS_API_KEY` |
| nvidia | `NVIDIA_API_KEY` |
| huggingface | `HF_TOKEN` |
| fireworks | `FIREWORKS_API_KEY` |
| together | `TOGETHER_API_KEY` |
| baseten | `BASETEN_API_KEY` |
| zai / zai-coding-cn | `ZAI_API_KEY` / `ZAI_CODING_CN_API_KEY` |
| moonshotai(-cn) | `MOONSHOT_API_KEY` |
| minimax(-cn) | `MINIMAX_API_KEY` / `MINIMAX_CN_API_KEY` |
| ant-ling | `ANT_LING_API_KEY` |
| qwen-token-plan(-cn/-individual) | `QWEN_TOKEN_PLAN_API_KEY` / `QWEN_TOKEN_PLAN_CN_API_KEY` |
| xiaomi (+3 variants) | `XIAOMI_API_KEY` / `XIAOMI_TOKEN_PLAN_AMS/CN/SGP_API_KEY` |

Ollama has no credential — it's reached via `OLLAMA_HOST`/`OLLAMA_BASE_URL` above.

### Test / dev only

Not read by the shipped `kiln` binary in normal operation — set by test harnesses, `cmd/faux`, or other dev-only binaries.

| Variable | Purpose |
|---|---|
| `HARNESS_FAUX_ADDR`, `HARNESS_FAUX_API` | address/API-shape of the scripted faux provider used in e2e tests |
| `HARNESS_FAUX_RECORD` | path to record faux-server requests as JSONL (`internal/testkit`, `cmd/faux` only) |
| `HARNESS_TEST_PAYLOAD_FILE` | hook-test payload capture file, injected into a spawned hook subprocess by tests |
| `HARNESS_SEARCH_LIVE` | gates a test that runs against the real `~/.harness/search.db` |
| `HARNESS_INTEROP_*` (`FIXTURE`, `CWD`, `ID`, `CREATED_AT`, `SESSDIR`, `PROJ`) | legacy-session-format interop test fixtures |
| `HARNESS_E2E_LIVE` | gates e2e tests that call a real model over the network |
| `HARNESS_LIVE_MODEL` | which model a live e2e test targets |
| `HARNESS_RECORD_TTY` | referenced only in a comment (`internal/testkit/screen/screen.go`); **not implemented** — no `os.Getenv("HARNESS_RECORD_TTY")` exists in the Go source |
| `UPDATE` | golden-file update mode for screen/style snapshot tests |
| `STUBTUI_CLEAR` | dev/stub TUI toggle, `internal/testkit/stubtui/cmd/stubtui` only |

## 3. Files under `~/.harness`

| Path | Resolved by | Writer | Reader | Format | Env override |
|---|---|---|---|---|---|
| `credentials.json` | `internal/auth/file_store.go` | `FileCredentialStore.save` (atomic temp+rename, mode 0600) | `FileCredentialStore.load` | JSON, `map[providerID]Credential` | none (callers always pass `""`) |
| `trusted.json` | `internal/claude/trust/trust.go` | `Store.save` (atomic, mode 0600) | `Store.load` | JSON, flat `[]string` of trusted absolute dirs | none; `HARNESS_TRUST_ALL=1` skips the check entirely rather than relocating the file |
| `sessions/<dir>/<file>.jsonl` | `internal/session/jsonl/repo.go` (`NewRepo`) | `Storage.Create`/`Commit` | `jsonl.Open`, `Storage.ScanEntries`/etc. | JSONL (header + writes, one JSON object per line) | `HARNESS_SESSIONS_DIR`, applied by callers (`internal/agent/session.go`, `internal/cli/chat.go`), not inside `NewRepo` itself |
| `history` (flat file) | `internal/tui/editor/history.go` | `Append` (`O_APPEND`, mode 0644, best-effort) | `Load`, capped at 500 entries | plain text, one prompt per line, embedded newlines escaped | none |
| `logs/harness-<ts>-<pid>[-<label>].log` | `internal/diag/diag.go` | `slog` text handler | `diag.L()` throughout, `diag.Latest()` for tooling | plain text (`slog.NewTextHandler`) | `HARNESS_LOG_DIR`; `HARNESS_DEBUG=1` raises level, doesn't relocate |
| — retention | `internal/diag/diag.go` (`prune`) | deletes oldest beyond `keepFiles = 30`, current run's file is never a victim | — | — | — |
| `search.db` | `internal/search/sqlite.go` (`DefaultDBPath`) | `Sync`/`indexFile` write `entries` (FTS5) and `indexed_files` tables | `SearchEntries`, `SearchSessions` | SQLite (`modernc.org/sqlite`, WAL) | none |

`search.db`'s `Sync` also reads (not writes) `~/.claude/projects` — Claude Code's own session history — alongside `~/.harness/sessions`, when called with no explicit roots (`internal/search/sqlite.go`).

Not shipped: `~/.harness/plans/<slug>.md` is named only in a doc comment (`internal/tui/permissionview.go`) for a feature with zero call sites — do not treat it as a real path.

## 4. Model selection and effort

`internal/cli/chat.go`, `internal/budget/tier.go`

### Model precedence

1. `--model <provider/model>`
2. `settings.json`'s `model` key, **only if it contains `/`** — a Claude Code alias like `opus[1m]` names nothing on this machine and is skipped (`chat.go`)
3. `HARNESS_MODEL`
4. `defaultModel` = `ollama/qwen3.8:latest` (`chat.go`)

The resolved `provider/model` is split on the first `/` (`splitProviderModel`, `chat.go`) and resolved against the provider registry; a window that's too small for the harness's own floor refuses to start (`budget.ContextTooSmallError`).

### Effort

Thinking effort comes from `--effort`, else `settings.json`'s `effortLevel` for Claude models (not for other providers), else it is unset. Unset leaves the model's default: Claude models with adaptive thinking decide how much to think. The level a run asks for also applies to a resumed session. Valid values: `low`, `medium`, `high`, `xhigh`, `max` (`internal/cli/args.go`); anything else is dropped by the parser.

### Tiers (`internal/budget/tier.go`)

The model's context window is the only input. A tier is a step function on name, but every budget inside it scales proportionally with the window (deliberately not a fixed table per tier name — see the file's own comment on why absolute per-tier budgets produced a smaller usable window on a bigger model).

| Context window | Tier name | Tool strategy |
|---|---|---|
| ≤ 32,768 | small | derived from `StrategyForWindow` (see below) |
| ≤ 131,072 | medium | derived |
| > 131,072 | large | derived |

Tool strategy is chosen independently of the tier name: the most generous strategy whose measured token cost fits 20% of the window (`toolBudgetShare = 0.2`, `StrategyForWindow`), tried in order `full-schemas` (31,897 tokens) → `full-index` (7,597) → `posture-index` (1,286, always fits).

Per-window budgets (all proportional, with floors/ceilings): system prompt 10% of window (floor 2,048, ceiling 32,768), reserve 10% (floor 2,048, ceiling 32,768), keep-recent 25% (floor 4,096, ceiling 100,000), tool output 12% (floor 2,048, ceiling 49,152). `UsableTokens` = window − system prompt − tool-strategy cost − reserve; a configuration below `MinUsableTokens = 4,096` refuses to start (`RequireTierForWindow`).

## 5. `settings.json`

`internal/claude/settings/settings.go`, `internal/claude/paths/paths.go`, `internal/claude/permission/permission.go`, `internal/claude/hooks/hooks.go`, `internal/claude/hooks/runner.go`, `internal/claude/writesettings`

### Locations and merge order

`paths.AllSettingsFiles(cwd)` (`paths.go`), read least- to most-specific, later wins for scalars:

1. `~/.claude/settings.json` (user)
2. `~/.kiln/settings.json` (user, kiln's own: the `/model` default)
3. `<cwd>/.claude/settings.json` (project)
4. `<cwd>/.claude/settings.local.json` (local — gitignored machine overrides)
5. `<cwd>/.kiln/settings.local.json` (local, kiln's own: rules from "don't ask again" and `/permissions`; highest precedence)

kiln reads Claude Code's `.claude` files exactly as Claude Code does and never writes them; everything it saves goes to the two `.kiln` files, which have the same JSON shape. Each kiln file sits right after Claude Code's file of the same scope: its rules are that scope's (a `/path` rule in `~/.kiln/settings.json` is anchored at `~/.kiln`, one in `.kiln/settings.local.json` at the primary working directory) and its scalars win over that file's. Deny still beats allow across every file. A `<cwd>/.kiln/settings.local.json` that came with the repository (tracked in git, or reached through a symlinked `.kiln`) is held until the folder is trusted: only its deny and ask rules apply, and its allow rules are added once the trust dialog is accepted (`Settings.HeldAllow`). `--settings <file>` adds a further file, read last, tagged as local scope — so it wins over all of them for scalars and is unioned in for list fields. `--setting-sources user,project,local` restricts which scopes are read at all (kiln's files included). Malformed JSON at a scope: warning to stderr, that scope skipped, load continues; a missing file is silently skipped.

### Keys

| Key (JSON) | Go field | Merge across scopes |
|---|---|---|
| `permissions.allow` | `[]string` | **unioned** (appended) across scopes |
| `permissions.deny` | `[]string` | unioned |
| `permissions.ask` | `[]string` | unioned |
| `permissions.defaultMode` | `PermissionMode` | last non-empty wins |
| `model` | `string` | last non-empty wins |
| `effortLevel` | `string` | last non-empty wins (parsed, not currently wired to effort resolution — see §4) |
| `modelRoles` | `map[string]string` | shallow-merged per key; a scope overrides only the role names it sets |
| `env` | `map[string]string` | shallow-merged per key, unconditionally (no empty check) |
| `statusLine.type`, `statusLine.command`, `statusLine.padding` | `*StatusLineConfig` | replaced wholesale, only when the later scope's `command` is non-empty |
| `autoMemoryEnabled` | `*bool` | last non-nil wins (§7's "Auto memory") |
| `autoMemoryDirectory` | `string` | last non-empty wins; absolute or `~/`-prefixed (§7's "Auto memory") |
| `autoMode.environment`, `autoMode.allow`, `autoMode.soft_deny`, `autoMode.hard_deny` | `AutoModeConfig` (`settings/automode.go`, `LoadAutoMode`) | read **only** from `~/.claude/settings.json`, `~/.kiln/settings.json` and `--settings`, concatenated in that order; ignored in project and local settings, with a startup warning (see "Auto mode classifier") |

### Permission rule syntax (`MatchesRule`, `settings.go`)

- Bare tool name (no parens): case-insensitive exact match, e.g. `Read` matches any `Read` call regardless of argument.
- Bare `mcp__` prefix: matches every tool whose name starts with it, e.g. `mcp__homelab-kb` matches every tool from that server.
- Paren form `Tool(pattern)`: tool name must match exactly (case-insensitive); `pattern` is glob-compiled (`*` → `.*`, everything else escaped) and anchored against the tool's primary argument. A `Tool(cmd:*)` colon suffix is rewritten to `Tool(cmd *)` first. A pattern ending in a wildcard also matches the bare command with no arguments (e.g. `Bash(git *)` matches both `git status` and bare `git`).
- Examples: `Bash(git *)` matches `git status`, `git commit -m x`, and bare `git`. `mcp__homelab-kb` matches `mcp__homelab-kb__hk_search` and every other tool from that server.
- A malformed pattern fails closed (never matches) rather than erroring.
- A bare `Edit` rule covers every edit tool (`edit`, `write`, …) and a bare `Read` every read tool; any other bare rule (`Write`) matches its own tool only.

### Bash commands (`bash_parse.go`, `bash_rules.go`, `readonly_bash.go`)

A `bash` or `bash_background` command line is parsed with a real bash parser (`mvdan.cc/sh/v3/syntax`, bash dialect), never split by hand, following Claude Code's "Bash" section ([permissions docs](https://code.claude.com/docs/en/permissions)). `Bash(…)` rules govern both tools.

- **Commands**: every simple command anywhere in the line is collected — in a list or pipeline (`&&`, `||`, `;`, `|`, `|&`, `&`, newlines), a subshell or `{ }` group, an `if`/`while`/`until`/`for`/`case` body or condition, a function body, `$(…)`, backticks, `<(…)`/`>(…)`, a substitution inside a parameter expansion or `[[ ]]`, and the substitutions in an unquoted heredoc. A heredoc or herestring fed to a shell (`bash`, `sh -s`, `bash -`, `/dev/stdin`, `/dev/fd/0`, `source /dev/stdin`, …), a `bash -c`/`sh -c` string and `eval`'s arguments are parsed in turn (8 levels deep at most). Brace expansion is applied first, as bash does (`{rm,-rf} x` is `rm -rf x`). Comments are comments: a `#` starts one only at a word start, as the parser reads it, and a backslash before a comment's newline continues nothing (kiln corrects the parser's reading of that case).
- **Unknown**: a command word that is an expansion (`$CMD`), a script read from a pipe or the terminal, `bash <(…)`, a shell run by `xargs`, a non-literal `eval`/`sh -c` string, nesting past the limit, and what bash 3.2 (macOS's `/bin/bash`, which kiln runs) reads differently from the parser or evaluates as code: any arithmetic (`$((…))`, `$[…]`, `((…))`, `let`, `for ((…))`, `[[ a -eq b ]]`), an array subscript in an expansion, assignment or literal argument (`unset 'a[$(cmd)]'` runs `cmd`), a `case` inside a substitution, and an expansion spanning lines in an unquoted heredoc. **Unparseable**: a syntax error, a dangling `&&`/`||`, an unclosed quote, substitution or heredoc, a carriage return anywhere (bash reads it as a word byte, the parser as a blank), or a line over 10,000 characters.
- **Deny and ask rules** apply when they match any collected command — as written, with every leading assignment and every wrapper stripped (`timeout`, `time`, `nice`, `nohup`, `stdbuf`, `command`, `builtin`, `noglob`, `xargs`, `sudo`, `doas`, `env`, `exec`, `watch`, `setsid`, `ionice`, `flock`, `rtk`, with their options), by its base name (`/bin/rm`), and the command `find -exec` runs — or the whole line, or a line of a heredoc body. On an unparseable line they match each piece between separators that parses. When the line is unparseable or runs something unknown while a Bash or Read/Edit deny or ask rule exists, the call asks at least (`Hits.Unsure`).
- **Allow rules** approve a line only when it parses, nothing in it is unknown, and every command is covered: matched by an allow rule, or read-only and naming no path outside the working directory, with at least one matched by a rule. An allow rule sees the command with Claude Code's safe wrappers stripped (`timeout`, `time`, `nice`, `nohup`, `stdbuf`, `command` except `command -v`, `builtin`, `noglob`, and a bare `xargs`) and leading assignments of safe variables with plain values (`NODE_ENV`, `RUST_LOG`, `RUST_BACKTRACE`, `GOOS`, `GOARCH`, `CGO_ENABLED`, `GO111MODULE`, `GOEXPERIMENT`, `PYTHONUNBUFFERED`, `PYTHONDONTWRITEBYTECODE`, `LANG`, `LANGUAGE`, `LC_ALL`, `LC_CTYPE`, `LC_TIME`, `TZ`, `TERM`, `COLORTERM`, `NO_COLOR`, `FORCE_COLOR`, `CLICOLOR`); any other assignment stops it. An exec wrapper (`watch`, `setsid`, `ionice`, `flock`, `sudo`, `doas`, `pkexec`, `env`, `exec`, `chroot`, `unshare`, `nsenter`) and `find` with `-exec`/`-execdir`/`-ok`/`-okdir`/`-delete` are approved only by an exact rule (no `*`). A bare `Bash` or `Bash(*)` approves any line that parses. A rule is tried on the words with quotes removed and as written, both stripped and as given (`Bash(timeout 3 pnpm start:*)` matches `timeout 3 pnpm start`; a command run by `xargs` is never matched as `xargs …`). No allow rule approves a line with an output redirect (`>`, `>>`, `&>`, `>|`, `<>`, `>&file`) to `~`, outside the working directory, or to a path that expands: Claude Code's "a rule … allows the command, not the target".
- **Read-only** (`IsReadOnlyCommand`): every command is in kiln's read-only set used read-only (`readOnlyPrograms`; options that write a file or run a program are refused in every spelling getopt accepts — clustered, with an attached value, or an abbreviated long option: `printf -v`, `sort -o`/`--compress-program`, `file -C`/`-m`/`-f`, `tree -o` and `-R` with `-H`, `date -s`, `rg --pre`/`--hostname-bin`, `git grep -O`, git `--output`/`--ext-diff`/`--textconv`/`--filters`/`--exec`, `go … -toolexec`/`-exec`), every word literal, no redirection that writes except to `/dev/null`, and no substitution, assignment, subshell, group, background job or heredoc. As in Claude Code, an unquoted glob given to `find`, `sort`, `sed` or `git` is not read-only, nor `git` after a `cd` to another directory (a `cd` to the working directory itself is a no-op), nor a relative redirect target after a `cd`, nor an assignment to `PATH`, `IFS` and the like.

### Read and Edit path rules (`pathrules.go`, `settings.go`)

`Read(path)` and `Edit(path)` follow Claude Code's "Read and Edit" and "Symlinks" rules ([permissions docs](https://code.claude.com/docs/en/permissions)). They are never compared with the raw argument text.

- **The path argument** is resolved the way the tool resolves it (`execenv.ResolveToolPath`: `@` prefix, `~`, `file://`, relative to the primary working directory, cleaned).
- **Anchors**: `//p` absolute; `~/p` under `$HOME`; `/p` under the rule's settings source (project and local settings, `--allowed-tools`/`--disallowed-tools` and session rules: the primary working directory; user settings: `~/.claude`; `--settings <file>`: that file's directory); `p` or `./p` under the current directory. kiln does not read `CLAUDE_CONFIG_DIR`, so user settings are always `~/.claude/settings.json`. Each rule keeps its source through the merge (`Permissions.AllowFrom`/`DenyFrom`/`AskFrom`, parallel to the lists; a missing entry means a CLI or session rule).
- **gitignore patterns**: `*` within a segment, `**` across. A relative pattern with no slash (`.env`, `*.key`) matches at any depth under the current directory. A relative single-directory pattern (`secrets/**`) matches at any depth in a deny or ask rule, but only at `<cwd>/secrets` in an allow rule. Every other shape is anchored. A pattern matching a directory covers what is under it. `./name` in an allow rule stays at `<cwd>/name` (Claude Code's example: `Read(./.env)` is "the .env file in the current directory"); in deny/ask kiln takes the wider reading. A pattern with only a trailing slash (`Edit(src/)`) is read as gitignore reads it: a `src` directory at any depth, in allow rules too.
- **`!` carve-outs** (deny and ask lists): carve out of the relative rules listed before them from the same settings file only, read relative to the current directory, and cannot reopen a file inside a blocked directory.
- **Symlinks**: a deny or ask rule applies if the requested path or its resolved target matches; an allow rule needs both. Paths resolve component by component the way the kernel walks them (`execenv.RealPath`): a link's target is walked as written and a `..` in it steps to the real parent of what resolved so far (so `evil -> sshl/..` with `sshl -> ~/.ssh` is `~`), a dangling link resolves to its missing target, and a file that does not exist yet resolves through its deepest existing ancestor. A rule is also tried at its anchor's real location, so `Read(//etc/**)` covers `/private/etc` on macOS. As in Claude Code, the `edit` and `write` tools refuse a path that is itself a symlink and name its target. The workspace boundary (`WithinRoots`) requires both the path and its resolved target to be inside a root.
- **Case and Unicode**: deny and ask rules compare in Unicode NFC, and on macOS and Windows, whose default filesystems ignore case, with Unicode case folding (`cases.Fold`: `.ENV` opens `.env`, and APFS also equates `ſ`/`s`, `µ`/`μ`, `ς`/`σ`) plus a supplementary table (`apfsFold`) for the letters APFS equates but `cases.Fold` keeps apart (Cherokee small letters, Garay, Cyrillic TJE, the Unicode 16 Latin Extended-D pairs; measured on an APFS volume); allow rules always compare exactly.
- **macOS aliases** (`execenv.CanonicalPath`): besides the path as asked and its symlink-resolved form, every path and rule anchor is also compared in the spelling the kernel reports for it (`fcntl(F_GETPATH)` on the longest existing prefix, memoised per directory for one decision). Only regular files and directories are opened for this (checked with `lstat` first, opened `O_EVTONLY|O_NONBLOCK|O_NOCTTY|O_NOFOLLOW`); a device, FIFO or socket takes its directory's canonical name plus its own. That maps APFS case-folded names, firmlink spellings (`/System/Volumes/Data/Users/x` is `/Users/x`) and `/.vol/<dev>/<inode>` paths to the file's ordinary path. Deny and ask apply if any spelling matches; allow needs all of them. The file tools also refuse `/.vol` paths outright, and a bash operand under `/.vol` counts as unknowable. Off macOS the canonical form is the real path.
- **The read tool's fallbacks**: `read` retries a curly-apostrophe and a narrow-no-break-space spelling when the literal file is missing (`execenv.ReadPathVariants`); every variant is judged, so a rule covers the file it actually opens.
- **Empty paths**: `Read()` in deny or ask is taken as the bare `Read` rule; in allow it approves nothing. Both are warned about at startup.
- **Invalid patterns**: a deny or ask pattern that is not a valid glob (or contains `..`) still guards the exact path it names; an invalid allow pattern approves nothing.
- **Tools**: Edit rules apply to `edit`, `write`, `multi_edit`, `notebook_edit`; Read rules to `read`, `grep`, `glob`, `ls`. A Read deny also blocks `edit`/`write`/`multi_edit` (not `notebook_edit`).
- **`Write(...)`, `MultiEdit(...)`, `NotebookEdit(...)`, `Glob(...)`**: Claude Code accepts these path rules but never consults them, and warns at startup. kiln honours the deny and ask ones as `Edit(...)`/`Read(...)` (never weaker than the rule's evident intent), ignores the allow ones (never broader than Claude Code), and prints a startup warning for each naming the replacement (to stderr in print mode, as a startup note in the TUI; never to the model). A `Glob(...)` rule passed in `--allowed-tools` is not warned about, as in Claude Code.
- **Bash** (`bash_paths.go`, `bash_words.go`, over the parsed command line of the section above): Read and Edit rules also apply to the files a `bash` or `bash_background` command names — a deny rule blocks, an ask rule asks: operands of recognised file commands (`cat`, `head`, `tail`, `sed`, `tee`, `cp`, `grep`, `dd if=/of=`, …) and redirection targets (`> f`, `>> f`, `>| f`, `< f`, `&> f`, `<> f`, `>&f`). Read rules check files read (and Read denies files written); Edit rules files written. Paths are checked both as written and as the shell's open(2) resolves them physically (`sshl/../x` with `sshl` a link). The scan follows `cd`/`pushd` (skipping their options), looks through wrappers and their option values (`sudo -u x`, `env -i A=1`, `env -C dir`, `env -S 'cmd'`, `timeout 5`, `nice -n 5`, `stdbuf`, `nohup`, `time -o f`, `command`, `exec`, `xargs -a f`), follows every command the parser finds (including a shell's heredoc or herestring script, after skipping its value options `-o`/`+o`/`-O`/`+O`/`--rcfile`/`--init-file`), decodes `$'…'` and `$"…"`, expands `~`, `$HOME`, braces (lists and sequences) and unquoted globs (every match, skipping dotfiles unless the pattern starts with `.`, capped at 1000 matches and 200 directory reads per command). When a named file cannot be known — another variable, a substitution used as an operand, `~user`, a relative path after `cd -`/`popd`, an over-budget glob, a command word that is itself an expansion (`$CMD`, `cat${IFS}f`) — the call asks (only ever lifting an Allow) whenever any Read/Edit path rule is in deny or ask. A file a command reads without naming it (`grep -r x .`) or that a script opens itself is not seen.
- **Cost**: path rules are compiled once per rule set and working directory (`compiledFor`) and grouped per tool class; `BenchmarkDecideIn80Deny*` measures a decision with 80 deny rules. Nothing about the filesystem is cached across calls: each decision resolves the requested path and each rule anchor's real location afresh (one resolution per distinct directory), since a directory can be swapped for a link between two calls.

### `Decide` (`settings.go`)

Order: deny rules first (absolute — deny wins even under `bypassPermissions`) → in `plan` mode, edit-family tools (`edit`, `write`, and the MultiEdit/NotebookEdit names) are denied whatever allow or ask rules say (Claude Code: "edits stay blocked until you approve the plan"; a deny rule's refusal still names the rules, not plan mode) → ask rules → `bypassPermissions` mode allows everything else → allow rules → mode default. This is Claude Code's order (permissions and permission-modes docs): a matching ask rule prompts even when a more specific allow rule also matches, and no mode auto-approves a call an explicit ask rule matches, `bypassPermissions` included; a print run has nobody to ask and refuses it. Last, a bash command that names a file kiln cannot resolve while Read/Edit path rules exist (`Hits.Unsure`) turns an Allow into an Ask; it never turns a Deny, plan mode's included, into a prompt. A "don't ask again" grant counts as an allow: the gate (`permission.Gate.CheckWithOutcome`) honours it only when no deny or ask rule matches the call, so a deny or ask rule added after the grant still applies; nor in `plan` mode for an edit (`settings.PlanOverridesAllow`). The prompt offers it only when it would be honoured (`permission.Request.Grantable`). For bash it saves allow rules, one per command still needing approval (`settings.BashDontAskRules`), to `.kiln/settings.local.json`.

| Mode | No explicit rule match |
|---|---|
| `plan` | allow if the tool is read-only, or `bash`/`bash_background` running a read-only command (the gate still asks when it reads outside the workspace, as in manual mode); ask for any other shell command — Claude Code's "any other shell command goes through the regular permission flow while you are still planning", so an allow rule or a session grant runs it; otherwise **deny outright**, not ask (edits are denied even when a rule allows them) |
| `acceptEdits` | allow if `edit`, `write`, or read-only; otherwise ask |
| `dontAsk` | allow if read-only or matched by an allow rule; otherwise **deny**, never ask (Claude Code's meaning: for CI and locked-down runs) |
| `auto` | allow as far as rules go; the gate then applies the outside-workspace check and sends the rest past the auto mode classifier (below) |
| `manual` | allow if read-only; otherwise ask |
| (unrecognized) | ask |

Read-only set: `read`, `glob`, `grep`, `session_search`, `tool_search`, `bash_output`, `exit_plan_mode`, `todo_write`. `bash` is deliberately excluded — the gate can't tell `bash(ls)` from `bash(rm -rf)`, so it's never blanket-allowed by mode alone.

### Outside-workspace rule

`internal/claude/permission/permission.go`. After `Decide` returns a verdict, if the call carries a path argument (`path`/`file_path`/`filePath`) that resolves outside the gate's registered roots, an otherwise-allowed call is forced to prompt (or denied, headless with no prompter) — unless the mode is `bypassPermissions`, which skips this check too. `WithinRoots` (`permission.go`) resolves the path as the tools do (`execenv.ResolveToolPath`: `@`, `~`, `file://`, relative to the first root) and rejects any `..`-escape. On macOS and Windows the comparison is case- and normalization-insensitive (`settings.CaseFoldPath`, the same folding deny rules use), so `/Users/me/PROJ/a.go` is inside the root `/Users/me/Proj`; on Linux it is exact.

`GateOptions.ReadOnlyRoots` (`AddReadOnlyRoot`/`WithinReadOnlyRoots`) is a second, narrower set of roots: a path there escapes this check only for a tool in `settings.ReadOnly`, never for a mutating one — used for Claude Code's auto-memory directory (§7) so reading a topic file doesn't prompt while a write there is still gated exactly as any other outside-workspace path.

### Session scratchpad

`execenv/scratchpad.go`, `permission/scratchpad.go`. Each session gets a private directory for temporary files, `<temp root>/<project>/<session id>/scratchpad`, where the temp root is `$KILN_TMPDIR`, else `$CLAUDE_CODE_TMPDIR`, else `/tmp`, joined with `kiln-<uid>`. Every level is created `0700`; a temp root that is a symlink, owned by someone else or open to others is refused, and the session runs without a scratchpad (logged). The system prompt names the directory and tells the model to use it for all temporary files instead of `/tmp` or the project.

Reading and writing it never prompts, in every mode (plan mode included), as Claude Code treats its session scratchpad: file tools whose one path argument is inside it, and bash commands kiln can fully analyse that write only there, read only there or in the workspace, and run only read-only or plain file commands (`mkdir`, `touch`, `cp`, `mv`, `tee`, `rm`, `rmdir`, `ln`). Deny and ask rules still apply first. A path counts only when it is inside both as written and through symlinks, so a symlink planted in the scratchpad that points elsewhere is gated like its target. Subagents share the parent session's gate and so its scratchpad. The sandbox branch's writable temp dir is meant to be the same `kiln-<uid>` root (merge note in `execenv/scratchpad.go`).

### Auto mode classifier

`internal/claude/permission/classifier.go` (the gate's side), `internal/automode` (the model call). Auto mode behaves as Claude Code documents it (code.claude.com/docs/en/permission-modes, "Eliminate permission prompts with auto mode"; code.claude.com/docs/en/auto-mode-config):

- **Order.** Deny rules and ask rules are settled first, exactly as in every other mode, so the classifier can narrow auto mode, never widen it. A path outside the workspace, which other modes ask about, goes to the classifier instead, marked `"outside_workspace": true` next to the workspace directories, as Claude Code's auto mode sends what would ask to its classifier; a write to a protected path outside the workspace still asks, and so does any call when the classifier fails.
- **What skips it.** Calls an allow rule or a session "don't ask again" grant approves; read-only tools; `edit`/`write` inside the workspace; a bash command that provably only reads, inside the workspace. Everything else is classified: other shell commands, `web_fetch`, MCP tools, `task` dispatches.
- **Protected paths** (`permission/protected.go`) are classified even past an allow rule: Claude Code's list (`.git`, `.claude` except `.claude/worktrees`, `.vscode`, `.husky`, shell startup files, package-manager config, `.mcp.json`, …) plus kiln's own `.kiln`, `CLAUDE.md` and `CLAUDE.local.md` in any directory (memory a later session loads as instructions). Checked for file tools on the path argument and for bash on every file the command writes (`settings.BashAutoModeWrites`), as written, through symlinks and in the OS's own spelling, folded as the filesystem folds names. A bash command that changes git's configuration (`git config` other than a read, `git remote add`/`set-url`/…, any `git -c`), or that writes a file kiln cannot name, is classified too. A file-tool call whose path key is misspelt or doubled is classified. Claude Code's docs do not list build or CI files (Makefile, `package.json`, `.github/workflows`), and neither does kiln: a change to them is reviewed by the classifier's rules when it is committed or pushed.
- **Broad allow rules** (`settings/automode_rules.go`). While auto mode is on, allow rules that would let arbitrary code run unreviewed are set aside, so what they cover goes to the classifier: bare `Bash` and `Bash(*)`; any rule whose program has a wildcard (`Bash(py*)`, `Bash(*.sh)`); a shell, interpreter or command wrapper (`sh`, `python3.12`, `node`, `env`, `xargs`, `sudo`, `ssh`, … by base name, so `/bin/sh` counts) as the whole rule or with a wildcard after it; any wildcard after a package manager or build tool (`npm`, `pnpm`, `yarn`, `uv`, `cargo`, `make`, …) and after `git` or `go` (`git -c`, aliases, `go test -exec`); and every `Task`/`Agent` rule. Narrow rules such as `Bash(npm test)` still apply. Claude Code's docs name blanket rules, interpreter wildcards, package-manager run commands and Agent rules; git, go and the build tools are kiln's extension of the same reasoning. Deny and ask rules are never touched, and nothing is deleted: the rules apply again as soon as the mode changes. As in Claude Code, the user is not told; the run log records `auto mode sets aside broad allow rules` once per stretch of auto mode.
- **What it sees** (`automode/transcript.go`): what the user typed, the agent's earlier tool calls other than read-only lookups, the CLAUDE.md memory the session loaded (which can add limits but never authorizes a blocked action, since a repository can supply it), and the action. What the user typed is the line kiln stores with each prompt and queued follow-up (`kilnTyped` on the session's user message, so it survives a resume); a user message without one — a session from before kiln recorded them, kiln's own follow-ups — is replaced by a note, never shown. In a subagent, the user's lines come from the root session and the subagent's own task is shown as `{"delegated_task": …}`: a model wrote it, so it is never the user's authorization. Never shown: tool results, the agent's prose or thinking, compaction summaries, `<hook-context>` blocks, `@file` contents. Every value is JSON-encoded inside its tag, and invisible format characters (Unicode tags, bidi controls, zero-width characters) are written as `\u` escapes. An action over 20,000 bytes is not reviewed at all (the user is asked).
- **Model.** `modelRoles.fast` from `~/.claude/settings.json`, `~/.kiln/settings.json` or `--settings` when it resolves, else the session's current model. A project or local `modelRoles.fast` is not used for the classifier (subagents still use it), with a startup warning when the session starts in auto mode and a run-log line otherwise: a repository must not pick the model that reviews the agent. One request per classified call, no tools, a 1,024-token answer cap, a 60s timeout.
- **Verdicts.** The answer must be exactly one JSON object, optionally in a code fence, with a string `decision` of `allow` or `block` and nothing after it; a key given twice, trailing text or any other shape is unreadable. Allow runs the call. Block returns `auto mode blocked this action: <reason>…` to the model as the tool result; the TUI shows the tool block with `blocked by auto mode` in its meta. The third block in a row, or the twentieth in a session, asks the user instead, after a system note naming the streak and the latest reason (Claude Code's numbers). A classifier allow or a user approval ends a streak; a call that skipped the classifier does not (Claude Code resets on any allowed call; kiln is stricter, so reads between blocks cannot keep the streak from its limit). The total resets when its own limit trips. Subagents share the gate, so they share the counts.
- **Failures.** An error, a timeout, an unreadable answer or no classifier at all asks the user; in print mode, which has nobody to ask, the call is refused. Never allowed. Failures do not count as blocks. A permission check that errors or panics (a cancelled prompt or classifier call) blocks the call (`harness` before_tool).
- **Settings.** `autoMode.environment`, `allow`, `soft_deny` and `hard_deny` are prose lists added to the classifier's instructions. A list containing `"$defaults"` keeps kiln's built-in entries at that position; a list without it replaces them. Read from user settings and `--settings` only: a repository cannot tell the classifier what to allow.
- **Run log.** Each call logs `auto mode classifier` with `tool`, `decision` (`allow`/`block`/`error`), `model`, `ms`, `input_tokens`, `output_tokens`, `cache_read`, `cache_write`, and the reason or error.
- **Prompts in auto mode** (a failed check, a run of blocks, an ask rule, a path outside the workspace) leave out the bash prompt's "switch to auto mode" option.

Not implemented from Claude Code's auto mode: `autoMode.classifyAllShell`, protected paths outside auto mode (manual and acceptEdits do not yet prompt for them), critical paths, reviewing a subagent's report before the parent reads it, and the `claude auto-mode` subcommands.

### `task` role-crossing gate

A `task` tool call itself has no `role` field in its JSON args (`subagent_type`, `description`, `prompt`, `model`) — a caller names a role through the `model` argument (e.g. `model: "heavy"`), which `internal/claude/agents` resolves against `modelRoles`. `internal/agent/dispatch.go` runs a **separate** gate check, scoped only to a dispatch that crosses onto a different, metered provider than the parent for a non-zero-cost model:

```go
d.Gate.Check(ctx, permission.Request{ToolName: "task", PrimaryArg: "role:" + requested})
```

So a rule like `task(role:heavy)` matches this specific check via the paren-form syntax above. Same-provider role reuse, free models, and non-crossing dispatches never hit this gate — they're covered by the subagent's own tool calls being checked generically as they happen.

### Permission modes

`ModeManual`, `ModeAcceptEdits`, `ModeAuto`, `ModeDontAsk`, `ModeBypassPermissions`, `ModePlan` (`settings.go`) — exactly these six string values; see the `Decide` table above for what each auto-allows.

### Hooks

`internal/claude/hooks/hooks.go`, `internal/claude/hooks/runner.go`

Events, exactly nine, keyed under the top-level `"hooks"` JSON key by event name: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SessionStart`, `SessionEnd`, `Stop`, `SubagentStop`, `Notification`, `PreCompact`.

Shape: `{"hooks": {"<Event>": [{"matcher": "<regex or \"*\">", "hooks": [{"type": "command", "command": "...", "timeout": <seconds>}]}]}}`. `matcher` empty or `"*"` matches every tool; otherwise it's a case-insensitive anchored regex against the tool name (so `Bash` doesn't accidentally match `BashOutput`). `timeout` defaults to 60s if unset.

Unlike settings scalars, **hooks accumulate across scopes** — a project hook does not replace a user hook of the same event; both run.

Payload on stdin (JSON), fields present depending on event: `session_id`, `transcript_path`, `cwd`, `hook_event_name` (always); `tool_name`, `tool_input` (Pre/PostToolUse); `tool_response` (PostToolUse, declared but not populated by the guard path); `prompt` (UserPromptSubmit); `stop_hook_active` (Stop/SubagentStop); `message`, `notification_type` (Notification); `trigger`, `custom_instructions` (PreCompact); `reason` (SessionEnd).

Exit-code / stdout contract, checked in this order:

1. Timed out → notice only, does not block.
2. Exit code 2 → **blocks**; reason from stderr, or a generic fallback.
3. Other non-zero exit → notice only (stderr surfaced), does not block.
4. Exit 0 with non-empty stderr → notice.
5. Empty stdout → nothing further.
6. Non-JSON stdout → appended as plain-text context.
7. JSON stdout: `continue: false` blocks; `hookSpecificOutput.permissionDecision: "deny"` blocks; `hookSpecificOutput.updatedInput` is merged key-by-key into the rewritten tool args (composes across multiple hooks); `hookSpecificOutput.additionalContext` is appended as context.

`PreToolUse` hooks run **before** the permission gate (`GuardToolCall`), so a hook-rewritten command is what the gate judges, not what the model proposed.

### `internal/claude/writesettings`

kiln's only settings writer, and it never writes a `.claude` file. `AddRule`/`RemoveRule` read-modify-write **`<cwd>/.kiln/settings.local.json`** (rules from "don't ask again" and `/permissions`), preserving unrelated keys; `/permissions` refuses to delete a rule that lives in a Claude Code file and names that file. `SetUserModel` writes `"model"` into **`~/.kiln/settings.json`** — the `/model` command's "set default" path; a model already in `~/.claude/settings.json` is still read, and kiln's wins over it. Writes are atomic (temp file + rename); creating `<cwd>/.kiln` also creates `.kiln/.gitignore` holding `*`, so kiln's files never show in `git status`.

## 6. `modelRoles`

`internal/claude/agents/agents.go`, `internal/claude/settings/settings.go`, `internal/tools/task.go`

`settings.json`'s `modelRoles` is a flat `map[string]string` of role name → `"provider/model"`, e.g. `{"fast": "ollama/qwen3.8:latest", "heavy": "anthropic/claude-opus-4"}`. A `task` tool call's `model` argument can be a role name, an explicit `provider/model`, `"inherit"`/empty, or a bare alias.

`ResolveModel` precedence (`agents.go`):

1. empty or `"inherit"` → parent's model
2. `requested` is a literal key of `roles` → that role's `provider/model` value, resolved against available models; falls back to parent if unresolvable
3. `requested` contains `/` → treated as an explicit `provider/model` literal
4. bare alias, lowercased: built-in aliases `haiku→fast`, `sonnet→structured`, `opus→heavy` resolve through the matching role **if that role is configured**; otherwise falls back to substring-matching `requested` against the parent's own provider's models only (never cross-provider, to avoid silently moving cost onto a different paid API)

`ValidateRoles(roles, candidates)` reports (for diagnostics only — `doctor`, `kiln models`, startup warnings) every role whose value isn't `provider/model`-shaped or doesn't resolve to an available model; `ResolveModel` itself never errors, only falls back.

A `task` call that crosses to a different, metered provider than the parent is separately gated by the permission system as `task(role:<name>)` — see §5.

## 7. `.claude/` assets

`internal/claude/skills`, `internal/claude/commands`, `internal/claude/agents`, `internal/claude/memory`, `internal/claude/keybindings`, `internal/tui/keybindings.go`, `internal/claude/statusline`, `internal/claude/trust`

All of skills/commands/agents/memory read the same two roots in precedence order (`paths.ClaudeRoots`, lowest first): `~/.claude` (user) then `<cwd>/.claude` (project) — project shadows user on a name collision.

### Skills

`~/.claude/skills/*/SKILL.md` and `.claude/skills/*/SKILL.md`. Frontmatter: `name` and `description` (both required, or the skill is dropped), `user-invocable` (bool, **defaults to `true`** if absent — this is what makes a skill become a slash command). Only `name`/`description`/`location` stay resident in the system prompt; the body loads on invocation.

### Commands

`~/.claude/commands/**/*.md` and `.claude/commands/**/*.md`, recursive. Frontmatter: `description`, `argument-hint`, `allowed-tools` (comma-string or YAML list), `model`. Malformed frontmatter doesn't drop the command — the body still runs without metadata.

Argument substitution: `$ARGUMENTS` is replaced with the whole raw argument string verbatim; `$1`, `$2`, ... are whitespace-split positional words (an out-of-range index becomes empty string, not left literal). If the body uses neither, the raw args are appended after the body rather than discarded.

Namespace: the relative path under `commands/` with `.md` stripped; every directory segment but the last joins with `:` (e.g. `commands/frontend/component.md` → `frontend:component`).

### Agents

`~/.claude/agents/*.md` and `.claude/agents/*.md`, **flat, not recursive**. Frontmatter: `name` (optional, defaults to filename), `description` (**required** — absent frontmatter or missing description drops the agent entirely), `model` (a hint, see §6's `ResolveModel`), `tools` (list, comma-string, scalar, or omitted meaning "inherit parent's tools"). Unknown keys (`color`, `paths`, `role`, `skills`, etc.) are ignored, not rejected — there is no `Color` field modeled.

### Memory (`CLAUDE.md`)

Discovery: `~/.claude/CLAUDE.md` and `~/.kiln/CLAUDE.md` (user; the second holds `#` notes kiln saves when the project has no `CLAUDE.md`), and `<cwd>/CLAUDE.md` and `<cwd>/.claude/CLAUDE.md` (project; Claude Code reads both). `.claude/rules/*.md` and `~/.claude/rules/*.md` are loaded automatically too, no import needed, sorted alphabetically.

`@import` syntax: only a line that is *entirely* `@path` triggers an import (an inline `@handle` in prose does not). `~/` expands to home; an absolute path is used as-is; otherwise resolved relative to the importing file's directory, not cwd. Recursion capped at depth 5; a cycle renders `<!-- skipped circular import: ... -->`; a broken import renders `<!-- missing import: ... -->` rather than vanishing silently.

Budget: `LoadMemory(cwd, budgetTokens)` estimates tokens as `ceil(len/4)`; the budget is the tier's `SystemPromptTokens` (10% of the context window, 2k–32k). CLAUDE.md files always load in full. Rules load in full while the budget allows, project rules first; the rest are listed in a `<memory-index>` block, one line each with the rule's path and its frontmatter `description` (else its first heading), and the model is told to read a rule before doing work it covers. `Assembled.Indexed` lists those paths (logged at startup); if the CLAUDE.md files alone exceed the budget they load anyway and kiln prints a warning.

### Auto memory (Claude Code's `MEMORY.md`), read-only

`internal/claude/memory/automemory.go`. kiln reads Claude Code's own auto-memory index — it never writes one; auto memory is otherwise Claude Code's feature, not kiln's (see `~/.claude/plans/kiln-self-improvement.md`'s "Claude Code parity" for the scope decision, and internal/learn for kiln's own, separate learned-preference store).

**Location** (`ResolveAutoMemoryDir`): `~/.claude/projects/<project>/memory/MEMORY.md`, where `<project>` is the current git repository's root directory (resolved via `git rev-parse --git-common-dir`, not `--show-toplevel`, so every worktree of a repository — which each have their own toplevel but share one `.git` — and every subdirectory of it resolve to the same `<project>`) with every character that isn't an ASCII letter or digit replaced by `-`, one-for-one (`projectDirName`). Verified against real `~/.claude/projects/` directory names on the machine this was written on: `/` (`/Users/andrepato/projects/harness` -> `-Users-andrepato-projects-harness`) and `_` (this machine's own `$TMPDIR`, which has underscores, maps to a directory name with `-` in their place, not `_` — an earlier version of this function only replaced `/` and got `_` wrong). No real example with a `.` in the path was available to confirm `.` specifically, but it follows the same one-rule-for-every-punctuation-character pattern the `_` evidence already established. Outside a git repository, `<project>` is derived from cwd itself. The `autoMemoryDirectory` setting (below), when set and safe (see "Permissions" below), overrides this resolution outright.

**Loading** (`LoadAutoMemory`), at session start, within the tier's remaining `SystemPromptTokens` budget after CLAUDE.md/rules:

- The first 200 lines or 25KB of `MEMORY.md`, whichever comes first (Claude Code's own cap) — never more, and topic files (`user_role.md`, `feedback_testing.md`, ...) are **never preloaded**; the model reads them on demand with the normal read tool once told where the directory is.
- Framed with a short neutral header naming the directory (`<auto-memory dir="...">`) and presented as project context, not instructions, matching how CLAUDE.md content is framed.
- Trimmed further, past Claude Code's own cap, to fit what budget remains after CLAUDE.md — and skipped entirely (not merely trimmed) on the small context tier, which has no headroom to spare once CLAUDE.md content (which always loads in full) has claimed its share.
- `/context` reports the outcome as `auto-memory   <loaded|trimmed|skipped> (...)`.

**Disabling**: `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` (env), or `autoMemoryEnabled: false` in `settings.json` (user or project scope — see §5's keys table; a project can turn off what the user's settings turned on, last-non-nil scope wins).

**`autoMemoryDirectory` safety** (`resolveAutoMemoryDir`, `trustedAutoMemoryDirectory` in `chat.go`): since the resolved directory becomes a read-only permission root (below), an `autoMemoryDirectory` value is trusted only under two conditions, both required:

- **Folder trust.** A project- or local-scope `autoMemoryDirectory` (a repo's own, possibly untrusted, checked-in or `.local` settings) is honoured only once the folder is trusted in kiln's own trust store (`internal/claude/trust`, same gate `chat.go` uses for `.mcp.json`'s project-scope servers). Until then, only the user's own `~/.claude/settings.json` (and an explicit `--settings <file>` the person typed themselves) may set it; an ignored project-scope value falls back to the default directory and prints a one-line startup warning. Matches Claude Code's own rule for this setting (docs: "Storage location" — honoured from project/local settings only under the same workspace-trust rule as hooks).
- **Not the filesystem root, home, or an ancestor of home**, trusted or not (`unsafeAutoMemoryDirectory`). Any of these, added as a read-only root, would turn the setting into "read anything under my home directory (or the whole disk) without asking." A rejected value falls back to the default directory and prints a one-line startup warning (`AutoMemory.DirectoryOverrideRejected`).

**Permissions**: the resolved directory is added to the permission gate as a **read-only root** (`GateOptions.ReadOnlyRoots`, `permission.go`'s `WithinReadOnlyRoots`) — a read-only tool's (`settings.ReadOnly`) path there is not treated as outside the workspace, so reading a topic file never prompts. `WithinReadOnlyRoots` resolves symlinks on both the stored root and the checked path before comparing, so a symlink planted inside the directory that points elsewhere does not inherit the root's no-prompt treatment for wherever it actually points. A mutating tool's path there is **not** exempted: it is checked only against the regular `Roots`, so a write or edit into the auto-memory directory is refused/asked exactly as any other outside-workspace path always was. kiln never writes auto memory, by construction, not just by convention.

### Keybindings

File: `~/.claude/keybindings.json`, a flat JSON object mapping an action id to one key string. The editor action ids it can override (`internal/tui/keybindings.go`) are exactly:

`tui.input.submit`, `tui.input.newLine`, `tui.editor.historyPrevious`, `tui.editor.historyNext`, `tui.editor.cursorWordLeft`, `tui.editor.cursorWordRight`, `tui.editor.deleteWordBackward`, `tui.editor.deleteToLineEnd`, `tui.editor.yank`, `tui.editor.undo`, `tui.editor.cursorLineStart`, `tui.editor.cursorLineEnd`

An unrecognized action id is ignored. Global-router keys (`ctrl+o`, `shift+tab`, `ctrl+f`, `esc`, etc., in `internal/tui/keys.go`) are hardcoded and not driven by this file. Conflicts (two actions bound to the same key) are reported, never auto-resolved.

### statusLine

`settings.json`'s `statusLine.command` runs with a JSON payload piped on stdin (`internal/claude/statusline/statusline.go`): `hook_event_name` (forced to `"Status"`), `session_id`, `transcript_path`, `cwd`, `model.{id,display_name}`, `workspace.{current_dir,project_dir}`, `version`, `output_style.name`, `cost.{total_cost_usd,total_duration_ms,total_api_duration_ms,total_lines_added,total_lines_removed}`, `context_window.{context_window_size,used_percentage,total_input_tokens,total_output_tokens,current_usage.{input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens}}`, `exceeds_200k_tokens`. Fields sourced from the Anthropic API in real Claude Code (rate limits, extra-usage credits) are omitted — a script should treat them as absent. Command output's trailing newlines are trimmed; each remaining line becomes a status-line row.

### Trust

`~/.harness/trusted.json` — the harness's own trust store, explicitly not a read of Claude Code's trust state. Flat JSON array of trusted absolute paths, written atomically at mode 0600. Trusting a directory trusts every descendant (`isAncestorOrSelf`); symlinks are not resolved. The dialog fires on TUI startup when the cwd isn't already trusted and `HARNESS_TRUST_ALL != "1"`, because the harness reads the project's `.claude` settings and hooks before the first prompt.

### Plugins

`internal/claude/plugins`. kiln reads the same files Claude Code's own `/plugin` writes — it does not install, enable or disable a plugin itself.

- **Installed:** `~/.claude/plugins/installed_plugins.json` — `{"version":2,"plugins":{"<plugin>@<marketplace>":[{"scope":"user"|"project","installPath":"<abs dir>","version":"...","projectPath":"<abs>"}]}}`. A `project`-scope install only applies when the current directory is `projectPath` or a descendant of it.
- **Enabled:** `settings.json`'s `enabledPlugins` map (`"<plugin>@<marketplace>": true|false`), merged across the usual three scopes (later wins — `settings.LoadEnabledPlugins`). Absent or `false` both mean inactive: enabling is opt-in.
- A plugin is **active** for a run only when both are true. `plugins.LoadPlugins(cwd)` returns the active set; `plugins.ListInstalled(cwd)` returns every installed entry (active or not), for `/plugin`'s full report.

A plugin's install directory (`installPath`, `${CLAUDE_PLUGIN_ROOT}` in its own files) holds:

- `.claude-plugin/plugin.json` — manifest: `name`, `version`, optional `mcpServers` (an inline object, or a string path to one relative to the root) and `hooks` (inline, or a string path). A manifest that fails to parse is skipped with a `diag` warning; the plugin is not counted as active.
- `skills/<name>/SKILL.md`, `commands/**/*.md`, `agents/*.md` — same frontmatter rules as §7's skills/commands/agents, loaded by `plugins.Skills`/`Commands`/`Agents`. Every item is namespaced `<plugin>:<item>` (a nested command keeps its subdirectory too, e.g. `commands/git/changelog.md` → `<plugin>:git:changelog`), which is how it shows up in the `/` palette and in the `task` tool's `subagent_type` enum.
- `hooks/hooks.json` (`{"hooks": {...}}`, same shape as `settings.json`'s own `hooks` key) and/or the manifest's own `hooks` field — merged into the session's hook config (`plugins.Hooks`, appended alongside `.claude/settings.json`'s hooks, not replacing them). Every plugin hook command gets `CLAUDE_PLUGIN_ROOT` in its environment, and any literal `${CLAUDE_PLUGIN_ROOT}` in the command string is expanded to the install directory first.
- `.mcp.json` and/or the manifest's `mcpServers` — merged into the session's MCP servers (`plugins.MCPServers`), named `plugin_<plugin>_<server>` (so its tools read `mcp__plugin_<plugin>_<server>__<tool>`, matching Claude Code) and expanded the same way every other server is (`mcp.ExpandConfig`), after `${CLAUDE_PLUGIN_ROOT}` substitution. Plugin servers are not gated on folder trust like `.mcp.json` is — installing and enabling the plugin is the opt-in.

`/plugin` reports the active set (version, marketplace, and how many skills/commands/agents/hooks/MCP servers each contributed) plus installed-but-inactive ones and why.

### The `skill` tool

`internal/tools/skill.go`. A model-invocable tool (`{"skill": "<name>", "args": "<optional>"}`) alongside the `/`-palette's skill commands — the two are independent: a skill's `user-invocable: false` only hides it from the palette, and `disable-model-invocation: true` only excludes it from this tool's catalog. The tool's own description lists every model-invocable skill (project, user and active-plugin) as `- <name>: <description>` (each description truncated to ~200 chars, since the list is sent on every turn). Invoking a listed name returns `Base directory for this skill: <dir>` followed by the SKILL.md body, so the skill's own relative file references resolve; an unlisted name is an error result naming what's available. It is resident (`internal/cli/mcp.go`'s `residentAll`) and read-only, so plan mode allows it (`settings.ReadOnly`).

## 8. MCP servers and postures

`internal/mcp/config.go`, `internal/mcp/gating.go`, `internal/mcp/hub.go`, `internal/cli/mcp.go`

### Config files

Three scopes, the same ones `claude mcp add --scope` uses, and kiln writes
and reads the identical files - kiln has no MCP config file of its own (see
`docs/claude-code-parity.md`'s divergence #7: settings are `.kiln`, MCP is
shared):

| Scope | File |
|---|---|
| `user` | `~/.claude.json`, top-level `mcpServers` |
| `local` (default) | `~/.claude.json`, `projects[<abs project dir>].mcpServers` |
| `project` | `<repo>/.mcp.json` (nearest ancestor up to the repo root), gated on folder trust |

`kiln mcp add [-s local\|project\|user]`, `kiln mcp add-json` and
`kiln mcp remove` write exactly what `claude mcp` does, so a server either
tool adds works in both, and either tool's `remove` deletes it for both
(`internal/mcp/config_write.go`'s `AddServer`/`RemoveServer`, a raw
pass-through that preserves every other key, written atomically and
keeping the file's permissions).

Precedence on a name clash: `--mcp-config`, then local, then project, then
user. A read/parse failure or missing file returns an empty server map
rather than erroring — a broken config never blocks startup.

Server entry (one struct covers both transports):

```json
{
  "type": "stdio | http",
  "command": "...", "args": ["..."], "env": {"K": "V"},
  "url": "...", "headers": {"K": "V"}
}
```

`type` is inferred when absent: explicit `type` if set, else `"http"` if `url` is set, else `"stdio"`.

`--mcp-config <file>` **replaces** the server set entirely (reads only that file; never merges with any of the files above). `--strict-mcp-config` with no `--mcp-config` connects to **nothing** — it does not fall back to the default files.

### Connect timeout

`HARNESS_MCP_CONNECT_TIMEOUT` (`internal/mcp/hub.go`), default 30s, bounds each server's connect + list-tools call; an invalid or non-positive value also falls back to 30s.

### Postures

`internal/mcp/gating.go`. A posture restricts which servers' tools are indexed/searchable at all. A server that no built-in posture names is in scope for every posture (`InPosture`): the lists exist to keep the known noisy servers out of a coding session, not to hide a newly configured server until `/posture` is discovered.

| Posture | Description | Servers |
|---|---|---|
| `coding` (default) | "Code, deployments and observability." | `infisical`, `argocd-mcp`, `personal-kb`, `homelab-kb`, `claude-relay`, `sentry`, `github` |
| `ops` | "Infrastructure and monitoring." | `grafana`, `proxmox`, `argocd-mcp`, `unifi-mcp`, `pocket-id`, `infisical` |
| `all` | "Every configured server. Expensive on a small context window." | every server (`*`) |

Confirms the README's claim that `coding` excludes `grafana`/`proxmox`/`unifi-mcp`.

`HARNESS_POSTURE` (`internal/cli/mcp.go`) sets the posture once at session start; falls back to `coding` if unset or unrecognized. The `/posture <name>` command changes it live for the rest of the session and clears admitted-tool state — it does not re-read the env var or persist back to it.

### `tool_search`

A real tool (`internal/tools/toolsearch.go`), present under `posture-index` and `full-index` tool strategies but **absent** under `full-schemas` (large-context tier, ≥ ~200k-token window), where every in-posture tool's full schema is loaded directly instead. It scores posture-scoped tools by keyword (name weighted over description), admits matches permanently for the session, and returns their full JSON schemas — schemas load on demand, not up front. Its searchable pool is filtered by the posture active when the tool was built (`ToolSearchTool`); `/posture` re-gates the lane's active set (`switchPosture` in `internal/cli/mcp.go`) but does not rebuild that pool.

## `HARNESS_OFFLINE`

`HARNESS_OFFLINE=1` makes kiln refuse every provider except `faux` and
`ollama` before any request is made. Scripted and visual test runs set it so
a settings-file model override can never route a test to a paid endpoint.
