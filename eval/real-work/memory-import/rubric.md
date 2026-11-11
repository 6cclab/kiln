# Predeclared code-quality evaluation

Freeze task, baseline commit, binaries, model IDs, effort, run limits, and evaluator tests before inference. Both arms get identical task text and clean full-repository clones. Hidden tests remain outside their checkouts and are added only after each final artifact is frozen. No corrective feedback or implementation edits are supplied after a run. Preserve failures and incomplete work.

## Objective evidence

Build the actual CLI; run existing memory/CLI/settings tests and targeted race tests, gofmt, and vet. Run independent hidden CLI acceptance tests for transfer, history collapse, scope isolation, validation, conflicts, dry-run, and transaction rollback. Record each named result independently. Measure elapsed time and reported cost; unknown usage stays unknown. Record patch size but do not equate fewer lines with quality.

## Independent patch review (0–4 per dimension)

Two reviewers receive anonymized candidate snapshots in different orders, the same task and rubric, baseline source, and test evidence. They do not receive harness names, prices, timings, or model output. Reviewers cite file/line evidence and reproducible counterexamples. They assess:

1. Correctness and robustness: explicit contract, failure atomicity, cancellation, history and source behavior.
2. Security and privacy: path boundaries, scope isolation, resource limits, no unintended disclosure.
3. Design and maintainability: idiomatic Go, cohesive ownership, reuse without broad refactoring, clear error paths and transaction boundaries.
4. Test quality: realistic regression tests, adversarial failures and rollback, assertions of behavior rather than implementation, meaningful race coverage.
5. Scope and usability: compatible existing commands, useful diagnostics and documentation, restrained changes and no dependencies.

Anchors: 0 absent or fundamentally broken; 1 major defects/rework; 2 useful but significant gaps; 3 sound with minor issues; 4 production-ready within this contract, supported by evidence. A blocker cannot be hidden by averaging: separately classify each patch as ready, minor revisions, major revisions, or unusable. Preserve reviewer disagreement and root adjudication with evidence. These are agent-assisted reviews, not human ground truth. No general superiority claim from one task or a small sample.

## Fairness and cost

Use Opus 4.8 as the primary model at high effort in both harnesses, scratch HOME/config, WebSearch disabled, same repository revision, and no remembered prior attempts. Preserve native delegation; record any helper-model differences instead of attributing every difference solely to harness code. Serialize inference, alternate order between repeats, and avoid unrelated builds during timed runs. Include CLI startup and tool/test execution in wall time. Both runs may use their native tools; record actual model routes and delegation. Use generous equal wall/turn ceilings and disclose budget admission differences. Total authorized reported/catalog cost allowance is $50 including the previous $1.1484105 sample. Stop on unknown-cost failure and retain conservative reservations; do not infer zero retry usage. Reviewer agent usage is separate and not available as provider billing in this ledger.
