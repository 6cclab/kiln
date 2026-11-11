package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tool"
)

// Repository contents can be larger than a worker's patch budget. Git output
// must remain bounded even when a command asks for a tracked large file.
func TestWorkerGitOutputIsBounded(t *testing.T) {
	repo := testRepo(t)
	large := strings.Repeat("x", maxWorkerPatchBytes+131072)
	if err := os.WriteFile(filepath.Join(repo, "large.txt"), []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "large.txt")
	testGit(t, repo, "commit", "-qm", "large file")
	if out, err := gitRun(context.Background(), repo, "show", "HEAD:large.txt"); err == nil {
		t.Fatalf("large Git output was accepted: %d bytes", len(out))
	}
}

func TestWorkerReportTruncatesAtUTF8Boundary(t *testing.T) {
	report := strings.Repeat("a", maxWorkerReportBytes-10) + strings.Repeat("é", 100)
	got := boundedWorkerReport(report)
	if len(got) > maxWorkerReportBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, "[worker report truncated]") {
		t.Fatalf("report was not safely bounded: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 100)) {
		t.Fatal("report prefix was lost")
	}
}

// A verified patch can still target a dangerous path in the parent checkout.
// Preflight and apply must both reject it before touching any file.
func TestWorkerBatchRefusesParentFilesystemObstacles(t *testing.T) {
	for _, tc := range []struct {
		name, destination, expected string
		setup                       func(*testing.T, string)
	}{
		{"symlink parent", "link/escape.txt", "symlink", func(t *testing.T, repo string) {
			outside := t.TempDir()
			if err := os.Symlink(outside, filepath.Join(repo, "link")); err != nil {
				t.Fatal(err)
			}
			testGit(t, repo, "add", "link")
			testGit(t, repo, "commit", "-qm", "link")
			t.Cleanup(func() {
				if _, err := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(err) {
					t.Errorf("worker patch wrote outside checkout: %v", err)
				}
			})
		}},
		{"file parent", "a.txt/child.txt", "non-directory", func(*testing.T, string) {}},
		{"directory endpoint", "dir", "nonregular", func(t *testing.T, repo string) {
			if err := os.Mkdir(filepath.Join(repo, "dir"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "dir", "keep.txt"), []byte("keep\n"), 0600); err != nil {
				t.Fatal(err)
			}
			testGit(t, repo, "add", "dir/keep.txt")
			testGit(t, repo, "commit", "-qm", "directory")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			tc.setup(t, repo)
			a := batchArtifact(t, repo, "safe.txt", "safe\n")
			patch := mustReadBatchPatch(t, a.PatchPath)
			rewriteBatchArtifact(t, &a, []byte(strings.ReplaceAll(string(patch), "safe.txt", tc.destination)))
			a.ChangedFiles = []string{tc.destination}
			// Match the manifest to isolate path validation from manifest validation.
			rewriteBatchArtifact(t, &a, mustReadBatchPatch(t, a.PatchPath))
			if _, err := InspectWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a}); err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("inspection error = %v, want %q", err, tc.expected)
			}
			if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a}); err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("apply error = %v, want %q", err, tc.expected)
			}
			if _, err := os.Stat(filepath.Join(repo, "safe.txt")); !os.IsNotExist(err) {
				t.Fatalf("rejected patch changed parent: %v", err)
			}
		})
	}
}

