package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/skills"
)

func TestSkillSourceOnlyUserInvocable(t *testing.T) {
	source := SkillSource([]skills.Skill{
		{Name: "simplify", Description: "d", Content: "Do the thing.", UserInvocable: true},
		{Name: "internal-only", Description: "d", Content: "hidden", UserInvocable: false},
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0].Name != "simplify" {
		t.Fatalf("got %+v", cmds)
	}
	if cmds[0].ArgumentHint != "[instructions]" {
		t.Fatalf("got %q", cmds[0].ArgumentHint)
	}
	res, err := cmds[0].Run(context.Background(), "extra args")
	if err != nil {
		t.Fatal(err)
	}
	if res.Prompt != "Do the thing.\n\nextra args" {
		t.Fatalf("got %q", res.Prompt)
	}
}

func TestClaudeCommandSourcesPersonalThenProject(t *testing.T) {
	cwd := t.TempDir()
	// A fake HOME so the personal scope resolves inside the test dir too.
	home := filepath.Join(cwd, "home")
	t.Setenv("HOME", home)

	mustWrite := func(dir, name, body string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(home, ".claude", "commands"), "track-work.md", "---\ndescription: Track it\n---\nDo the thing: $ARGUMENTS\n")
	mustWrite(filepath.Join(cwd, ".claude", "commands"), "review.md", "Review the diff.\n")

	sources := ClaudeCommandSources(cwd)
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2 (personal, project)", len(sources))
	}

	var names []string
	for _, s := range sources {
		cmds, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cmds {
			names = append(names, c.Name)
		}
	}
	if len(names) != 2 || !contains2(names, "track-work") || !contains2(names, "review") {
		t.Fatalf("got %v", names)
	}

	personalCmds, err := sources[0].Load()
	if err != nil {
		t.Fatal(err)
	}
	res, err := personalCmds[0].Run(context.Background(), "src/a.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Prompt, "src/a.ts") {
		t.Fatalf("got %q", res.Prompt)
	}
}

func contains2(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
