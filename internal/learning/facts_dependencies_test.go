package learning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/tools"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func proofFile(t *testing.T, cwd, rel string) tools.VerificationFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cwd, rel))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return tools.VerificationFile{Path: rel, SHA256: hex.EncodeToString(sum[:]), Mode: 0600}
}

// proofDir snapshots dir (relative to cwd) the way the verification tools do.
func proofDir(t *testing.T, cwd, dir string) tools.VerificationDirectory {
	t.Helper()
	top := filepath.Join(cwd, dir)
	d := tools.VerificationDirectory{Path: dir, Mode: 0700, Files: []tools.VerificationFile{}, Subdirectories: []tools.VerificationSubdirectory{}}
	err := filepath.Walk(top, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == top {
			return err
		}
		rel, _ := filepath.Rel(cwd, path)
		if info.IsDir() {
			d.Subdirectories = append(d.Subdirectories, tools.VerificationSubdirectory{Path: rel, Mode: uint32(info.Mode().Perm())})
			return nil
		}
		d.Files = append(d.Files, proofFile(t, cwd, rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(top, 0700); err != nil {
		t.Fatal(err)
	}
	return d
}

// promote records one proof launched from workdir and returns the stored fact.
func promote(t *testing.T, ctx context.Context, store *memory.Store, root, workdir string, outcome Outcome, proof tools.VerificationEvidence) *memory.Record {
	t.Helper()
	if err := RecordFactCandidatesIn(ctx, store, "project", root, workdir, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "stored" {
		t.Fatalf("not promoted: %+v %v", candidates, err)
	}
	return currentFact(t, ctx, store, root, workdir, proof)
}

func currentFact(t *testing.T, ctx context.Context, store *memory.Store, root, workdir string, proof tools.VerificationEvidence) *memory.Record {
	t.Helper()
	record, err := store.Current(ctx, "project", factKeyIn(workdir, proof), root)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestFactSubdirectoryLaunchStoresNormalisedPathsAndInvalidates(t *testing.T) {
	ctx, store, root, outcome, _ := factFixture(t)
	workdir := "internal/foo"
	cwd := filepath.Join(root, workdir)
	writeTree(t, root, map[string]string{"internal/foo/foo.go": "package foo\n"})
	proof := tools.VerificationEvidence{CallID: "call-1", Command: "go test ./...", Files: []tools.VerificationFile{proofFile(t, cwd, "foo.go")}}
	record := promote(t, ctx, store, root, workdir, outcome, proof)
	if !strings.Contains(record.Text, "(in internal/foo): go test ./...") {
		t.Fatalf("working directory missing from text: %q", record.Text)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || len(record.Sources) != 1 || record.Sources[0].Path != filepath.Join(canonical, "internal", "foo", "foo.go") {
		t.Fatalf("sources are not root-normalised: %+v %v", record.Sources, err)
	}
	// Another worktree of the same project sees the same fact, rebound to its root.
	other := t.TempDir()
	writeTree(t, other, map[string]string{"internal/foo/foo.go": "package foo\n"})
	if err := os.Chmod(filepath.Join(other, "internal/foo/foo.go"), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := store.Current(ctx, "project", factKeyIn(workdir, proof), other); err != nil || r == nil {
		t.Fatalf("fact not valid in a second worktree: %+v %v", r, err)
	}
	writeTree(t, root, map[string]string{"internal/foo/foo.go": "package foo // edited\n"})
	if r := currentFact(t, ctx, store, root, workdir, proof); r != nil {
		t.Fatalf("edited source still retrieved: %+v", r)
	}
}

func TestFactSubdirectoryLaunchRejectsEscapesAndOddNames(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	writeTree(t, root, map[string]string{"sub/x.go": "package x\n"})
	proof.Files = []tools.VerificationFile{proofFile(t, filepath.Join(root, "sub"), "x.go")}
	for _, workdir := range []string{"../sub", "sub/../sub", "/sub", "sub dir", ".git/x", "sub/"} {
		if err := RecordFactCandidatesIn(ctx, store, "project", root, workdir, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" {
		t.Fatalf("unsupported working directories: %+v %v", candidates, err)
	}
	// An evidence path that climbs out of the project is never a source.
	proof.Files = []tools.VerificationFile{{Path: "../../outside.go", SHA256: strings.Repeat("a", 64), Mode: 0600}}
	if reasonOf(t, ctx, store, root, "sub", outcome, proof) == "" {
		t.Fatal("escaping path promoted")
	}
}

func reasonOf(t *testing.T, ctx context.Context, store *memory.Store, root, workdir string, outcome Outcome, proof tools.VerificationEvidence) string {
	t.Helper()
	_, reason := factSourcesForPromotion(ctx, root, workdir, proof, func(string) bool { return true })
	return reason
}

func TestFactDirectoryDependencyInvalidatesOnMembershipAndContent(t *testing.T) {
	for _, workdir := range []string{"", "svc"} {
		t.Run("workdir="+workdir, func(t *testing.T) {
			ctx, store, root, outcome, _ := factFixture(t)
			prefix := ""
			if workdir != "" {
				prefix = workdir + "/"
			}
			writeTree(t, root, map[string]string{prefix + "pkg/a.go": "package pkg\n", prefix + "pkg/deep/b.go": "package deep\n", prefix + "go.mod": "module m\n"})
			cwd := filepath.Join(root, workdir)
			proof := tools.VerificationEvidence{CallID: "c", Command: "go test ./pkg", Files: []tools.VerificationFile{proofFile(t, cwd, "go.mod")}, Directories: []tools.VerificationDirectory{proofDir(t, cwd, "pkg")}, DirectoryFileLimit: 128, DirectoryByteLimit: 64 << 20}
			record := promote(t, ctx, store, root, workdir, outcome, proof)
			if len(record.Sources) != 2 || record.Sources[1].Kind != memory.SourceDir {
				t.Fatalf("directory source missing: %+v", record.Sources)
			}
			// A new member changes membership.
			added := filepath.Join(cwd, "pkg", "new.go")
			writeTree(t, cwd, map[string]string{"pkg/new.go": "package pkg\n"})
			if r := currentFact(t, ctx, store, root, workdir, proof); r != nil {
				t.Fatal("added directory member did not invalidate the fact")
			}
			if err := os.Remove(added); err != nil {
				t.Fatal(err)
			}
			if r := currentFact(t, ctx, store, root, workdir, proof); r == nil {
				t.Fatal("fact should be fresh again once membership is restored")
			}
			// A removed member and an edited member both invalidate too.
			writeTree(t, cwd, map[string]string{"pkg/deep/b.go": "package deep // edited\n"})
			if r := currentFact(t, ctx, store, root, workdir, proof); r != nil {
				t.Fatal("edited directory member did not invalidate the fact")
			}
		})
	}
}

func TestFactAbsentDependencyInvalidatesWhenPathAppears(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	proof.MissingFiles = []string{"go.work"}
	record := promote(t, ctx, store, root, "", outcome, proof)
	if len(record.Sources) != 2 || record.Sources[1].Kind != memory.SourceAbsent {
		t.Fatalf("absent source missing: %+v", record.Sources)
	}
	writeTree(t, root, map[string]string{"go.work": "go 1.26\n"})
	if r := currentFact(t, ctx, store, root, "", proof); r != nil {
		t.Fatal("appearing path did not invalidate the fact")
	}
	// A path that already exists is never captured as absent.
	if reasonOf(t, ctx, store, root, "", outcome, proof) == "" {
		t.Fatal("existing path accepted as absent")
	}
}

func TestFactDependencyReadPermissionStillGatesDirectories(t *testing.T) {
	ctx, _, root, _, _ := factFixture(t)
	writeTree(t, root, map[string]string{"pkg/a.go": "package pkg\n"})
	proof := tools.VerificationEvidence{CallID: "c", Command: "go test ./pkg", Directories: []tools.VerificationDirectory{proofDir(t, root, "pkg")}, MissingFiles: []string{"go.work"}, DirectoryFileLimit: 128, DirectoryByteLimit: 64 << 20}
	denied := factConfig()
	for name, deny := range map[string]string{"directory": "pkg", "member": "pkg/a.go", "absent path": "go.work"} {
		denied.FactReadAllowed = func(path string) bool { return path != deny }
		if _, reason := factSourcesForPromotion(ctx, root, "", proof, denied.FactReadAllowed); reason == "" {
			t.Errorf("%s: denied Read still promoted", name)
		} else if name != "member" && !strings.Contains(reason, "Read permission") {
			t.Errorf("%s: reason %q", name, reason)
		}
	}
	if _, reason := factSourcesForPromotion(ctx, root, "", proof, factConfig().FactReadAllowed); reason != "" {
		t.Fatalf("allowed Read refused: %s", reason)
	}
}
