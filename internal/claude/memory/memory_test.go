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

func TestAddMemoryPrefersProjectFile(t *testing.T) {
	setupHome(t)
	cwd := t.TempDir()
	projectFile := filepath.Join(cwd, "CLAUDE.md")
	writeFile(t, projectFile, "Existing.")

	path, err := AddMemory("remember this", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if path != projectFile {
		t.Errorf("path = %q, want %q", path, projectFile)
	}
	data, _ := os.ReadFile(projectFile)
	if !strings.Contains(string(data), "remember this") {
		t.Errorf("note not appended: %s", data)
	}
}

func TestAddMemoryFallsBackToUserFile(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir() // no CLAUDE.md here

	path, err := AddMemory("remember this", cwd)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".claude", "CLAUDE.md")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

func TestAddMemoryCreatesMissingClaudeDir(t *testing.T) {
	// A fresh HOME (or a HOME whose ~/.claude was never created) must not
	// crash AddMemory with a raw ENOENT — the parent directory needs to be
	// created before the file is opened.
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir() // no CLAUDE.md here, and no ~/.claude either

	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("test setup: expected ~/.claude to not exist yet, stat err = %v", err)
	}

	path, err := AddMemory("remember this", cwd)
	if err != nil {
		t.Fatalf("AddMemory returned error on missing ~/.claude dir: %v", err)
	}
	want := filepath.Join(home, ".claude", "CLAUDE.md")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("note file not written: %v", err)
	}
	if !strings.Contains(string(data), "remember this") {
		t.Errorf("note not appended: %s", data)
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
