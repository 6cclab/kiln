package learning

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/memory"
)

func sampleOutcome(id string) Outcome {
	return Outcome{ID: HashIdentity(id), ObservedAt: time.Now().Unix(), Status: "completed", ModelHash: HashIdentity("model"), ModelsUsed: 1, BinaryHash: HashIdentity("binary"), UsageScope: "main-lane model responses; worker and side usage excluded", InputTokens: 100, ToolSuccess: 1}
}

func TestOutcomeRetentionIdempotenceAndPrivacy(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, id := range []string{"secret prompt one", "secret prompt one", "secret prompt two", "secret prompt three"} {
		if err := RecordOutcome(ctx, store, "a", sampleOutcome(id), 2); err != nil {
			t.Fatal(err)
		}
	}
	outcomes, err := ListOutcomes(ctx, store, "a")
	if err != nil || len(outcomes) != 2 {
		t.Fatalf("retention/idempotence: %+v %v", outcomes, err)
	}
	if err := RecordOutcome(ctx, store, "a", sampleOutcome("secret prompt three"), 1); err != nil {
		t.Fatal(err)
	}
	outcomes, err = ListOutcomes(ctx, store, "a")
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("duplicate did not tighten retention: %+v %v", outcomes, err)
	}
	raw, err := store.Export(ctx, OutcomeScope("a"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret prompt") || strings.Contains(string(raw), "command") || strings.Contains(string(raw), "path") {
		t.Fatalf("raw content persisted: %s", raw)
	}
	other, err := ListOutcomes(ctx, store, "b")
	if err != nil || len(other) != 0 {
		t.Fatalf("scope leak: %+v %v", other, err)
	}
	invalid := sampleOutcome("bad")
	invalid.ModelHash = "provider/model with prompt text"
	if err := RecordOutcome(ctx, store, "a", invalid, 2); err == nil {
		t.Fatal("unbounded identity accepted")
	}
}

func TestSuggestionsAreHypothesesAndDismissible(t *testing.T) {
	ctx := context.Background()
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	for i := 0; i < 5; i++ {
		o := sampleOutcome(string(rune('a' + i)))
		o.Status = "failed"
		o.InputTokens = 40000
		o.ToolFailure = 1
		o.BashFailure = 1
		if err := RecordOutcome(ctx, store, "project", o, 10); err != nil {
			t.Fatal(err)
		}
	}
	suggestions, err := ReviewSuggestions(ctx, store, "project", 5)
	if err != nil || len(suggestions) != 3 {
		t.Fatalf("suggestions: %+v %v", suggestions, err)
	}
	for _, s := range suggestions {
		if !strings.Contains(s.Evidence, "unknown") && !strings.Contains(s.Evidence, "no improvement") {
			t.Fatalf("unqualified correctness inference: %+v", s)
		}
	}
	if err := DismissSuggestion(ctx, store, "project", "tool-failures"); err != nil {
		t.Fatal(err)
	}
	if err := DismissSuggestion(ctx, store, "project", "tool-failures"); err != nil {
		t.Fatal(err)
	}
	dismissals, err := store.Export(ctx, "outcome-dismissed:project")
	if err != nil {
		t.Fatal(err)
	}
	var dismissalRecords []memory.Record
	if err := json.Unmarshal(dismissals, &dismissalRecords); err != nil || len(dismissalRecords) != 1 {
		t.Fatalf("duplicate dismissal history: %s %v", dismissals, err)
	}
	suggestions, err = ReviewSuggestions(ctx, store, "project", 5)
	if err != nil || len(suggestions) != 2 {
		t.Fatalf("dismissal: %+v %v", suggestions, err)
	}
	if err := DismissSuggestion(ctx, store, "project", "arbitrary user text"); err == nil {
		t.Fatal("arbitrary suggestion key accepted")
	}
	if err := ForgetOutcomes(ctx, store, "project"); err != nil {
		t.Fatal(err)
	}
	outcomes, err := ListOutcomes(ctx, store, "project")
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("forget: %+v %v", outcomes, err)
	}
	dismissed, err := store.Current(ctx, "outcome-dismissed:project", "tool-failures", "")
	if err != nil || dismissed != nil {
		t.Fatalf("dismissal survived forget: %+v %v", dismissed, err)
	}
}

func TestObservationConfigBounds(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config ObservationConfig
		valid  bool
	}{
		{"disabled bypasses invalid values", ObservationConfig{MaxOutcomes: -1}, true},
		{"defaults", ObservationConfig{Enabled: true}, true},
		{"min exceeds retention", ObservationConfig{Enabled: true, MaxOutcomes: 3, MinSamples: 5}, false},
		{"max too large", ObservationConfig{Enabled: true, MaxOutcomes: 1001, MinSamples: 5}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeObservationConfig(tt.config)
			if (err == nil) != tt.valid {
				t.Fatalf("config=%+v err=%v", tt.config, err)
			}
		})
	}
	// Ensure the public record schema stays constrained to aggregate fields.
	raw, err := json.Marshal(sampleOutcome("sensitive input"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sensitive input") {
		t.Fatal("identity was not hashed")
	}
}

func TestConcurrentOutcomeInsertAcrossStoreConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	a, err := memory.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := memory.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := a
			if i%2 == 1 {
				store = b
			}
			errs <- RecordOutcome(context.Background(), store, "same", sampleOutcome("one-operation"), 2)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	outcomes, err := ListOutcomes(context.Background(), a, "same")
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("duplicate outcome records: %+v %v", outcomes, err)
	}
	raw, err := a.Export(context.Background(), OutcomeScope("same"))
	if err != nil {
		t.Fatal(err)
	}
	var records []memory.Record
	if err := json.Unmarshal(raw, &records); err != nil || len(records) != 1 {
		t.Fatalf("duplicate history: %s %v", raw, err)
	}
}
