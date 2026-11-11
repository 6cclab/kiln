package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/diag"
)

const maxWorkerBatchArtifacts = 16
const maxWorkerBatchBytes = 4 << 20

// WorkerBatchInspection describes a validated set of independent worker patches.
type WorkerBatchInspection struct {
	BaseCommit string   `json:"baseCommit"`
	Artifacts  int      `json:"artifacts"`
	Bytes      int      `json:"bytes"`
	Files      []string `json:"files"`
}

type preparedWorkerBatch struct {
	inspection WorkerBatchInspection
	patch      []byte
}

// InspectWorkerArtifactBatch validates manifests and patches against a clean
// checkout without changing it. Apply repeats this preflight under the lock.
func InspectWorkerArtifactBatch(ctx context.Context, repo string, artifacts []WorkerArtifact) (WorkerBatchInspection, error) {
	batch, err := prepareWorkerBatch(ctx, repo, artifacts)
	if err != nil {
		return WorkerBatchInspection{}, err
	}
	return batch.inspection, nil
}

// ApplyWorkerArtifactBatch applies all independent patches in one Git apply
// invocation after rechecking the parent and every artifact under the lock.
func ApplyWorkerArtifactBatch(ctx context.Context, repo string, artifacts []WorkerArtifact) error {
	if len(artifacts) == 0 || len(artifacts) > maxWorkerBatchArtifacts {
		return fmt.Errorf("worker batch requires 1 to %d artifacts", maxWorkerBatchArtifacts)
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return err
	}
	common, err := gitRun(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	lockPath := filepath.Join(strings.TrimSpace(string(common)), "kiln-worker-apply.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("worker integration lock: %w", err)
	}
	if err := lock.Close(); err != nil {
		_ = os.Remove(lockPath)
		return fmt.Errorf("close worker integration lock: %w", err)
	}
	defer func() {
		if err := os.Remove(lockPath); err != nil {
			diag.L().Warn("worker integration lock cleanup failed", "path", lockPath, "error", err)
		}
	}()
	batch, err := prepareWorkerBatch(ctx, repo, artifacts)
	if err != nil {
		return err
	}
	head, err := cleanHead(ctx, repo)
	if err != nil {
		return err
	}
	if head != batch.inspection.BaseCommit {
		return fmt.Errorf("parent HEAD changed during worker batch preflight")
	}
	if _, err := gitRunInput(ctx, repo, batch.patch, "apply", "--binary", "-"); err != nil {
		return fmt.Errorf("apply worker batch: %w", err)
	}
	return nil
}

func prepareWorkerBatch(ctx context.Context, repo string, artifacts []WorkerArtifact) (preparedWorkerBatch, error) {
	if len(artifacts) == 0 || len(artifacts) > maxWorkerBatchArtifacts {
		return preparedWorkerBatch{}, fmt.Errorf("worker batch requires 1 to %d artifacts", maxWorkerBatchArtifacts)
	}
	head, err := cleanHead(ctx, repo)
	if err != nil {
		return preparedWorkerBatch{}, err
	}
	batch := preparedWorkerBatch{inspection: WorkerBatchInspection{BaseCommit: head, Artifacts: len(artifacts)}}
	seen := map[string]int{}
	for i, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return preparedWorkerBatch{}, err
		}
		if artifact.BaseCommit != head {
			return preparedWorkerBatch{}, fmt.Errorf("worker artifact %d has a different base commit", i+1)
		}
		if artifact.PatchPath == "" || artifact.ManifestPath == "" {
			return preparedWorkerBatch{}, fmt.Errorf("worker artifact %d lacks a retained manifest or patch", i+1)
		}
		// Re-read the manifest during preflight so callers cannot silently supply
		// stale or altered metadata after LoadWorkerArtifact.
		loaded, err := LoadWorkerArtifact(artifact.ManifestPath)
		if err != nil {
			return preparedWorkerBatch{}, fmt.Errorf("worker artifact %d manifest: %w", i+1, err)
		}
		if loaded.BaseCommit != artifact.BaseCommit || loaded.PatchPath != artifact.PatchPath || loaded.SHA256 != artifact.SHA256 || loaded.Bytes != artifact.Bytes || !samePaths(loaded.ChangedFiles, artifact.ChangedFiles) {
			return preparedWorkerBatch{}, fmt.Errorf("worker artifact %d manifest changed", i+1)
		}
		patch, err := readRegularBounded(artifact.PatchPath, maxWorkerPatchBytes)
		if err != nil {
			return preparedWorkerBatch{}, fmt.Errorf("worker artifact %d patch: %w", i+1, err)
		}
		sum := sha256.Sum256(patch)
		if len(patch) == 0 || len(patch) != artifact.Bytes || hex.EncodeToString(sum[:]) != artifact.SHA256 {
			return preparedWorkerBatch{}, fmt.Errorf("worker artifact %d patch size or digest mismatch", i+1)
		}
		if len(batch.patch)+len(patch)+1 > maxWorkerBatchBytes {
			return preparedWorkerBatch{}, fmt.Errorf("worker batch exceeds %d bytes", maxWorkerBatchBytes)
		}
		paths, err := workerPatchPaths(ctx, repo, patch)
		if err != nil {
			return preparedWorkerBatch{}, fmt.Errorf("worker artifact %d: %w", i+1, err)
		}
		if !samePaths(paths, artifact.ChangedFiles) {
			return preparedWorkerBatch{}, fmt.Errorf("worker artifact %d changed files differ from verified patch", i+1)
		}
		for _, p := range paths {
			if err := workerBatchPathSafe(repo, p); err != nil {
				return preparedWorkerBatch{}, err
			}
			for prior, owner := range seen {
				folded, foldedPrior := strings.ToLower(p), strings.ToLower(prior)
				if folded == foldedPrior || strings.HasPrefix(folded, foldedPrior+"/") || strings.HasPrefix(foldedPrior, folded+"/") || workerBatchSameFile(repo, p, prior) {
					return preparedWorkerBatch{}, fmt.Errorf("worker artifacts %d and %d overlap at %q", owner, i+1, p)
				}
			}
			seen[p] = i + 1
			batch.inspection.Files = append(batch.inspection.Files, p)
		}
		batch.patch = append(batch.patch, patch...)
		if patch[len(patch)-1] != '\n' {
			batch.patch = append(batch.patch, '\n')
		}
	}
	batch.inspection.Bytes = len(batch.patch)
	sort.Strings(batch.inspection.Files)
	if _, err := gitRunInput(ctx, repo, batch.patch, "apply", "--check", "--binary", "-"); err != nil {
		return preparedWorkerBatch{}, fmt.Errorf("worker batch conflict: %w", err)
	}
	return batch, nil
}

