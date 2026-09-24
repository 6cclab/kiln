package search

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

const (
	largeFixture   = "../../testdata/sessions/2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1.jsonl"
	minimalFixture = "../../testdata/sessions/2026-09-23T13-15-57-415Z_01a0ce68-9c67-7740-9867-7150069d61e6.jsonl"
	demoFixture    = "../../testdata/sessions/2026-09-23T04-00-59-438Z_01a0cc6c-862e-70d7-b46f-cf4005693013.jsonl"
)

// copyFixtures copies the three testdata fixtures into dir and returns dir,
// so Sync can walk a stable, disposable root.
func copyFixtures(t *testing.T, dir string) {
	t.Helper()
	for _, src := range []string{largeFixture, minimalFixture, demoFixture} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, filepath.Base(src))
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func openTestSearch(t *testing.T) *Search {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "search.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func countIndexedFiles(t *testing.T, s *Search) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM indexed_files").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countEntries(t *testing.T, s *Search) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM entries").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSyncIndexesFixturesAndFindsTerm(t *testing.T) {
	sessDir := t.TempDir()
	copyFixtures(t, sessDir)

	s := openTestSearch(t)
	ctx := context.Background()

	if err := s.Sync(ctx, []string{sessDir}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got := countIndexedFiles(t, s); got != 3 {
		t.Fatalf("indexed_files count = %d, want 3", got)
	}
	if got := countEntries(t, s); got == 0 {
		t.Fatal("entries count = 0, want > 0")
	}

	hits, err := s.SearchEntries(ctx, "codeword", 10)
	if err != nil {
		t.Fatalf("SearchEntries: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits for \"codeword\"")
	}
	hit := hits[0]
	if !strings.Contains(hit.Snippet, "«") || !strings.Contains(hit.Snippet, "»") {
		t.Errorf("snippet %q missing « » markers", hit.Snippet)
	}
	if hit.SessionID != "2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1" {
		t.Errorf("hit session id = %q, want the large-fixture session", hit.SessionID)
	}

	sessHits, err := s.SearchSessions(ctx, "codeword", 10)
	if err != nil {
		t.Fatalf("SearchSessions: %v", err)
	}
	if len(sessHits) != 1 {
		t.Fatalf("SearchSessions returned %d sessions, want 1 (one session mentions the term)", len(sessHits))
	}
}

func TestSyncNoChangesIndexesNothing(t *testing.T) {
	sessDir := t.TempDir()
	copyFixtures(t, sessDir)

	s := openTestSearch(t)
	ctx := context.Background()
	if err := s.Sync(ctx, []string{sessDir}); err != nil {
		t.Fatalf("first Sync: %v", err)
	}

	filesBefore := countIndexedFiles(t, s)
	entriesBefore := countEntries(t, s)

	if err := s.Sync(ctx, []string{sessDir}); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	if got := countIndexedFiles(t, s); got != filesBefore {
		t.Errorf("indexed_files count changed on no-op sync: %d -> %d", filesBefore, got)
	}
	if got := countEntries(t, s); got != entriesBefore {
		t.Errorf("entries count changed on no-op sync: %d -> %d", entriesBefore, got)
	}
}

