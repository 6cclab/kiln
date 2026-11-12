package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

func TestIsolatedFileToolsRequireConfinementAtExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("confined file tools are unavailable on Windows")
	}
	root := t.TempDir()
	args := json.RawMessage(`{"path":"safe.txt","content":"safe"}`)
	run := func(env *execenv.Env, raw json.RawMessage) tool.Result {
		t.Helper()
		var write *tool.Tool
		for _, candidate := range isolatedFileTools(env, root, nil, nil, nil) {
			if candidate.Name == "write" {
				write = candidate
				break
			}
		}
		if write == nil {
			t.Fatal("isolated write tool missing")
		}
		result, err := write.Execute(context.Background(), raw, func(tool.Result) {}, tool.Invocation{ToolName: "write", Cwd: root})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := run(execenv.New(root), args); !result.IsError {
		t.Fatal("unconfined worker write was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "safe.txt")); !os.IsNotExist(err) {
		t.Fatalf("unconfined worker wrote a file: %v", err)
	}
	confined, err := execenv.New(root).CloneForConfinedFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = confined.CloseConfinedFiles() })
	if result := run(confined, json.RawMessage("{")); !result.IsError {
		t.Fatal("malformed write arguments were accepted")
	}
	if result := run(confined, json.RawMessage(`{"path":"../escaped","content":"unsafe"}`)); !result.IsError {
		t.Fatal("path escape was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "..", "escaped")); !os.IsNotExist(err) {
		t.Fatalf("isolated write escaped the worker root: %v", err)
	}
	if result := run(confined, args); result.IsError {
		t.Fatalf("confined worker could not write inside its root: %+v", result)
	}
	if got, err := os.ReadFile(filepath.Join(root, "safe.txt")); err != nil || string(got) != "safe" {
		t.Fatalf("confined write = %q, %v", got, err)
	}
}

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func testRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testGit(t, dir, "init", "-q")
	testGit(t, dir, "config", "user.name", "Kiln Test")
	testGit(t, dir, "config", "user.email", "kiln@example.test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "add", "a.txt")
	testGit(t, dir, "commit", "-qm", "base")
	return dir
}

