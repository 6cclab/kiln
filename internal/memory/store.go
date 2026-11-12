// Package memory stores scoped, versioned observations separately from chat history.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/andrepato/harness/internal/diag"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Record is an attributed, scoped observation and its correction lineage.
type Record struct {
	Sources    []Source `json:"sources,omitempty"`
	ID         int64    `json:"id"`
	Scope      string   `json:"scope"`
	Key        string   `json:"key"`
	Kind       string   `json:"kind"`
	Text       string   `json:"text"`
	Evidence   string   `json:"evidence"`
	Created    int64    `json:"created"`
	Expires    int64    `json:"expires,omitempty"`
	Supersedes int64    `json:"supersedes,omitempty"`
}

// Store owns the private SQLite database and serializes its transactions.
type Store struct{ db *sql.DB }

// GlobalPreferencesScope is reserved for preferences explicitly stored by the user.
// Model tools are bound to a project scope and cannot write to it.
const GlobalPreferencesScope = "user:preferences"

// Open creates or opens a private memory database.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// Create with private permissions before SQLite opens it.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS memories(id INTEGER PRIMARY KEY, scope TEXT NOT NULL, key TEXT NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, evidence TEXT NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL DEFAULT 0, supersedes INTEGER NOT NULL DEFAULT 0, active INTEGER NOT NULL DEFAULT 1);
 CREATE TABLE IF NOT EXISTS memory_sources(memory_id INTEGER PRIMARY KEY, sources TEXT NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS memory_current ON memories(scope,key) WHERE active=1;
 CREATE VIRTUAL TABLE IF NOT EXISTS memory_search USING fts5(text, evidence);
 PRAGMA user_version=1;`)
	if err != nil {
		diag.Close(db)
		return nil, err
	}
	return &Store{db}, nil
}

// Close releases the database connection.
func (s *Store) Close() error { return s.db.Close() }

// Put supersedes a key atomically, retaining the previous record for inspection.
// Evidence is attribution, never an automatic claim of verified correctness.
func (s *Store) Put(ctx context.Context, r Record) (int64, error) {
	if r.Scope == "" || strings.TrimSpace(r.Key) == "" || strings.TrimSpace(r.Text) == "" || strings.TrimSpace(r.Evidence) == "" {
		return 0, errors.New("scope, key, text and evidence are required")
	}
	if len(r.Key) > 256 || len(r.Text) > 8192 || len(r.Evidence) > 2048 {
		return 0, errors.New("memory exceeds field limits")
	}
	switch r.Kind {
	case "preference", "fact", "procedure", "observation":
	default:
		return 0, errors.New("invalid memory kind")
	}
	if r.Scope == GlobalPreferencesScope && (r.Kind != "preference" || len(r.Sources) != 0) {
		return 0, errors.New("global memory accepts explicit preferences without project file sources only")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	var previous int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM memories WHERE scope=? AND key=? AND active=1`, r.Scope, r.Key).Scan(&previous)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memories SET active=0 WHERE scope=? AND key=? AND active=1`, r.Scope, r.Key); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO memories(scope,key,kind,text,evidence,created,expires,supersedes) VALUES(?,?,?,?,?,?,?,?)`, r.Scope, r.Key, r.Kind, r.Text, r.Evidence, time.Now().Unix(), r.Expires, previous)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO memory_search(rowid,text,evidence) VALUES(?,?,?)`, id, r.Key+"\n"+r.Text, r.Evidence); err != nil {
		return 0, err
	}
	if len(r.Sources) > 0 {
		raw, err := json.Marshal(r.Sources)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_sources(memory_id,sources) VALUES(?,?)`, id, string(raw)); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// Current returns the current record for one key, honoring expiry and source freshness.
func (s *Store) Current(ctx context.Context, scope, key, projectRoot string) (*Record, error) {
	var r Record
	var sources string
	err := s.db.QueryRowContext(ctx, `SELECT id,scope,key,kind,text,evidence,created,expires,supersedes,COALESCE((SELECT sources FROM memory_sources WHERE memory_id=memories.id),'[]') FROM memories WHERE scope=? AND key=? AND active=1 AND (expires=0 OR expires>?)`, scope, key, time.Now().Unix()).Scan(&r.ID, &r.Scope, &r.Key, &r.Kind, &r.Text, &r.Evidence, &r.Created, &r.Expires, &r.Supersedes, &sources)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(sources), &r.Sources); err != nil {
		return nil, err
	}
	if !SourcesCurrentAt(r.Sources, projectRoot) {
		return nil, nil
	}
	return &r, nil
}

