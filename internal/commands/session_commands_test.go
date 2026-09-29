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

// TestResumeWithArg: /resume <id-prefix> resolves the session; with a
// Relaunch hook it hands the id over and exits, without one it names the
// restart command, and an unknown id says so.
func TestResumeWithArg(t *testing.T) {
	repo, _ := jsonl.NewRepo(t.TempDir())
	cwd := t.TempDir()
	_, meta, err := repo.Create(jsonl.CreateOptions{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	prefix := meta.ID[:8]

	res, err := findCmd(t, SessionCommands(SessionCommandDeps{Repo: repo, Cwd: cwd}), "resume").Run(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(res.Output, "\n"); !strings.Contains(joined, "kiln --resume "+meta.ID) || res.Exit {
		t.Errorf("without Relaunch: exit=%v output %q, want the restart command", res.Exit, joined)
	}

	var relaunched string
	deps := SessionCommandDeps{Repo: repo, Cwd: cwd, Relaunch: func(id string) { relaunched = id }}
	res, err = findCmd(t, SessionCommands(deps), "resume").Run(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	if relaunched != meta.ID || !res.Exit {
		t.Errorf("with Relaunch: relaunched %q exit=%v, want %q and exit", relaunched, res.Exit, meta.ID)
	}

	relaunched = ""
	res, _ = findCmd(t, SessionCommands(deps), "resume").Run(context.Background(), "zzz")
	if relaunched != "" || res.Exit || !strings.Contains(strings.Join(res.Output, " "), "No session") {
		t.Errorf("unknown id: relaunched %q exit=%v output %q", relaunched, res.Exit, res.Output)
	}

	deps.CurrentID = meta.ID
	res, _ = findCmd(t, SessionCommands(deps), "resume").Run(context.Background(), prefix)
	if relaunched != "" || !strings.Contains(strings.Join(res.Output, " "), "is this session") {
		t.Errorf("current session: relaunched %q output %q", relaunched, res.Output)
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
