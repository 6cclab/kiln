// Package harness is the Go port of pi-agent-core's AgentHarness: the agent
// runtime that turns a session.Storage, a provider.Registry and a tool.Set
// into a running conversation.
//
// A Harness owns zero or more named Lanes. Each Lane drives one branch of
// one session through pi's operation state machine: every transition is
// written durably to the session's value store (namespaces "pi.op.*",
// "pi.pending.*", "pi.lane.*", "pi.result") before the in-process state
// changes, so a crashed process can Resume a lane from exactly where it
// left off by replaying the last committed "pi.op.state".
//
// # Scope of this phase
//
// This package implements the write sequence and event/hook surface for
// the core turn loop: prompt -> assistant response (streamed) -> tool
// execution -> assistant response -> ... -> completion. The operation FSM
// has 13 named `at` states (see OpState); this phase fully implements
// starting, checkpoint, assistant.ready, assistant.effect_pending and
// tools. deferred.pending/deferred.ready (provider-side deferred
// responses), summary.pending/summary.ready (mid-run summarization) and
// navigation.ready_to_commit's non-run paths (NavigateTree) are not
// implemented: Lane methods that would need them return an error whose
// text names the missing state, rather than an incorrect approximation.
//
// Tool execution is sequential per turn, matching the "toolExecution":
// "sequential" setting pi's own reference sessions record; nothing here
// runs the tool calls of one assistant message concurrently.
//
// # Dependency on internal/compaction and internal/tools
//
// internal/compaction and internal/tools (each owned by a parallel phase)
// landed partway through this package's development. Options.Compaction is
// an alias for compaction.Settings, and this package calls
// compaction.Prepare/Compact/ShouldCompact/CalculateContextTokens directly
// (see compaction.go), using the lane's own configured provider as the
// compaction.Streamer (that interface's Stream method has the same
// signature as provider.Provider's, so every provider already satisfies
// it). This package otherwise depends only on internal/tool's Tool and Set
// types, which it treats as opaque; internal/tools' Builtins (bash, read,
// edit, write) is what tests exercise Lane.Prompt's tool-execution path
// against.
package harness
