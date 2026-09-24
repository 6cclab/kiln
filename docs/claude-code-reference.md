# Claude Code reference screens: the contract

The harness looks and behaves like Claude Code v2.1.281. `claude-code-parity.md` is the
behaviour checklist; this file is the rendering contract. Every element cites a screen
captured from the real `claude` binary through `harness-drive` on 2026-09-24, kept under
`testdata/reference/claude-code/`. Quoted renderings are quoted from those files. Anything
not captured is marked **[chk]** and must be captured before it is treated as settled.

Out of scope: Claude Code's product content rather than its interface: Anthropic account
rows, `/fast`, `/claim-credit`, cloud sessions, the auto-mode teach dialog, plugin tips.
Where the harness's data differs (its providers, MCP servers, tiers), the layout, wording
pattern, glyphs and columns still match.

Conventions: `␠` marks a significant leading space; widths are for a 100-column terminal
unless stated; right-aligned items end at the last column.

## 1. Startup (`startup.txt`, `startup-default-home.txt`)

```
 Claude Code
 ▐▛███▛█   Claude Code v2.1.281
▝▜██████▀  Opus 5 (1M context) with medium effort · Claude Max
  ▝▝ ▝▝    /private/var/folders/93/248j_5ds3ls8k4ggh_fxndjh0000gn/T/cc-ref/proj
⚠ 1 MCP server needs authentication · run /mcp
  Get to finished work sooner with Opus 5.5. Switch anytime with /model.
                                                                                ◐ medium · /effort
────────────────────────────────────────────────────────────────────────────────────────────────────
❯ Try "how do I log an error?"
────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents
```

- Row 1: ` Claude Code`; the terminal title is also "Claude Code". Harness: ` harness`.
- Rows 2 to 4: the logo block (`▐▛███▛█` / `▝▜██████▀` / `▝▝ ▝▝`) in the brand orange, then
  from column 11: name and version; `<Model display> with <effort> effort · <plan or auth>`;
  the working directory. Harness: `harness v<version>`, `<provider/model> with <effort>
  effort · <auth kind>` where auth kind is `Claude Max`, `API key`, `Ollama` from the
  credential store.
- Optional warning rows at column 0: `⚠ 1 MCP server needs authentication · run /mcp`. The
  harness uses this row for connect failures: `⚠ 2 MCP servers unavailable · run /mcp`,
  replacing the dim `mcp: … · /mcp for details` line.
- A tip row indented two spaces (free text; one tip).
- `◐ medium · /effort` right-aligned on the row above the input's top rule whenever no
  other right-aligned hint occupies it. Glyph by effort: `◔ low`, `◐ medium`, `◕ high`,
  `● xhigh/max` **[chk]**; `◐ medium` observed.
- The bottom area is the input box and the mode line only. **No status row by default.**
  A `statusLine` command in `settings.json` adds one row above the mode line with the
  command's stdout (`startup.txt` shows this machine's). The harness's model/context/cost
  footer goes away; that information moves to `/status` and to `statusLine` support.
- No `Type / for commands…` hint; the placeholder carries `Try "…"`.

## 2. Input box (`startup.txt`, `mode-manual.txt`, `turn-edit.txt`, `resize-60.txt`)

- Full-width `─` rule, `❯␠<text>`, full-width `─` rule. Empty input shows the placeholder
  dim after `❯ `: `❯ Try "how do I log an error?"`.
- Right-aligned hint on the row above the top rule: `Ctrl+Y to paste deleted text` after a
  kill (`mode-manual.txt`), otherwise the effort indicator.
- `turn-bash-auto.txt` shows `❯␠␠Parity-check output` dim in an empty box: a prompt
  suggestion, not the session name **[chk]**; not ported until confirmed.
- Long lines wrap at the box width with a two-space continuation indent (`resize-60.txt`).
- Mode line under the box, indented two spaces:
  - `⏵⏵ auto mode on (shift+tab to cycle) · ← for agents`
  - `⏸ manual mode on · ← for agents` (no cycle suffix in manual)
  - `⏵⏵ accept edits on (shift+tab to cycle)`
  - `⏸ plan mode on (shift+tab to cycle)`
  - `⏵⏵ bypass permissions on (shift+tab to cycle)` **[chk]**
  - ` · ← for agents` only while the input is empty and idle.
  - `Press Ctrl-C again to exit` replaces the mode line for a second after one Ctrl+C
    (`ctrl-c-hint.txt`).
