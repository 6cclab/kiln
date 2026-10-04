package agent

import (
	"context"
	"testing"

	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/session"
)

// ResumedModel names the model the session a resume would open last ran
// on; Start then runs that lane on whatever model it is given, so kiln
// never shows one model while sending to another.
func TestResumedModelAndStartAlignTheLane(t *testing.T) {
	reg := newFauxRegistry(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	resolved := mustResolve(t, reg)
	cwd, root := t.TempDir(), t.TempDir()

	if _, ok := ResumedModel(Options{Cwd: cwd, SessionsRoot: root}); ok {
		t.Fatal("no resume asked for, but ResumedModel found a model")
	}
	if _, ok := ResumedModel(Options{Cwd: cwd, SessionsRoot: root, ResumeLatest: true}); ok {
		t.Fatal("no session to resume, but ResumedModel found a model")
	}

	started, err := Start(context.Background(), Options{Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	other := session.ModelRef{Provider: fauxprovider.ProviderID, ModelID: fauxprovider.ModelID2}
	if err := started.Lane.SetModel(other, ""); err != nil {
		t.Fatal(err)
	}

	got, ok := ResumedModel(Options{Cwd: cwd, SessionsRoot: root, ResumeLatest: true})
	if !ok || got != other {
		t.Fatalf("ResumedModel = %+v, %v; want the session's last model %+v", got, ok, other)
	}
	if got, ok := ResumedModel(Options{Cwd: cwd, SessionsRoot: root, Resume: started.SessionID}); !ok || got != other {
		t.Fatalf("ResumedModel by id = %+v, %v", got, ok)
	}

	// The defaults Start uses: the working directory and the sessions
	// root from the environment.
	t.Chdir(cwd)
	t.Setenv(defaultSessionsDirEnv, root)
	if got, ok := ResumedModel(Options{ResumeLatest: true}); !ok || got != other {
		t.Fatalf("ResumedModel from defaults = %+v, %v", got, ok)
	}

	// Resumed on faux-1 (say, --model): the lane follows.
	resumed, err := Start(context.Background(), Options{Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root, ResumeLatest: true})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := resumed.Lane.Model(); m.ModelID != fauxprovider.ModelID {
		t.Fatalf("resumed lane runs on %+v, want the model Start was given", m)
	}
}
