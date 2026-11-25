package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
)

const loopSentence = "I will now check the configuration file again. "

func loopingStep() string {
	return fmt.Sprintf("  - text: %q\n    end_turn: true\n", "Let me look. "+strings.Repeat(loopSentence, 200))
}

// TestRepeatingReplyIsCutAndContinued: a reply that degenerates into the
// same sentence is cut as it streams, kept up to the first copy, and the
// model is asked once, with a marked note, to go on without repeating.
func TestRepeatingReplyIsCutAndContinued(t *testing.T) {
	for _, api := range []string{"anthropic-messages", "openai-completions"} {
		t.Run(api, func(t *testing.T) { testRepeatingReplyIsCutAndContinued(t, api) })
	}
}

func testRepeatingReplyIsCutAndContinued(t *testing.T, api string) {
	script := "model: faux-1\nsteps:\n" + loopingStep() + "  - text: \"Done: the configuration is fine.\"\n    end_turn: true\n"
	rig := newTestRigWith(t, script, []string{"bash"}, nil, func(_ *Options, reg *provider.Registry) {
		t.Setenv("HARNESS_FAUX_API", api)
		fp, ok := fauxprovider.New()
		if !ok {
			t.Fatal("fauxprovider.New failed")
		}
		reg.Register(fp)
	})
	lane := rig.mustLane("main")
	var faults []string
	rig.H.Events().On(EventFault, func(ev Event) { faults = append(faults, ev.Err.Error()) })

	res, err := lane.Prompt(context.Background(), "check the config", nil)
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("Prompt = %+v, %v; want completed", res, err)
	}
	if len(faults) != 0 {
		t.Errorf("faults = %v, want none", faults)
	}
	reqs := rig.Faux.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model requests, want 2 (the repeating reply, then its continuation)", len(reqs))
	}
	if want := strings.SplitN(api, "-", 2)[0]; reqs[0].Protocol != want {
		t.Fatalf("request protocol %q, want %q", reqs[0].Protocol, want)
	}
	var second []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(reqs[1].Messages, &second); err != nil {
		t.Fatal(err)
	}
	if last := second[len(second)-1]; last.Role != "user" || !strings.Contains(string(last.Content), "started repeating the same text") {
		t.Fatalf("second request ends with %s %s, want the repetition note", last.Role, last.Content)
	}

	entries, _ := lane.FindEntries(context.Background())
	var cut string
	var notes int
	for _, e := range entries {
		switch m := e.Message.(type) {
		case msg.AssistantMessage:
			if t := msg.TextOf(m.Content); strings.HasPrefix(t, "Let me look.") {
				cut = t
			}
		case msg.UserMessage:
			if m.KilnHook == repetitionSource {
				notes++
			}
		}
	}
	if n := strings.Count(cut, "I will now check the configuration file again"); n != 1 || !strings.HasPrefix(cut, "Let me look. ") {
		t.Errorf("kept reply has %d copies of the sentence, want the intro and 1:\n%q", n, cut)
	}
	if notes != 1 {
		t.Errorf("%d repetition notes on the branch, want 1", notes)
	}
}

// TestRepeatingRepliesEndWithAFault: after maxRepetitionRecoveries nudges
// the turn ends, with a fault saying why.
func TestRepeatingRepliesEndWithAFault(t *testing.T) {
	script := "model: faux-1\nsteps:\n" + strings.Repeat(loopingStep(), maxRepetitionRecoveries+1)
	rig := newTestRig(t, script, []string{"bash"})
	lane := rig.mustLane("main")
	var faults []string
	rig.H.Events().On(EventFault, func(ev Event) { faults = append(faults, ev.Err.Error()) })
	if _, err := lane.Prompt(context.Background(), "check the config", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(rig.Faux.Requests()); n != maxRepetitionRecoveries+1 {
		t.Errorf("%d requests, want %d", n, maxRepetitionRecoveries+1)
	}
	if len(faults) != 1 || !strings.Contains(faults[0], "kept repeating itself") {
		t.Errorf("faults = %v, want one repetition fault", faults)
	}
}

// TestPeriodicTailIgnoresOrdinaryText: long but varied text, a long rule,
// and a short repeat are not loops; a paragraph repeated is.
func TestPeriodicTailIgnoresOrdinaryText(t *testing.T) {
	var varied strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&varied, "line %d computes %d\n", i, i*i)
	}
	for name, text := range map[string]string{
		"varied lines": varied.String(),
		"long rule":    strings.Repeat("─", 3000),
		"table rule":   strings.Repeat("|-----", 600),
		"three copies": "intro " + strings.Repeat("a sentence that appears a few times. ", 3),
	} {
		if _, _, ok := periodicTail([]byte(text)); ok {
			t.Errorf("%s: flagged as a loop", name)
		}
	}
	text := "intro " + strings.Repeat("The quick brown fox jumps over the lazy dog, again and again. ", 40)
	span, period, ok := periodicTail([]byte(text))
	if !ok || period != len("The quick brown fox jumps over the lazy dog, again and again. ") || span < len(text)-10 {
		t.Errorf("repeated paragraph: span %d period %d ok %v", span, period, ok)
	}
}
