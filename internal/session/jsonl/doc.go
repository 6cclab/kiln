// Package jsonl is the JSONL-file-backed implementation of
// internal/session.Storage, plus session lifecycle (create, open, list,
// delete, fork) and legacy v3 header detection.
//
// # On-disk format (v4)
//
// A session lives at one file:
//
//	<sessionsRoot>/<dirName>/<fileName>.jsonl
//
// dirName is DirectoryName(cwd): "--" + cwd with a leading "/" or "\"
// stripped and every "/", "\" and ":" replaced with "-", then "--" again
// (repo.js:23 — sessionDirectoryName). This encoding is lossy: "/a/b" and
// "/a-b" both map to "--a-b--".
//
// fileName is FileName(createdAt, id): the session's createdAt as an ISO
// timestamp with ":" and "." replaced by "-", an underscore, the session id
// URL-encoded, and ".jsonl" (repo.js:27 — sessionFileName).
//
// Line 1 is the header, a JSON object with "kind":"header", "v":4,
// "storageVersion":1, "id", "createdAt" (epoch ms), "cwd", and optionally
// "parentSessionId", "legacyParentSessionPath" and "nextSeq" (present only
// when the file was created by a fork or a legacy-v3 upgrade, to preserve a
// sequence high-water mark above the highest sequence actually written).
//
// Every following line is one transaction: a JSON array of CommittedWrite
// values when a commit wrote more than one, or a bare CommittedWrite object
// when it wrote exactly one (io.js:61). The reader accepts either shape on
// any line. seq is one global counter shared by every write kind (entry,
// usage, value, list); it only ever increases, in file order.
//
// A commit is a plain append of one line to the file — no fsync
// (storage.js). Create, Fork and the legacy-v3 upgrade instead write the
// whole file to "<path>.tmp" and rename it over the destination
// (publishFileAtomically in io.js), so a reader never observes a partial
// header or a partial full-file rewrite.
//
// # Legacy v3
//
// A line-1 header {"type":"session","version":3,...} identifies a legacy
// session. This package detects it (see legacy_v3.go) but does not yet
// upgrade it in place; Open on such a file returns an error. Porting the
// full v3 record model (compaction tails, branch summaries, per-field
// change records folded into pi.lane.config) is deferred; see legacy_v3.go
// for exactly what is and is not implemented.
package jsonl
