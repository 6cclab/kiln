package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestLoadMemoryBasic(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()

	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "User preferences.")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "Project instructions.")

	assembled := LoadMemory(cwd, 10_000)
	if len(assembled.Files) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(assembled.Files), assembled.Files)
	}
	if !strings.Contains(assembled.Text, "User preferences.") || !strings.Contains(assembled.Text, "Project instructions.") {
		t.Errorf("text missing content: %s", assembled.Text)
	}
	if len(assembled.Indexed) != 0 {
		t.Errorf("expected nothing indexed, got %v", assembled.Indexed)
	}
}

func TestLoadMemoryImports(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()

	writeFile(t, filepath.Join(home, "RTK.md"), "RTK contents.")
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "Main file.\n@~/RTK.md\n")

	assembled := LoadMemory(cwd, 10_000)
	if !strings.Contains(assembled.Text, "RTK contents.") {
		t.Errorf("import not resolved: %s", assembled.Text)
	}
}

func TestLoadMemoryMissingImport(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "Main.\n@nope.md\n")

	assembled := LoadMemory(cwd, 10_000)
	if !strings.Contains(assembled.Text, "<!-- missing import: nope.md -->") {
		t.Errorf("expected missing-import marker, got: %s", assembled.Text)
	}
}

func TestLoadMemoryCircularImport(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()
	path := filepath.Join(home, ".claude", "CLAUDE.md")
	writeFile(t, path, "@CLAUDE.md\n")

	assembled := LoadMemory(cwd, 10_000)
	if !strings.Contains(assembled.Text, "skipped circular import") {
		t.Errorf("expected circular-import marker, got: %s", assembled.Text)
	}
}

func TestLoadMemoryRules(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "rules"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".claude", "rules", "evidence.md"), "Evidence rule.")

	assembled := LoadMemory(cwd, 10_000)
	if !strings.Contains(assembled.Text, "Evidence rule.") {
		t.Errorf("rule not loaded: %s", assembled.Text)
	}
}

func TestLoadMemoryNestedRules(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()
	userRule := filepath.Join(home, ".claude", "rules", "coding", "go.md")
	projectRootRule := filepath.Join(cwd, ".claude", "rules", "general.md")
	projectRule := filepath.Join(cwd, ".claude", "rules", "tests", "regressions.md")
	writeFile(t, userRule, "Run the Go formatter.")
	writeFile(t, projectRootRule, "Check the whole project.")
	writeFile(t, projectRule, "---\npaths:\n  - 'internal/**/*.go'\n---\nAdd a regression test for each fix.")
	writeFile(t, filepath.Join(cwd, ".claude", "rules", "tests", "notes.txt"), "Not a rule.")

	assembled := LoadMemory(cwd, 10_000)
	for _, want := range []string{"Run the Go formatter.", "Check the whole project.", "Add a regression test for each fix."} {
		if !strings.Contains(assembled.Text, want) {
			t.Errorf("nested rule %q missing from memory:\n%s", want, assembled.Text)
		}
	}
	if strings.Contains(assembled.Text, "Not a rule.") {
		t.Errorf("non-Markdown file loaded as a rule:\n%s", assembled.Text)
	}
	wantPaths := []string{userRule, projectRootRule, projectRule}
	if len(assembled.Files) != len(wantPaths) {
		t.Fatalf("loaded files = %+v, want paths %v", assembled.Files, wantPaths)
	}
	for i, want := range wantPaths {
		if assembled.Files[i].Path != want {
			t.Errorf("loaded file %d = %s, want %s", i, assembled.Files[i].Path, want)
		}
	}
}

func TestListRulesMissingDirectoryAndSymlinkCycle(t *testing.T) {
	root := t.TempDir()
	if got := listRules(filepath.Join(root, "missing")); len(got) != 0 {
		t.Fatalf("missing rules directory returned %v", got)
	}
	writeFile(t, filepath.Join(root, "nested", "rule.md"), "A rule.")
	if err := os.Symlink(root, filepath.Join(root, "nested", "cycle")); err != nil {
		t.Fatal(err)
	}
	if got := listRules(root); len(got) != 1 || got[0] != filepath.Join(root, "nested", "rule.md") {
		t.Errorf("rules with symlink cycle = %v, want only the Markdown file", got)
	}
}

func TestListRulesSymlinkedRoot(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared")
	writeFile(t, filepath.Join(shared, "direct.md"), "Direct rule.")
	logical := filepath.Join(root, ".claude", "rules")
	if err := os.MkdirAll(filepath.Dir(logical), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, logical); err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(logical, "direct.md")}
	got := listRules(logical)
	if len(got) != len(want) {
		t.Fatalf("symlinked rules root returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("symlinked rules root returned %v, want %v", got, want)
		}
	}
}

// TestLoadMemoryTightBudgetIndexesRules: under a tight budget every
// CLAUDE.md still loads in full; rules that do not fit are listed by path
// and description, the most specific (project) rules loading first.
func TestLoadMemoryTightBudgetIndexesRules(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "global instructions")
	writeFile(t, filepath.Join(cwd, ".claude", "CLAUDE.md"), "project pointer: read AGENTS.md")
	writeFile(t, filepath.Join(home, ".claude", "rules", "evidence.md"), "# Evidence\n\n"+strings.Repeat("x", 800))
	writeFile(t, filepath.Join(cwd, ".claude", "rules", "gate.md"), "---\ndescription: The preflight is the gate\n---\n\n"+strings.Repeat("y", 200))

	a := LoadMemory(cwd, 120)
	for _, want := range []string{"global instructions", "project pointer: read AGENTS.md", strings.Repeat("y", 200)} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("missing %q in:\n%s", want[:min(len(want), 30)], a.Text)
		}
	}
	if strings.Contains(a.Text, strings.Repeat("x", 800)) {
		t.Errorf("the user rule loaded in full despite the budget")
	}
	wantIndex := "- " + filepath.Join(home, ".claude", "rules", "evidence.md") + ": Evidence"
	if !strings.Contains(a.Text, wantIndex) || len(a.Indexed) != 1 {
		t.Errorf("index missing %q (indexed %v):\n%s", wantIndex, a.Indexed, a.Text)
	}
}

func TestRuleSummary(t *testing.T) {
	cases := map[string]string{
		"---\ndescription: \"Quoted desc\"\n---\n# Title": "Quoted desc",
		"# Just A Title\n\nbody":                          "Just A Title",
		"plain first line\nmore":                          "plain first line",
	}
	for in, want := range cases {
		if got := ruleSummary(in); got != want {
			t.Errorf("ruleSummary(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
