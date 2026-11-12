package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
)

func batchArtifact(t *testing.T, repo, name, content string) WorkerArtifact {
	t.Helper()
	w, env, err := newWorkerWorktree(context.Background(), execenv.New(repo))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Cwd, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := w.artifact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(a.ManifestPath)) })
	return *a
}

func rewriteBatchArtifact(t *testing.T, a *WorkerArtifact, patch []byte) {
	t.Helper()
	if err := os.WriteFile(a.PatchPath, patch, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(patch)
	a.SHA256 = hex.EncodeToString(sum[:])
	a.Bytes = len(patch)
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.ManifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerBatchAppliesIndependentPatches(t *testing.T) {
	repo := testRepo(t)
	a := batchArtifact(t, repo, "one.txt", "one\n")
	b := batchArtifact(t, repo, "two.txt", "two\n")
	inspection, err := InspectWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a, b})
	if err != nil || inspection.Artifacts != 2 || len(inspection.Files) != 2 {
		t.Fatalf("inspection=%+v err=%v", inspection, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "one.txt")); !os.IsNotExist(err) {
		t.Fatal("inspect changed checkout")
	}
	if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a, b}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerBatchRefusesOverlapAndLeavesParentClean(t *testing.T) {
	repo := testRepo(t)
	a := batchArtifact(t, repo, "a.txt", "one\n")
	b := batchArtifact(t, repo, "a.txt", "two\n")
	if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a, b}); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap error = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "base\n" {
		t.Fatalf("parent changed: %q", got)
	}
}

func TestWorkerBatchPreflightFailuresLeaveParentClean(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, a *WorkerArtifact)
	}{
		{"tampered patch", func(t *testing.T, a *WorkerArtifact) {
			t.Helper()
			if err := os.WriteFile(a.PatchPath, []byte("tampered"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing patch", func(t *testing.T, a *WorkerArtifact) {
			t.Helper()
			if err := os.Remove(a.PatchPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"changed manifest", func(t *testing.T, a *WorkerArtifact) {
			t.Helper()
			a.ChangedFiles = []string{"other.txt"}
		}},
		{"unsupported symlink", func(t *testing.T, a *WorkerArtifact) {
			t.Helper()
			patch, err := os.ReadFile(a.PatchPath)
			if err != nil {
				t.Fatal(err)
			}
			rewriteBatchArtifact(t, a, []byte(strings.Replace(string(patch), "new file mode 100644", "new file mode 120000", 1)))
		}},
		{"unsupported index mode", func(t *testing.T, a *WorkerArtifact) {
			t.Helper()
			patch := mustReadBatchPatch(t, a.PatchPath)
			lines := strings.Split(string(patch), "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "index ") {
					lines[i] = line + " 160000"
					break
				}
			}
			rewriteBatchArtifact(t, a, []byte(strings.Join(lines, "\n")))
		}},
		{"path traversal", func(t *testing.T, a *WorkerArtifact) {
			t.Helper()
			patch, err := os.ReadFile(a.PatchPath)
			if err != nil {
				t.Fatal(err)
			}
			rewriteBatchArtifact(t, a, []byte(strings.ReplaceAll(string(patch), "two.txt", "../two.txt")))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			a := batchArtifact(t, repo, "one.txt", "one\n")
			b := batchArtifact(t, repo, "two.txt", "two\n")
			tc.change(t, &b)
			if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a, b}); err == nil {
				t.Fatal("expected preflight failure")
			}
			if _, err := os.Stat(filepath.Join(repo, "one.txt")); !os.IsNotExist(err) {
				t.Fatal("first patch applied despite failure")
			}
		})
	}
}

func TestWorkerBatchRejectsDirtyParentAndCancellation(t *testing.T) {
	repo := testRepo(t)
	a := batchArtifact(t, repo, "one.txt", "one\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ApplyWorkerArtifactBatch(ctx, repo, []WorkerArtifact{a}); err == nil {
		t.Fatal("cancelled apply succeeded")
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a}); err == nil {
		t.Fatal("dirty parent accepted")
	}
}

func TestWorkerBatchCombinedCheckFailureLeavesParentPristine(t *testing.T) {
	repo := testRepo(t)
	a := batchArtifact(t, repo, "one.txt", "one\n")
	b := batchArtifact(t, repo, "a.txt", "worker\n")
	patch, err := os.ReadFile(b.PatchPath)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(string(patch), "-base", "-does-not-match", 1)
	if bad == string(patch) {
		t.Fatal("fixture patch has no expected context")
	}
	rewriteBatchArtifact(t, &b, []byte(bad))
	if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a, b}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("combined check error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "one.txt")); !os.IsNotExist(err) {
		t.Fatal("first patch applied despite combined check failure")
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "base\n" {
		t.Fatalf("parent changed: %q", got)
	}
}

func TestWorkerBatchRejectsDifferentBaseAndLock(t *testing.T) {
	repo := testRepo(t)
	a := batchArtifact(t, repo, "one.txt", "one\n")
	b := batchArtifact(t, repo, "two.txt", "two\n")
	b.BaseCommit = strings.Repeat("a", 40)
	rewriteBatchArtifact(t, &b, mustReadBatchPatch(t, b.PatchPath))
	if _, err := InspectWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a, b}); err == nil {
		t.Fatal("different base accepted")
	}
	common := strings.TrimSpace(testGit(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	lock := filepath.Join(common, "kiln-worker-apply.lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })
	if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a}); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("lock error = %v", err)
	}
}

func TestSingleWorkerApplyRejectsUnsupportedMode(t *testing.T) {
	repo := testRepo(t)
	a := batchArtifact(t, repo, "link.txt", "target\n")
	patch := mustReadBatchPatch(t, a.PatchPath)
	rewriteBatchArtifact(t, &a, []byte(strings.Replace(string(patch), "new file mode 100644", "new file mode 120000", 1)))
	if err := ApplyWorkerArtifact(context.Background(), repo, a); err == nil || !strings.Contains(err.Error(), "unsupported file mode") {
		t.Fatalf("single apply error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(repo, "link.txt")); !os.IsNotExist(err) {
		t.Fatal("single apply created unsupported symlink")
	}
}

func mustReadBatchPatch(t *testing.T, path string) []byte {
	t.Helper()
	patch, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return patch
}
