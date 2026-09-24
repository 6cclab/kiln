// Package agent is the Go port of harness/src/agent/session.ts: the agent
// session, headless — resident tools (bash, read, edit, write) plus the
// internal/harness loop, wired to a chosen provider/model and a JSONL
// session file.
//
// Start is startSession: it resolves which session file to open (resume by
// id/prefix or by "most recently modified in this cwd", fork, or create
// fresh), builds an internal/harness.Harness over it with the resident
// tools plus any caller-supplied extras, and returns the harness's "main"
// lane ready to Prompt against. SetModel is the model-switch half of
// cli.ts's /model command: it moves the lane's configured model and the
// harness's compaction settings to a newly resolved tier.
//
// # Deviations from session.ts
//
// Reasoning suppression ("/no_think" for Qwen-family models on Ollama) is
// not applied here. session.ts wires
// `toProviderMessages: (m) => applySuppression(m, resolved.suppression)`
// into AgentHarness.create, transforming the transcript right before every
// request. This Go port moved that responsibility to the provider layer
// instead: internal/provider/ollama.Provider.Stream calls
// provider.SuppressionFor(model) and provider.ApplySuppression itself, on
// every call, computed fresh from the model rather than carried from
// Options.Resolved.Suppression. internal/harness does have an equivalent
// hook (Hooks.OnTransformContext, invoked at exactly the same point in
// turn.go's drive()), so wiring suppression there instead was possible;
// it was left out because it would either duplicate the ollama provider's
// own (idempotent) suppression call or require Start to know which
// providers are prompt-suppressed, which is exactly the fact
// provider.SuppressionFor already encapsulates. Net effect on behavior:
// none observed — ApplySuppression never double-appends the suffix — but
// the mechanism session.ts describes (a harness-level hook) does not exist
// here.
//
// Skill resources (session.ts's `skills`/`resources.skills`, kept resident
// as name+description only, with bodies injected on invocation) have no
// counterpart in internal/harness.Options at all as of this port; Options
// here has no Skills field, and nothing downstream of Start can inject a
// skill body into context. A caller that needs skill resources must build
// its own resource_search-equivalent tool and add it via ExtraTools.
//
// toolExecution: "sequential", set explicitly in session.ts's
// AgentHarness.create call, has no Options field here because
// internal/harness's turn loop is unconditionally sequential (see its
// doc.go) — not a gap, just not a knob.
//
// internal/session/jsonl.Repo has no Fork method (only Create, Open, List,
// Delete); jsonl.Fork is a free function taking a source path, its already
// -open header/NextSeq, and an explicit destination path. Start's
// forkSession composes these itself (open source read-only to capture
// header+NextSeq, close it, pick a destination id and path via
// jsonl.FileName, call jsonl.Fork, then Repo.Open the result), following
// the same sequence internal/session/jsonl/fork_test.go uses. Picking the
// destination id ahead of the call (rather than letting Fork generate one)
// was necessary to keep the file name jsonl.FileName encodes and the
// header id Fork writes in agreement; the id generator
// (newSessionID) duplicates jsonl's own unexported uuidv7At byte-for-byte,
// since that helper is not exported.
//
// Start's session-id-prefix resume (Options.Resume matching an 8+ character
// id prefix when no session has that exact id) is new: session.ts's
// startSession only ever matched resumeId by exact equality
// (`candidates.find((m) => m.id === opts.resumeId)`). An ambiguous prefix
// (more than one session sharing it) is an error here rather than an
// undefined pick.
package agent