func TestSyncReindexesChangedFileWithoutDuplicates(t *testing.T) {
	sessDir := t.TempDir()
	copyFixtures(t, sessDir)

	s := openTestSearch(t)
	ctx := context.Background()
	if err := s.Sync(ctx, []string{sessDir}); err != nil {
		t.Fatalf("first Sync: %v", err)
	}

	beforeHits, err := s.SearchEntries(ctx, "codeword", 10)
	if err != nil {
		t.Fatal(err)
	}
	beforeCount := len(beforeHits)

	// Append a new transaction with a unique word to the minimal fixture's
	// copy, sleeping past filesystem mtime resolution isn't needed: the
	// appended bytes change the file's size, which Sync's size+mtime check
	// picks up regardless of mtime granularity.
	target := filepath.Join(sessDir, filepath.Base(minimalFixture))
	entry := session.Entry{
		ID:   "test-entry-glorbnaxriffle",
		Type: session.EntryMessage,
		Message: msg.UserMessage{
			Role:      msg.RoleUser,
			Timestamp: 1790169999999,
			Content:   msg.Blocks{msg.Text("please remember the word glorbnaxriffle")},
		},
		Seq:       4,
		Timestamp: 1790169999999,
	}
	line, err := jsonl.SerializeTransaction([]session.CommittedWrite{{Kind: "entry", Entry: &entry}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := s.Sync(ctx, []string{sessDir}); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	afterHits, err := s.SearchEntries(ctx, "codeword", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterHits) != beforeCount {
		t.Errorf("\"codeword\" hits changed after re-sync: %d -> %d (want unchanged, no duplicates)", beforeCount, len(afterHits))
	}

	newHits, err := s.SearchEntries(ctx, "glorbnaxriffle", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(newHits) != 1 {
		t.Fatalf("\"glorbnaxriffle\" hits = %d, want exactly 1 (no duplicates from re-index)", len(newHits))
	}
	if got := countIndexedFiles(t, s); got != 3 {
		t.Errorf("indexed_files count = %d, want 3 (file count unchanged across re-sync)", got)
	}
}

func TestClaudeCodeFormat(t *testing.T) {
	sessDir := t.TempDir()
	projDir := filepath.Join(sessDir, "-Users-tester-projects-demo")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	lines := []string{
		`{"type":"mode","mode":"normal","sessionId":"cc-session-1"}`,
		`{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"where did we leave the plutonium-flavored yak"},"uuid":"cc-uuid-1","timestamp":"2026-09-16T17:31:55.300Z","sessionId":"cc-session-1"}`,
		`{"parentUuid":"cc-uuid-1","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"The plutonium-flavored yak is in the barn."}]},"uuid":"cc-uuid-2","timestamp":"2026-09-16T17:32:10.500Z","sessionId":"cc-session-1"}`,
	}
	target := filepath.Join(projDir, "cc-session-1.jsonl")
	if err := os.WriteFile(target, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := openTestSearch(t)
	ctx := context.Background()
	if err := s.Sync(ctx, []string{sessDir}); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	hits, err := s.SearchEntries(ctx, "plutonium yak", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 (one user, one assistant entry)", len(hits))
	}
	for _, h := range hits {
		if h.SessionID != "cc-session-1" {
			t.Errorf("hit session id = %q, want cc-session-1", h.SessionID)
		}
		if h.Timestamp == 0 {
			t.Error("ISO timestamp parsed to epoch 0")
		}
	}
}

func TestRemove(t *testing.T) {
	sessDir := t.TempDir()
	copyFixtures(t, sessDir)

	s := openTestSearch(t)
	ctx := context.Background()
	if err := s.Sync(ctx, []string{sessDir}); err != nil {
		t.Fatal(err)
	}

	sessionID := "2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1"
	hitsBefore, err := s.SearchEntries(ctx, "codeword", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hitsBefore) == 0 {
		t.Fatal("expected hits before Remove")
	}

	if err := s.Remove(sessionID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	hitsAfter, err := s.SearchEntries(ctx, "codeword", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hitsAfter) != 0 {
		t.Errorf("hits after Remove = %d, want 0", len(hitsAfter))
	}

	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM indexed_files WHERE session_id = ?", sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("indexed_files rows for removed session = %d, want 0", n)
	}
}

func TestConcurrentOpenSameDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "search.db")

	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	defer s1.Close()

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer s2.Close()

	sessDir := t.TempDir()
	copyFixtures(t, sessDir)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- s1.Sync(context.Background(), []string{sessDir})
	}()
	go func() {
		defer wg.Done()
		errs <- s2.Sync(context.Background(), []string{sessDir})
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Sync failed: %v", err)
		}
	}

	hits, err := s1.SearchEntries(context.Background(), "codeword", 10)
	if err != nil {
		t.Fatalf("SearchEntries after concurrent sync: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected hits after concurrent sync")
	}
}

// TestLiveDB opens the real ~/.harness/search.db strictly read-only (a
// "mode=ro" DSN, not Search.Open, which would set PRAGMA journal_mode=WAL —
// a write — against a file this change must never touch) and runs one MATCH
// query, reporting only the hit count. Guarded by HARNESS_SEARCH_LIVE=1 and
// skipped (never failed) if the db doesn't exist, since this environment's
// real search.db is out of scope for what this change can assert about.
func TestLiveDB(t *testing.T) {
	if os.Getenv("HARNESS_SEARCH_LIVE") != "1" {
		t.Skip("set HARNESS_SEARCH_LIVE=1 to run against the real ~/.harness/search.db")
	}
	path := DefaultDBPath()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no live db at %s: %v", path, err)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open live db read-only: %v", err)
	}
	defer db.Close()

	match := toMatchQuery("the")
	rows, err := db.Query(`SELECT session_id FROM entries WHERE entries MATCH ? ORDER BY rank LIMIT 10`, match)
	if err != nil {
		t.Fatalf("query live db: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scan live db results: %v", err)
	}
	t.Logf("live db query hit count: %d", n)
}