- Shift+Tab order from auto: auto → manual → accept edits → plan → auto. Harness modes map
  auto, manual, acceptEdits, plan; bypassPermissions renders `⏵⏵ bypass permissions on` and
  is never entered by cycling; dontAsk renders `⏵⏵ don't ask on` **[chk]**.

## 3. Transcript (`turn-edit.txt`, `turn-bash-auto.txt`, `verbose-*.txt`, `spinner*.txt`)

```
❯ Use the Edit tool to change math.js so add returns a + b. Do not explain.
  Read 1 file
⏺ Update(math.js)
  ⎿  Added 1 line, removed 1 line
      1 -function add(a,b){ return a - b }
      1 +function add(a,b){ return a + b }
⏺ Done.
✻ Crunched for 4s · done 10:03 AM
```

- User echo `❯␠<text>`, two-space continuation indent, one blank row above and below.
- Read-only tool calls are grouped into one plain two-space-indented row: `  Reading 1
  file…` while running (`spinner.txt`), `  Read 1 file` when done; `⏺ Running 1 shell
  command…` while running (`spinner-2.txt`) becomes `  Ran 1 shell command`
  (`turn-bash-auto.txt`). Plurals `Read 3 files`, `Ran 2 shell commands` **[chk]**. Grouped:
  Read, Glob, Grep, Bash without shown output, web fetches. Edit and Write never group.
- Mutating calls: `⏺ Update(math.js)`. Edit is named **Update**; Write **Write**; Bash
  **Bash**; Read **Read**. Path relative to cwd in the collapsed view, absolute in verbose.
- Result row `␠␠⎿␠␠<summary>`. Edit: `Added 1 line, removed 1 line`, then diff lines
  indented six spaces: `      1 -old` / `      1 +new` (number right-aligned to the widest,
  then `-`/`+`, no space before text). Bash: output lines (`  ⎿  parity-check`). Read: `Read
  2 lines`. Long outputs end `… +N lines (ctrl+o to expand)` **[chk]**. A running Bash call with a
  description renders `⏺ <description>` then `  ⎿  $ <command>` (`permission-bash.txt`).
- Assistant text `⏺␠<markdown>`, continuation indented two.
- Turn summary `✻ <Verb> for <N>s · done <h:mm AM/PM>`. Observed verbs: Brewed, Crunched,
  Cooked. Full set **[chk]**.
- Spinner row `<frame> <Label>…`, frames `·✢✳✶✻✽`, labels observed: Computing, Whirring,
  Smooshing, Lollygagging; suffix while thinking `(1s · thinking with medium effort)`;
  `(4s · ↓ 1.2k tokens)` once tokens flow **[chk]**. No `esc to interrupt` in the row
  **[chk]**.
- Verbose (Ctrl+O, `verbose-ctrl-o.txt`): every tool as `⏺ Tool(absolute path)` with `⎿`
  results; a right-aligned dim `10:03 AM claude-opus-5` row after a turn's last tool; the
  bottom area becomes `  Showing detailed transcript · ctrl+o to toggle · ? for shortcuts`
  with `verbose` right-aligned. Ctrl+O toggles back. Ctrl+R is not the expand key.
- Slash commands echo like prompts, result as a `⎿` row: `❯ /model` / `  ⎿  Kept model as
  Opus 5 (1M context)`.

## 4. Autocomplete (`autocomplete-slash.txt`, `autocomplete-at.txt`)

Above the input's top rule, indented two, no border, no marker; selection by colour only.

```
  /model                                  Set the AI model for Claude Code (currently Opus 5 (1M
                                          context))
  /track-work                             Track work as epics and stories in the self-hosted Task
                                          Tracker, streamed live to the mobile dashboard. Use whe…
```

- `/`: name column 40 wide; description wraps once, aligned to its column, then `…`.
- `@`: `  + <path>` per match, directories with trailing `/`, absolute for matches outside
  the project, up to five rows.
