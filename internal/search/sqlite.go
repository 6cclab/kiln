package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	sqlitedriver "modernc.org/sqlite"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

// sqliteBusyCode is SQLITE_BUSY. FTS5 write operations do not honor a
// connection's PRAGMA busy_timeout the way ordinary table writes do — they
// return SQLITE_BUSY to the caller immediately on lock contention instead of
// blocking through the registered busy handler (confirmed empirically:
// against a plain table, two connections racing a write both succeed under
// busy_timeout; against an FTS5 table, one gets SQLITE_BUSY at once). So
// indexFile retries at the application level instead. sqlite.ts has no
// counterpart to this because node:sqlite is single-connection and there is
// nothing else to contend with.
const sqliteBusyCode = 5

// snippetTokens is the snippet width in tokens, roughly. Keeps a hit
// affordable on a small context window. Mirrors SNIPPET_TOKENS in
// sqlite.ts.
const snippetTokens = 12

// defaultLimit mirrors sqlite.ts's `query.limit ?? 10`.
const defaultLimit = 10

// Search is a session-search index over SQLite FTS5.
type Search struct {
	db *sql.DB
}

// EntryHit is one matched entry: a session/entry id pair with a rendered
// snippet and a higher-is-better relevance score.
type EntryHit struct {
	SessionID string
	EntryID   string
	Timestamp int64
	Snippet   string
	Score     float64
}

// SessionHit is the best entry hit for one session, which is what
// SearchSessions groups down to.
type SessionHit struct {
	SessionID string
	Score     float64
	Top       EntryHit
}

// DefaultDBPath is ~/.harness/search.db, the default Open target.
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".harness", "search.db")
}

// DefaultSessionsDirs are the roots Sync walks when called with no roots:
// the harness's own sessions store plus Claude Code's own history, so
// prior work is recallable from the first run rather than only after the
// harness has accumulated a history of its own.
func DefaultSessionsDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return []string{
		filepath.Join(home, ".harness", "sessions"),
		filepath.Join(home, ".claude", "projects"),
	}
}

