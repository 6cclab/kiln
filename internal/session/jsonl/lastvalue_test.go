package jsonl

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/andrepato/harness/internal/msg"
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

// LastResponseModel is the newest successful reply's model: errored and
// aborted replies are skipped, and a user message is not a reply.
func TestLastResponseModel(t *testing.T) {
	path := t.TempDir() + "/session.jsonl"
	header := session.Header{V: session.FormatVersion, Kind: "header", ID: "id", StorageVersion: session.StorageVersion, CreatedAt: 1, Cwd: "/tmp"}
	st, err := Create(path, header, threeSeedWrites("main"), nil)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	add := func(m msg.Message) {
		n++
		e := session.Entry{ID: fmt.Sprintf("e%d", n), Type: session.EntryMessage, Message: m}
		if _, err := st.Commit([]session.Write{session.EntryWrite{Entry: e}}); err != nil {
			t.Fatal(err)
		}
	}
	reply := func(model string, stop msg.StopReason) msg.AssistantMessage {
		return msg.AssistantMessage{Role: msg.RoleAssistant, Provider: "faux", Model: model, StopReason: stop}
	}
	add(msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.TextContent{Type: "text", Text: "hi"}}})
	add(reply("faux-1", msg.StopStop))
	add(reply("faux-2", msg.StopToolUse))
	add(reply("faux-3", msg.StopError))
	add(reply("faux-4", msg.StopAborted))
	add(msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.TextContent{Type: "text", Text: "assistant"}}})
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if p, m, ok := LastResponseModel(path); !ok || p != "faux" || m != "faux-2" {
		t.Fatalf("LastResponseModel = %s/%s, %v; want faux/faux-2", p, m, ok)
	}
	if _, _, ok := LastResponseModel(path + ".nope"); ok {
		t.Fatal("found a reply in a file that does not exist")
	}
}
