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
	if len(assembled.Dropped) != 0 {
		t.Errorf("expected nothing dropped, got %v", assembled.Dropped)
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

func TestLoadMemoryBudgetDropsLeastSpecificFirst(t *testing.T) {
	home := setupHome(t)
	cwd := t.TempDir()
	// User memory is found first (least specific); project is most
	// specific and should survive when the budget can't fit both.
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), strings.Repeat("x", 4000))
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "small project note")

	assembled := LoadMemory(cwd, 100)
	if len(assembled.Dropped) == 0 {
		t.Fatal("expected something dropped under a tight budget")
	}
	if !strings.Contains(assembled.Text, "small project note") {
		t.Errorf("expected project memory to survive, got: %s", assembled.Text)
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
