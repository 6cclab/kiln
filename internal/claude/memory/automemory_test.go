package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitT runs git in dir, failing the test on error. Kept local to this
// test file (not exported) since only these tests need to drive real git
// repos to exercise projectRoot's git-common-dir resolution.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=kiln-test", "GIT_AUTHOR_EMAIL=kiln-test@example.com",
		"GIT_COMMITTER_NAME=kiln-test", "GIT_COMMITTER_EMAIL=kiln-test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitT(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "add", "f.txt")
	runGitT(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestProjectRoot_PlainRepo(t *testing.T) {
	repo := initRepo(t)
	if got := projectRoot(repo); got != mustResolve(t, repo) {
		t.Errorf("projectRoot(%q) = %q, want %q", repo, got, mustResolve(t, repo))
	}
}

func TestProjectRoot_Subdirectory(t *testing.T) {
	repo := initRepo(t)
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := projectRoot(sub), mustResolve(t, repo); got != want {
		t.Errorf("projectRoot(subdir) = %q, want %q (repo root)", got, want)
	}
}

func TestProjectRoot_WorktreeSharesMainRoot(t *testing.T) {
	repo := initRepo(t)
	wtParent := t.TempDir()
	wt := filepath.Join(wtParent, "wt")
	runGitT(t, repo, "worktree", "add", "-q", wt, "-b", "wt-branch")

	mainRoot := projectRoot(repo)
	wtRoot := projectRoot(wt)
	if mainRoot != wtRoot {
		t.Errorf("worktree root %q != main root %q; auto-memory would split across worktrees", wtRoot, mainRoot)
	}
	if mainRoot != mustResolve(t, repo) {
		t.Errorf("main root = %q, want %q", mainRoot, mustResolve(t, repo))
	}
}

func TestProjectRoot_NonGitUsesCwd(t *testing.T) {
	dir := t.TempDir()
	if got, want := projectRoot(dir), mustResolve(t, dir); got != want {
		t.Errorf("projectRoot(non-git) = %q, want %q", got, want)
	}
}

func mustResolve(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	return resolved
}

func TestProjectDirName_MatchesRealConvention(t *testing.T) {
	// Verified 2026-09-30 against real ~/.claude/projects/ directory
	// names on the machine this was written on: every "/" becomes "-",
	// including the leading one.
	got := projectDirName("/Users/andrepato/projects/harness")
	want := "-Users-andrepato-projects-harness"
	if got != want {
		t.Errorf("projectDirName = %q, want %q", got, want)
	}
}

// TestProjectDirName_UnderscoreAlsoEscaped fails without the character-
// class fix (replacing only "/" left "_" untouched): this machine's own
// $TMPDIR, /var/folders/93/248j_5ds3ls8k4ggh_fxndjh0000gn/T (symlink-
// resolved to /private/var/...), has two real entries under
// ~/.claude/projects/ whose names replace both underscores with "-", not
// leave them as "_" — confirmed by a read-only `ls` of that directory.
func TestProjectDirName_UnderscoreAlsoEscaped(t *testing.T) {
	got := projectDirName("/private/var/folders/93/248j_5ds3ls8k4ggh_fxndjh0000gn/T/claude-cli-transport-dUDLvV")
	want := "-private-var-folders-93-248j-5ds3ls8k4ggh-fxndjh0000gn-T-claude-cli-transport-dUDLvV"
	if got != want {
		t.Errorf("projectDirName = %q, want %q", got, want)
	}
}

func TestResolveAutoMemoryDir_Default(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initRepo(t)

	dir := ResolveAutoMemoryDir(repo, "")
	want := filepath.Join(home, ".claude", "projects", projectDirName(mustResolve(t, repo)), "memory")
	if dir != want {
		t.Errorf("ResolveAutoMemoryDir = %q, want %q", dir, want)
	}
}

func TestResolveAutoMemoryDir_DirectoryOverrideAbsolute(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-memory")
	dir := ResolveAutoMemoryDir("/some/cwd", override)
	if dir != filepath.Clean(override) {
		t.Errorf("got %q, want %q", dir, override)
	}
}

func TestResolveAutoMemoryDir_DirectoryOverrideHomeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := ResolveAutoMemoryDir("/some/cwd", "~/my-memory")
	want := filepath.Join(home, "my-memory")
	if dir != want {
		t.Errorf("got %q, want %q", dir, want)
	}
}

func TestLoadAutoMemory_DisabledByEnv(t *testing.T) {
	cwd := t.TempDir()
	got := LoadAutoMemory(cwd, AutoMemoryOptions{EnvDisabled: true, BudgetTokens: 1000})
	if got.Status != AutoMemorySkipped || !got.Disabled {
		t.Errorf("got %+v, want skipped+disabled", got)
	}
	if got.Text != "" {
		t.Errorf("expected no text when disabled, got %q", got.Text)
	}
}

