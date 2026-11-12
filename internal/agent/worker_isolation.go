package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/automode"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

func removeToolName(names []string, remove string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name != remove {
			out = append(out, name)
		}
	}
	return out
}

// isolatedToolPath accepts only file tool paths contained by the worker
// tree. The original gate checks the same relative argument against the
// parent's logical path, preserving its rules and user grants. The worker
// runs it against its own tree. Symlinks are refused to avoid traversal.
func isolatedToolPath(root string, call msg.ToolCall) error {
	if call.Name == "task" {
		return nil
	}
	if call.Name != "read" && call.Name != "write" && call.Name != "edit" {
		return fmt.Errorf("tool %q is unavailable in isolated workers", call.Name)
	}
	raw, ok := call.Arguments["path"].(string)
	if !ok || raw == "" {
		return fmt.Errorf("isolated worker requires a relative path")
	}
	normalized := execenv.NormalizeToolPath(raw)
	if filepath.IsAbs(normalized) || strings.HasPrefix(normalized, "~") || strings.HasPrefix(normalized, "file://") {
		return fmt.Errorf("isolated worker path must be relative")
	}
	path := execenv.ResolveToolPath(root, raw)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("isolated worker path leaves worktree")
	}
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if strings.EqualFold(component, ".git") {
			return fmt.Errorf("isolated worker cannot access Git metadata")
		}
	}
	for p := root; p != path; {
		part := strings.TrimPrefix(path, p+string(filepath.Separator))
		segment, _, _ := strings.Cut(part, string(filepath.Separator))
		p = filepath.Join(p, segment)
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("isolated worker path traverses a symlink")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func isolatedFileTools(env *execenv.Env, root string, gate *permission.Gate, started *Started, userHistory func(context.Context) []msg.Message) []*tool.Tool {
	builtins := tools.Builtins(env)
	var wrapped []*tool.Tool
	for _, name := range []string{"read", "edit", "write"} {
		original, _ := builtins.Get(name)
		copyTool := *original
		baseExecute := original.Execute
		copyTool.Execute = func(ctx context.Context, raw json.RawMessage, update tool.Update, inv tool.Invocation) (tool.Result, error) {
			if !env.ConfinedFilesEnabled() {
				return tool.Errorf("isolated file access is not confined"), nil
			}
			var args map[string]any
			if err := json.Unmarshal(raw, &args); err != nil {
				return tool.Errorf("invalid isolated worker arguments: %s", err), nil
			}
			call := msg.ToolCall{Name: copyTool.Name, Arguments: args, ID: inv.ToolCallID}
			if err := isolatedToolPath(root, call); err != nil {
				return tool.Errorf("%s", err), nil
			}
			if gate != nil {
				primary, _ := permission.PrimaryArgOf(args)
				req := permission.Request{ToolName: copyTool.Name, PrimaryArg: primary, Args: args, CallID: inv.ToolCallID, Delegated: true,
					History: func() []msg.Message { return automode.BranchMessages(ctx, started.Lane) }}
				if userHistory != nil {
					req.UserHistory = func() []msg.Message { return userHistory(ctx) }
				}
				blocked, err := gate.Check(ctx, req)
				if err != nil {
					return tool.Errorf("permission check failed: %s", err), nil
				}
				if blocked != nil {
					return tool.Errorf("%s", blocked.Reason), nil
				}
			}
			return baseExecute(ctx, raw, update, inv)
		}
		wrapped = append(wrapped, &copyTool)
	}
	return wrapped
}

const maxWorkerPatchBytes = 1 << 20
const maxWorkerReportBytes = 8192

func boundedWorkerReport(report string) string {
	if len(report) <= maxWorkerReportBytes {
		return report
	}
	const suffix = "\n[worker report truncated]"
	return strings.ToValidUTF8(report[:maxWorkerReportBytes-len(suffix)], "") + suffix
}

// WorkerArtifact is a bounded, reviewable patch from an isolated worker.
// PatchPath is a private temporary file retained until the caller removes it.
type WorkerArtifact struct {
	ManifestPath string          `json:"manifestPath,omitempty"`
	BaseCommit   string          `json:"baseCommit"`
	PatchPath    string          `json:"patchPath"`
	SHA256       string          `json:"sha256"`
	Bytes        int             `json:"bytes"`
	ChangedFiles []string        `json:"changedFiles,omitempty"`
	Baseline     *WorkerBaseline `json:"baseline,omitempty"`
}

type workerWorktree struct {
	repo          string
	path          string
	root          string
	base          string
	baseline      *WorkerBaseline
	snapshotFiles []snapshotFile
}

func gitRun(ctx context.Context, dir string, args ...string) ([]byte, error) {
	argv, err := safeGitArgs(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = dir
	cmd.Env = cleanGitEnvironment()
	out, err := runGitBounded(cmd)
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// safeGitArgs shadows every configured filter driver for this command.
// Git otherwise runs clean/smudge/process programs during worktree checkout
// and staging, even though isolated workers have no bash tool. A worker may
// add .gitattributes, but cannot add new filter commands to Git config.
func safeGitArgs(ctx context.Context, dir string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "config", "--null", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process|required)$`)
	cmd.Dir = dir
	cmd.Env = cleanGitEnvironment()
	out, err := runGitBounded(cmd)
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || len(out) != 0 {
			return nil, fmt.Errorf("list Git filters: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	drivers := map[string]bool{}
	for _, keyBytes := range bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0}) {
		key := string(keyBytes)
		if !strings.HasPrefix(key, "filter.") {
			continue
		}
		rest := strings.TrimPrefix(key, "filter.")
		dot := strings.LastIndexByte(rest, '.')
		if dot <= 0 {
			continue
		}
		drivers[rest[:dot]] = true
	}
	if len(drivers) > 256 {
		return nil, fmt.Errorf("too many configured Git filter drivers")
	}
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	argv := []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "core.attributesFile=/dev/null"}
	for _, name := range names {
		for _, setting := range []string{"clean=", "smudge=", "process=", "required=false"} {
			argv = append(argv, "-c", "filter."+name+"."+setting)
		}
	}
	return append(argv, args...), nil
}

type boundedOutput struct {
	buf   bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("git output exceeds %d bytes", b.limit)
	}
	return b.buf.Write(p)
}

func runGitBounded(cmd *exec.Cmd) ([]byte, error) {
	buf := &boundedOutput{limit: maxWorkerPatchBytes + 65536}
	stderr := &boundedOutput{limit: 65536}
	cmd.Stdout = buf
	cmd.Stderr = stderr
	err := cmd.Run()
	if err != nil && stderr.buf.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.buf.String()))
	}
	return buf.buf.Bytes(), err
}

func cleanGitEnvironment() []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "GIT_OPTIONAL_LOCKS=0")
}

func cleanHead(ctx context.Context, repo string) (string, error) {
	head, err := repositoryHead(ctx, repo)
	if err != nil {
		return "", err
	}
	status, err := gitRun(ctx, repo, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if len(status) != 0 {
		return "", fmt.Errorf("isolated workers require a clean parent working tree; existing changes were left untouched")
	}
	return head, nil
}

func repositoryHead(ctx context.Context, repo string) (string, error) {
	root, err := gitRun(ctx, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	resolved := strings.TrimSpace(string(root))
	physicalRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolved) != filepath.Clean(physicalRepo) {
		return "", fmt.Errorf("isolated workers require the repository root as cwd: %s", resolved)
	}
	head, err := gitRun(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(head)), nil
}

func newWorkerWorktree(ctx context.Context, env *execenv.Env) (*workerWorktree, *execenv.Env, error) {
	return newWorkerWorktreeWithSnapshot(ctx, env, false)
}

func newWorkerWorktreeWithSnapshot(ctx context.Context, env *execenv.Env, snapshotDirty bool) (*workerWorktree, *execenv.Env, error) {
	if env == nil || env.Cwd == "" {
		return nil, nil, fmt.Errorf("isolated worker requires a working directory")
	}
	repo, err := filepath.Abs(env.Cwd)
	if err != nil {
		return nil, nil, err
	}
	var base string
	var snapshot workerSnapshot
	if snapshotDirty {
		base, err = repositoryHead(ctx, repo)
		if err == nil {
			snapshot, err = captureWorkerSnapshot(ctx, repo, base)
		}
	} else {
		base, err = cleanHead(ctx, repo)
	}
	if err != nil {
		return nil, nil, err
	}
	root, err := os.MkdirTemp("", "kiln-worker-")
	if err != nil {
		return nil, nil, err
	}
	w := &workerWorktree{repo: repo, root: root, path: filepath.Join(root, "worktree"), base: base}
	if _, err := gitRun(ctx, repo, "worktree", "add", "--detach", w.path, base); err != nil {
		_ = os.RemoveAll(root)
		return nil, nil, err
	}
	if snapshotDirty {
		w.baseline = &snapshot.baseline
		w.snapshotFiles = snapshot.files
		if _, err := gitRun(ctx, w.path, "read-tree", snapshot.baseline.Tree); err != nil {
			_ = w.close()
			return nil, nil, err
		}
		for _, file := range snapshot.files {
			path := filepath.Join(w.path, filepath.FromSlash(file.path))
			if file.deleted {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					_ = w.close()
					return nil, nil, err
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				_ = w.close()
				return nil, nil, err
			}
			if err := os.WriteFile(path, file.data, file.mode); err != nil {
				_ = w.close()
				return nil, nil, err
			}
			if err := os.Chmod(path, file.mode); err != nil {
				_ = w.close()
				return nil, nil, err
			}
		}
	}
	childEnv := *env
	childEnv.Cwd = w.path
	return w, &childEnv, nil
}

func (w *workerWorktree) close() error {
	// Cleanup still runs after cancellation; use a fresh context so the
	// cancelled worker context cannot strand an active Git worktree.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gitRun(ctx, w.repo, "worktree", "remove", "--force", w.path)
	if err != nil {
		return err
	}
	return os.RemoveAll(w.root)
}

func (w *workerWorktree) artifact(ctx context.Context) (*WorkerArtifact, error) {
	// Stage only in the disposable worktree. This includes new files in the
	// binary patch and leaves the parent index untouched.
	if _, err := gitRun(ctx, w.path, "add", "-A"); err != nil {
		return nil, err
	}
	// Staging can normalize line endings or apply attributes. A captured
	// parent file that the worker left untouched must retain its raw baseline
	// blob in the worker index, so it never appears in the exported patch.
	if w.baseline != nil {
		var restoreInput bytes.Buffer
		for _, file := range w.snapshotFiles {
			if file.deleted {
				continue
			}
			path := filepath.Join(w.path, filepath.FromSlash(file.path))
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != file.mode {
				continue
			}
			data, err := readRegularBounded(path, maxWorkerSnapshotBytes)
			if err != nil || !bytes.Equal(data, file.data) {
				continue
			}
			restoreInput.WriteString(file.path)
			restoreInput.WriteByte(0)
		}
		if restoreInput.Len() > 0 {
			if _, err := gitRunInput(ctx, w.path, restoreInput.Bytes(), "--literal-pathspecs", "restore", "--staged", "--source="+w.baseline.Tree, "--pathspec-from-file=-", "--pathspec-file-nul"); err != nil {
				return nil, err
			}
		}
	}
	patchBase := w.base
	if w.baseline != nil {
		patchBase = w.baseline.Tree
	}
	patch, err := gitRun(ctx, w.path, "diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv", patchBase)
	if err != nil {
		return nil, err
	}
	if len(patch) == 0 {
		return nil, nil
	}
	if len(patch) > maxWorkerPatchBytes {
		return nil, fmt.Errorf("worker patch exceeds %d bytes", maxWorkerPatchBytes)
	}
	names, err := gitRun(ctx, w.path, "diff", "--cached", "--name-only", "-z", patchBase)
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(bytes.TrimSuffix(names, []byte{0}), []byte{0})
	changed := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > 0 {
			changed = append(changed, string(p))
		}
	}
	if len(changed) > 1000 {
		return nil, fmt.Errorf("worker patch exceeds 1000 changed files")
	}
	dir, err := os.MkdirTemp("", "kiln-worker-artifact-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "changes.patch")
	if err := os.WriteFile(path, patch, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	sum := sha256.Sum256(patch)
	artifact := &WorkerArtifact{BaseCommit: w.base, PatchPath: path, SHA256: hex.EncodeToString(sum[:]), Bytes: len(patch), ChangedFiles: changed, Baseline: w.baseline}
	manifest := filepath.Join(dir, "artifact.json")
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := os.WriteFile(manifest, data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	artifact.ManifestPath = manifest
	return artifact, nil
}

// LoadWorkerArtifact reads a worker's manifest for explicit integration.
func LoadWorkerArtifact(path string) (WorkerArtifact, error) {
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return WorkerArtifact{}, err
	}
	data, err := readRegularBounded(path, 65536)
	if err != nil {
		return WorkerArtifact{}, err
	}
	var artifact WorkerArtifact
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&artifact); err != nil {
		return WorkerArtifact{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return WorkerArtifact{}, fmt.Errorf("worker manifest has trailing data")
	}
	if filepath.Dir(artifact.PatchPath) != filepath.Dir(path) {
		return WorkerArtifact{}, fmt.Errorf("worker patch must be beside its manifest")
	}
	if filepath.Base(artifact.PatchPath) != "changes.patch" {
		return WorkerArtifact{}, fmt.Errorf("invalid worker patch name")
	}
	artifact.ManifestPath = path
	return artifact, nil
}

func readRegularBounded(path string, limit int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("worker artifact is not a regular file")
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("worker artifact exceeds %d bytes", limit)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			diag.L().Warn("worker artifact close failed", "path", path, "error", closeErr)
		}
	}()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("worker artifact exceeds %d bytes", limit)
	}
	return data, nil
}

// ApplyWorkerArtifact explicitly applies one worker patch using the same
// validation and integration lock as a batch of independent patches.
func ApplyWorkerArtifact(ctx context.Context, repo string, artifact WorkerArtifact) error {
	return ApplyWorkerArtifactBatch(ctx, repo, []WorkerArtifact{artifact})
}

func gitRunInput(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	argv, err := safeGitArgs(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Dir = dir
	cmd.Env = cleanGitEnvironment()
	cmd.Stdin = bytes.NewReader(input)
	out, err := runGitBounded(cmd)
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
