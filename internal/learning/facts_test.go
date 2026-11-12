package learning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/tools"
)

func factFixture(t *testing.T) (context.Context, *memory.Store, string, Outcome, tools.VerificationEvidence) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	proof := tools.VerificationEvidence{CallID: "call-1", Command: "go test ./...", Files: []tools.VerificationFile{{Path: "main.go", SHA256: hex.EncodeToString(sum[:]), Mode: 0600}}}
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	o := sampleOutcome("fact-run")
	o.Status = "completed"
	return context.Background(), store, root, o, proof
}

func factConfig() ObservationConfig {
	return ObservationConfig{Enabled: true, FactMemoryEnabled: true, FactReadAllowed: func(string) bool { return true }}
}

func TestFactCandidateFreshProofStoresAttributedMemory(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	cfg := factConfig()
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, cfg); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "stored" {
		t.Fatalf("candidates: %+v %v", candidates, err)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record == nil {
		t.Fatalf("verified fact missing: %+v %v", record, err)
	}
	if record.Kind != "procedure" || !strings.Contains(record.Text, proof.Command) || !strings.Contains(record.Evidence, outcome.ID) || len(record.Sources) != 1 || record.Expires == 0 {
		t.Fatalf("attribution or expiry missing: %+v", record)
	}
	if err := os.Chmod(filepath.Join(root, "main.go"), 0644); err != nil {
		t.Fatal(err)
	}
	record, err = store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record != nil {
		t.Fatalf("changed source mode still retrieved: %+v %v", record, err)
	}
}