// Artifacts are external files retained between worker completion and
// integration. Each read must validate the current file, including its type,
// size, and manifest shape, before the parent can apply it.
func TestWorkerArtifactLoadRejectsUnsafeRetainedFiles(t *testing.T) {
	repo := testRepo(t)
	a := batchArtifact(t, repo, "safe.txt", "safe\n")
	manifest, err := os.ReadFile(a.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, contents, expected        string
		asSymlink, useOriginal, missing bool
	}{
		{"trailing document", string(manifest) + "\n{}", "trailing data", false, false, false},
		{"patch outside manifest directory", strings.Replace(string(manifest), a.PatchPath, filepath.Join(repo, "changes.patch"), 1), "beside its manifest", false, false, false},
		{"wrong patch filename", strings.Replace(string(manifest), a.PatchPath, filepath.Join(filepath.Dir(a.PatchPath), "other.patch"), 1), "invalid worker patch name", false, true, false},
		{"oversized manifest", strings.Repeat(" ", 65537), "exceeds", false, false, false},
		{"symlink manifest", string(manifest), "not a regular file", true, false, false},
		{"malformed manifest", "{", "unexpected EOF", false, false, false},
		{"missing manifest", "", "no such file", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact.json")
			if tc.useOriginal {
				path = a.ManifestPath
				t.Cleanup(func() { _ = os.WriteFile(path, manifest, 0600) })
			}
			if tc.missing {
				// Keep the path absent to test a removed retained artifact.
			} else if tc.asSymlink {
				if err := os.Symlink(a.ManifestPath, path); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadWorkerArtifact(path); err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("LoadWorkerArtifact = %v, want %q", err, tc.expected)
			}
		})
	}
}

func TestWorkerBatchRejectsEmptyAndOversizedRequests(t *testing.T) {
	repo := testRepo(t)
	for _, artifacts := range [][]WorkerArtifact{nil, make([]WorkerArtifact, maxWorkerBatchArtifacts+1)} {
		if _, err := InspectWorkerArtifactBatch(context.Background(), repo, artifacts); err == nil {
			t.Fatalf("inspection accepted %d artifacts", len(artifacts))
		}
		if err := ApplyWorkerArtifactBatch(context.Background(), repo, artifacts); err == nil {
			t.Fatalf("apply accepted %d artifacts", len(artifacts))
		}
	}
	// A retained patch cannot be applied outside its Git checkout.
	if err := ApplyWorkerArtifactBatch(context.Background(), t.TempDir(), []WorkerArtifact{{}}); err == nil {
		t.Fatal("apply accepted a directory that is not a Git checkout")
	}
}

func TestWorkerBatchRejectsRemovedManifestAndRenameMetadata(t *testing.T) {
	for _, tc := range []struct {
		name, expected string
		change         func(*testing.T, *WorkerArtifact)
	}{
		{"removed manifest", "manifest", func(t *testing.T, a *WorkerArtifact) {
			if err := os.Remove(a.ManifestPath); err != nil {
				t.Fatal(err)
			}
		}},
		{"rename metadata", "rename or copy", func(t *testing.T, a *WorkerArtifact) {
			patch := mustReadBatchPatch(t, a.PatchPath)
			rewriteBatchArtifact(t, a, append([]byte("rename from safe.txt\n"), patch...))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepo(t)
			a := batchArtifact(t, repo, "safe.txt", "safe\n")
			tc.change(t, &a)
			if err := ApplyWorkerArtifactBatch(context.Background(), repo, []WorkerArtifact{a}); err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("apply error = %v, want %q", err, tc.expected)
			}
			if _, err := os.Stat(filepath.Join(repo, "safe.txt")); !os.IsNotExist(err) {
				t.Fatalf("rejected patch changed parent: %v", err)
			}
		})
	}
}

func TestIsolatedWorkerWriteRejectsMalformedAndEscapingArguments(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "escape-from-worker.txt")
	var write *tool.Tool
	for _, candidate := range isolatedFileTools(execenv.New(root), root, nil, nil, nil) {
		if candidate.Name == "write" {
			write = candidate
		}
	}
	if write == nil {
		t.Fatal("isolated worker write tool missing")
	}
	for _, raw := range []string{`{`, `{"path":"../escape-from-worker.txt","content":"bad"}`} {
		result, err := write.Execute(context.Background(), []byte(raw), nil, tool.Invocation{Cwd: root})
		if err != nil || !result.IsError {
			t.Fatalf("worker write %q: result=%+v err=%v, want refusal", raw, result, err)
		}
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("worker wrote outside isolated tree: %v", err)
	}
}
