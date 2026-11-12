package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

func TestPrintResumeFailureStopsBeforeNewPrompt(t *testing.T) {
	srv := startFaux(t, "model: faux-1\nsteps:\n  - text: first reply\n")
	cwd := scratchProject(t)
	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "first prompt"
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 0 {
		t.Fatalf("initial print run: code=%d stderr=%s", code, stderr.String())
	}
	path := sessionFile(t, os.Getenv("HARNESS_SESSIONS_DIR"), cwd)
	storage, err := jsonl.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := storage.Header().ID
	opID := "incomplete-operation"
	stateWrite, err := session.SetValue(session.OperationState(opID), json.RawMessage(`{"at":"deferred.pending"}`))
	if err != nil {
		t.Fatal(err)
	}
	laneWrite, err := session.SetValue(session.LaneStateValue("main"), session.LaneState{CurrentOperationID: &opID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Commit([]session.Write{stateWrite, laneWrite}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	requests := len(srv.Requests())
	args.Resume = id
	args.ResumeSet = true
	args.PrintPrompt = "new prompt"
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader("")); code != 1 {
		t.Fatalf("resume failure code=%d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if got := len(srv.Requests()); got != requests {
		t.Fatalf("model requests=%d, want %d: new prompt ran after failed recovery", got, requests)
	}
	if !strings.Contains(stderr.String(), "resume") || !strings.Contains(stderr.String(), string(harness.AtDeferredPending)) {
		t.Fatalf("missing recovery error: %s", stderr.String())
	}
}