// Open opens (creating if necessary) the search database at path, or at
// DefaultDBPath if path is empty, in WAL mode, and ensures the schema
// exists.
func Open(path string) (*Search, error) {
	if path == "" {
		path = DefaultDBPath()
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("search: create db dir %s: %w", dir, err)
		}
	}
	// _busy_timeout is a per-connection PRAGMA, unlike journal_mode (which
	// persists in the database file itself): database/sql pools several
	// physical connections, and a one-time `PRAGMA busy_timeout` run through
	// migrate() would only apply to whichever single connection happened to
	// run it. Setting it in the DSN makes modernc.org/sqlite apply it to
	// every connection the pool opens, which is what concurrent Sync callers
	// against the same WAL database need to avoid SQLITE_BUSY.
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("search: open %s: %w", path, err)
	}
	s := &Search{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrate mirrors SqliteSessionSearch#migrate in sqlite.ts exactly: WAL,
// then the FTS5 "entries" table and the "indexed_files" bookkeeping table.
func (s *Search) migrate() error {
	// WAL: indexing can run while a session is being written. Without it a
	// sync would block the agent loop's own appends.
	if _, err := s.db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return fmt.Errorf("search: enable WAL: %w", err)
	}
	if _, err := s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS entries USING fts5(
			session_id UNINDEXED,
			entry_id UNINDEXED,
			timestamp UNINDEXED,
			role UNINDEXED,
			text,
			tokenize = 'porter unicode61'
		)
	`); err != nil {
		return fmt.Errorf("search: create entries table: %w", err)
	}

	if _, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS indexed_files (
			path TEXT PRIMARY KEY,
			size INTEGER NOT NULL,
			mtime INTEGER NOT NULL,
			session_id TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("search: create indexed_files table: %w", err)
	}
	return nil
}

// Close closes the underlying database handle.
func (s *Search) Close() error {
	return s.db.Close()
}

// findSessionFiles walks every root looking for *.jsonl files, mirroring
// findSessionFiles in sqlite.ts: a root or subdirectory that cannot be read
// (missing, permission denied) is silently skipped rather than failing the
// whole walk.
func findSessionFiles(roots []string) []string {
	var found []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(d.Name(), ".jsonl") {
				found = append(found, path)
			}
			return nil
		})
	}
	return found
}

type indexedFileInfo struct {
	size  int64
	mtime int64
}

// Sync indexes any session file under roots (default DefaultSessionsDirs)
// that is new or has grown, by size+mtime. It mirrors
// SqliteSessionSearch#sync in sqlite.ts: JSONL sessions are append-only, so
// a file that has not grown cannot have new entries — no hashing required.
func (s *Search) Sync(ctx context.Context, roots []string) error {
	if len(roots) == 0 {
		roots = DefaultSessionsDirs()
	}
	files := findSessionFiles(roots)

	known := make(map[string]indexedFileInfo)
	rows, err := s.db.QueryContext(ctx, "SELECT path, size, mtime FROM indexed_files")
	if err != nil {
		return fmt.Errorf("search: list indexed_files: %w", err)
	}
	for rows.Next() {
		var path string
		var info indexedFileInfo
		if err := rows.Scan(&path, &info.size, &info.mtime); err != nil {
			_ = rows.Close()
			return fmt.Errorf("search: scan indexed_files: %w", err)
		}
		known[path] = info
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("search: scan indexed_files: %w", err)
	}
	_ = rows.Close()

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		size := info.Size()
		mtime := info.ModTime().UnixMilli()

		if prior, ok := known[file]; ok && prior.size == size && prior.mtime == mtime {
			continue
		}
		if err := s.indexFile(ctx, file, size, mtime); err != nil {
			return err
		}
	}
	return nil
}

// sessionIDFromPath mirrors sqlite.ts's
// `path.split("/").pop()?.replace(/\.jsonl$/, "")`.
func sessionIDFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".jsonl")
}

// isSQLiteBusy reports whether err (possibly wrapped) is SQLITE_BUSY.
func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlitedriver.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteBusyCode
}

// indexFile retries indexFileOnce against SQLITE_BUSY: see sqliteBusyCode's
// comment for why FTS5 writes need an application-level retry that plain
// SQLite writes wouldn't.
func (s *Search) indexFile(ctx context.Context, path string, size, mtime int64) error {
	const maxAttempts = 8
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = s.indexFileOnce(ctx, path, size, mtime)
		if err == nil || !isSQLiteBusy(err) {
			return err
		}
		backoff := time.Duration(10*(1<<attempt)) * time.Millisecond
		backoff += time.Duration(rand.Intn(10)) * time.Millisecond
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// indexFileOnce (re)indexes one session file: its old rows are deleted
// first (Re-index the whole file rather than tracking byte offsets —
// sessions are small and this avoids an entire class of partial-line bugs
// at the cost of re-parsing a file that changed, exactly as sqlite.ts
// does), then every line is parsed and any entry with renderable text is
// inserted.
func (s *Search) indexFileOnce(ctx context.Context, path string, size, mtime int64) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		// A file that vanished or became unreadable between the walk and the
		// read is not indexed this round; sqlite.ts's readFile catch does the
		// same (return, leaving indexed_files untouched so the next sync
		// retries it).
		return nil
	}
	sessionID := sessionIDFromPath(path)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("search: begin index tx for %s: %w", path, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "DELETE FROM entries WHERE session_id = ?", sessionID); err != nil {
		return fmt.Errorf("search: delete stale entries for %s: %w", sessionID, err)
	}

	insert, err := tx.PrepareContext(ctx,
		"INSERT INTO entries(session_id, entry_id, timestamp, role, text) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("search: prepare insert: %w", err)
	}
	defer insert.Close()

	lines := strings.Split(string(raw), "\n")

	isV4 := false
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
		if h, err := jsonl.ParseHeader(lines[0]); err == nil && h.Format == jsonl.FormatV4 {
			isV4 = true
		}
	}

	if isV4 {
		for _, line := range lines[1:] {
			if strings.TrimSpace(line) == "" {
				continue
			}
			writes, err := jsonl.ParseTransaction([]byte(line))
			if err != nil {
				// A truncated final line is normal while a session is being
				// written; sqlite.ts's JSON.parse catch does the same.
				continue
			}
			for _, w := range writes {
				if w.Kind != "entry" || w.Entry == nil {
					continue
				}
				role, text, ok := extractHarnessText(*w.Entry)
				if !ok {
					continue
				}
				if _, err := insert.ExecContext(ctx, sessionID, w.Entry.ID, w.Entry.Timestamp, role, text); err != nil {
					return fmt.Errorf("search: insert entry %s: %w", w.Entry.ID, err)
				}
			}
		}
	} else {
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			hit, ok := extractGenericText(line)
			if !ok {
				continue
			}
			if _, err := insert.ExecContext(ctx, sessionID, hit.entryID, hit.timestamp, hit.role, hit.text); err != nil {
				return fmt.Errorf("search: insert entry %s: %w", hit.entryID, err)
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO indexed_files(path, size, mtime, session_id) VALUES (?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime
	`, path, size, mtime, sessionID); err != nil {
		return fmt.Errorf("search: upsert indexed_files for %s: %w", path, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("search: commit index of %s: %w", path, err)
	}
	return nil
}