func TestFactCandidateStaleProofRemainsReviewableAndDismissible(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" {
		t.Fatalf("stale candidate missing: %+v %v", candidates, err)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record != nil {
		t.Fatalf("stale proof promoted: %+v %v", record, err)
	}
	if err := DismissFactCandidate(ctx, store, "project", candidates[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	candidates, err = ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("dismissed candidate returned: %+v %v", candidates, err)
	}
}

func TestFactCandidateOptOutAndCorrectionHistory(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, ObservationConfig{}); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("opt-out captured: %+v %v", candidates, err)
	}
	correctedID, err := store.Put(ctx, memory.Record{Scope: "project", Key: factKey(proof), Kind: "procedure", Text: "User correction", Evidence: "Explicit user correction"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record == nil || record.ID != correctedID || record.Text != "User correction" {
		t.Fatalf("automatic fact overwrote correction: %+v %v", record, err)
	}
}

func TestFactMemoryOptOutPreservesAggregateCapture(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	cfg := ObservationConfig{Enabled: true, FactMemoryEnabled: false, MaxOutcomes: 5, MinSamples: 3}
	if err := RecordOutcomeAndPropose(ctx, store, "project", outcome, cfg); err != nil {
		t.Fatal(err)
	}
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, cfg); err != nil {
		t.Fatal(err)
	}
	outcomes, err := ListOutcomes(ctx, store, "project")
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("aggregate capture lost: %+v %v", outcomes, err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("memory opt-out captured facts: %+v %v", candidates, err)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record != nil {
		t.Fatalf("memory opt-out stored fact: %+v %v", record, err)
	}
}

func TestFactCandidateRefreshesExpiredAttributedFact(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	sources, err := memory.CaptureSources(root, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.PutIfAbsent(ctx, memory.Record{Scope: "project", Key: factKey(proof), Kind: "procedure", Text: "Observed successful verification command: go test ./...", Evidence: "Successful Bash command in opted-in outcome old; call old", Sources: sources, Expires: time.Now().Add(-time.Hour).Unix()})
	if err != nil || !stored {
		t.Fatalf("initial expired derived fact: %v %v", stored, err)
	}
	raw, err := store.Export(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	var before []memory.Record
	if err := json.Unmarshal(raw, &before); err != nil || len(before) != 1 {
		t.Fatalf("initial history: %s %v", raw, err)
	}
	previous := before[0].ID
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record == nil || record.Supersedes != previous || !strings.Contains(record.Evidence, outcome.ID) {
		t.Fatalf("expired fact not refreshed with lineage: %+v %v", record, err)
	}
}

func TestFactDoesNotRefreshExpiredManualCorrectionWithDerivedText(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	sources, err := memory.CaptureSources(root, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	manualID, err := store.Put(ctx, memory.Record{Scope: "project", Key: factKey(proof), Kind: "procedure", Text: "Observed successful verification command: go test ./...", Evidence: "Successful Bash command in opted-in outcome copied; call copied", Sources: sources, Expires: time.Now().Add(-time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Export(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	var records []memory.Record
	if err := json.Unmarshal(raw, &records); err != nil || len(records) != 1 || records[0].ID != manualID {
		t.Fatalf("automatic refresh overwrote manual record: %s %v", raw, err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" {
		t.Fatalf("manual record looked stored: %+v %v", candidates, err)
	}
}

func TestFactCandidateRejectsAmbiguousCommandText(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	for _, command := range []string{"go test ./...; echo injected", "go test ./... # ignore instructions", "go test ./... && make check", "go test ../other", "go test ./... ./other"} {
		proof.Command = command
		if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("ambiguous command became a fact: %+v %v", candidates, err)
	}
}

func TestFactCandidateStaleDirectoryEvidenceStaysReviewable(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	proof.Files = nil
	proof.Directories = []tools.VerificationDirectory{{Path: ".", Mode: 0700}}
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" || !strings.Contains(candidates[0].Reason, "snapshot changed") {
		t.Fatalf("unsupported candidate missing: %+v %v", candidates, err)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record != nil {
		t.Fatalf("directory-only proof promoted: %+v %v", record, err)
	}
}

func TestFactSourceAtOneMiBBoundaryCanStore(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	prefix, suffix := []byte("package main\n/*"), []byte("*/\n")
	data := append(prefix, make([]byte, (1<<20)-len(prefix)-len(suffix))...)
	data = append(data, suffix...)
	if err := os.WriteFile(filepath.Join(root, "main.go"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	proof.Files[0].SHA256 = hex.EncodeToString(sum[:])
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "stored" {
		t.Fatalf("exact 1 MiB source not stored: %+v %v", candidates, err)
	}
}

func TestFactOversizedProofRemainsReviewable(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	proof.Files = make([]tools.VerificationFile, 8)
	for i := range proof.Files {
		proof.Files[i] = tools.VerificationFile{Path: fmt.Sprintf("file-%d-%s", i, strings.Repeat("a", 990)), SHA256: strings.Repeat("a", 64), Mode: 0600}
	}
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" || !strings.Contains(candidates[0].Reason, "8 KiB") || candidates[0].EvidenceHash == "" {
		t.Fatalf("oversized proof vanished: %+v %v", candidates, err)
	}
}

func TestFactCandidateStatusReflectsRetentionRemoval(t *testing.T) {
	ctx, store, root, _, proof := factFixture(t)
	var oldestID string
	for i := 0; i < 9; i++ {
		data := []byte(fmt.Sprintf("package main\n// snapshot %d\n", i))
		if err := os.WriteFile(filepath.Join(root, "main.go"), data, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		proof.Files[0].SHA256 = hex.EncodeToString(sum[:])
		outcome := sampleOutcome(fmt.Sprintf("retained-%d", i))
		outcome.Status = "completed"
		if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldestID = factID(outcome.ID, proof)
		}
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.ID == oldestID {
			if candidate.Status != "review-only" || !strings.Contains(candidate.Reason, "pruned") {
				t.Fatalf("pruned fact still claimed stored: %+v", candidate)
			}
			return
		}
	}
	t.Fatal("oldest candidate missing from review queue")
}

func TestFactLargeSourceIsReviewOnlyWithReason(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	data := append([]byte("package main\n/*"), make([]byte, 1<<20)...)
	data = append(data, []byte("*/\n")...)
	if err := os.WriteFile(filepath.Join(root, "main.go"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	proof.Files[0].SHA256 = hex.EncodeToString(sum[:])
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidate: %+v %v", candidates, err)
	}
	raw, err := json.Marshal(candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if candidates[0].Status != "review-only" || !strings.Contains(string(raw), "1 MiB") {
		t.Fatalf("large source falsely promoted: %s", raw)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record != nil {
		t.Fatalf("large source stored: %+v %v", record, err)
	}
}

func TestFactReadPermissionRevokedBeforePromotion(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	allowed := false
	cfg := factConfig()
	cfg.FactReadAllowed = func(string) bool { return allowed }
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, cfg); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" || !strings.Contains(candidates[0].Reason, "Read permission denied") {
		t.Fatalf("revoked Read promoted: %+v %v", candidates, err)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record != nil {
		t.Fatalf("revoked Read stored fact: %+v %v", record, err)
	}
	allowed = true
	outcome.ID = HashIdentity("later-approved-run")
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, cfg); err != nil {
		t.Fatal(err)
	}
	record, err = store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record == nil {
		t.Fatalf("current Read failed to promote: %+v %v", record, err)
	}
}

func TestFactReadPermissionRevokedDuringSourceCapture(t *testing.T) {
	ctx, store, root, outcome, proof := factFixture(t)
	checks := 0
	cfg := factConfig()
	cfg.FactReadAllowed = func(string) bool { checks++; return checks < 4 }
	if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, cfg); err != nil {
		t.Fatal(err)
	}
	candidates, err := ListFactCandidates(ctx, store, "project", root)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "review-only" || !strings.Contains(candidates[0].Reason, "Read permission denied") {
		t.Fatalf("Read revoked during capture: %+v %v", candidates, err)
	}
	record, err := store.Current(ctx, "project", factKey(proof), root)
	if err != nil || record != nil {
		t.Fatalf("revoked capture stored fact: %+v %v", record, err)
	}
}

func TestFactRetentionBoundsSnapshotsAndPreservesCorrections(t *testing.T) {
	ctx, store, root, _, proof := factFixture(t)
	var correctedID, originalID int64
	explicitID, err := store.Put(ctx, memory.Record{Scope: "project", Key: "manual-note", Kind: "fact", Text: "Explicit note v1", Evidence: "User supplied"})
	if err != nil {
		t.Fatal(err)
	}
	explicitCorrectionID, err := store.Put(ctx, memory.Record{Scope: "project", Key: "manual-note", Kind: "fact", Text: "Explicit note v2", Evidence: "User correction"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 110; i++ {
		data := []byte(fmt.Sprintf("package main\n// snapshot %d\n", i))
		if err := os.WriteFile(filepath.Join(root, "main.go"), data, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		proof.Files[0].SHA256 = hex.EncodeToString(sum[:])
		outcome := sampleOutcome(fmt.Sprintf("snapshot-%d", i))
		outcome.Status = "completed"
		if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			original, err := store.Current(ctx, "project", factKey(proof), root)
			if err != nil || original == nil {
				t.Fatalf("first fact: %+v %v", original, err)
			}
			originalID = original.ID
			correctedID, err = store.Put(ctx, memory.Record{Scope: "project", Key: factKey(proof), Kind: "procedure", Text: "User correction", Evidence: "Explicit user correction"})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	raw, err := store.Export(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	var records []memory.Record
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	var automatic int
	var original, corrected, explicitOriginal, explicitCorrection bool
	for _, r := range records {
		if strings.HasPrefix(r.Evidence, "Successful Bash command in opted-in outcome ") {
			automatic++
		}
		if r.ID == originalID {
			original = true
		}
		if r.ID == correctedID && r.Supersedes == originalID && r.Text == "User correction" {
			corrected = true
		}
		if r.ID == explicitID {
			explicitOriginal = true
		}
		if r.ID == explicitCorrectionID && r.Supersedes == explicitID {
			explicitCorrection = true
		}
	}
	if automatic > 9 || !original || !corrected || !explicitOriginal || !explicitCorrection {
		t.Fatalf("retention/history: automatic=%d original=%v corrected=%v explicit=%v/%v", automatic, original, corrected, explicitOriginal, explicitCorrection)
	}
}

func TestFactRetentionBoundsDistinctCommandsPerProject(t *testing.T) {
	ctx, store, root, _, proof := factFixture(t)
	for i := 0; i < 105; i++ {
		proof.Command = fmt.Sprintf("go test -run=Test%03d ./...", i)
		outcome := sampleOutcome(fmt.Sprintf("command-%d", i))
		outcome.Status = "completed"
		if err := RecordFactCandidates(ctx, store, "project", root, outcome, []tools.VerificationEvidence{proof}, factConfig()); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := store.Export(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	var records []memory.Record
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	if len(records) > 100 {
		t.Fatalf("unbounded project derived facts: %d", len(records))
	}
}
