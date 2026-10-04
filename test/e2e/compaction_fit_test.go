//go:build e2e

package e2e

// A model switch to a smaller window. The session of 2026-10-02 grew to
// ~107k tokens on a 200k model, switched to a 49k Ollama model and ran
// /compact: kiln sent the whole history in one summary request the new
// model could not hold. faux-1 (128k window) and faux-2 (32k) stand in
// for the two models here.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/screen"
)

const faux2Window = 32768

// switchScript seeds four ~12k-token replies on faux-1, then gives each
// model the answers named.
func switchScript(faux1After, faux2 []string) string {
	var b strings.Builder
	b.WriteString("models:\n  faux-1:\n")
	for i := 0; i < 4; i++ {
		b.WriteString("    - text: \"" + longReply + "\"\n      end_turn: true\n")
	}
	for _, a := range faux1After {
		b.WriteString("    - text: \"" + a + "\"\n      end_turn: true\n")
	}
	b.WriteString("  faux-2:\n")
	for _, a := range faux2 {
		b.WriteString("    - text: \"" + a + "\"\n      end_turn: true\n")
	}
	return b.String()
}

// requestTokens is a recorded request's size by kiln's chars/4 estimate.
func requestTokens(r tkfaux.Request) int {
	return (len(r.System) + len(r.Messages) + len(fmt.Sprint(r.Tools)) + 3) / 4
}

func modelRequests(srv *tkfaux.Server, model string) []tkfaux.Request {
	var out []tkfaux.Request
	for _, r := range srv.Requests() {
		if r.Model == model {
			out = append(out, r)
		}
	}
	return out
}

// startSwitchedSession seeds a ~48k-token conversation on faux-1, opens
// it in the TUI and switches to faux-2, asserting the switch warns.
func startSwitchedSession(t *testing.T, script string) (*tkfaux.Server, *screen.Screen) {
	t.Helper()
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, srv := startFaux(t, script)
	seedConversation(t, proj, home, sessDir, addr, 4)
	sc := startTUI(t, 120, 40, proj, home, sessDir, addr, "-c")
	waitReady(t, sc)
	sc.Send("/model faux/faux-2")
	sc.SendKey("enter")
	if err := sc.WaitFor("Now on faux/faux-2", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := sc.WaitFor("takes in one request", 2*time.Second); err != nil {
		t.Fatalf("switching to a window smaller than the conversation did not say so: %v\n%s", err, strings.Join(sc.Rows(), "\n"))
	}
	if err := sc.WaitFor("/compact now to summarise it with faux/faux-1", 2*time.Second); err != nil {
		t.Fatalf("the switch did not offer to summarise with the outgoing model: %v", err)
	}
	return srv, sc
}

// TestTUI_ModelSwitch_NextMessageCompactsInParts: after the switch, the
// next message compacts with faux-2 in parts, and no request faux-2
// receives (summary parts or the turn) is larger than its window.
func TestTUI_ModelSwitch_NextMessageCompactsInParts(t *testing.T) {
	srv, s := startSwitchedSession(t, switchScript(nil, []string{"part one", "part two", "part three", "part four", "Small model here."}))
	s.Send("carry on")
	s.SendKey("enter")
	if err := s.WaitFor("Small model here.", 30*time.Second); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	small := modelRequests(srv, "faux-2")
	if len(small) < 3 {
		t.Fatalf("faux-2 got %d requests; want the compaction in at least two parts, then the turn", len(small))
	}
	for i, r := range small {
		if n := requestTokens(r); n > faux2Window {
			t.Errorf("faux-2 request %d was ~%d tokens, over its %d-token window", i+1, n, faux2Window)
		}
	}
	if !strings.Contains(string(small[len(small)-1].Messages), "part ") {
		t.Error("the turn on faux-2 was not sent the summary")
	}
}

// TestTUI_ModelSwitch_CompactUsesOutgoingModel: /compact right after the
// switch, as the warning offers, summarises with faux-1, which holds the
// whole conversation without splitting it; faux-2 then answers the next turn
// from the summary.
func TestTUI_ModelSwitch_CompactUsesOutgoingModel(t *testing.T) {
	srv, s := startSwitchedSession(t, switchScript([]string{"Summary by the big model.", "Prefix summary."}, []string{"Small model here."}))
	before := len(modelRequests(srv, "faux-1"))
	submitSlashCommand(s, "compact")
	if err := s.WaitFor("summarised by faux/faux-1", 10*time.Second); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(s.Rows(), "\n"))
	}
	// One request for the history; the cut falls inside the last turn
	// (its reply alone outgrows faux-2's retained tail), so pi's split-turn
	// prefix summary is a second one.
	if n := len(modelRequests(srv, "faux-1")) - before; n < 1 || n > 2 {
		t.Errorf("/compact sent %d requests to faux-1, want the history and the split turn's prefix", n)
	}
	if n := len(modelRequests(srv, "faux-2")); n != 0 {
		t.Errorf("/compact sent %d requests to faux-2, want none", n)
	}
	s.Send("carry on")
	s.SendKey("enter")
	if err := s.WaitFor("Small model here.", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	small := modelRequests(srv, "faux-2")
	if len(small) != 1 {
		t.Fatalf("faux-2 got %d requests, want just the turn (the conversation already fits)", len(small))
	}
	if !strings.Contains(string(small[0].Messages), "Summary by the big model.") {
		t.Error("the turn on faux-2 was not sent faux-1's summary")
	}
	if n := requestTokens(small[0]); n > faux2Window {
		t.Errorf("the turn on faux-2 was ~%d tokens, over its window", n)
	}
}
