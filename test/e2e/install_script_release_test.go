//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// These tests cover install.sh's release path (downloading a prebuilt
// archive and verifying its checksum) without any network access: a
// local httptest server stands in for GitHub's release asset CDN, pointed
// at through KILN_RELEASE_BASE (see the script's "RELEASE_BASE" comment).
// They build the real binary for the host's own platform, since the
// installed binary's --version has to actually run.

// runInstallEnv is runInstall with extra environment variables, used here
// to set KILN_RELEASE_BASE.
func runInstallEnv(t *testing.T, tmpdir string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(repoRootDir(t), "scripts", "install.sh")}, args...)...)
	cmd.Env = append(append(os.Environ(), "TMPDIR="+tmpdir), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// hostReleasePlatform returns the goos/goarch spelling install.sh's
// release_platform computes for the host running the test (uname-based,
// but it matches runtime.GOOS/GOARCH for darwin/linux/amd64/arm64, which
// is all CI and local development runs this suite on).
func hostReleasePlatform(t *testing.T) (string, string) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin", "linux":
	default:
		t.Skipf("release asset tests assume darwin or linux, this is %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		t.Skipf("release asset tests assume amd64 or arm64, this is %s", runtime.GOARCH)
	}
	return runtime.GOOS, runtime.GOARCH
}

// buildReleaseAsset builds kiln at version for the host's platform and
// archives it the way release.yml does
// (kiln_<version>_<goos>_<goarch>.tar.gz), writing the archive into dir.
// Returns the archive's path and basename.
func buildReleaseAsset(t *testing.T, dir, version string) (path, name string) {
	t.Helper()
	goos, goarch := hostReleasePlatform(t)

	work := t.TempDir()
	bin := filepath.Join(work, "kiln")
	build := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-X github.com/andrepato/harness/internal/cli.Version="+version,
		"-o", bin, "./cmd/kiln")
	build.Dir = repoRootDir(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	name = fmt.Sprintf("kiln_%s_%s_%s", version, goos, goarch)
	stageParent := filepath.Join(work, "stage")
	stage := filepath.Join(stageParent, name)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "kiln"), data, 0o755); err != nil {
		t.Fatal(err)
	}

	path = filepath.Join(dir, name+".tar.gz")
	tarCmd := exec.Command("tar", "-C", stageParent, "-czf", path, name)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		t.Fatalf("tar: %v\n%s", err, out)
	}
	return path, name + ".tar.gz"
}

// writeChecksums writes dir/checksums.txt with one correct sha256 line per
// archive path given.
func writeChecksums(t *testing.T, dir string, archives ...string) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, a := range archives {
		data, err := os.ReadFile(a)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if _, err := fmt.Fprintf(f, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(a)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestInstallScript_ReleaseGoodChecksum: install.sh fetches the release
// archive for this platform and installs it once its checksum verifies.
func TestInstallScript_ReleaseGoodChecksum(t *testing.T) {
	serveDir := t.TempDir()
	archivePath, _ := buildReleaseAsset(t, serveDir, "v9.9.9")
	writeChecksums(t, serveDir, archivePath)

	srv := httptest.NewServer(http.FileServer(http.Dir(serveDir)))
	defer srv.Close()

	dir, tmp := t.TempDir(), t.TempDir()
	out, err := runInstallEnv(t, tmp, []string{"KILN_RELEASE_BASE=" + srv.URL}, "--ref", "v9.9.9", "--dir", dir)
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "fetching") {
		t.Errorf("install.sh did not report fetching a release:\n%s", out)
	}
	if v := kilnVersion(t, filepath.Join(dir, "kiln")); !strings.Contains(v, "v9.9.9") {
		t.Errorf("--version = %q, want it to name v9.9.9", v)
	}
	assertEmptyDir(t, tmp)
}

// TestInstallScript_ReleaseBadChecksum: a checksums.txt that does not
// match the archive makes install.sh fail and install nothing, rather
// than silently building from source instead.
func TestInstallScript_ReleaseBadChecksum(t *testing.T) {
	serveDir := t.TempDir()
	_, archiveName := buildReleaseAsset(t, serveDir, "v9.9.8")
	bogus := strings.Repeat("0", 64)
	if err := os.WriteFile(filepath.Join(serveDir, "checksums.txt"),
		[]byte(bogus+"  "+archiveName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.FileServer(http.Dir(serveDir)))
	defer srv.Close()

	dir, tmp := t.TempDir(), t.TempDir()
	out, err := runInstallEnv(t, tmp, []string{"KILN_RELEASE_BASE=" + srv.URL}, "--ref", "v9.9.8", "--dir", dir)
	if err == nil {
		t.Fatalf("install.sh succeeded despite a checksum mismatch\n%s", out)
	}
	if !strings.Contains(out, "checksum mismatch") {
		t.Errorf("no checksum mismatch message in output:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "kiln")); statErr == nil {
		t.Error("a kiln binary was installed despite the checksum mismatch")
	}
	assertEmptyDir(t, tmp)
}

// TestInstallScript_ReleaseMissingAssetFallsBackToSource: when the
// release server has no archive for this platform/ref (e.g. no release
// was ever published, or not for this OS/arch), install.sh falls back to
// building from source rather than failing.
func TestInstallScript_ReleaseMissingAssetFallsBackToSource(t *testing.T) {
	hostReleasePlatform(t) // skip on platforms the other release tests skip, for symmetry

	root := repoRootDir(t)
	work := t.TempDir()
	src, repo := filepath.Join(work, "src"), filepath.Join(work, "repo.git")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-c", `git -C "$1" archive HEAD | tar -x -C "$2"`, "sh", root, src).CombinedOutput(); err != nil {
		t.Fatalf("git archive: %v\n%s", err, out)
	}
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=kiln test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=kiln test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	for _, args := range [][]string{
		{"-C", src, "init", "--quiet", "--initial-branch", "main"},
		{"-C", src, "add", "-A"},
		{"-C", src, "commit", "--quiet", "--no-gpg-sign", "-m", "test"},
		{"-C", src, "tag", "v9.9.7"},
		{"clone", "--quiet", "--bare", src, repo},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// An empty release server: no archive, no checksums.txt for any ref.
	srv := httptest.NewServer(http.FileServer(http.Dir(t.TempDir())))
	defer srv.Close()

	dir, tmp := t.TempDir(), t.TempDir()
	out, err := runInstallEnv(t, tmp,
		[]string{"KILN_RELEASE_BASE=" + srv.URL},
		"--ref", "v9.9.7", "--repo", "file://"+repo, "--dir", dir)
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "cloning") {
		t.Errorf("install.sh did not fall back to cloning from source:\n%s", out)
	}
	if v := kilnVersion(t, filepath.Join(dir, "kiln")); !strings.Contains(v, "v9.9.7") {
		t.Errorf("--version = %q, want it to name v9.9.7", v)
	}
	assertEmptyDir(t, tmp)
}
