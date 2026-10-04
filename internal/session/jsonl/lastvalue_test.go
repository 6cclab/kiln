package jsonl

import (
	"encoding/json"
	"testing"

	"github.com/andrepato/harness/internal/session"
)

// LastValue reads the value last set at an address straight from the
// file: the latest set wins, a delete clears it, and other keys are not
// confused with it.
func TestLastValue(t *testing.T) {
	path := t.TempDir() + "/session.jsonl"
	header := session.Header{V: session.FormatVersion, Kind: "header", ID: "id", StorageVersion: session.StorageVersion, CreatedAt: 1, Cwd: "/tmp"}
	st, err := Create(path, header, threeSeedWrites("main"), nil)
	if err != nil {
		t.Fatal(err)
	}
	set := func(lane, model string) {
		w, err := session.SetValue(session.LaneConfig(lane), session.LaneConfiguration{Model: session.ModelRef{Provider: "p", ModelID: model}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Commit([]session.Write{w}); err != nil {
			t.Fatal(err)
		}
	}
	set("main", "first")
	set("main", "second")
	set("other", "third")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	raw, ok := LastValue(path, session.NamespaceLaneConfig, "main")
	var cfg session.LaneConfiguration
	if !ok || json.Unmarshal(raw, &cfg) != nil || cfg.Model.ModelID != "second" {
		t.Fatalf("LastValue = %s, %v; want main's last model \"second\"", raw, ok)
	}
	if _, ok := LastValue(path, session.NamespaceLaneConfig, "missing"); ok {
		t.Fatal("found a value for a key never set")
	}
	if _, ok := LastValue(path+".nope", session.NamespaceLaneConfig, "main"); ok {
		t.Fatal("found a value in a file that does not exist")
	}
}
