// Package session defines the pure, storage-agnostic session model: the
// conversation-tree Entry union, the value/list addressing scheme pi calls
// "namespace/key", the write/commit primitives that assign sequence numbers
// and timestamps, and the Storage interface that a concrete backend (see
// internal/session/jsonl) implements.
//
// # Model
//
// A session is a tree of Entry nodes (kind "entry" on the wire) linked by
// ParentID, plus a flat key/value store partitioned into namespaces (kind
// "value" and "list" on the wire), plus a usage ledger (kind "usage"). Every
// write to any of these three stores is assigned a seq from one global,
// monotonically increasing counter shared across all four kinds — seq order
// is commit order, not a per-kind index.
//
// Entry.Type discriminates the union:
//
//   - "message": Entry.Message holds the verbatim msg.Message (user,
//     assistant, toolResult, or system) exactly as decoded from disk.
//   - "compaction": a summary that replaces a run of prior entries.
//   - "branch_summary": a summary anchored to FromID (nil means the branch
//     root).
//   - "custom": an application-defined entry; Entry.CustomType names it and
//     Entry.Data carries its payload.
//
// # Values and lists
//
// Value[T] and ValueList[T] are typed addresses: a namespace string (for
// example "pi.branch.tip") plus a key string (for example a lane or branch
// name). The phantom type T only guides the typed Get/Set helpers in this
// package; on the wire and in Storage, every value and list element is
// carried as a JSON value (json.RawMessage in this package) keyed by
// (namespace, key). See values.go for the full namespace catalog pi defines:
// pi.branch.tip, pi.lane.config, pi.lane.state, pi.op.meta, pi.op.state,
// pi.op.tool_args, pi.pending.entry, pi.pending.tool_output,
// pi.pending.assistant_frame (a list), pi.result, pi.session.name and
// pi.entry.label.
//
// pi.op.meta and pi.op.state hold the 13-leaf operation FSM; this package
// keeps their payload as json.RawMessage (decoding only far enough to read
// the "at" discriminator where useful) because the FSM itself is out of
// scope for this phase.
//
// # Commit
//
// A commit is a batch of Write values (EntryWrite, UsageWrite, ValueWrite,
// ListWrite). PrepareCommit assigns each write the next seq values in order
// starting at the store's current high-water mark and a single timestamp for
// the whole batch, producing CommittedWrite values ready to serialize.
// ValidateCommittedWrites enforces monotonic seq, entry/usage id uniqueness
// within the batch and against existing state, and that every non-root
// entry's parent already exists (in prior state or earlier in the same
// batch) before the batch is applied.
package session