func workerPatchPaths(ctx context.Context, repo string, patch []byte) ([]string, error) {
	// Git parses all patch path encodings and binary hunks for us. Reject
	// renames and copies because numstat reports only their destination.
	for _, line := range bytes.Split(patch, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("index ")) {
			fields := bytes.Fields(line)
			if len(fields) == 3 && !bytes.Equal(fields[2], []byte("100644")) && !bytes.Equal(fields[2], []byte("100755")) {
				return nil, fmt.Errorf("worker patch has unsupported index mode %q", fields[2])
			}
		}
		if bytes.HasPrefix(line, []byte("rename from ")) || bytes.HasPrefix(line, []byte("rename to ")) || bytes.HasPrefix(line, []byte("copy from ")) || bytes.HasPrefix(line, []byte("copy to ")) {
			return nil, fmt.Errorf("worker batch does not support rename or copy patches")
		}
		for _, prefix := range [][]byte{[]byte("new file mode "), []byte("old mode "), []byte("new mode "), []byte("deleted file mode ")} {
			if bytes.HasPrefix(line, prefix) {
				mode := bytes.TrimPrefix(line, prefix)
				if !bytes.Equal(mode, []byte("100644")) && !bytes.Equal(mode, []byte("100755")) {
					return nil, fmt.Errorf("worker patch has unsupported file mode %q", mode)
				}
			}
		}
	}
	out, err := gitRunInput(ctx, repo, patch, "apply", "--numstat", "-z", "--binary", "-")
	if err != nil {
		return nil, fmt.Errorf("parse worker patch: %w", err)
	}
	if len(out) == 0 || out[len(out)-1] != 0 {
		return nil, fmt.Errorf("worker patch has no valid paths")
	}
	seen := map[string]bool{}
	var paths []string
	for _, record := range bytes.Split(out[:len(out)-1], []byte{0}) {
		first := bytes.IndexByte(record, '\t')
		second := bytes.IndexByte(record[first+1:], '\t')
		if first < 1 || second < 1 {
			return nil, fmt.Errorf("invalid worker patch path record")
		}
		p := string(record[first+second+2:])
		if err := validWorkerPatchPath(p); err != nil {
			return nil, err
		}
		if seen[p] {
			return nil, fmt.Errorf("duplicate worker patch path %q", p)
		}
		seen[p] = true
		paths = append(paths, p)
	}
	if len(paths) > 1000 {
		return nil, fmt.Errorf("worker patch exceeds 1000 changed files")
	}
	return paths, nil
}

func validWorkerPatchPath(p string) error {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, "\\") {
		return fmt.Errorf("unsafe worker patch path %q", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.EqualFold(part, ".git") {
			return fmt.Errorf("unsafe worker patch path %q", p)
		}
	}
	return nil
}

func workerBatchPathSafe(repo, p string) error {
	current := repo
	parts := strings.Split(p, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("worker patch path traverses symlink %q", p)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("worker patch path traverses non-directory %q", p)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("worker patch has nonregular endpoint %q", p)
		}
	}
	return nil
}

func workerBatchSameFile(repo, a, b string) bool {
	first, firstErr := os.Lstat(filepath.Join(repo, filepath.FromSlash(a)))
	second, secondErr := os.Lstat(filepath.Join(repo, filepath.FromSlash(b)))
	return firstErr == nil && secondErr == nil && os.SameFile(first, second)
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, p := range a {
		seen[p]++
	}
	for _, p := range b {
		seen[p]--
		if seen[p] < 0 {
			return false
		}
	}
	return true
}
