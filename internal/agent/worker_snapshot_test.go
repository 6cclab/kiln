package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

func snapshotArtifact(t *testing.T, repo, name, content string) WorkerArtifact {
	t.Helper()
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(env.Cwd, "a.txt")); err != nil || string(got) != "parent unstaged\n" {
		t.Fatalf("worker baseline = %q, %v", got, err)
	}
	parentContent, err := os.ReadFile(filepath.Join(repo, "parent.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(env.Cwd, "parent.txt")); err != nil || !bytes.Equal(got, parentContent) {
		t.Fatalf("worker untracked baseline = %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(env.Cwd, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := w.artifact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Fatal("missing worker patch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(a.ManifestPath)) })
	return *a
}

func dirtySnapshotRepo(t *testing.T) string {
	t.Helper()
	repo := testRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("parent staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "a.txt")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("parent unstaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ignored.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo
}

func indexBytes(t *testing.T, repo string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDirtyWorkerSnapshotApplyPreservesIndex(t *testing.T) {
	repo := dirtySnapshotRepo(t)
	before := indexBytes(t, repo)
	a := snapshotArtifact(t, repo, "a.txt", "worker\n")
	if a.Baseline == nil || !samePaths(a.ChangedFiles, []string{"a.txt"}) {
		t.Fatalf("artifact = %+v", a)
	}
	if !bytes.Equal(indexBytes(t, repo), before) {
		t.Fatal("snapshot changed parent index")
	}
	patch, err := os.ReadFile(a.PatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(patch, []byte("parent staged")) || bytes.Contains(patch, []byte("parent new")) {
		t.Fatalf("worker patch included parent changes: %s", patch)
	}
	if err := ApplyWorkerArtifact(context.Background(), repo, a); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexBytes(t, repo), before) {
		t.Fatal("apply changed parent index")
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "worker\n" {
		t.Fatalf("applied content = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "parent.txt")); string(got) != "parent new\n" {
		t.Fatalf("parent untracked = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "ignored.txt")); string(got) != "secret\n" {
		t.Fatalf("ignored file = %q", got)
	}
}

func TestDirtyWorkerSnapshotRejectsDriftAndTamper(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string, *WorkerArtifact)
	}{
		{"worktree", func(t *testing.T, repo string, _ *WorkerArtifact) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("other\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"index", func(t *testing.T, repo string, _ *WorkerArtifact) { t.Helper(); testGit(t, repo, "add", "a.txt") }},
		{"untracked", func(t *testing.T, repo string, _ *WorkerArtifact) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("other\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"new parent file", func(t *testing.T, repo string, _ *WorkerArtifact) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(repo, "other.txt"), []byte("new\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"baseline metadata", func(t *testing.T, _ string, a *WorkerArtifact) {
			t.Helper()
			a.Baseline.State = strings.Repeat("0", 64)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := dirtySnapshotRepo(t)
			a := snapshotArtifact(t, repo, "a.txt", "worker\n")
			before := indexBytes(t, repo)
			tc.change(t, repo, &a)
			if err := ApplyWorkerArtifact(context.Background(), repo, a); err == nil {
				t.Fatal("accepted stale or tampered baseline")
			}
			if tc.name != "index" && !bytes.Equal(indexBytes(t, repo), before) {
				t.Fatal("failed apply changed index")
			}
			if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) == "worker\n" {
				t.Fatal("failed apply changed worktree")
			}
		})
	}
}

func TestDirtyWorkerSnapshotBatchAndCancellation(t *testing.T) {
	repo := dirtySnapshotRepo(t)
	a := snapshotArtifact(t, repo, "one.txt", "one\n")
	b := snapshotArtifact(t, repo, "two.txt", "two\n")
	if !sameWorkerBaseline(a.Baseline, b.Baseline) {
		t.Fatal("same parent state produced different baselines")
	}
	before := indexBytes(t, repo)
	if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a, b}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexBytes(t, repo), before) {
		t.Fatal("batch changed index")
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := newWorkerWorktreeWithSnapshot(ctx, execenv.New(repo), true); err == nil {
		t.Fatal("cancelled snapshot succeeded")
	}
}

func TestDirtyWorkerSnapshotRejectsSpecialFilesAndFilters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"symlink", func(t *testing.T, repo string) {
			t.Helper()
			if err := os.Symlink("a.txt", filepath.Join(repo, "link.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"filter", func(t *testing.T, repo string) {
			t.Helper()
			testGit(t, repo, "config", "filter.danger.clean", "sh -c 'echo run > marker; cat'")
			if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("a.txt filter=danger\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := dirtySnapshotRepo(t)
			tc.change(t, repo)
			w, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
			if w != nil {
				t.Cleanup(func() {
					if err := w.close(); err != nil {
						t.Error(err)
					}
				})
			}
			if tc.name == "symlink" && err == nil {
				t.Fatal("accepted symlink")
			}
			if tc.name == "filter" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(repo, "marker")); !os.IsNotExist(err) {
					t.Fatal("clean filter ran")
				}
			}
		})
	}
}

func TestDirtyWorkerSnapshotManifestBaselineMismatch(t *testing.T) {
	repo := dirtySnapshotRepo(t)
	a := snapshotArtifact(t, repo, "one.txt", "one\n")
	loaded, err := LoadWorkerArtifact(a.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Baseline = &WorkerBaseline{Tree: a.Baseline.Tree, State: strings.Repeat("0", 64)}
	if err := ApplyWorkerArtifact(context.Background(), repo, loaded); err == nil {
		t.Fatal("accepted changed in-memory baseline")
	}
	data, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.ManifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyWorkerArtifact(context.Background(), repo, a); err == nil {
		t.Fatal("accepted changed manifest")
	}
}

func TestDirtyWorkerSnapshotIgnoredExcluded(t *testing.T) {
	repo := dirtySnapshotRepo(t)
	// An ignored path is not copied into the worker and remains untouched.
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(filepath.Join(env.Cwd, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatal("ignored file entered snapshot")
	}
}

func TestDirtyWorkerSnapshotDoesNotExportLineEndingNormalization(t *testing.T) {
	repo := testRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt text eol=lf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("parent\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	if got, _ := os.ReadFile(filepath.Join(env.Cwd, "a.txt")); string(got) != "parent\r\n" {
		t.Fatalf("snapshot lost raw line endings: %q", got)
	}
	a, err := w.artifact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a != nil {
		t.Fatalf("unchanged file exported normalization: %+v", a)
	}
}

func TestDirtyWorkerSnapshotStagedRename(t *testing.T) {
	repo := testRepo(t)
	testGit(t, repo, "mv", "a.txt", "renamed.txt")
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(filepath.Join(env.Cwd, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("renamed source remained in worker")
	}
	if got, err := os.ReadFile(filepath.Join(env.Cwd, "renamed.txt")); err != nil || string(got) != "base\n" {
		t.Fatalf("renamed destination = %q, %v", got, err)
	}
}

func TestDirtyWorkerSnapshotRejectsIndexFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"assume unchanged", []string{"update-index", "--assume-unchanged", "a.txt"}},
		{"skip worktree", []string{"update-index", "--skip-worktree", "a.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			testGit(t, repo, tc.args...)
			if _, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true); err == nil {
				t.Fatal("accepted hidden index state")
			}
		})
	}
}

func TestDirtyWorkerSnapshotEmptyIndexWithUntrackedFile(t *testing.T) {
	repo := t.TempDir()
	testGit(t, repo, "init", "-q")
	testGit(t, repo, "config", "user.name", "Kiln Test")
	testGit(t, repo, "config", "user.email", "kiln@example.test")
	testGit(t, repo, "commit", "--allow-empty", "-qm", "empty")
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("parent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	if got, err := os.ReadFile(filepath.Join(env.Cwd, "new.txt")); err != nil || string(got) != "parent\n" {
		t.Fatalf("untracked baseline = %q, %v", got, err)
	}
}

func TestDirtyWorkerSnapshotWorkerEditsParentUntracked(t *testing.T) {
	repo := dirtySnapshotRepo(t)
	a := snapshotArtifact(t, repo, "parent.txt", "worker edits untracked\n")
	before := indexBytes(t, repo)
	if err := ApplyWorkerArtifact(context.Background(), repo, a); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "parent.txt")); string(got) != "worker edits untracked\n" {
		t.Fatalf("untracked edit = %q", got)
	}
	if !bytes.Equal(indexBytes(t, repo), before) {
		t.Fatal("untracked apply changed index")
	}
}

