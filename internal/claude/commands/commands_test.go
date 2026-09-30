package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitFrontmatter(t *testing.T) {
	t.Run("separates frontmatter from body", func(t *testing.T) {
		fm, body := SplitFrontmatter("---\ndescription: Track work\n---\nDo the thing.\n")
		if fm.Description != "Track work" {
			t.Errorf("description = %q", fm.Description)
		}
		if strings.TrimSpace(body) != "Do the thing." {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("treats a file without frontmatter as all body", func(t *testing.T) {
		fm, body := SplitFrontmatter("Just a prompt.")
		if fm.Description != "" || fm.ArgumentHint != "" || fm.AllowedTools != nil || fm.Model != "" {
			t.Errorf("expected zero frontmatter, got %+v", fm)
		}
		if body != "Just a prompt." {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("keeps the body usable when the YAML is malformed", func(t *testing.T) {
		// Losing the description is survivable; losing the command is not.
		_, body := SplitFrontmatter("---\n: : bad\n---\nStill works.\n")
		if strings.TrimSpace(body) != "Still works." {
			t.Errorf("body = %q", body)
		}
	})
}

// FuzzSplitFrontmatter feeds arbitrary bytes to SplitFrontmatter, which
// must never panic - malformed YAML or a missing/truncated delimiter falls
// back to treating the whole input as body.
func FuzzSplitFrontmatter(f *testing.F) {
	seeds := []string{
		"---\ndescription: Track work\n---\nDo the thing.\n",
		"Just a prompt.",
		"---\n: : bad\n---\nStill works.\n",
		"",
		"---\n---\n",
		"---",
		"---\n\n---\n",
		"---\ndescription: Track work\nargument-hint: <file>\nallowed-tools: Read, Grep\nmodel: sonnet\n---\nBody.",
		"---\nallowed-tools:\n  - Read\n  - Grep\n---\nBody.",
		"---\r\ndescription: crlf\r\n---\r\nBody.\r\n",
		"---\ndescription: [not, a, string]\n---\nBody.",
		"---\nallowed-tools: 42\n---\nBody.",
		"---\ndescription: \"unterminated\n---\nBody.",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, source string) {
		_, _ = SplitFrontmatter(source)
	})
}

// FuzzApplyArguments feeds arbitrary body/args pairs to ApplyArguments,
// which must never panic regardless of malformed placeholders.
func FuzzApplyArguments(f *testing.F) {
	seeds := []struct{ body, args string }{
		{"Review $ARGUMENTS", "src/a.ts"},
		{"Compare $1 to $2", "main dev"},
		{"Summarize", "the repo"},
		{"Review $ARGUMENTS", ""},
		{"$999999999999999999", "x"},
		{"$0 $-1 $abc", "one two"},
		{"", ""},
		{"$ARGUMENTS$ARGUMENTS", "a b c"},
	}
	for _, s := range seeds {
		f.Add(s.body, s.args)
	}
	f.Fuzz(func(t *testing.T, body, args string) {
		_ = ApplyArguments(body, args)
	})
}

func TestApplyArguments(t *testing.T) {
	t.Run("substitutes $ARGUMENTS", func(t *testing.T) {
		if got := ApplyArguments("Review $ARGUMENTS", "src/a.ts"); got != "Review src/a.ts" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("substitutes positional $1 $2", func(t *testing.T) {
		if got := ApplyArguments("Compare $1 to $2", "main dev"); got != "Compare main to dev" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("appends arguments when the template uses no placeholder", func(t *testing.T) {
		// Silently dropping what the user typed is the worse failure.
		if got := ApplyArguments("Summarize", "the repo"); got != "Summarize\n\nthe repo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("leaves a placeholder template alone when no arguments are given", func(t *testing.T) {
		if got := ApplyArguments("Review $ARGUMENTS", ""); got != "Review " {
			t.Errorf("got %q", got)
		}
	})
}

func TestLoadCommands(t *testing.T) {
	dir := t.TempDir()
	cmdDir := filepath.Join(dir, ".claude", "commands")
	if err := os.MkdirAll(filepath.Join(cmdDir, "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "track-work.md"), []byte("---\ndescription: Track work\n---\nDo the thing."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "frontend", "component.md"), []byte("---\ndescription: New component\n---\nBuild $1."), 0o644); err != nil {
		t.Fatal(err)
	}

	files := LoadCommands(dir)
	var flat, namespaced *CommandFile
	for i := range files {
		if files[i].Name == "track-work" && files[i].Namespace == "" {
			flat = &files[i]
		}
		if files[i].Name == "component" && files[i].Namespace == "frontend" {
			namespaced = &files[i]
		}
	}
	if flat == nil {
		t.Fatal("expected track-work command")
	}
	if flat.Origin != Project {
		t.Errorf("origin = %v", flat.Origin)
	}
	if got := flat.Render(""); got != "Do the thing." {
		t.Errorf("render = %q", got)
	}
	if namespaced == nil {
		t.Fatal("expected frontend:component command")
	}
	if got := namespaced.Render("Button"); got != "Build Button." {
		t.Errorf("render = %q", got)
	}
}

// Claude Code follows symlinked command folders and files, naming a
// command inside a linked folder "<folder>:<name>"; a link back up the tree
// must not loop.
func TestLoadCommandsFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	cmdDir := filepath.Join(dir, ".claude", "commands")
	shared := t.TempDir()
	if err := os.MkdirAll(filepath.Join(shared, "linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "linked", "deploy.md"), []byte("---\ndescription: Deploy\n---\nShip it."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shared, "notes.md"), []byte("---\ndescription: Notes\n---\nWrite notes."), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"linked":   filepath.Join(shared, "linked"),
		"notes.md": filepath.Join(shared, "notes.md"),
		"loop":     cmdDir,
	} {
		if err := os.Symlink(target, filepath.Join(cmdDir, link)); err != nil {
			t.Fatal(err)
		}
	}

	got := map[string]bool{}
	for _, f := range LoadCommands(dir) {
		if f.Origin == Project {
			got[f.Namespace+":"+f.Name] = true
		}
	}
	for _, want := range []string{"linked:deploy", ":notes"} {
		if !got[want] {
			t.Errorf("missing command %q; loaded %v", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("loaded %v, want exactly linked:deploy and notes (the loop link adds nothing)", got)
	}
}

// ParseCommandFile is how plugins build their commands; it keeps the
// frontmatter out of the body and carries name, namespace and origin.
func TestParseCommandFile(t *testing.T) {
	c := ParseCommandFile("---\ndescription: Ship it\nargument-hint: <env>\n---\nDeploy to $ARGUMENTS.\n", "deploy", "ops", Plugin, "/p/commands/deploy.md")
	if c.Name != "deploy" || c.Namespace != "ops" || c.Origin != Plugin || c.Path != "/p/commands/deploy.md" {
		t.Errorf("identity = %+v", c)
	}
	if c.Description != "Ship it" || c.ArgumentHint != "<env>" {
		t.Errorf("frontmatter = %q, %q", c.Description, c.ArgumentHint)
	}
	if got := c.Render("staging"); strings.Contains(got, "description:") || !strings.Contains(got, "Deploy to staging.") {
		t.Errorf("Render = %q", got)
	}
}