// extractHarnessText pulls role/text out of a harness v4 EntryMessage
// write, mirroring sqlite.ts's extractText: entries whose Type isn't
// EntryMessage, or whose message renders to empty text, are skipped
// (matching `if (!message?.role) return undefined` and the trim-then-check
// at the end of extractText). No role is excluded — a system or toolResult
// message with text is indexed exactly like a user or assistant one, since
// sqlite.ts's extractText never filters by role.
func extractHarnessText(e session.Entry) (role, text string, ok bool) {
	if e.Type != session.EntryMessage || e.Message == nil {
		return "", "", false
	}
	var blocks msg.Blocks
	switch m := e.Message.(type) {
	case msg.SystemMessage:
		role, blocks = string(m.MessageRole()), m.Content
	case msg.UserMessage:
		role, blocks = string(m.MessageRole()), m.Content
	case msg.AssistantMessage:
		role, blocks = string(m.MessageRole()), m.Content
	case msg.ToolResultMessage:
		role, blocks = string(m.MessageRole()), m.Content
	default:
		return "", "", false
	}
	text = strings.TrimSpace(msg.TextOf(blocks))
	if text == "" {
		return "", "", false
	}
	return role, text, true
}

// genericHit is one extracted Claude-Code-format (or any other flat
// per-line JSONL) entry.
type genericHit struct {
	entryID   string
	timestamp int64
	role      string
	text      string
}

// genericEntry is the flat per-line shape sqlite.ts parses generically:
// `uuid` (Claude Code) or `id`/`entryId` (pi/harness-shaped), a
// numeric-or-ISO-string timestamp under `timestamp` or `ts`, and a
// `message.role`/`message.content` pair.
type genericEntry struct {
	UUID      string          `json:"uuid"`
	ID        string          `json:"id"`
	EntryID   string          `json:"entryId"`
	Timestamp json.RawMessage `json:"timestamp"`
	TS        json.RawMessage `json:"ts"`
	Message   *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// extractGenericText mirrors sqlite.ts's per-line JSON.parse +
// extractText + id/timestamp fallback chain in indexFile, for any JSONL
// line that is not part of a recognized harness v4 transaction (in
// practice: Claude Code's ~/.claude/projects history).
func extractGenericText(line string) (genericHit, bool) {
	var entry genericEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		// Not a JSON object (or invalid JSON) on this line: sqlite.ts's
		// JSON.parse catch treats this the same way a truncated line does.
		return genericHit{}, false
	}
	if entry.Message == nil || entry.Message.Role == "" {
		return genericHit{}, false
	}
	text := extractContentText(entry.Message.Content)
	if text == "" {
		return genericHit{}, false
	}

	id := entry.UUID
	if id == "" {
		id = entry.ID
	}
	if id == "" {
		id = entry.EntryID
	}

	tsRaw := entry.Timestamp
	if len(tsRaw) == 0 {
		tsRaw = entry.TS
	}

	return genericHit{
		entryID:   id,
		timestamp: toEpochMs(tsRaw),
		role:      entry.Message.Role,
		text:      text,
	}, true
}

