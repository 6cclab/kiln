package harness

import (
	"os"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/tools"
)

// testRig bundles everything one harness test needs: a fresh JSONL session
// under a temp sessions root, a Harness wired to a scripted faux provider
// and the real bash/read/edit/write tools, and the faux server itself (for
// asserting on recorded requests).
type testRig struct {
	t       *testing.T
	Storage *jsonl.Storage
	Path    string
	Faux    *tkfaux.Server
	H       *Harness
}

// newTestRig starts a faux server with scriptYAML, creates a fresh session
// (header only, per item 6) in a temp sessions root, and builds a Harness
// over it with the real built-in tools rooted at a temp cwd.
func newTestRig(t *testing.T, scriptYAML string, activeTools []string) *testRig {
	t.Helper()
	return newTestRigWithStorage(t, scriptYAML, activeTools, nil)
}

// newTestRigWithStorage is newTestRig, but the Harness is built over
// wrap(storage) instead of storage directly when wrap is non-nil — e.g. to
// inject a Commit failure at a chosen point. rig.Storage is always the
// real, unwrapped *jsonl.Storage, for assertions against the file on disk.
func newTestRigWithStorage(t *testing.T, scriptYAML string, activeTools []string, wrap func(session.Storage) session.Storage) *testRig {
	t.Helper()

	fauxSrv, err := tkfaux.New(tkfaux.Options{ScriptYAML: scriptYAML})
	if err != nil {
		t.Fatalf("faux.New: %v", err)
	}
	addr, err := fauxSrv.Start()
	if err != nil {
		t.Fatalf("faux.Start: %v", err)
	}
	t.Cleanup(func() { _ = fauxSrv.Close() })

	t.Setenv("HARNESS_FAUX_ADDR", addr)
	t.Setenv("HARNESS_FAUX_API", "anthropic-messages")

	fp, ok := fauxprovider.New()
	if !ok {
		t.Fatal("fauxprovider.New reported false with HARNESS_FAUX_ADDR set")
	}
	registry := provider.NewRegistry(nil)
	registry.Register(fp)

	cwd := t.TempDir()
	env := execenv.New(cwd)
	toolSet := tools.Builtins(env)
	if activeTools == nil {
		activeTools = toolSet.Names()
	}

	sessionsRoot := t.TempDir()
	repo, err := jsonl.NewRepo(sessionsRoot)
	if err != nil {
		t.Fatalf("jsonl.NewRepo: %v", err)
	}
	storage, meta, err := repo.Create(jsonl.CreateOptions{Cwd: cwd})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	var wrapped session.Storage = storage
	if wrap != nil {
		wrapped = wrap(storage)
	}

	h, err := New(Options{
		Storage:         wrapped,
		Registry:        registry,
		Model:           session.ModelRef{Provider: fauxprovider.ProviderID, ModelID: fauxprovider.ModelID},
		ThinkingLevel:   "off",
		Tools:           toolSet,
		ActiveToolNames: activeTools,
		SystemPrompt:    "test harness",
		Cwd:             cwd,
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}

	return &testRig{t: t, Storage: storage, Path: meta.Path, Faux: fauxSrv, H: h}
}

// mustLane returns (creating if needed) the named lane.
func (r *testRig) mustLane(name string) *Lane {
	l, err := r.H.Lane(name)
	if err != nil {
		r.t.Fatalf("Lane(%q): %v", name, err)
	}
	return l
}

// writeTuple is the structural projection of one write inside a
// transaction: (kind, sub, op), where sub is an entry's "type" (and, for
// message entries, "message:<role>") or a value/list write's namespace.
// This is what the golden comparison matches against the reference
// fixture — never ids, timestamps, seqs or message contents.
type writeTuple struct {
	Kind string
	Sub  string
	Op   string
}

func tuplesForWrites(writes []session.CommittedWrite) []writeTuple {
	out := make([]writeTuple, 0, len(writes))
	for _, w := range writes {
		switch w.Kind {
		case "entry":
			sub := string(w.Entry.Type)
			if w.Entry.Type == session.EntryMessage && w.Entry.Message != nil {
				sub = "message:" + string(w.Entry.Message.MessageRole())
			}
			out = append(out, writeTuple{Kind: "entry", Sub: sub})
		case "usage":
			out = append(out, writeTuple{Kind: "usage"})
		case "value", "list":
			out = append(out, writeTuple{Kind: w.Kind, Sub: w.Value.Namespace, Op: w.Value.Op})
		}
	}
	return out
}

// readTransactionLines parses every transaction line (i.e. every line
// after the header) of path into its per-line write tuples.
func readTransactionLines(t *testing.T, path string) [][]writeTuple {
	t.Helper()
	lines := readRawLines(t, path)
	var out [][]writeTuple
	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		writes, err := jsonl.ParseTransaction([]byte(line))
		if err != nil {
			t.Fatalf("parse transaction line %d: %v", i+1, err)
		}
		out = append(out, tuplesForWrites(writes))
	}
	return out
}

func readRawLines(t *testing.T, path string) []string {
	t.Helper()
	data := mustReadFile(t, path)
	var lines []string
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, string(data[start:i]))
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, string(data[start:]))
	}
	return lines
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// isFrameAppendLine reports whether a parsed line is exactly one
// pi.pending.assistant_frame append, the only write shape this test
// collapses runs of (streamed-chunk count is provider/content-dependent,
// not part of the FSM's write-shape contract; see golden_test.go).
func isFrameAppendLine(line []writeTuple) bool {
	return len(line) == 1 && line[0].Kind == "list" && line[0].Sub == session.NamespacePendingAssistantFrame && line[0].Op == "append"
}

// collapseFrameRuns replaces every maximal run of consecutive
// isFrameAppendLine lines with a single representative line, so a
// structural comparison is insensitive to exactly how many chunks a
// provider happened to stream.
func collapseFrameRuns(lines [][]writeTuple) [][]writeTuple {
	var out [][]writeTuple
	inRun := false
	for _, line := range lines {
		if isFrameAppendLine(line) {
			if !inRun {
				out = append(out, line)
				inRun = true
			}
			continue
		}
		inRun = false
		out = append(out, line)
	}
	return out
}
