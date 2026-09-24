package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
)

// newFauxRegistry starts a scripted faux server and returns a Registry with
// it as the sole provider, matching internal/harness's own test rig
// (testhelpers_test.go).
func newFauxRegistry(t *testing.T, scriptYAML string) *provider.Registry {
	t.Helper()
	srv, err := tkfaux.New(tkfaux.Options{ScriptYAML: scriptYAML})
	if err != nil {
		t.Fatalf("tkfaux.New: %v", err)
	}
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("srv.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("HARNESS_FAUX_ADDR", addr)
	t.Setenv("HARNESS_FAUX_API", "anthropic-messages")

	fp, ok := fauxprovider.New()
	if !ok {
		t.Fatal("fauxprovider.New reported false with HARNESS_FAUX_ADDR set")
	}
	reg := provider.NewRegistry(nil)
	reg.Register(fp)
	return reg
}

func mustResolve(t *testing.T, reg *provider.Registry) provider.Resolved {
	t.Helper()
	resolved, err := reg.Resolve(fauxprovider.ProviderID, fauxprovider.ModelID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolved
}

func sha256File(t *testing.T, path string) [32]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func TestStartCreatesSessionFile(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	started, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !started.Created {
		t.Fatal("Created = false, want true")
	}

	dir := filepath.Join(root, jsonl.DirectoryName(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d files under %s, want 1", len(entries), dir)
	}
	if filepath.Join(dir, entries[0].Name()) != started.TranscriptPath {
		t.Fatalf("TranscriptPath = %s, want %s", started.TranscriptPath, filepath.Join(dir, entries[0].Name()))
	}

	data, err := os.ReadFile(started.TranscriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"cwd":"`+cwd+`"`)) {
		t.Fatalf("header line missing cwd %q:\n%s", cwd, data)
	}
}

func TestStartResumeLatestPicksNewest(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	repo, err := jsonl.NewRepo(root)
	if err != nil {
		t.Fatal(err)
	}

	st1, meta1, err := repo.Create(jsonl.CreateOptions{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}
	st2, meta2, err := repo.Create(jsonl.CreateOptions{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := st2.Close(); err != nil {
		t.Fatal(err)
	}

	// Fix mtimes explicitly so "newest" is deterministic regardless of how
	// fast the two Creates above actually ran.
	older := time.UnixMilli(1_700_000_000_000)
	newer := time.UnixMilli(1_700_000_100_000)
	if err := os.Chtimes(meta1.Path, older, older); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(meta2.Path, newer, newer); err != nil {
		t.Fatal(err)
	}

	started, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root, ResumeLatest: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.Created {
		t.Fatal("Created = true, want false (resumed)")
	}
	if started.SessionID != meta2.ID {
		t.Fatalf("SessionID = %s, want %s (the more recently modified session)", started.SessionID, meta2.ID)
	}
}

func TestStartResumeByIDPrefix(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	repo, err := jsonl.NewRepo(root)
	if err != nil {
		t.Fatal(err)
	}
	st, meta, err := repo.Create(jsonl.CreateOptions{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	prefix := meta.ID[:8]
	started, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root, Resume: prefix,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if started.Created {
		t.Fatal("Created = true, want false (resumed by prefix)")
	}
	if started.SessionID != meta.ID {
		t.Fatalf("SessionID = %s, want %s", started.SessionID, meta.ID)
	}
	if started.TranscriptPath != meta.Path {
		t.Fatalf("TranscriptPath = %s, want %s", started.TranscriptPath, meta.Path)
	}
}

func TestStartSessionID(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	started, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root, SessionID: "my-fixed-session-id",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !started.Created {
		t.Fatal("Created = false, want true")
	}
	if started.SessionID != "my-fixed-session-id" {
		t.Fatalf("SessionID = %q, want %q", started.SessionID, "my-fixed-session-id")
	}
}

func TestStartForkSessionLeavesSourceUnchanged(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hello\"\n")
	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	started, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := started.Lane.Prompt(context.Background(), "hi", nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	before := sha256File(t, started.TranscriptPath)

	forked, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root,
		ResumeLatest: true, ForkSession: true,
	})
	if err != nil {
		t.Fatalf("fork Start: %v", err)
	}
	if forked.Created {
		t.Fatal("Created = true for a fork, want false")
	}
	if forked.TranscriptPath == started.TranscriptPath {
		t.Fatal("fork produced the same file as its source")
	}

	after := sha256File(t, started.TranscriptPath)
	if before != after {
		t.Fatal("forking modified the source file")
	}

	srcEntries, err := started.Lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dstEntries, err := forked.Lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(srcEntries) == 0 {
		t.Fatal("source has no entries to compare against")
	}
	if len(srcEntries) != len(dstEntries) {
		t.Fatalf("forked session has %d entries, source has %d", len(dstEntries), len(srcEntries))
	}
	for i := range srcEntries {
		if srcEntries[i].ID != dstEntries[i].ID {
			t.Fatalf("entry[%d] id = %s, want %s", i, dstEntries[i].ID, srcEntries[i].ID)
		}
	}
}

func TestStartPromptCompletesWithToolResult(t *testing.T) {
	script := `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`
	reg := newFauxRegistry(t, script)
	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	started, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	result, err := started.Lane.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != harness.StatusCompleted {
		t.Fatalf("status = %q, err = %v", result.Status, result.Error)
	}

	entries, err := started.Lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if _, ok := e.Message.(msg.ToolResultMessage); ok {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no toolResult entry found on the lane's branch")
	}
}

func TestSetModelSwitchesLaneAndCompaction(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	resolved := mustResolve(t, reg)
	cwd := t.TempDir()
	root := t.TempDir()

	started, err := Start(context.Background(), Options{
		Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var hookCalled bool
	var hookResolved provider.Resolved
	started.OnModelChanged = func(ctx context.Context, r provider.Resolved) {
		hookCalled = true
		hookResolved = r
	}

	var sawCompactionUpdate bool
	unsub := started.Harness.Events().On(harness.EventConfigUpdate, func(ev harness.Event) {
		if ev.ConfigProperty == harness.ConfigCompactionSettings {
			sawCompactionUpdate = true
		}
	})
	defer unsub()

	newTier := budget.TierForWindow(200_000) // "large", distinct from faux's 32768 ("small")
	newResolved := provider.Resolved{
		Model: provider.Model{ID: "faux-2", Provider: fauxprovider.ProviderID, ContextWindow: 200_000},
		Tier:  newTier,
	}

	if err := SetModel(context.Background(), started, newResolved); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	if !hookCalled {
		t.Fatal("OnModelChanged was not called")
	}
	if hookResolved.Tier.Name != "large" {
		t.Fatalf("hook saw tier %q, want large", hookResolved.Tier.Name)
	}
	if !sawCompactionUpdate {
		t.Fatal("SetModel did not emit a compactionSettings config_update event")
	}
	if started.Tier.Name != "large" {
		t.Fatalf("started.Tier.Name = %q, want large", started.Tier.Name)
	}
	if started.Model.ID != "faux-2" {
		t.Fatalf("started.Model.ID = %q, want faux-2", started.Model.ID)
	}

	// The lane's model configuration is durable state, committed to the
	// session file; re-open it read-only to confirm the switch landed there
	// too, not just on the in-memory Started.
	st, err := jsonl.Open(started.TranscriptPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw, _, ok := st.GetValue(session.NamespaceLaneConfig, MainLane)
	if !ok {
		t.Fatal("no lane config found in the reopened session")
	}
	var cfg session.LaneConfiguration
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Model.ModelID != "faux-2" || cfg.Model.Provider != fauxprovider.ProviderID {
		t.Fatalf("committed lane config model = %+v, want {faux faux-2}", cfg.Model)
	}
}
