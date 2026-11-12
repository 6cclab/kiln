package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

func TestWorkerBatchCompletionRefusesUncertainOrFailedReplay(t *testing.T) {
	for _, tc := range []struct {
		name       string
		calls      []msg.ToolCall
		results    []msg.ToolResultMessage
		prior      string
		recovered  bool
		cancel     bool
		wantStatus string
		wantError  string
	}{
		{name: "no task", calls: []msg.ToolCall{{Name: "read", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one"}}},
		{name: "missing dispatch details", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one"}}, wantStatus: "blocked"},
		{name: "malformed dispatch details", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte("{")}}, wantStatus: "blocked"},
		{name: "sibling tool failed", calls: []msg.ToolCall{{Name: "task", ID: "one"}, {Name: "write", ID: "two"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true}`)}, {ToolCallID: "two", IsError: true}}, wantStatus: "blocked"},
		{name: "dispatch failed", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true,"dispatchFailed":true}`)}}, wantStatus: "blocked"},
		{name: "no changes", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true}`)}}, wantStatus: "unchanged"},
		{name: "recovered without decision", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true}`)}}, recovered: true, wantStatus: "blocked"},
		{name: "canceled batch", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true}`)}}, cancel: true, wantStatus: "blocked"},
		{name: "empty durable decision", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true}`)}}, prior: `{}`, wantError: "invalid durable decision"},
		{name: "prior intent", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true}`)}}, prior: `{"status":"intent","artifacts":["retained.json"]}`, recovered: true, wantStatus: "uncertain"},
		{name: "prior intent recovers current manifest", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true,"artifact":{"manifestPath":"current.json"}}`)}}, prior: `{"status":"intent"}`, recovered: true, wantStatus: "uncertain"},
		{name: "prior final decision", calls: []msg.ToolCall{{Name: "task", ID: "one"}}, results: []msg.ToolResultMessage{{ToolCallID: "one", Details: []byte(`{"dispatched":true}`)}}, prior: `{"status":"blocked","text":"original decision"}`, recovered: true, wantStatus: "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := integrationStorage(t, t.TempDir())
			if tc.prior != "" {
				if _, err := store.Commit([]session.Write{session.SetValueRaw(harness.WorkerIntegrationNamespace, "assistant", json.RawMessage(tc.prior))}); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := WorkerBatchCompletion(&Dispatcher{})(ctx, store, "op", "assistant", tc.calls, tc.results, tc.recovered)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v; want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantStatus == "" {
				if got != nil {
					t.Fatalf("non-worker batch got decision: %+v", got)
				}
				return
			}
			if got == nil || got.Status != tc.wantStatus {
				t.Fatalf("decision = %+v; want %q", got, tc.wantStatus)
			}
			if tc.name == "prior intent" && (len(got.Artifacts) != 1 || got.Artifacts[0] != "retained.json") {
				t.Fatalf("uncertain replay lost retained evidence: %+v", got)
			}
			if tc.name == "prior intent recovers current manifest" && (len(got.Artifacts) != 1 || got.Artifacts[0] != "current.json") {
				t.Fatalf("uncertain replay lost current manifest: %+v", got)
			}
			if tc.name == "prior final decision" && got.Text != "original decision" {
				t.Fatalf("final decision changed on replay: %+v", got)
			}
		})
	}
}

func TestIntegrateWorkerResultsRequiresCompleteAuthorizedBatch(t *testing.T) {
	repo := testRepo(t)
	if err := ApplyWorkerArtifactBatch(context.Background(), repo, nil); err == nil || !strings.Contains(err.Error(), "requires 1 to") {
		t.Fatalf("empty patch batch was accepted: %v", err)
	}
	good := &Dispatcher{IsolatedWorkers: true, AutoIntegrate: true, Env: execenv.New(repo)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name    string
		d       *Dispatcher
		ctx     context.Context
		results []DispatchResult
		want    string
	}{
		{"disabled", &Dispatcher{Env: execenv.New(repo)}, context.Background(), []DispatchResult{{Status: harness.StatusCompleted}}, "not enabled"},
		{"canceled", good, ctx, []DispatchResult{{Status: harness.StatusCompleted}}, "canceled"},
		{"empty batch", good, context.Background(), nil, "requires 1 to"},
		{"incomplete worker", good, context.Background(), []DispatchResult{{}}, "not completed"},
		{"missing retained manifest", good, context.Background(), []DispatchResult{{Status: harness.StatusCompleted, Artifact: &WorkerArtifact{}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.d.IntegrateWorkerResults(tc.ctx, tc.results)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("integration error = %v; want %q", err, tc.want)
			}
		})
	}
	got, err := good.IntegrateWorkerResults(context.Background(), []DispatchResult{{Status: harness.StatusCompleted}})
	if err != nil || got.Artifacts != 0 || len(got.Files) != 0 {
		t.Fatalf("completed worker with no patch = %+v, %v", got, err)
	}
}
