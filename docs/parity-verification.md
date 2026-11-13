# Manual parity verification against the real Claude Code binary

Some rows in `docs/claude-code-parity.md` are marked `[chk]`. They describe
behavior of the real, closed-source `claude` binary, which no test in this repo
can observe. Only running the real thing settles them. This procedure verifies
those rows by hand. It is not for CI or routine use: it spends turns against a
Max-plan account, so run it sparingly, and only when a `[chk]` row needs
settling before you implement against it.

Rows this procedure applies to (see `docs/claude-code-parity.md` for the
full context of each):

- §1 Input line — `\` + Enter / Option+Shift+Enter newline combos
- §1 Input line — paste of an image renders an attachment chip
- §8 Permission prompts — exact wording of yes / yes-and-remember / no-with-feedback
- Deliberate divergences #4–#6 — model-role indirection, concurrent `task`
  dispatch, and nested subagent dispatch depth, as compared to Claude
  Code's own `subagent_type` selection and dispatch model

Do not use this procedure for a row that a test in this repo can already
prove. Check `docs/claude-code-parity.md` and the suites in
`docs/testing.md` first.

## Never in CI

This procedure needs a real `claude` binary and a real Max-plan session.
Never run it in CI or against a shared or automated account.

## Procedure

1. Build kiln's own driver, `cmd/kiln-drive` (see `docs/testing.md`'s
   "Screen — PTY driver" section for what its line protocol can do):

   ```bash
   go build -o bin/kiln-drive ./cmd/kiln-drive
   ```

2. Drive the real `claude` binary through it instead of `bin/kiln`. Strip
   the environment variables that would make `claude` think it is nested
   inside another Claude Code session:

   ```bash
   env -u CLAUDE_CODE_CHILD_SESSION -u CLAUDECODE \
     ./bin/kiln-drive --bin "$(which claude)" \
     -- <claude flags matching the scenario under test>
   ```

3. Send the line protocol commands (`SEND`, `KEY`, `WAIT`, `SCREEN`,
   `RESIZE`, `EXIT`; see `docs/testing.md`) needed to reach the behavior
   in question. Record the `SCREEN` output or the observed wording or
   keystroke behavior.

4. Record the result in the table below: the row it settles, the
   `claude --version` at the time, and the result. A result that confirms a
   `[chk]` row promotes it to `[obs]` in `docs/claude-code-parity.md`, citing
   this record. A result that contradicts the row means the parity doc's row
   needs correcting, not this procedure.

5. Close the session. Do not leave a `claude` session driven this way
   running in the background.

## Verification log

Rows stay blank until someone runs the real binary and has a result to
record.

| Row / behavior | Claude Code version observed | Result |
|---|---|---|
| `\` + Enter / Option+Shift+Enter newline combos | | |
| Paste of an image renders an attachment chip | | |
| Permission prompt option wording (yes / yes-and-remember / no-with-feedback) | | |
| Model-role indirection vs. `subagent_type` | | |
| Concurrent `task` dispatch vs. Claude Code's serial dispatch | | |
| Nested subagent dispatch depth | | |