func TestDirtyWorkerSnapshotSHA256Deletion(t *testing.T) {
	repo := t.TempDir()
	testGit(t, repo, "init", "-q", "--object-format=sha256")
	testGit(t, repo, "config", "user.name", "Kiln Test")
	testGit(t, repo, "config", "user.email", "kiln@example.test")
	if err := os.WriteFile(filepath.Join(repo, "gone.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "gone.txt")
	testGit(t, repo, "commit", "-qm", "base")
	if err := os.Remove(filepath.Join(repo, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(filepath.Join(env.Cwd, "gone.txt")); !os.IsNotExist(err) {
		t.Fatal("deleted parent file appeared in worker")
	}
}

func TestDirtyWorkerSnapshotLiteralRestorePath(t *testing.T) {
	repo := testRepo(t)
	name := "literal[1].txt"
	if err := os.WriteFile(filepath.Join(repo, name), []byte("parent\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt text eol=lf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	a, err := w.artifact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a != nil {
		t.Fatalf("unchanged literal path exported: %+v", a)
	}
}

func TestDirtyWorkerSnapshotBoundsAndSpecialFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{"bytes", func(t *testing.T, repo string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(repo, "large.bin"), bytes.Repeat([]byte{'x'}, maxWorkerSnapshotBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "exceeds"},
		{"files", func(t *testing.T, repo string) {
			t.Helper()
			for i := 0; i <= maxWorkerSnapshotFiles; i++ {
				name := filepath.Join(repo, fmt.Sprintf("file-%04d", i))
				if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}, "files"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			tc.prepare(t, repo)
			before := indexBytes(t, repo)
			_, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("snapshot error = %v, want %q", err, tc.want)
			}
			if !bytes.Equal(indexBytes(t, repo), before) {
				t.Fatal("rejected snapshot changed index")
			}
		})
	}
}

func TestDirtyWorkerSnapshotRejectsChangedHEADAndMixedBatch(t *testing.T) {
	repo := dirtySnapshotRepo(t)
	a := snapshotArtifact(t, repo, "one.txt", "one\n")
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("different baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := snapshotArtifact(t, repo, "two.txt", "two\n")
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := indexBytes(t, repo)
	if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a, b}); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("mixed baseline error = %v", err)
	}
	if !bytes.Equal(indexBytes(t, repo), before) {
		t.Fatal("mixed batch changed index")
	}
	testGit(t, repo, "add", "-A")
	testGit(t, repo, "commit", "-qm", "move head")
	if err := ApplyWorkerArtifact(context.Background(), repo, b); err == nil {
		t.Fatal("accepted changed HEAD")
	}
	if _, err := os.Stat(filepath.Join(repo, "two.txt")); !os.IsNotExist(err) {
		t.Fatal("changed HEAD apply wrote worker file")
	}
}

func TestDirtyWorkerSnapshotDeletedParentNoop(t *testing.T) {
	repo := testRepo(t)
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Stat(filepath.Join(env.Cwd, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("deleted parent file appeared")
	}
	a, err := w.artifact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a != nil {
		t.Fatalf("deleted parent exported as worker patch: %+v", a)
	}
}

func TestDirtyWorkerSnapshotExcludesUntrackedFIFO(t *testing.T) {
	repo := testRepo(t)
	if err := syscall.Mkfifo(filepath.Join(repo, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := os.Lstat(filepath.Join(env.Cwd, "pipe")); !os.IsNotExist(err) {
		t.Fatal("special parent file appeared in worker")
	}
}

func TestDirtyWorkerSnapshotRejectsUnsupportedIndexStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{"staged symlink", func(t *testing.T, repo string) {
			t.Helper()
			if err := os.Symlink("a.txt", filepath.Join(repo, "link")); err != nil {
				t.Fatal(err)
			}
			testGit(t, repo, "add", "link")
		}, "index mode"},
		{"unmerged", func(t *testing.T, repo string) {
			t.Helper()
			mainBranch := testGit(t, repo, "symbolic-ref", "--short", "HEAD")
			testGit(t, repo, "checkout", "-qb", "side")
			if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("side\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			testGit(t, repo, "commit", "-qam", "side")
			testGit(t, repo, "checkout", "-q", mainBranch)
			if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("main\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			testGit(t, repo, "commit", "-qam", "main")
			cmd := exec.Command("git", "merge", "--no-commit", "side")
			cmd.Dir = repo
			if err := cmd.Run(); err == nil {
				t.Fatal("expected merge conflict")
			}
		}, "unmerged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			tc.prepare(t, repo)
			_, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("snapshot error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDirtyWorkerSnapshotRejectsInvalidInputs(t *testing.T) {
	if _, _, err := newWorkerWorktreeWithSnapshot(context.Background(), nil, true); err == nil {
		t.Fatal("nil environment accepted")
	}
	if _, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(t.TempDir()), true); err == nil {
		t.Fatal("non-repository accepted")
	}
	repo := testRepo(t)
	if err := os.Mkdir(filepath.Join(repo, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(filepath.Join(repo, "subdir")), true); err == nil {
		t.Fatal("non-root cwd accepted")
	}
	if _, err := batchParentHead(context.Background(), repo, &WorkerBaseline{Tree: "bad", State: "bad"}); err == nil {
		t.Fatal("malformed baseline accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := newWorkerWorktreeWithSnapshot(ctx, execenv.New(repo), true); err == nil {
		t.Fatal("cancelled snapshot accepted")
	}
}

func TestDirtyWorkerSnapshotRejectsUnbornHEAD(t *testing.T) {
	repo := t.TempDir()
	testGit(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true); err == nil {
		t.Fatal("unborn HEAD accepted")
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "new.txt")); string(got) != "untracked\n" {
		t.Fatalf("failed snapshot changed parent: %q", got)
	}
}

func TestDirtyWorkerSnapshotRejectsSpecialParentAfterDispatch(t *testing.T) {
	repo := dirtySnapshotRepo(t)
	a := snapshotArtifact(t, repo, "one.txt", "one\n")
	before := indexBytes(t, repo)
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("parent.txt", filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := ApplyWorkerArtifact(context.Background(), repo, a); err == nil {
		t.Fatal("accepted special parent path")
	}
	if !bytes.Equal(indexBytes(t, repo), before) {
		t.Fatal("failed apply changed index")
	}
	if _, err := os.Stat(filepath.Join(repo, "one.txt")); !os.IsNotExist(err) {
		t.Fatal("failed apply wrote worker file")
	}
}

func TestDirtyWorkerSnapshotPrivateIndexFailureLeavesParentUntouched(t *testing.T) {
	repo := testRepo(t)
	before := indexBytes(t, repo)
	badIndex := filepath.Join(t.TempDir(), "missing", "index")
	if _, err := gitRunIndex(context.Background(), repo, badIndex, nil, "write-tree"); err == nil {
		t.Fatal("unwritable private index accepted")
	}
	if !bytes.Equal(indexBytes(t, repo), before) {
		t.Fatal("private index failure changed parent index")
	}
}

func TestDirtyWorkerSnapshotRejectsOversizedWorkerPatch(t *testing.T) {
	repo := dirtySnapshotRepo(t)
	before := indexBytes(t, repo)
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	content := make([]byte, maxWorkerPatchBytes+200000)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Cwd, "large.bin"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.artifact(context.Background()); err == nil {
		t.Fatal("oversized worker patch exported")
	}
	if !bytes.Equal(indexBytes(t, repo), before) {
		t.Fatal("failed artifact changed parent index")
	}
	if _, err := os.Stat(filepath.Join(repo, "large.bin")); !os.IsNotExist(err) {
		t.Fatal("failed artifact changed parent worktree")
	}
}

func TestDirtyWorkerSnapshotPreservesExecutableMode(t *testing.T) {
	repo := testRepo(t)
	path := filepath.Join(repo, "a.txt")
	if err := os.WriteFile(path, []byte("parent executable\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	w, env, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.close(); err != nil {
			t.Error(err)
		}
	})
	info, err := os.Stat(filepath.Join(env.Cwd, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("worker mode = %o", info.Mode().Perm())
	}
	a, err := w.artifact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a != nil {
		t.Fatalf("unchanged executable exported: %+v", a)
	}
}

func TestDirtyWorkerSnapshotRejectsAmbiguousPathWithoutChangingParent(t *testing.T) {
	repo := testRepo(t)
	before := indexBytes(t, repo)
	path := filepath.Join(repo, `ambiguous\name.txt`)
	if err := os.WriteFile(path, []byte("parent data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(repo), true); err == nil {
		t.Fatal("snapshot accepted a path the integration validator cannot represent")
	}
	if !bytes.Equal(before, indexBytes(t, repo)) {
		t.Fatal("refused snapshot changed parent index")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "parent data\n" {
		t.Fatalf("refused snapshot changed parent file: %q, %v", data, err)
	}
}