- Tab and Enter accept, Esc closes, typing filters.

## 5. Dialogs (`dialog-*.txt`, `permission-edit.txt`, `plan-approval.txt`)

Full-screen dialogs (`/model`, `/mcp`, `/permissions`, `/config`, `/status`, Rewind)
replace the input box and mode line under the transcript:

```
▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔ ◐ medium · /effort ▔
   Select model
   Switch between Claude models. Your pick becomes the default for new sessions. For
   other/previous model names, specify with --model.
     1. Default (recommended)  Opus 5 with 1M context · Best for everyday, complex tasks
   ❯ 2. Opus (1M context) ✔    Opus 5 with 1M context · Best for everyday, complex tasks
     3. Fable                  Fable 5.1 · Most capable for your hardest and longest-running
                               tasks
   ◐ Medium effort ←/→ to adjust
   Enter to set as default · s to use this session only · Esc to cancel
```

- `▔` top rule with the effort indicator embedded near the right edge. Body indented
  three. Title bold, description dim, numbered options with `❯` on the selection and `✔`
  after the current value, second column for descriptions with aligned wrap, last row the
  key legend joined by ` · `.
- `/model`: the harness's resolved models grouped by provider in the same columns; same
  legend; `←/→` adjusts effort.
- `/mcp` (`dialog-mcp.txt`): `Manage MCP servers`, `N servers`, section headers (`User MCPs
  (/path/.claude.json)`, `Built-in MCPs (always available)`), rows `✔ name   16 tools`,
  `✘ name`, `⚠ name   needs authentication`, `◯ name` disabled, `※ Run harness --debug to see
  error logs`, a docs URL, legend `↑/↓ to navigate · Enter to confirm · Esc to cancel`.
  Enter opens the server's detail (tools, reconnect, disable) **[chk]**.
- `/permissions` (`dialog-permissions.txt`): tabs `Permissions  Recently denied   Allow   Ask
  Deny   Auto mode   Workspace`, one explanation line, bordered search `╭ ⌕ Search… ╮`,
  numbered list with `Add a new rule…` first and `↓` before the last visible number when
  more follow, legend `←/→ to switch · ↓ to select · Esc to cancel`.
- `/config` and `/status` (`dialog-config.txt`, `dialog-status.txt`): one dialog, tabs
  `Settings  Status   Config   Usage   Stats`. Settings: search field, two-column list,
  `↓ 18 more below`, legend `Type to filter · Enter/↓ to select · ↑ to tabs · Esc to
  clear`. Status: `Version:`, `Session name:`, `Session ID:`, `cwd:`, `Login method:`,
  `Model:`, `MCP servers: 14 connected, 4 need auth, 1 disabled, 1 failed · /mcp`,
  `Setting sources:`, legend `Esc to cancel`. Values are the harness's own.
- Edit permission (`permission-edit.txt`), inline in place of the input box, tool header
  left in the transcript:

```
⏺ Update(math.js)
────────────────────────────────────────────────────────────────────────────────────────────────────
 Edit file
 math.js
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 1 -function add(a,b){ return a - b }
 1 +function add(a,b){ return a + b }
╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌
 Do you want to make this edit to math.js?
 ❯ 1. Yes
   2. Yes, and switch to accept edits (auto-approve file edits and common file commands) for this
      session (shift+tab)
   3. No
 Esc to cancel · Tab to amend
```

  Bash (`permission-bash.txt`), inline in place of the input box; the transcript above it
  shows the call as `⏺ <tool description>` and `  ⎿  $ <command>`:

