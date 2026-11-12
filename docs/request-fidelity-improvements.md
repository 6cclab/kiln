# Request fidelity and efficiency improvements

This batch fixes concrete request-construction and context-delivery defects found
after the GPT-6-Sol comparison. It does not claim a measured cost or generated-code
quality improvement. Live comparison is deferred until the implementation work
is ready, as requested.

## Preserve conversation prefixes

Automatic memory retrieval, capability/skill suggestions, and recovered task
notes previously copied and modified the latest historical user message. The
stored session was untouched, but a change to retrieved state changed an earlier
part of the next provider request. They now append bounded, unstored context at
the end, leaving the replayed conversation intact. Synthetic context is marked
so later retrieval hooks cannot treat it as a typed user query.

Retrieval still runs before each request: permission changes, changed source
files, corrected memory and current verification status remain visible. Existing
memory, discovery and task-state settings still control inclusion and budgets.
Nothing is frozen merely to preserve cache reuse. Later requests can still
stop sharing a prefix at the previous transient context tail; this change does
not guarantee a full cache hit. OpenAI documents the importance of stable
prefixes in its [prompt caching guide](https://developers.openai.com/api/docs/guides/prompt-caching).

The new four-case regression failed against the old transforms and passes after
the fix. It checks the actual projected request prefix, fresh context after a
state change, and unchanged stored input. A separate chained-transform test
ensures memory notes cannot become a discovery query. Existing budget, permission,
source-freshness and branch-recovery assertions remain in place.

## Deliver hook feedback through compaction

PostToolUse additionalContext was removed from its queue during the first
context transform. Fitting and compaction can invoke transforms speculatively,
so the actual assistant request could lose that feedback. Transforms now read
without consuming it; the successful normal response acknowledges delivery.
Compaction and failed requests do not clear the queue.

A faux-model regression reproduced missing feedback after compaction before the
fix. It now confirms one delivery in the first normal request after compaction
and no replay in its following continuation. A second regression confirms that
a failed stream and its retry both receive the pending feedback. These tests
use local scripted providers, not paid inference.

## Replay Responses message identity and phase

OpenAI Responses, Codex Responses, and Azure Responses previously replaced
assistant text item IDs and omitted the provider's phase on replay. They now
persist that metadata in the existing textSignature field, alongside source
API/provider/model identity and a digest of the text. Replay retains it only for
the matching origin and unchanged text; older, edited or foreign messages use
the existing synthesized-ID fallback without inventing a phase.

Completed and interrupted text streams retain available metadata. Hashing is
done when the item or stream ends, not on every token delta. This preserves the
provider's distinction between commentary and answers on subsequent requests;
it does not change the harness's stop-reason mapping. The OpenAI SDK explicitly
requires preserving and resending phase in
[ResponseOutputMessage](https://github.com/openai/openai-python/blob/main/src/openai/types/responses/response_output_message.py).

Regressions cover each adapter's stream-to-session-entry-to-request path,
interrupted streams, unknown phases, and legacy/edited/cross-model fallback.
Google and Vertex additionally require matching API identity before replaying
thought signatures, preventing Responses metadata from leaking when the same
provider/model identifiers are reconfigured to use another API format.

## Budget admission

The shared USD budget previously charged a full context at both the uncached
rate and the highest cache rate. Input, cache-read, and cache-write usage are
disjoint categories, so a context of C tokens costs at most C times the highest
applicable input category rate. Admission now uses that maximum instead of the
sum. The existing doubled cache-write allowance and highest tier rates remain,
along with output reservations, concurrency protection, unknown-price refusal,
and retention of reservations when usage is unknown.

Exact-bound tests failed before the change and pass afterward for input-,
read-, write- and tier-dominated pricing. They reject a budget just below the
bound. A concurrent test admits one of twenty calls at a one-request allowance
and retains it after a failed request. This improves admission accuracy; it does
not change provider prices, waive a budget, or guarantee an invoice amount.

## Validation and remaining work

Validated with Go 1.26.6:

- `make check`: formatting, vet, staticcheck, full unit suite and e2e vet passed.
- Full `make e2e`: passed in 223.899 seconds on the final patch.
- Race tests for CLI, provider, provider API and message packages passed.
- `make covercheck` passed all existing coverage floors.
- Pinned golangci-lint 2.15.0 reported zero issues on new changes.
- All four fixes have behavioral regressions that failed before the fix.

Existing tests now inspect the appended context message where appropriate;
content, budget, permission and branch-isolation expectations were retained or
strengthened. No UI goldens or unrelated test expectations were changed.

No mandatory model reviewer, automatic memory extraction, background learning
scheduler, or live benchmark was added. The broader outstanding roadmap remains
tracked in [efficiency implementation status](efficiency-implementation-status.md).