// SearchWithGlobal merges explicit user preferences into a project search.
// A current project record with the same key always takes precedence.
func (s *Store) SearchWithGlobal(ctx context.Context, projectScope, query string, limit int, projectRoot string) ([]Record, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	project, err := s.Search(ctx, projectScope, query, limit, projectRoot)
	if err != nil {
		return nil, err
	}
	if len(project) >= limit {
		return project, nil
	}
	global, err := s.Search(ctx, GlobalPreferencesScope, query, limit)
	if err != nil {
		return nil, err
	}
	for _, r := range global {
		if r.Kind != "preference" {
			continue
		}
		current, err := s.Current(ctx, projectScope, r.Key, projectRoot)
		if err != nil {
			return nil, err
		}
		if current != nil {
			continue
		}
		project = append(project, r)
		if len(project) == limit {
			break
		}
	}
	return project, nil
}

// Search returns current, non-expired observations ranked by lexical relevance.
func (s *Store) Search(ctx context.Context, scope, query string, limit int, projectRoot ...string) ([]Record, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	// Quote terms individually: user input is text, never FTS syntax.
	var terms []string
	for _, term := range strings.Fields(query) {
		terms = append(terms, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		if len(terms) == 32 {
			break
		}
	}
	base := `SELECT m.id,m.scope,m.key,m.kind,m.text,m.evidence,m.created,m.expires,m.supersedes,COALESCE((SELECT sources FROM memory_sources WHERE memory_id=m.id),'[]') FROM memories m `
	var args []any
	if len(terms) > 0 {
		base += `JOIN memory_search ON memory_search.rowid=m.id WHERE memory_search MATCH ? AND `
		args = append(args, strings.Join(terms, " OR "))
	} else {
		base += `WHERE `
	}
	base += `m.scope=? AND m.active=1 AND (m.expires=0 OR m.expires>?) ORDER BY `
	if len(terms) > 0 {
		base += `bm25(memory_search),`
	}
	base += `m.created DESC,m.id DESC LIMIT ?`
	args = append(args, scope, time.Now().Unix(), limit*10)
	rows, err := s.db.QueryContext(ctx, base, args...)
	if err != nil {
		return nil, err
	}
	defer diag.Close(rows)
	out := []Record{}
	for rows.Next() {
		var r Record
		var sources string
		if err := rows.Scan(&r.ID, &r.Scope, &r.Key, &r.Kind, &r.Text, &r.Evidence, &r.Created, &r.Expires, &r.Supersedes, &sources); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sources), &r.Sources); err != nil {
			return nil, err
		}
		root := ""
		if len(projectRoot) > 0 {
			root = projectRoot[0]
		}
		if SourcesCurrentAt(r.Sources, root) {
			out = append(out, r)
			if len(out) == limit {
				break
			}
		}
	}
	return out, rows.Err()
}

// Forget removes all versions and their search entries; correction history must
// not make forgotten material discoverable again.
func (s *Store) Forget(ctx context.Context, scope, key string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.ExecContext(ctx, `DELETE FROM memory_search WHERE rowid IN (SELECT id FROM memories WHERE scope=? AND key=?)`, scope, key); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM memory_sources WHERE memory_id IN (SELECT id FROM memories WHERE scope=? AND key=?)`, scope, key); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM memories WHERE scope=? AND key=?`, scope, key); err != nil {
		return err
	}
	return tx.Commit()
}

