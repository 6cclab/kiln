//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
)

func batchGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

func e2eBatchArtifact(t *testing.T, repo, name string) agent.WorkerArtifact {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "worktree")
	batchGit(t, repo, "worktree", "add", "--detach", worktree, "HEAD")
	t.Cleanup(func() { batchGit(t, repo, "worktree", "remove", "--force", worktree) })
	if err := os.WriteFile(filepath.Join(worktree, name), []byte(name+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	batchGit(t, worktree, "add", "-A")
	patch := batchGit(t, worktree, "diff", "--cached", "--binary", "HEAD")
	dir := t.TempDir()
	patchPath := filepath.Join(dir, "changes.patch")
	if err := os.WriteFile(patchPath, patch, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(patch)
	a := agent.WorkerArtifact{BaseCommit: strings.TrimSpace(string(batchGit(t, repo, "rev-parse", "HEAD"))), PatchPath: patchPath, SHA256: hex.EncodeToString(sum[:]), Bytes: len(patch), ChangedFiles: []string{name}}
	a.ManifestPath = filepath.Join(dir, "artifact.json")
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.ManifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestWorkerBatchCLIExplicitIntegration(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	batchGit(t, proj, "init", "-q")
	batchGit(t, proj, "add", "-A")
	batchGit(t, proj, "-c", "user.name=Kiln Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	a := e2eBatchArtifact(t, proj, "one.txt")
	b := e2eBatchArtifact(t, proj, "two.txt")
	env := baseEnv(home, sessDir, "")
	res := runHarness(t, proj, env, "worker", "inspect-batch", a.ManifestPath, b.ManifestPath)
	if res.Code != 0 || !strings.Contains(res.Stdout, "one.txt") || !strings.Contains(res.Stdout, "two.txt") {
		t.Fatalf("inspect batch: %s %s", res.Stdout, res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(proj, "one.txt")); !os.IsNotExist(err) {
		t.Fatal("inspect batch changed the parent")
	}
	res = runHarness(t, proj, env, "worker", "apply-batch", a.ManifestPath, b.ManifestPath)
	if res.Code != 0 {
		t.Fatalf("apply batch: %s %s", res.Stdout, res.Stderr)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(proj, name)); err != nil {
			t.Fatal(err)
		}
	}
	res = runHarness(t, proj, env, "worker", "apply-batch", a.ManifestPath, b.ManifestPath)
	if res.Code == 0 {
		t.Fatal("dirty parent accepted a second batch")
	}
}

func TestWorkerSingleCLIRejectsUnsupportedPatchOnInspectAndApply(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	batchGit(t, proj, "init", "-q")
	batchGit(t, proj, "add", "-A")
	batchGit(t, proj, "-c", "user.name=Kiln Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	a := e2eBatchArtifact(t, proj, "link.txt")
	patch, err := os.ReadFile(a.PatchPath)
	if err != nil {
		t.Fatal(err)
	}
	patch = []byte(strings.Replace(string(patch), "new file mode 100644", "new file mode 120000", 1))
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
	env := baseEnv(home, sessDir, "")
	for _, command := range []string{"inspect", "apply"} {
		res := runHarness(t, proj, env, "worker", command, a.ManifestPath)
		if res.Code == 0 || !strings.Contains(res.Stderr, "unsupported file mode") {
			t.Fatalf("%s: stdout=%s stderr=%s", command, res.Stdout, res.Stderr)
		}
	}
	if _, err := os.Lstat(filepath.Join(proj, "link.txt")); !os.IsNotExist(err) {
		t.Fatal("unsupported patch changed parent")
	}
}
