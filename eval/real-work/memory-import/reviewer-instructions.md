You are independently reviewing four anonymous implementations of the same real repository feature. You are not told which harness produced each, its cost, timing, or the authors' claimed success. Do not infer authorship. Review each patch on its own merits against task.md and rubric.md before comparing.

Read the baseline implementation, candidate patch and changed source/tests/docs, and evaluator evidence. Do not modify a candidate or fix it. You may run local, offline, isolated counterexamples if useful; save new probes outside the candidate so submitted artifacts stay unchanged. Do not make model calls. Do not inspect provenance/identity mappings or other reviewers' results.

For each candidate, produce:
- Exact score 0–4 for each of the five rubric dimensions, with a short evidence-based reason.
- Readiness: ready, minor revisions, major revisions, or unusable. Explicit contract or security blockers override a high average score.
- Concrete findings ordered by severity, with changed source path and line, trigger, observed/expected consequence, and whether reproduced or inferred. Distinguish contract violations from optional improvements. Verify the contract actually requires your expectation.
- Assessment of submitted tests (not just evaluator checks), transaction/cancellation design, error handling, source-boundary checks, scope, and documentation.
- A concise comparison and any uncertainty/disagreement you would want a human maintainer to resolve.

Do not reward extra abstractions, long comments, file count, or test count by themselves. Do not invent failures to differentiate equally sound patches. Do not treat a passing test suite as proof of quality or a failed hidden check as automatically a candidate bug; first assess the test expectation. Keep your findings independently reasoned, with enough evidence for a maintainer to reproduce them.