// ForgetScope permanently removes all records in one scope.
func (s *Store) ForgetScope(ctx context.Context, scope string) error {
	if scope == "" {
		return errors.New("scope is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_search WHERE rowid IN (SELECT id FROM memories WHERE scope=?)`, scope); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_sources WHERE memory_id IN (SELECT id FROM memories WHERE scope=?)`, scope); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE scope=?`, scope); err != nil {
		return err
	}
	return tx.Commit()
}

// PutUniquePruned atomically inserts a new key once and retains only the
// newest limit records in its scope. It never creates correction history.
func (s *Store) PutUniquePruned(ctx context.Context, r Record, limit int) (bool, error) {
	validScope := (strings.HasPrefix(r.Scope, "outcome:") && r.Kind == "observation") || (strings.HasPrefix(r.Scope, "outcome-dismissed:") && r.Kind == "procedure")
	if !validScope || strings.TrimSpace(r.Key) == "" || strings.TrimSpace(r.Text) == "" || strings.TrimSpace(r.Evidence) == "" || len(r.Key) > 256 || len(r.Text) > 8192 || len(r.Evidence) > 2048 || limit < 1 || limit > 1000 || len(r.Sources) != 0 {
		return false, errors.New("invalid unique retained record")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memories(scope,key,kind,text,evidence,created,expires,supersedes) VALUES(?,?,?,?,?,?,0,0)`, r.Scope, r.Key, r.Kind, r.Text, r.Evidence, time.Now().Unix())
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_search(rowid,text,evidence) VALUES(?,?,?)`, id, r.Key+"\n"+r.Text, r.Evidence); err != nil {
			return false, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM memories WHERE scope=? ORDER BY id DESC LIMIT -1 OFFSET ?`, r.Scope, limit)
	if err != nil {
		return false, err
	}
	var old []int64
	for rows.Next() {
		var oldID int64
		if err := rows.Scan(&oldID); err != nil {
			return false, errors.Join(err, rows.Close())
		}
		old = append(old, oldID)
	}
	if err := rows.Err(); err != nil {
		return false, errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, oldID := range old {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_search WHERE rowid=?`, oldID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_sources WHERE memory_id=?`, oldID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE id=? AND scope=?`, oldID, r.Scope); err != nil {
			return false, err
		}
	}
	return affected != 0, tx.Commit()
}

// Recent returns up to limit current outcome records, newest first. Outcome
// records cannot have file sources, so this narrow reader omits that table.
func (s *Store) Recent(ctx context.Context, scope string, limit int) ([]Record, error) {
	if !strings.HasPrefix(scope, "outcome:") || limit < 1 || limit > 1000 {
		return nil, errors.New("invalid recent-record query")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,scope,key,kind,text,evidence,created,expires,supersedes FROM memories WHERE scope=? AND active=1 AND (expires=0 OR expires>?) ORDER BY id DESC LIMIT ?`, scope, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer diag.Close(rows)
	out := make([]Record, 0)
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Scope, &r.Key, &r.Kind, &r.Text, &r.Evidence, &r.Created, &r.Expires, &r.Supersedes); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Export includes historical versions for inspection and recovery.
func (s *Store) Export(ctx context.Context, scope string) ([]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,scope,key,kind,text,evidence,created,expires,supersedes,COALESCE((SELECT sources FROM memory_sources WHERE memory_id=memories.id),'[]') FROM memories WHERE scope=? ORDER BY id`, scope)
	if err != nil {
		return nil, err
	}
	defer diag.Close(rows)
	out := []Record{}
	for rows.Next() {
		var r Record
		var sources string
		if err := rows.Scan(&r.ID, &r.Scope, &r.Key, &r.Kind, &r.Text, &r.Evidence, &r.Created, &r.Expires, &r.Supersedes, &sources); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sources), &r.Sources); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(out, "", "  ")
}

// Restore copies a historical version into a new current record. The newer
// history remains inspectable; forgetting a key removes restore candidates too.
func (s *Store) Restore(ctx context.Context, scope string, id int64) (int64, error) {
	var r Record
	err := s.db.QueryRowContext(ctx, `SELECT key,kind,text,evidence,expires FROM memories WHERE scope=? AND id=?`, scope, id).Scan(&r.Key, &r.Kind, &r.Text, &r.Evidence, &r.Expires)
	if err != nil {
		return 0, err
	}
	r.Scope = scope
	var sources string
	err = s.db.QueryRowContext(ctx, `SELECT sources FROM memory_sources WHERE memory_id=?`, id).Scan(&sources)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if err == nil {
		if err := json.Unmarshal([]byte(sources), &r.Sources); err != nil {
			return 0, err
		}
	}
	return s.Put(ctx, r)
}

// A successful commit makes deferred rollback return ErrTxDone. Other cleanup
// failures are recorded without hiding the operation's original error.
func rollback(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		diag.L().Warn("memory rollback failed", "error", err)
	}
}