func TestWorkerIsolationArtifactAndApply(t *testing.T) {
	repo := testRepo(t)
	w, env, err := newWorkerWorktree(context.Background(), execenv.New(repo))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Cwd, "a.txt"), []byte("worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Cwd, "b.txt"), []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	artifact, err := w.artifact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if artifact == nil || len(artifact.ChangedFiles) != 2 {
		t.Fatalf("artifact = %+v", artifact)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "base\n" {
		t.Fatalf("parent changed before apply: %q", got)
	}
	loaded, err := LoadWorkerArtifact(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeManifest, err := filepath.Rel(cwd, artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkerArtifact(relativeManifest); err != nil {
		t.Fatalf("relative manifest: %v", err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if err := ApplyWorkerArtifact(context.Background(), repo, loaded); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "worker\n" {
		t.Fatalf("parent after apply: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "b.txt")); string(got) != "new\n" {
		t.Fatalf("new file: %q", got)
	}
}

func TestWorkerIsolationPreservesDirtyParent(t *testing.T) {
	repo := testRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("parent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := newWorkerWorktree(context.Background(), execenv.New(repo))
	if err == nil || !strings.Contains(err.Error(), "clean parent") {
		t.Fatalf("error = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "parent\n" {
		t.Fatalf("parent changed: %q", got)
	}
}

func TestWorkerArtifactRejectsChangedParentAndTamper(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, repo string, a *WorkerArtifact)
	}{
		{"dirty parent", func(t *testing.T, repo string, _ *WorkerArtifact) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("parent\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"changed head", func(t *testing.T, repo string, _ *WorkerArtifact) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("next\n"), 0600); err != nil {
				t.Fatal(err)
			}
			testGit(t, repo, "commit", "-qam", "next")
		}},
		{"tampered patch", func(t *testing.T, _ string, a *WorkerArtifact) {
			t.Helper()
			if err := os.WriteFile(a.PatchPath, []byte("evil"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			w, env, err := newWorkerWorktree(context.Background(), execenv.New(repo))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(env.Cwd, "a.txt"), []byte("worker\n"), 0600); err != nil {
				t.Fatal(err)
			}
			a, err := w.artifact(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := w.close(); err != nil {
				t.Fatal(err)
			}
			tc.change(t, repo, a)
			if err := ApplyWorkerArtifact(context.Background(), repo, *a); err == nil {
				t.Fatal("expected rejection")
			}
			if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) == "worker\n" {
				t.Fatal("patch applied unexpectedly")
			}
		})
	}
}

func TestIsolatedToolPath(t *testing.T) {
	root := t.TempDir()
	if err := isolatedToolPath(root, msg.ToolCall{Name: "task"}); err != nil {
		t.Fatalf("nested task was rejected: %v", err)
	}
	if err := isolatedToolPath(root, msg.ToolCall{Name: "write"}); err == nil {
		t.Fatal("write without a relative path was accepted")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		wantErr    bool
	}{
		{"relative", "src/a.go", false}, {"parent", "../escape", true}, {"absolute", filepath.Join(root, "a.go"), true}, {"symlink", "link/a.go", true}, {"home", "~/a", true}, {"git metadata", ".git", true}, {"git nested", "src/.GiT/config", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := isolatedToolPath(root, msg.ToolCall{Name: "write", Arguments: map[string]any{"path": tc.path}})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := isolatedToolPath(root, msg.ToolCall{Name: "bash"}); err == nil {
		t.Fatal("bash allowed")
	}
}

func TestWorkerIsolationRequiresWorkingDirectory(t *testing.T) {
	if _, _, err := newWorkerWorktreeWithSnapshot(context.Background(), nil, false); err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("nil worker environment: %v", err)
	}
	if _, _, err := newWorkerWorktreeWithSnapshot(context.Background(), execenv.New(""), false); err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("empty worker directory: %v", err)
	}
}

func TestWorkerCleanupAfterCancelledContext(t *testing.T) {
	repo := testRepo(t)
	w, _, err := newWorkerWorktree(context.Background(), execenv.New(repo))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.artifact(ctx); err == nil {
		t.Fatal("expected cancellation")
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.path); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
}

func TestDispatchIsolatedWorkerReturnsPatchWithoutChangingParent(t *testing.T) {
	script := `
model: faux-1
steps:
  - tool_call: {name: write, args: {path: "a.txt", content: "worker\n"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "complete"
`
	d, _, _ := newParentAndDispatcher(t, script, nil)
	repo := testRepo(t)
	d.Env = execenv.New(repo)
	d.IsolatedWorkers = true
	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "edit a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact == nil {
		t.Fatalf("no artifact: %+v", result)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "base\n" {
		t.Fatalf("parent changed: %q", got)
	}
	if err := ApplyWorkerArtifact(context.Background(), repo, *result.Artifact); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(got) != "worker\n" {
		t.Fatalf("apply = %q", got)
	}
}

func TestDispatchIsolatedWritePromptsOnce(t *testing.T) {
	script := `
model: faux-1
steps:
  - tool_call: {name: write, args: {path: "a.txt", content: "worker\n"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "complete"
`
	d, _, _ := newParentAndDispatcher(t, script, nil)
	repo := testRepo(t)
	d.Env = execenv.New(repo)
	d.IsolatedWorkers = true
	var prompts int
	d.Gate = permission.NewGate(permission.GateOptions{Roots: []string{repo}, Mode: settings.ModeManual, Prompt: func(context.Context, permission.Request) (permission.PromptChoice, error) {
		prompts++
		return permission.PromptChoice{Kind: permission.PromptAllow}, nil
	}})
	result, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "edit a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifact == nil {
		t.Fatalf("no artifact: %+v", result)
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1", prompts)
	}
}

func TestWorkerGitCommandsIgnoreInheritedGitEnvironment(t *testing.T) {
	repo := testRepo(t)
	other := testRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(other, ".git", "index"))
	w, _, err := newWorkerWorktree(context.Background(), execenv.New(repo))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadWorkerArtifactRejectsNonRegularAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "artifact.json")
	if err := os.WriteFile(manifest, []byte(`{"baseCommit":"x","patchPath":"`+filepath.Join(dir, "changes.patch")+`","unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkerArtifact(manifest); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkerArtifact(manifest); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestDispatchIsolatedNestedTaskStillUsesGate(t *testing.T) {
	script := `
model: faux-1
steps:
  - tool_call: {name: task, args: {subagent_type: general-purpose, prompt: "nested"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	d, _, _ := newParentAndDispatcher(t, script, nil)
	repo := testRepo(t)
	d.Env = execenv.New(repo)
	d.IsolatedWorkers = true
	d.Gate = permission.NewGate(permission.GateOptions{Roots: []string{repo}, Mode: settings.ModeManual, Permissions: settings.Permissions{Deny: []string{"Task"}}})
	var starts int
	d.OnEvent = func(ev SubagentEvent) {
		if ev.Kind == SubagentEventStart {
			starts++
		}
	}
	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "nested task"}); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("subagent starts = %d, want only outer worker", starts)
	}
}

func TestWorkerGitFiltersNeverExecute(t *testing.T) {
	repo := testRepo(t)
	marker := filepath.Join(t.TempDir(), "filter-ran")
	command := "sh -c 'touch \"" + marker + "\"; cat'"
	for _, name := range []string{"existing", "worker"} {
		testGit(t, repo, "config", "filter."+name+".clean", command)
		testGit(t, repo, "config", "filter."+name+".smudge", command)
		testGit(t, repo, "config", "filter."+name+".process", command)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=existing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", ".gitattributes")
	testGit(t, repo, "commit", "-qm", "attributes")
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	w, env, err := newWorkerWorktree(context.Background(), execenv.New(repo))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("smudge filter ran during checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(env.Cwd, "a.txt"), []byte("literal worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.Cwd, ".gitattributes"), []byte("*.txt filter=worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	artifact, err := w.artifact(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("clean filter ran during export: %v", err)
	}
	patch, err := os.ReadFile(artifact.PatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "+literal worker") {
		t.Fatalf("patch lacks literal content: %s", patch)
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
}

// Concurrent isolated workers must not run "git worktree add" or "remove" at
// the same time. Each lists every worktree of the repository and reads its
// commondir, so one add can read another's half-written admin directory and
// die with "failed to read .git/worktrees/<id>/commondir: Success" (seen in
// CI). The window is microseconds wide, so a git shim widens it: it marks any
// overlap of two worktree commands.
func TestConcurrentWorkerWorktreeCommandsDoNotOverlap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	repo := testRepo(t)
	shimDir := t.TempDir()
	busy := filepath.Join(shimDir, "busy")
	overlap := filepath.Join(shimDir, "overlap")
	shim := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = worktree ]; then\n" +
		"    mkdir '" + busy + "' 2>/dev/null || touch '" + overlap + "'\n" +
		"    sleep 0.1\n" +
		"    '" + realGit + "' \"$@\"; status=$?\n" +
		"    rmdir '" + busy + "' 2>/dev/null\n" +
		"    exit $status\n" +
		"  fi\n" +
		"done\n" +
		"exec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	const workers = 4
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			w, _, err := newWorkerWorktree(context.Background(), execenv.New(repo))
			if err == nil {
				err = w.close()
			}
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Errorf("worker worktree: %v", err)
		}
	}
	if _, err := os.Stat(overlap); err == nil {
		t.Fatal("concurrent git worktree add/remove commands overlapped")
	}
}
