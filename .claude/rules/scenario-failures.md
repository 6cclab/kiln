# Scenario failures

- Classify every failing scenario before fixing anything: a stale expectation (wording or
  behaviour changed on purpose), a fixture that no longer exercises its path, a driver
  limitation, or a kiln defect.
- Fix a stale expectation in the `.steps` file; do not bend kiln to match old wording.
- Read-only commands (`echo`, `cat`, `ls`, … in `internal/claude/settings/readonly_bash.go`) are
  auto-approved. A permission fixture needs a command that still asks, such as `sh -c '…'`.
- Report a driver limitation as one; never mark the scenario passed around it.
- Record kiln defects with `scripts/qa/findings.py new`, one file per finding.