```
⏺ Generating 4 random hex bytes
  ⎿  $ openssl rand -hex 4
────────────────────────────────────────────────────────────────────────────────────────────────────
 Bash command
 Tip: auto mode handles these prompts for you — choose "switch to auto mode" below
   openssl rand -hex 4
   Generate 4 random hex bytes
 This command requires approval
 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don’t ask again for: openssl rand *
   3. Yes, and switch to auto mode · auto mode handles these prompts for you
   4. No
 Esc to cancel · Tab to amend
```

  ` Bash command`, one tip row, the command indented three, its description indented
  three, ` This command requires approval`, ` Do you want to proceed?`, four options (the
  "don't ask again" rule is the command's first two words plus ` *`), legend. Keys: 1 to 4,
  Enter, Esc = No, Tab amends. Interrupting a turn (Esc) leaves `  ⎿  Interrupted · What
  should Claude do instead?` under the collapsed group row (`permission-bash-after-esc.txt`).
  Write prompts mirror Edit with ` Write file`.
- Plan approval (`plan-approval.txt`): `▔` rule; inner `─` rule indented two; ` Ready to
  code?`; ` Here is Claude's plan:` (harness: `Here is the plan:`); `╌` rule; the plan
  indented three with `↓` at the right edge when it scrolls; `─` rule; `Claude has written
  up a plan and is ready to execute. Would you like to proceed?`; `1. Yes, and use auto
  mode` / `2. Yes, manually approve edits` / `3. Tell Claude what to change` with sub-hint
  `shift+tab to approve with this feedback`; last row `ctrl+g to edit in $EDITOR · <plan
  path>`. The transcript shows `  ⎿  /plan to preview` first.
- Rewind (`dialog-rewind.txt`), Esc Esc on an empty input: `Rewind`, `Restore the code
  and/or conversation to the point before…`, one row per user message with `No code
  changes` under it, `❯ (current)` last, `Enter to continue · Esc to cancel`. Selecting a
  message navigates the session tree to before it.
- Trust (`dialog-trust.txt`): once per new folder, `─` rule, ` Accessing workspace:` …
  `❯ No, exit` / `Yes, I trust this folder`, `Enter to confirm · Esc to cancel`. Whether the
  harness needs it **[chk]**; it does if it reads `.claude/settings.json` from untrusted
  repos.

## 6. Shortcuts (`shortcuts.txt`)

`?` on an empty input shows three columns under the box:

```
  ! for shell mode        double tap esc to clear input      ctrl + shift + _ to undo
  / for commands          shift + tab to auto-accept edits   ctrl + z to suspend
  @ for file paths        ctrl + o for verbose output        ctrl + v to paste images
  /btw for side question  ctrl + t to toggle tasks           opt + p to switch model
                          shift + ⏎ for newline              opt + o to toggle fast mode
                                                             ctrl + s to stash prompt
                                                             ctrl + g to edit in $EDITOR
                                                             /keybindings to customize
```

The harness lists the entries it implements in the same columns and order; dead entries
are omitted.

## 7. Keys

| Key | Claude Code | Harness |
|---|---|---|
| Enter | submit | same |
| Shift+Enter, `\`+Enter | newline | same |
| Esc | interrupt while busy; clear or close otherwise | same |
| Esc Esc | Rewind dialog | to build |
| Ctrl+C | clear input; twice within a second exits; `Press Ctrl-C again to exit` | same |
| Ctrl+O | toggle verbose transcript | to build; Ctrl+R view removed |
| Ctrl+U / Ctrl+Y | kill line / paste; hint `Ctrl+Y to paste deleted text` | hint to build |
| Shift+Tab | auto → manual → accept edits → plan | names and order to match |
| `?` on empty input | shortcuts panel | to build |
| `!` `/` `@` `#` | shell, commands, paths, memory | same |
| Ctrl+G, Ctrl+T, Ctrl+S, Ctrl+Z, Opt+P, Opt+O, Ctrl+V | editor, tasks, stash, suspend, model, fast, image | later |

## 8. Colours

From the raw captures (`*.rec`): locate the text, read the SGR in force. Logo and the `✻`
family in the brand orange; `⏺` green for success, red for error; `❯`, rules and `⎿` rows
dim; selected dialog rows in the accent; diff `-` red and `+` green on tinted backgrounds
**[chk]** until the style dump exists. Parity tests compare colours once `harness-drive`
can dump them (`SCREEN --styles`).

## 9. Verification

`test/e2e/ccparity_test.go` drives the harness through the capture scripts' scenarios with
faux standing in for the model and diffs rows against the reference files after
normalising: version string, model and provider names, working directory, times, elapsed
seconds, token counts, and rows that are Claude Code product content (tips, account rows,
claim-credit banner, teach-auto dialog). Any other difference fails, including a
one-column shift.
