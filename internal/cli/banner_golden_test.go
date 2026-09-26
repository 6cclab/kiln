package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/andrepato/harness/internal/session/jsonl"
	"github.com/andrepato/harness/internal/tui"
)

// Render snapshots for bannerRows (Phase 5 of the kiln UI pass). bannerRows
// lives in package cli, so it cannot reuse internal/tui's unexported
// assertRenderGolden; this is the same ~20-line compare helper copied
// verbatim (see internal/tui/golden_test.go's comment for the format).
//
// bannerRows itself calls readGitStatus(context.Background()), which reads
// git status from the process's real os.Getwd() (not deps.Cwd) — so these
// tests t.Chdir into a fresh, non-git temp directory to keep that segment
// deterministic (no branch/dirty marker). recentSessionRows likewise goes
// through jsonl.NewRepo(""), which is hardcoded to "$HOME/.harness/sessions"
// rather than taking a root — these tests set $HOME via t.Setenv to a temp
// directory so they never touch the real user's session store.
// bannerGoldenPath resolves a path under internal/cli/testdata/banner,
// relative to this source file rather than the process cwd — several of
// these tests t.Chdir elsewhere to keep bannerRows' git lookup
// deterministic, so a cwd-relative path would write into (or read from)
// the wrong directory.
func bannerGoldenPath(name string) string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "testdata", "banner", name)
}

func assertBannerGolden(t *testing.T, name string, lines []string) {
	t.Helper()

	plainPath := bannerGoldenPath(name + ".txt")
	stylesPath := bannerGoldenPath(name + ".styles.txt")

	gotStyles := strings.Join(lines, "\n") + "\n"
	plainLines := make([]string, len(lines))
	for i, l := range lines {
		plainLines[i] = ansi.Strip(l)
	}
	gotPlain := strings.Join(plainLines, "\n") + "\n"

	if os.Getenv("UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(plainPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plainPath, []byte(gotPlain), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stylesPath, []byte(gotStyles), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("UPDATE=1: wrote %s and %s", plainPath, stylesPath)
		return
	}

	wantPlain, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `UPDATE=1 go test ./internal/cli -run %s` to create it)", plainPath, err, t.Name())
	}
	wantStyles, err := os.ReadFile(stylesPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `UPDATE=1 go test ./internal/cli -run %s` to create it)", stylesPath, err, t.Name())
	}

	if string(wantPlain) != gotPlain {
		t.Errorf("plain output does not match golden %s\n--- want ---\n%s\n--- got ---\n%s\n(run `UPDATE=1 go test ./internal/cli -run %s` to update)",
			plainPath, wantPlain, gotPlain, t.Name())
	}
	if string(wantStyles) != gotStyles {
		t.Errorf("styled output does not match golden %s\n--- want ---\n%s\n--- got ---\n%s\n(run `UPDATE=1 go test ./internal/cli -run %s` to update)",
			stylesPath, wantStyles, gotStyles, t.Name())
	}
}

// chdirNoGit chdirs the test into a fresh temp directory outside any git
// repository, so readGitStatus's os.Getwd()-based lookup deterministically
// fails (no branch segment).
func chdirNoGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func TestRenderGolden_BannerNoSessions(t *testing.T) {
	tui.SetColorEnabled(true)
	tui.SetPlainMode(false)
	t.Cleanup(func() { tui.SetPlainMode(false) })

	chdirNoGit(t)
	t.Setenv("HOME", t.TempDir()) // empty session store: no "Recent sessions" block

	Version = "0.9.2"
	deps := InteractiveDeps{Cwd: "/home/dev/relay-api", ModelLabel: "kiln-large", IsResume: false}
	assertBannerGolden(t, "banner-no-sessions", bannerRows(deps))
}

func TestRenderGolden_BannerWithSessions(t *testing.T) {
	tui.SetColorEnabled(true)
	tui.SetPlainMode(false)
	t.Cleanup(func() { tui.SetPlainMode(false) })

	chdirNoGit(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	const testCwd = "/home/dev/relay-api"
	repo, err := jsonl.NewRepo("")
	if err != nil {
		t.Fatalf("jsonl.NewRepo: %v", err)
	}
	now := time.Now()
	for i, prompt := range []string{"add the retry loop", "fix the upload timeout"} {
		st, meta, err := repo.Create(jsonl.CreateOptions{Cwd: testCwd})
		if err != nil {
			t.Fatalf("repo.Create: %v", err)
		}
		st.Close()
		// firstUserMessageTitle scans for `"role":"user"` and then looks
		// BACKWARD for the last `"text":"` before it — so the text field
		// must appear before the role field in this raw line.
		line := fmt.Sprintf(`{"type":"message","text":"%s","role":"user"}`+"\n", prompt)
		f, err := os.OpenFile(meta.Path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("open session file: %v", err)
		}
		if _, err := f.WriteString(line); err != nil {
			t.Fatalf("write session line: %v", err)
		}
		f.Close()
		// Stagger mtimes so recentSessionRows' sort is deterministic (most
		// recent first): the second-created session sorts first.
		mt := now.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(meta.Path, mt, mt); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	Version = "0.9.2"
	deps := InteractiveDeps{Cwd: testCwd, ModelLabel: "kiln-large", IsResume: false}
	assertBannerGolden(t, "banner-with-sessions", bannerRows(deps))
}
