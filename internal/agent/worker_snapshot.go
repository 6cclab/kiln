package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const maxWorkerSnapshotFiles = 1000
const maxWorkerSnapshotBytes = 4 << 20

// WorkerBaseline identifies the captured parent worktree and index. The tree
// is the worker's patch base; state also detects staged and unstaged changes
// that happen to produce the same worktree content.
type WorkerBaseline struct {
	Tree  string `json:"tree"`
	State string `json:"state"`
}

type snapshotFile struct {
	path    string
	data    []byte
	mode    os.FileMode
	deleted bool
}

type workerSnapshot struct {
	baseline WorkerBaseline
	files    []snapshotFile
}

func gitRunIndex(ctx context.Context, repo, index string, input []byte, args ...string) ([]byte, error) {
	argv, err := safeGitArgs(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = repo
	cmd.Env = append(cleanGitEnvironment(), "GIT_INDEX_FILE="+index)
	cmd.Stdin = bytes.NewReader(input)
	out, err := runGitBounded(cmd)
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func snapshotPaths(ctx context.Context, repo string) ([]string, []byte, []byte, error) {
	status, err := gitRun(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, nil, nil, err
	}
	index, err := gitRun(ctx, repo, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, nil, nil, err
	}
	for _, record := range bytes.Split(bytes.TrimSuffix(index, []byte{0}), []byte{0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(fields) != 2 || !bytes.HasSuffix(fields[0], []byte(" 0")) {
			return nil, nil, nil, fmt.Errorf("parent snapshot refuses unmerged index")
		}
	}
	flags, err := gitRun(ctx, repo, "ls-files", "-v", "-z")
	if err != nil {
		return nil, nil, nil, err
	}
	for _, record := range bytes.Split(bytes.TrimSuffix(flags, []byte{0}), []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if len(record) < 3 || record[1] != ' ' {
			return nil, nil, nil, fmt.Errorf("invalid Git index flags")
		}
		if record[0] != 'H' {
			return nil, nil, nil, fmt.Errorf("parent snapshot refuses skip-worktree or assume-unchanged paths")
		}
	}
	seen := map[string]bool{}
	for _, args := range [][]string{
		{"diff", "--name-only", "-z", "--no-renames", "--no-ext-diff", "HEAD"},
		{"diff", "--cached", "--name-only", "-z", "--no-renames", "--no-ext-diff", "HEAD"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
	} {
		out, err := gitRun(ctx, repo, args...)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, part := range bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
			if len(part) == 0 {
				continue
			}
			p := string(part)
			if err := validWorkerPatchPath(p); err != nil {
				return nil, nil, nil, err
			}
			seen[p] = true
		}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > maxWorkerSnapshotFiles {
		return nil, nil, nil, fmt.Errorf("parent snapshot exceeds %d files", maxWorkerSnapshotFiles)
	}
	if len(status) > 0 && len(paths) == 0 {
		return nil, nil, nil, fmt.Errorf("parent has unsupported Git changes")
	}
	return paths, status, index, nil
}

func captureWorkerSnapshot(ctx context.Context, repo, head string) (workerSnapshot, error) {
	paths, status, index, err := snapshotPaths(ctx, repo)
	if err != nil {
		return workerSnapshot{}, err
	}
	state := sha256.New()
	_, _ = state.Write(status)
	_, _ = state.Write(index)
	files := make([]snapshotFile, 0, len(paths))
	total := 0
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return workerSnapshot{}, err
		}
		for _, entry := range bytes.Split(index, []byte{0}) {
			fields := bytes.SplitN(entry, []byte{'\t'}, 2)
			if len(fields) == 2 && string(fields[1]) == p && !bytes.HasPrefix(fields[0], []byte("100644 ")) && !bytes.HasPrefix(fields[0], []byte("100755 ")) {
				return workerSnapshot{}, fmt.Errorf("parent snapshot has unsupported index mode at %q", p)
			}
		}
		if err := workerBatchPathSafe(repo, p); err != nil {
			return workerSnapshot{}, err
		}
		path := filepath.Join(repo, filepath.FromSlash(p))
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			files = append(files, snapshotFile{path: p, deleted: true})
			_, _ = state.Write([]byte("D\x00" + p + "\x00"))
			continue
		}
		if err != nil {
			return workerSnapshot{}, err
		}
		if !info.Mode().IsRegular() {
			return workerSnapshot{}, fmt.Errorf("parent snapshot has nonregular file %q", p)
		}
		if info.Size() > int64(maxWorkerSnapshotBytes-total) {
			return workerSnapshot{}, fmt.Errorf("parent snapshot exceeds %d bytes", maxWorkerSnapshotBytes)
		}
		data, err := readRegularBounded(path, maxWorkerSnapshotBytes-total)
		if err != nil {
			return workerSnapshot{}, err
		}
		total += len(data)
		mode := info.Mode().Perm()
		files = append(files, snapshotFile{path: p, data: data, mode: mode})
		_, _ = state.Write([]byte("F\x00" + p + "\x00" + fmt.Sprint(mode) + "\x00"))
		_, _ = state.Write(data)
	}
	// A private index constructs the baseline tree without touching the parent
	// index or invoking clean filters for arbitrary worktree content.
	tmp, err := os.MkdirTemp("", "kiln-worker-index-")
	if err != nil {
		return workerSnapshot{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	privateIndex := filepath.Join(tmp, "index")
	if _, err := gitRunIndex(ctx, repo, privateIndex, nil, "read-tree", head); err != nil {
		return workerSnapshot{}, err
	}
	// Hash captured bytes from private numeric filenames in one Git call.
	// No attributes or filters can alter those raw snapshot bytes.
	var hashInput bytes.Buffer
	var hashed []snapshotFile
	for _, f := range files {
		if f.deleted {
			continue
		}
		name := filepath.Join(tmp, fmt.Sprintf("blob-%d", len(hashed)))
		if err := os.WriteFile(name, f.data, 0o600); err != nil {
			return workerSnapshot{}, err
		}
		hashInput.WriteString(name)
		hashInput.WriteByte('\n')
		hashed = append(hashed, f)
	}
	var oids [][]byte
	if len(hashed) > 0 {
		out, err := gitRunInput(ctx, repo, hashInput.Bytes(), "hash-object", "-w", "--no-filters", "--stdin-paths")
		if err != nil {
			return workerSnapshot{}, err
		}
		oids = bytes.Split(bytes.TrimSuffix(out, []byte{'\n'}), []byte{'\n'})
		if len(oids) != len(hashed) {
			return workerSnapshot{}, fmt.Errorf("git returned %d object ids for %d snapshot files", len(oids), len(hashed))
		}
	}
	var indexInput bytes.Buffer
	hashIndex := 0
	for _, f := range files {
		if f.deleted {
			indexInput.WriteString("0 " + strings.Repeat("0", len(head)) + "\t")
		} else {
			mode := "100644"
			if f.mode&0o111 != 0 {
				mode = "100755"
			}
			indexInput.WriteString(mode + " " + string(oids[hashIndex]) + "\t")
			hashIndex++
		}
		indexInput.WriteString(f.path)
		indexInput.WriteByte(0)
	}
	if indexInput.Len() > 0 {
		if _, err := gitRunIndex(ctx, repo, privateIndex, indexInput.Bytes(), "update-index", "-z", "--index-info"); err != nil {
			return workerSnapshot{}, err
		}
	}
	tree, err := gitRunIndex(ctx, repo, privateIndex, nil, "write-tree")
	if err != nil {
		return workerSnapshot{}, err
	}
	// Recheck after reads and object creation so a concurrent edit cannot
	// silently become a baseline assembled from two different parent states.
	pathsAfter, statusAfter, indexAfter, err := snapshotPaths(ctx, repo)
	if err != nil {
		return workerSnapshot{}, err
	}
	if !samePaths(paths, pathsAfter) || !bytes.Equal(status, statusAfter) || !bytes.Equal(index, indexAfter) {
		return workerSnapshot{}, fmt.Errorf("parent changed during snapshot")
	}
	for _, f := range files {
		path := filepath.Join(repo, filepath.FromSlash(f.path))
		if f.deleted {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				return workerSnapshot{}, fmt.Errorf("parent changed during snapshot")
			}
			continue
		}
		data, err := readRegularBounded(path, maxWorkerSnapshotBytes)
		info, statErr := os.Lstat(path)
		if err != nil || statErr != nil || !bytes.Equal(data, f.data) || !info.Mode().IsRegular() || info.Mode().Perm() != f.mode {
			return workerSnapshot{}, fmt.Errorf("parent changed during snapshot")
		}
	}
	currentHead, err := gitRun(ctx, repo, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(currentHead)) != head {
		return workerSnapshot{}, fmt.Errorf("parent HEAD changed during snapshot")
	}
	return workerSnapshot{baseline: WorkerBaseline{Tree: strings.TrimSpace(string(tree)), State: hex.EncodeToString(state.Sum(nil))}, files: files}, nil
}