func TestLoadAutoMemory_DisabledBySetting(t *testing.T) {
	cwd := t.TempDir()
	f := false
	got := LoadAutoMemory(cwd, AutoMemoryOptions{Enabled: &f, BudgetTokens: 1000})
	if got.Status != AutoMemorySkipped || !got.Disabled || got.Reason == "" {
		t.Errorf("got %+v, want skipped+disabled with a reason", got)
	}
}

func TestLoadAutoMemory_SkippedOnSmallTier(t *testing.T) {
	cwd := t.TempDir()
	got := LoadAutoMemory(cwd, AutoMemoryOptions{SmallTier: true, BudgetTokens: 1000})
	if got.Status != AutoMemorySkipped || !got.Disabled {
		t.Errorf("got %+v, want skipped on small tier", got)
	}
}

func TestLoadAutoMemory_NoMemoryDirYet(t *testing.T) {
	cwd := t.TempDir()
	got := LoadAutoMemory(cwd, AutoMemoryOptions{Directory: filepath.Join(cwd, "nope"), BudgetTokens: 1000})
	if got.Status != AutoMemorySkipped || got.Disabled {
		t.Errorf("got %+v, want skipped (not disabled): no MEMORY.md", got)
	}
}

func TestLoadAutoMemory_LoadsWithinBudget(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "mem")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "MEMORY.md"), "- user_role: works on kiln\n- feedback_testing: prefers table-driven tests\n")

	got := LoadAutoMemory(cwd, AutoMemoryOptions{Directory: dir, BudgetTokens: 1000})
	if got.Status != AutoMemoryLoaded {
		t.Fatalf("got status %v reason %q, want loaded", got.Status, got.Reason)
	}
	if !strings.Contains(got.Text, "user_role: works on kiln") {
		t.Errorf("text missing memory content: %s", got.Text)
	}
	if !strings.Contains(got.Text, dir) {
		t.Errorf("text should name the memory dir so the model can read topic files: %s", got.Text)
	}
	if got.Disabled {
		t.Errorf("loaded index should not report Disabled")
	}
}

// TestLoadAutoMemory_CapsAt200Lines fails without capMemoryIndex's line
// cap: a MEMORY.md with 500 one-line entries must load only the first 200.
func TestLoadAutoMemory_CapsAt200Lines(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "mem")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("- entry line that is reasonably short\n")
	}
	writeFile(t, filepath.Join(dir, "MEMORY.md"), sb.String())

	got := LoadAutoMemory(cwd, AutoMemoryOptions{Directory: dir, BudgetTokens: 1_000_000})
	if got.Lines != AutoMemoryMaxLines {
		t.Errorf("Lines = %d, want %d (the 200-line cap)", got.Lines, AutoMemoryMaxLines)
	}
	if got.Status != AutoMemoryTrimmed {
		t.Errorf("status = %v, want trimmed", got.Status)
	}
	if strings.Count(got.Text, "entry line") > AutoMemoryMaxLines {
		t.Errorf("text carries more than %d entries", AutoMemoryMaxLines)
	}
}

// TestLoadAutoMemory_CapsAt25KB fails without capMemoryIndex's byte cap: a
// MEMORY.md under 200 lines but over 25KB must still be cut at the byte
// limit, not loaded in full.
func TestLoadAutoMemory_CapsAt25KB(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "mem")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 100 lines, each ~300 bytes: ~30KB total, well under the 200-line cap
	// but over the 25KB one.
	line := "- " + strings.Repeat("x", 290) + "\n"
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString(line)
	}
	content := sb.String()
	if len(content) <= AutoMemoryMaxBytes {
		t.Fatalf("test fixture too small: %d bytes", len(content))
	}
	writeFile(t, filepath.Join(dir, "MEMORY.md"), content)

	got := LoadAutoMemory(cwd, AutoMemoryOptions{Directory: dir, BudgetTokens: 1_000_000})
	if got.Bytes > AutoMemoryMaxBytes {
		t.Errorf("Bytes = %d, want <= %d (the 25KB cap)", got.Bytes, AutoMemoryMaxBytes)
	}
	if got.Status != AutoMemoryTrimmed {
		t.Errorf("status = %v, want trimmed", got.Status)
	}
}

// TestLoadAutoMemory_TrimmedByTierBudget fails without the system-prompt
// budget check: a MEMORY.md that fits the 200-line/25KB cap but not the
// (small) remaining tier budget must still be trimmed further, not loaded
// whole.
func TestLoadAutoMemory_TrimmedByTierBudget(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "mem")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("- some memory entry with real content\n", 20) // ~780 bytes, ~195 tokens
	writeFile(t, filepath.Join(dir, "MEMORY.md"), content)

	got := LoadAutoMemory(cwd, AutoMemoryOptions{Directory: dir, BudgetTokens: 20})
	if got.Status != AutoMemoryTrimmed {
		t.Fatalf("status = %v reason %q, want trimmed", got.Status, got.Reason)
	}
	if len(got.Text) >= len(content) {
		t.Errorf("text (%d bytes) should be shorter than the full file (%d bytes)", len(got.Text), len(content))
	}
}

