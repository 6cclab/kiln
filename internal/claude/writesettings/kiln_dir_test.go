package writesettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshot records every file under dir (path -> content), to show a tree
// was left byte-identical.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(p)
			out[p] = string(data)
		}
		return nil
	})
	return out
}

func sameTree(t *testing.T, name string, before, after map[string]string) {
	t.Helper()
	if len(before) != len(after) {
		t.Errorf("%s: %d files before, %d after", name, len(before), len(after))
	}
	for p, c := range before {
		if after[p] != c {
			t.Errorf("%s: %s changed", name, p)
		}
	}
}

// TestWritesLandOnlyInKiln: rules and the model default are written to
// .kiln files; Claude Code's ~/.claude and <cwd>/.claude stay byte-identical.
func TestWritesLandOnlyInKiln(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	for _, f := range []struct{ path, body string }{
		{filepath.Join(home, ".claude", "settings.json"), `{"model":"a/b","permissions":{"allow":["Read"]}}`},
		{filepath.Join(cwd, ".claude", "settings.json"), `{"permissions":{"deny":["Bash(rm *)"]}}`},
		{filepath.Join(cwd, ".claude", "settings.local.json"), `{"permissions":{"allow":["Bash(ls *)"]}}`},
	} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	userBefore := snapshot(t, filepath.Join(home, ".claude"))
	projBefore := snapshot(t, filepath.Join(cwd, ".claude"))

	if err := AddRule(cwd, Allow, "Bash(npm test *)"); err != nil {
		t.Fatal(err)
	}
	if err := AddRule(cwd, Deny, "Bash(ls *)"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRule(cwd, Allow, "Bash(ls *)"); err != nil {
		t.Fatal(err)
	}
	if err := SetUserModel("x/y"); err != nil {
		t.Fatal(err)
	}

	sameTree(t, "~/.claude", userBefore, snapshot(t, filepath.Join(home, ".claude")))
	sameTree(t, "<cwd>/.claude", projBefore, snapshot(t, filepath.Join(cwd, ".claude")))

	if want := filepath.Join(cwd, ".kiln", "settings.local.json"); LocalSettingsPath(cwd) != want {
		t.Errorf("LocalSettingsPath = %q, want %q", LocalSettingsPath(cwd), want)
	}
	local, err := os.ReadFile(filepath.Join(cwd, ".kiln", "settings.local.json"))
	if err != nil || !strings.Contains(string(local), `"Bash(npm test *)"`) || !strings.Contains(string(local), `"deny"`) {
		t.Errorf(".kiln/settings.local.json = %s (%v)", local, err)
	}
	user, err := os.ReadFile(filepath.Join(home, ".kiln", "settings.json"))
	if err != nil || !strings.Contains(string(user), `"model": "x/y"`) {
		t.Errorf("~/.kiln/settings.json = %s (%v)", user, err)
	}
	if !HasRule(cwd, Allow, "Bash(npm test *)") || HasRule(cwd, Allow, "Bash(ls *)") {
		t.Error("HasRule does not reflect kiln's file alone")
	}
}

// TestKilnDirGitignore: creating <cwd>/.kiln also creates .kiln/.gitignore
// holding "*"; an existing one is left alone; no temp file is left behind.
func TestKilnDirGitignore(t *testing.T) {
	cwd := t.TempDir()
	if err := AddRule(cwd, Allow, "Read"); err != nil {
		t.Fatal(err)
	}
	gi := filepath.Join(cwd, ".kiln", ".gitignore")
	data, err := os.ReadFile(gi)
	if err != nil || string(data) != "*\n" {
		t.Fatalf(".kiln/.gitignore = %q (%v), want \"*\\n\"", data, err)
	}
	if err := os.WriteFile(gi, []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddRule(cwd, Allow, "Edit"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(gi); string(data) != "custom\n" {
		t.Errorf("existing .gitignore rewritten: %q", data)
	}
	entries, _ := os.ReadDir(filepath.Join(cwd, ".kiln"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != ".gitignore,settings.local.json" {
		t.Errorf(".kiln holds %q, want only .gitignore and settings.local.json", names)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".gitignore")); !os.IsNotExist(err) {
		t.Error("the project's own .gitignore was touched")
	}
}
