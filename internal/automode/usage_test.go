package automode

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Every classifier call reports its usage, answered or not, so the
// session's cost includes auto mode's spend.
func TestClassify_ReportsUsage(t *testing.T) {
	for name, s := range map[string]*fakeStreamer{
		"answered":   answering(`{"decision":"allow"}`),
		"unreadable": answering(`not json`),
	} {
		c := classifierWith(s)
		var got []msg.Usage
		c.OnUsage = func(m provider.Model, u msg.Usage) {
			if m.ID != "fast-1" {
				t.Errorf("%s: usage for model %q", name, m.ID)
			}
			got = append(got, u)
		}
		_, _ = c.Classify(context.Background(), bash("make deploy"))
		if len(got) != 1 || got[0].Input != 900 || got[0].Output != 20 {
			t.Errorf("%s: OnUsage got %+v, want the call's usage once", name, got)
		}
	}
}