// extractContentText mirrors the content-shape branch of sqlite.ts's
// extractText: a bare string is returned as-is; an array is filtered to
// its text blocks, joined with "\n", and trimmed; anything else yields no
// text.
func extractContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// toEpochMs mirrors sqlite.ts's toEpochMs: a JSON number is epoch
// milliseconds already; a JSON string is parsed as ISO-8601 (Date.parse's
// equivalent here is time.Parse with RFC3339Nano, which accepts the
// fractional-second, "Z"-suffixed timestamps Claude Code writes);
// anything else — including an unparseable string, which JS's Date.parse
// would turn into NaN — is 0, not silently corrupted sort order.
func toEpochMs(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		if !math.IsNaN(f) && !math.IsInf(f, 0) {
			return int64(f)
		}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

// nonWordRun splits a query into terms exactly as sqlite.ts's
// toMatchQuery does: `text.toLowerCase().split(/[^\p{L}\p{N}_-]+/u)`.
var nonWordRun = regexp.MustCompile(`[^\p{L}\p{N}_\-]+`)

// toMatchQuery builds an FTS5 MATCH expression from free text, mirroring
// sqlite.ts's toMatchQuery: FTS5 MATCH syntax is a query language and user
// text is not, so every term is quoted (making it a literal phrase) and
// OR'd together. Terms of length 1 are dropped, matching `t.length > 1`.
func toMatchQuery(text string) string {
	lower := strings.ToLower(text)
	terms := nonWordRun.Split(lower, -1)
	var quoted []string
	for _, t := range terms {
		if len([]rune(t)) <= 1 {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, "")+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// SearchEntries returns the entries best matching query, most relevant
// first, mirroring searchEntries in sqlite.ts: bm25's rank is
// lower-is-better, inverted here to score (higher is better) so callers
// don't need to know the ranking backend.
func (s *Search) SearchEntries(ctx context.Context, query string, limit int) ([]EntryHit, error) {
	match := toMatchQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultLimit
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, entry_id, timestamp,
		       snippet(entries, 4, char(171), char(187), char(8230), ?) AS snip,
		       rank
		FROM entries WHERE entries MATCH ? ORDER BY rank LIMIT ?
	`, snippetTokens, match, limit)
	if err != nil {
		return nil, fmt.Errorf("search: search entries: %w", err)
	}
	defer rows.Close()

	var out []EntryHit
	for rows.Next() {
		var hit EntryHit
		var rank float64
		if err := rows.Scan(&hit.SessionID, &hit.EntryID, &hit.Timestamp, &hit.Snippet, &rank); err != nil {
			return nil, fmt.Errorf("search: scan entry hit: %w", err)
		}
		hit.Score = -rank
		out = append(out, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search: scan entry hits: %w", err)
	}
	return out, nil
}

// SearchSessions groups SearchEntries hits down to one, best-scoring hit
// per session, mirroring searchSessions in sqlite.ts: a session matching
// twenty times should appear once, not twenty times, or one verbose
// session crowds out everything else.
func (s *Search) SearchSessions(ctx context.Context, query string, limit int) ([]SessionHit, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	entries, err := s.SearchEntries(ctx, query, limit*4)
	if err != nil {
		return nil, err
	}

	best := make(map[string]EntryHit)
	order := make([]string, 0)
	for _, hit := range entries {
		current, ok := best[hit.SessionID]
		if !ok {
			order = append(order, hit.SessionID)
			best[hit.SessionID] = hit
			continue
		}
		if hit.Score > current.Score {
			best[hit.SessionID] = hit
		}
	}

	out := make([]SessionHit, 0, len(order))
	for _, id := range order {
		hit := best[id]
		out = append(out, SessionHit{SessionID: hit.SessionID, Score: hit.Score, Top: hit})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Remove deletes every indexed row for sessionID, mirroring remove in
// sqlite.ts.
func (s *Search) Remove(sessionID string) error {
	if _, err := s.db.Exec("DELETE FROM entries WHERE session_id = ?", sessionID); err != nil {
		return fmt.Errorf("search: remove entries for %s: %w", sessionID, err)
	}
	if _, err := s.db.Exec("DELETE FROM indexed_files WHERE session_id = ?", sessionID); err != nil {
		return fmt.Errorf("search: remove indexed_files for %s: %w", sessionID, err)
	}
	return nil
}
