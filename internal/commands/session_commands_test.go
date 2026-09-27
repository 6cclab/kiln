package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/session/jsonl"
)

func findCmd(t *testing.T, source Source, name string) Command {
	t.Helper()
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no /%s command", name)
	return Command{}
}

func TestResumeWithNoSessionsSaysSo(t *testing.T) {
	repo, err := jsonl.NewRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	source := SessionCommands(SessionCommandDeps{Repo: repo, Cwd: cwd})
	res, err := findCmd(t, source, "resume").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 1 || !strings.Contains(res.Output[0], "No past sessions") {
		t.Fatalf("got %+v", res)
	}
}

func TestResumeWithArgPrintsTheCLIInvocation(t *testing.T) {
	repo, _ := jsonl.NewRepo(t.TempDir())
	source := SessionCommands(SessionCommandDeps{Repo: repo, Cwd: t.TempDir()})
	res, err := findCmd(t, source, "resume").Run(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "kiln --resume abc123") {
		t.Fatalf("got %q", joined)
	}
}

func TestExportWritesMarkdown(t *testing.T) {
	cwd := t.TempDir()
	source := SessionCommands(SessionCommandDeps{Cwd: cwd})
	res, err := findCmd(t, source, "export").Run(context.Background(), "out.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 1 || !strings.Contains(res.Output[0], filepath.Join(cwd, "out.md")) {
		t.Fatalf("got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(cwd, "out.md")); err != nil {
		t.Fatalf("expected the file to exist: %v", err)
	}
}

func TestMemoryWithNoEditorNamesThePath(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	cwd := t.TempDir()
	source := SessionCommands(SessionCommandDeps{Cwd: cwd})
	res, err := findCmd(t, source, "memory").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "No $EDITOR set") || !strings.Contains(joined, filepath.Join(cwd, "CLAUDE.md")) {
		t.Fatalf("got %q", joined)
	}
}

func TestMemoryCompletionsOfferUserAndProject(t *testing.T) {
	source := SessionCommands(SessionCommandDeps{Cwd: t.TempDir()})
	items := findCmd(t, source, "memory").ArgumentCompletions("")
	if len(items) != 2 {
		t.Fatalf("got %d completions, want 2", len(items))
	}
}

func TestAddDirWithNoGateSaysUnenforced(t *testing.T) {
	source := SessionCommands(SessionCommandDeps{Cwd: t.TempDir()})
	res, err := findCmd(t, source, "add-dir").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 1 || !strings.Contains(res.Output[0], "not enforced") {
		t.Fatalf("got %+v", res)
	}
}

type fakeGate struct {
	roots []string
}

func (g *fakeGate) AddRoot(dir string) string {
	g.roots = append(g.roots, dir)
	return dir
}
func (g *fakeGate) Roots() []string { return g.roots }

func TestAddDirAddsAResolvedRoot(t *testing.T) {
	cwd := t.TempDir()
	gate := &fakeGate{}
	source := SessionCommands(SessionCommandDeps{Cwd: cwd, Gate: gate})
	res, err := findCmd(t, source, "add-dir").Run(context.Background(), "sub")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cwd, "sub")
	if len(gate.roots) != 1 || gate.roots[0] != want {
		t.Fatalf("got %v, want [%s]", gate.roots, want)
	}
	if len(res.Output) != 1 || !strings.Contains(res.Output[0], want) {
		t.Fatalf("got %+v", res)
	}
}

func TestInitReturnsAPromptNotOutput(t *testing.T) {
	source := SessionCommands(SessionCommandDeps{Cwd: t.TempDir()})
	res, err := findCmd(t, source, "init").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Prompt == "" || len(res.Output) != 0 {
		t.Fatalf("got %+v, want a Prompt and no Output", res)
	}
	if !strings.Contains(res.Prompt, "CLAUDE.md") {
		t.Fatalf("got %q", res.Prompt)
	}
}

func TestConfigListsSettingsFilesInOrder(t *testing.T) {
	cwd := t.TempDir()
	source := SessionCommands(SessionCommandDeps{Cwd: cwd})
	res, err := findCmd(t, source, "config").Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "settings.json") || !strings.Contains(joined, "settings.local.json") {
		t.Fatalf("got %q", joined)
	}
}
