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