func TestResolveAutoMemoryDir_RelativeOverrideJoinsCwd(t *testing.T) {
	cwd := t.TempDir()
	dir := ResolveAutoMemoryDir(cwd, "relative-memory")
	want := filepath.Clean(filepath.Join(cwd, "relative-memory"))
	if dir != want {
		t.Errorf("got %q, want %q", dir, want)
	}
}

func TestAutoMemory_StatusLine(t *testing.T) {
	cases := []struct {
		name string
		a    AutoMemory
		want string
	}{
		{"loaded", AutoMemory{Status: AutoMemoryLoaded, Lines: 3, Dir: "/mem"}, "loaded (3 lines) from /mem"},
		{"trimmed", AutoMemory{Status: AutoMemoryTrimmed, Reason: "too big", Dir: "/mem"}, "trimmed (too big) from /mem"},
		{"skipped with reason", AutoMemory{Status: AutoMemorySkipped, Reason: "disabled"}, "skipped (disabled)"},
		{"skipped without reason", AutoMemory{Status: AutoMemorySkipped}, "skipped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.a.StatusLine(); got != c.want {
				t.Errorf("StatusLine() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestResolveAutoMemoryDir_RejectsRootOverride fails without
// unsafeAutoMemoryDirectory's "/" check: an autoMemoryDirectory of "/"
// would otherwise be honoured and, once added as a read-only permission
// root, would let the read tool open anything on disk without asking.
func TestResolveAutoMemoryDir_RejectsRootOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	dir, rejected := resolveAutoMemoryDir(cwd, "/")
	if !rejected {
		t.Fatal("expected \"/\" to be rejected as an auto-memory directory override")
	}
	if dir == "/" {
		t.Errorf("dir = %q, want the default directory, not the root override", dir)
	}
}

// TestResolveAutoMemoryDir_RejectsHomeOverride fails without
// unsafeAutoMemoryDirectory's home-or-ancestor check: an
// autoMemoryDirectory of "~/" (or "~/.ssh", an ancestor-of-home example is
// covered by the home check itself since .ssh is a *descendant*, not an
// ancestor — home itself and any of ITS ancestors are what must be
// rejected) would, once added as a read-only permission root, let the read
// tool open the user's entire home directory without asking.
func TestResolveAutoMemoryDir_RejectsHomeOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	dir, rejected := resolveAutoMemoryDir(cwd, "~/")
	if !rejected {
		t.Fatal("expected the home directory itself to be rejected as an override")
	}
	if dir == filepath.Clean(home) {
		t.Errorf("dir = %q, want the default directory, not home itself", dir)
	}
}

// TestResolveAutoMemoryDir_RejectsAncestorOfHomeOverride fails the same
// way: an override that is an ancestor of home (e.g. home's parent
// directory) is just as dangerous as home itself, since every read under
// it - including all of home - would be exempted from the workspace
// prompt.
func TestResolveAutoMemoryDir_RejectsAncestorOfHomeOverride(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	dir, rejected := resolveAutoMemoryDir(cwd, parent)
	if !rejected {
		t.Fatal("expected an ancestor of home to be rejected as an override")
	}
	if dir == filepath.Clean(parent) {
		t.Errorf("dir = %q, want the default directory, not the ancestor override", dir)
	}
}

// TestResolveAutoMemoryDir_SafeOverrideStillHonoured is the control: a
// perfectly ordinary override (some unrelated temp directory) must still
// be honoured and not flagged, so the safety check above isn't just
// rejecting everything.
func TestResolveAutoMemoryDir_SafeOverrideStillHonoured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	override := filepath.Join(t.TempDir(), "my-memory")

	dir, rejected := resolveAutoMemoryDir(cwd, override)
	if rejected {
		t.Fatalf("an ordinary override should not be rejected, got dir=%q", dir)
	}
	if dir != filepath.Clean(override) {
		t.Errorf("dir = %q, want %q", dir, override)
	}
}

func TestLoadAutoMemory_NoBudgetLeftSkips(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, "mem")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "MEMORY.md"), "- something\n")

	got := LoadAutoMemory(cwd, AutoMemoryOptions{Directory: dir, BudgetTokens: 0})
	if got.Status != AutoMemorySkipped || got.Disabled {
		t.Errorf("got %+v, want skipped (not disabled) when no budget remains", got)
	}
}

// TestLoadAutoMemory_UnsafeOverrideRejectedAndReported fails without
// LoadAutoMemory routing through resolveAutoMemoryDir (versus calling
// ResolveAutoMemoryDir, which discards the rejected flag): callers like
// chat.go need to know the override was rejected so they can warn, not
// just silently fall back.
func TestLoadAutoMemory_UnsafeOverrideRejectedAndReported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	got := LoadAutoMemory(cwd, AutoMemoryOptions{Directory: "/", BudgetTokens: 1000})
	if !got.DirectoryOverrideRejected {
		t.Fatal("expected DirectoryOverrideRejected to be true for a \"/\" override")
	}
	if got.Dir == "/" {
		t.Errorf("Dir = %q, should be the default, not the rejected override", got.Dir)
	}
}
