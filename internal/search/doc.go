// Package search implements session recall over SQLite FTS5.
//
// This is the Go port of src/search/sqlite.ts. The design constraint that
// shapes everything here, carried over unchanged: recall must cost tens of
// tokens, not thousands. A search that returns whole entries defeats its own
// purpose on a small context window, so the index stores snippets and
// returns snippets, never full transcript text.
//
// # Storage
//
// modernc.org/sqlite (a CGO-free, pure-Go SQLite build with FTS5 compiled
// in) backs the index, registered under the database/sql driver name
// "sqlite". The schema is one FTS5 virtual table, "entries", plus one plain
// table, "indexed_files", tracking which session files have been read and
// at what size/mtime so a Sync only re-parses files that grew.
//
// The "entries" table is NOT declared with an empty content option, despite
// sqlite.ts's doc comment calling it "contentless" — the actual CREATE
// VIRTUAL TABLE statement (mirrored here verbatim) omits the content
// option entirely, which makes it a standalone FTS5 table that manages its
// own storage. That means ordinary DELETE ... WHERE session_id = ? works
// directly; the special contentless delete command (an INSERT INTO entries
// naming the entries table itself with a delete pseudo-value) is not
// needed and is not used, because the table this schema actually creates
// is not contentless. See sqlite.go for exactly what SQL runs.
//
// # Formats indexed
//
// Two on-disk session formats are indexed, both JSONL:
//
//   - harness v4 (internal/session/jsonl): line 1 is a header
//     ({"kind":"header","v":4,...}); every following line is a
//     transaction, decoded with jsonl.ParseTransaction into one or more
//     session.CommittedWrite values. Only "entry" writes whose Entry.Type
//     is EntryMessage carry a message; its text comes from
//     msg.TextOf(message content blocks).
//   - Claude Code's own ~/.claude/projects/*.jsonl history: one flat JSON
//     object per line, an entry id under "uuid" (falling back to "id" or
//     "entryId"), a timestamp under "timestamp" or "ts" as either an epoch
//     millisecond number or an ISO-8601 string, and a "message" object
//     whose "content" is either a bare string or an array of blocks with
//     "type"/"text" fields.
//
// A file is classified by trying jsonl.ParseHeader on its first line: a
// successful v4 header selects the transaction path; anything else
// (including Claude Code's non-header first line) selects the generic
// per-line path, so a harness v4 session is never misparsed as generic
// JSON even though a single-write transaction line happens to look like
// one.
package search
