package cli

import (
	"fmt"
	"os"
	"os/exec"
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
	assertBannerGolden(t, "banner-no-sessions", bannerRows(deps, 80))
}

// TestRenderGolden_BannerNarrow: below the art's minimum text column the
// banner drops the kiln and keeps the compact text-only rows.
func TestRenderGolden_BannerNarrow(t *testing.T) {
	tui.SetColorEnabled(true)
	tui.SetPlainMode(false)
	t.Cleanup(func() { tui.SetPlainMode(false) })

	chdirNoGit(t)
	t.Setenv("HOME", t.TempDir())

	Version = "0.9.2"
	deps := InteractiveDeps{Cwd: "/home/dev/relay-api", ModelLabel: "kiln-large", IsResume: false}
	assertBannerGolden(t, "banner-narrow", bannerRows(deps, 50))
}

// TestBannerPlainModeHasNoArt: screen-reader mode never draws the kiln.
func TestBannerPlainModeHasNoArt(t *testing.T) {
	tui.SetPlainMode(true)
	t.Cleanup(func() { tui.SetPlainMode(false) })
	chdirNoGit(t)
	t.Setenv("HOME", t.TempDir())
	rows := bannerRows(InteractiveDeps{Cwd: "/home/dev/relay-api", ModelLabel: "kiln-large"}, 120)
	if joined := strings.Join(rows, "\n"); strings.ContainsAny(joined, "█▀▄░▒▓") {
		t.Errorf("plain-mode banner draws block art:\n%s", joined)
	}
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
	var parentID string
	for i, prompt := range []string{"add the retry loop", "fix the upload timeout"} {
		st, meta, err := repo.Create(jsonl.CreateOptions{Cwd: testCwd})
		if err != nil {
			t.Fatalf("repo.Create: %v", err)
		}
		st.Close()
		if i == 0 {
			parentID = meta.ID
		}
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

	// A subagent's session: a child of the first session above
	// (ParentSessionID set), with the most recent mtime of all three and a
	// prompt that would sort first and read out of place if the banner ever
	// listed it — it must be excluded, not just outranked.
	childSt, childMeta, err := repo.Create(jsonl.CreateOptions{Cwd: testCwd, ParentSessionID: parentID})
	if err != nil {
		t.Fatalf("repo.Create (child): %v", err)
	}
	childSt.Close()
	childLine := `{"type":"message","text":"Check the Redis config and survey the upload tests","role":"user"}` + "\n"
	cf, err := os.OpenFile(childMeta.Path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open child session file: %v", err)
	}
	if _, err := cf.WriteString(childLine); err != nil {
		t.Fatalf("write child session line: %v", err)
	}
	cf.Close()
	childMt := now.Add(10 * time.Minute)
	if err := os.Chtimes(childMeta.Path, childMt, childMt); err != nil {
		t.Fatalf("chtimes (child): %v", err)
	}

	Version = "0.9.2"
	deps := InteractiveDeps{Cwd: testCwd, ModelLabel: "kiln-large", IsResume: false}
	rows := bannerRows(deps, 80)
	for _, row := range rows {
		if strings.Contains(row, "Redis") {
			t.Fatalf("banner listed a subagent (child) session: %q\nrows: %v", row, rows)
		}
	}
	assertBannerGolden(t, "banner-with-sessions", rows)
}

// initGitRepoWithBranch creates a fresh git repo at dir on the named
// branch with one empty commit — readGitStatus needs at least one commit
// before "git rev-parse --abbrev-ref HEAD" resolves to the branch name
// rather than erroring on an unborn HEAD.
func initGitRepoWithBranch(t *testing.T, dir, branch string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("checkout", "-q", "-b", branch)
	run("commit", "-q", "--allow-empty", "-m", "init")
}

// TestBannerRow1_LongCwdKeepsBranchAndModelAt120And80 is defect 2: at 120
// columns a long cwd used to render row 1 as
// "/private/tmp/.../real-proj · b…", truncating away the branch name and
// the whole model segment. bannerRows must now shorten the cwd first (a
// left-truncated "…/…" form) so " · branch <b> · model <m>" always
// survives, at both a realistic wide (120) and narrow (80) terminal.
func TestBannerRow1_LongCwdKeepsBranchAndModelAt120And80(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	const branch = "main"
	initGitRepoWithBranch(t, dir, branch)

	Version = "0.9.2"
	// The real repro's cwd (docs/kiln-design-handoff/Terminal.dc.html's own
	// example is "~/src/relay-api"; this is the long-cwd shape the real
	// 120-column session actually hit): a deep temp/scratchpad path, with
	// realistic (short) branch and model names — the defect was never
	// about a long branch/model, only a long cwd crowding them out.
	longCwd := "/private/tmp/very/deeply/nested/scratchpad/directory/for/a/realistic-project-name"
	const model = "kiln-large"
	deps := InteractiveDeps{Cwd: longCwd, ModelLabel: model, IsResume: false}

	for _, width := range []int{120, 80, 50} {
		rows := bannerRows(deps, width)
		// The repo line: beside the kiln art when it fits, row 1 of the
		// text-only banner otherwise.
		row1 := ""
		for _, r := range rows {
			if strings.Contains(ansi.Strip(r), "branch ") {
				row1 = r
				break
			}
		}
		plain := ansi.Strip(row1)
		if tui.VisibleWidth(row1) > width {
			t.Errorf("width %d: banner row 1 overflowed: %q (%d cols)", width, plain, tui.VisibleWidth(row1))
		}
		if !strings.Contains(plain, "· branch "+branch) {
			t.Errorf("width %d: branch missing from row 1: %q", width, plain)
		}
		if !strings.Contains(plain, "· model "+model) {
			t.Errorf("width %d: model missing from row 1: %q", width, plain)
		}
	}
}
