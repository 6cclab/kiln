package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
)

func writeNamedContextSkill(t *testing.T, project, name, description string) {
	t.Helper()
	dir := filepath.Join(project, ".claude", "skills", name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nbody "+name), 0600); err != nil {
		t.Fatal(err)
	}
}

// seedDecisionRequest runs one typed request that produces a pinned metadata
// suggestion, a personal-memory hit and a capability-search budget drop.
func seedDecisionRequest(t *testing.T) Args {
	t.Helper()
	startFaux(t, `model: faux-1
steps:
  - tool_call: {name: capability_search, args: {query: "database migration"}, id: search-1}
  - on_tool_result: search-1
    then:
      - text: "Searched."
        end_turn: true
  - text: "Second."
    end_turn: true
`)
	project := scratchProject(t)
	long := strings.Repeat("handles database migration planning and review. ", 4)
	for _, name := range []string{"dbone", "dbtwo", "dbthree", "dbfour", "dbfive"} {
		writeNamedContextSkill(t, project, name, long+name)
	}
	writeNamedContextSkill(t, project, "pinned-helper", "unrelated "+long)
	settingsPath := filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"skillDiscovery":{"budgetTokens":128,"pinned":["pinned-helper"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(filepath.Join(os.Getenv("HOME"), ".kiln", "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), memory.Record{Scope: memoryScope(project), Key: "dbnote", Kind: "preference", Text: "database migration uses goose", Evidence: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	args := baseArgs()
	if out := runContextPrint(t, args, "database migration"); !strings.Contains(out, "Searched.") {
		t.Fatal(out)
	}
	args.ContinueLatest = true
	return args
}

// TestContextSelectionLiveRestartKeepsRequestDecisions reads /context and
// kiln config from a fresh process after the request, then checks that a new
// typed request replaces the restored decisions.
func TestContextSelectionLiveRestartKeepsRequestDecisions(t *testing.T) {
	args := seedDecisionRequest(t)
	report := runContextPrint(t, args, "/context")
	for _, want := range []string{"pinned-helper skill · metadata included · pinned", ":dbnote memory · record included · search hit", "dbtwo skill · metadata left out · budget-dropped"} {
		if !strings.Contains(report, want) {
			t.Errorf("restarted /context omitted %q:\n%s", want, report)
		}
	}
	var out, stderr bytes.Buffer
	if code := EfficiencyConfig(args, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(out.String(), "pinned-helper") {
		t.Errorf("kiln config omitted the restored suggestion:\n%s", out.String())
	}
	if out := runContextPrint(t, args, "unrelated words"); !strings.Contains(out, "Second.") {
		t.Fatal(out)
	}
	report = runContextPrint(t, args, "/context")
	if strings.Contains(report, ":dbnote") {
		t.Errorf("a new typed request kept the old memory hit:\n%s", report)
	}
}

// TestContextSelectionLiveOldSessionWithoutRecord strips the record, as an
// older kiln would have written the session: /context keeps today's behaviour.
func TestContextSelectionLiveOldSessionWithoutRecord(t *testing.T) {
	args := seedDecisionRequest(t)
	stripped := 0
	err := filepath.WalkDir(os.Getenv("HARNESS_SESSIONS_DIR"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var kept []string
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, ContextSelectionNamespace) {
				stripped++
				continue
			}
			kept = append(kept, line)
		}
		return os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if stripped == 0 {
		t.Fatal("the session never recorded selection decisions")
	}
	report := runContextPrint(t, args, "/context")
	for _, gone := range []string{":dbnote", "dbtwo · budget-dropped"} {
		if strings.Contains(report, gone) {
			t.Errorf("old session reported %q:\n%s", gone, report)
		}
	}
	if !strings.Contains(report, "dbone skill · metadata included · search hit") {
		t.Errorf("committed search hits must still be restored:\n%s", report)
	}
}

func testTypedTranscript(text string) []msg.Message {
	return []msg.Message{msg.UserMessage{Role: msg.RoleUser, Timestamp: 1, Content: msg.Blocks{msg.Text(text)}}}
}

func TestContextSelectionRecordHoldsNoContentAndIsBounded(t *testing.T) {
	ledger := newContextSelectionLedger()
	ledger.BeginRequest(testTypedTranscript("secret prompt text"))
	for i := 0; i < 100; i++ {
		ledger.Record("memory", "scope:key"+string(rune('a'+i%26))+string(rune('a'+i/26)), "search hit", true)
		ledger.RecordSearch("call-"+string(rune('A'+i%26))+string(rune('a'+i/26)), "skill", "s", "budget-dropped", false)
	}
	raw := ledger.Marshal()
	if strings.Contains(string(raw), "secret prompt text") {
		t.Fatalf("record leaks prompt text: %s", raw)
	}
	restored := newContextSelectionLedger()
	restored.Restore(raw)
	if len(restored.transient) > maxContextSelections || len(restored.dropOrder) > maxContextSelections {
		t.Fatalf("restored %d rows, %d drops", len(restored.transient), len(restored.dropOrder))
	}
	for _, bad := range []string{"", "not json", `{"v":2,"request":"x"}`, `{"v":1}`} {
		l := newContextSelectionLedger()
		l.Restore([]byte(bad))
		if l.requestUser != "" || len(l.transient) != 0 {
			t.Errorf("Restore(%q) loaded state", bad)
		}
	}
}

// A failed save of the /context record must not fail the model request, and
// the record is retried on the next request rather than marked as saved.
func TestContextSelectionRecorderSaveFailureDoesNotFailRequest(t *testing.T) {
	ledger := newContextSelectionLedger()
	ledger.BeginRequest(testTypedTranscript("prompt"))
	ledger.Record("memory", "scope:key", "search hit", true)
	saves, fail := 0, true
	hook := selectionRecorder(ledger, func(json.RawMessage) error {
		saves++
		if fail {
			return errors.New("disk full")
		}
		return nil
	})
	if err := hook(context.Background()); err != nil {
		t.Fatalf("save failure failed the request: %v", err)
	}
	fail = false
	if err := hook(context.Background()); err != nil || saves != 2 {
		t.Fatalf("record not retried after a failed save: saves=%d err=%v", saves, err)
	}
	if err := hook(context.Background()); err != nil || saves != 2 {
		t.Fatalf("unchanged record rewritten: saves=%d err=%v", saves, err)
	}
}
