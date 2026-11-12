package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryScopeCorrectionExpiryForget(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := context.Background()
	put := func(scope, text string, expires int64) int64 {
		t.Helper()
		id, err := s.Put(ctx, Record{Scope: scope, Key: "test", Kind: "fact", Text: text, Evidence: "user correction", Expires: expires})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := put("a", "uses npm", 0)
	put("b", "uses yarn", 0)
	put("a", "uses pnpm", 0)
	got, err := s.Search(ctx, "a", "pnpm", 5)
	if err != nil || len(got) != 1 || got[0].Supersedes != first {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = s.Search(ctx, "a", "npm", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("stale: %+v %v", got, err)
	}
	exported, err := s.Export(ctx, "a")
	if err != nil || !strings.Contains(string(exported), "uses npm") || strings.Contains(string(exported), "yarn") {
		t.Fatalf("export %s %v", exported, err)
	}
	put("a", "expired", time.Now().Add(-time.Hour).Unix())
	got, err = s.Search(ctx, "a", "", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("expired: %+v %v", got, err)
	}
	if err := s.Forget(ctx, "a", "test"); err != nil {
		t.Fatal(err)
	}
	exported, err = s.Export(ctx, "a")
	if err != nil || string(exported) != "[]" {
		t.Fatalf("forgotten: %s %v", exported, err)
	}
	got, err = s.Search(ctx, "b", "yarn", 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("other scope: %+v %v", got, err)
	}
}

func TestAutomaticFactMetadataMigratesOnceAndForgetCleansIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := CaptureSources(root, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{Scope: "project", Key: "verified-command:legacy", Kind: "procedure", Text: "Observed successful verification command: go test ./...", Evidence: "Successful Bash command in opted-in outcome legacy; call legacy", Sources: sources, Expires: time.Now().Add(time.Hour).Unix()}
	if stored, err := s.PutIfAbsent(ctx, record); err != nil || !stored {
		t.Fatalf("initial fact: %v %v", stored, err)
	}
	if _, err := s.db.Exec(`DELETE FROM memory_automatic_facts; PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memory_automatic_facts WHERE scope='project'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy marker migration: %d %v", n, err)
	}
	if err := s.Forget(ctx, "project", record.Key); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memory_automatic_facts WHERE scope='project'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("forgotten metadata retained: %d %v", n, err)
	}
}

func TestRestoreCannotCrossScopes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := context.Background()
	id, err := s.Put(ctx, Record{Scope: "a", Key: "runner", Kind: "preference", Text: "go test", Evidence: "explicit request"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Restore(ctx, "b", id); err == nil {
		t.Fatal("cross-project restore")
	}
	if _, err = s.Put(ctx, Record{Scope: "a", Key: "runner", Kind: "preference", Text: "make check", Evidence: "correction"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Restore(ctx, "a", id); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, "a", "", 1)
	if err != nil || len(got) != 1 || got[0].Text != "go test" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSourceChangesInvalidateRecallAndRestore(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := CaptureSources(root, []string{"config.txt"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := context.Background()
	id, err := s.Put(ctx, Record{Scope: "project", Key: "config", Kind: "fact", Text: "uses old config", Evidence: "config.txt", Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, "project", "config", 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("initial: %v %v", got, err)
	}
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = s.Search(ctx, "project", "config", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("stale: %v %v", got, err)
	}
	if _, err := s.Restore(ctx, "project", id); err != nil {
		t.Fatal(err)
	}
	got, err = s.Search(ctx, "project", "config", 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("restore lost evidence binding: %v %v", got, err)
	}
	if _, err := CaptureSources(root, []string{filepath.Join(t.TempDir(), "outside")}); err == nil {
		t.Fatal("outside source accepted")
	}
}

func TestSourcesAreRecheckedInCurrentWorktree(t *testing.T) {
	origin, current := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(origin, "config"), []byte("A"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "config"), []byte("B"), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := CaptureSources(origin, []string{"config"})
	if err != nil {
		t.Fatal(err)
	}
	if !SourcesCurrentAt(sources, origin) {
		t.Fatal("original evidence rejected")
	}
	if SourcesCurrentAt(sources, current) {
		t.Fatal("different worktree accepted stale fact")
	}
}

func TestGlobalPreferenceExplicitScopePrecedenceAndForget(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	if _, err := s.Put(ctx, Record{Scope: GlobalPreferencesScope, Key: "test", Kind: "fact", Text: "go test", Evidence: "model assertion"}); err == nil {
		t.Fatal("global fact accepted")
	}
	id, err := s.Put(ctx, Record{Scope: GlobalPreferencesScope, Key: "test", Kind: "preference", Text: "Use go test", Evidence: "explicit user command"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SearchWithGlobal(ctx, "project-a", "test", 5, "")
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatalf("global read: %+v %v", got, err)
	}
	corrected, err := s.Put(ctx, Record{Scope: GlobalPreferencesScope, Key: "test", Kind: "preference", Text: "Use go test -race", Evidence: "explicit user correction"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.SearchWithGlobal(ctx, "project-b", "test", 5, "")
	if err != nil || len(got) != 1 || got[0].ID != corrected || got[0].Supersedes != id {
		t.Fatalf("global correction: %+v %v", got, err)
	}
	if _, err := s.Put(ctx, Record{Scope: "project-a", Key: "test", Kind: "preference", Text: "Use make check for tests", Evidence: "explicit project command"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.SearchWithGlobal(ctx, "project-a", "test", 5, "")
	if err != nil || len(got) != 1 || got[0].Scope != "project-a" {
		t.Fatalf("project precedence: %+v %v", got, err)
	}
	got, err = s.SearchWithGlobal(ctx, "project-b", "test", 5, "")
	if err != nil || len(got) != 1 || got[0].ID != corrected {
		t.Fatalf("project isolation: %+v %v", got, err)
	}
	if err := s.Forget(ctx, GlobalPreferencesScope, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(ctx, GlobalPreferencesScope, id); err == nil {
		t.Fatal("forgotten global preference restored")
	}
	got, err = s.SearchWithGlobal(ctx, "project-b", "test", 5, "")
	if err != nil || len(got) != 0 {
		t.Fatalf("forgotten global preference visible: %+v %v", got, err)
	}
	got, err = s.SearchWithGlobal(ctx, "project-a", "test", 5, "")
	if err != nil || len(got) != 1 || got[0].Scope != "project-a" {
		t.Fatalf("global forget changed project: %+v %v", got, err)
	}
}
