package learning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/tools"
)

// FactCandidate is an observed successful command with its exact declared
// input snapshot. It makes no claim that a flag or command is required.
type FactCandidate struct {
	ID           string                     `json:"id"`
	OutcomeID    string                     `json:"outcomeId"`
	Command      string                     `json:"command"`
	Proof        tools.VerificationEvidence `json:"proof"`
	EvidenceHash string                     `json:"evidenceHash,omitempty"`
	CreatedAt    int64                      `json:"createdAt"`
	Status       string                     `json:"status,omitempty"`
	Reason       string                     `json:"reason,omitempty"`
}

func factScope(project string) string          { return "outcome-fact:" + project }
func factDismissalScope(project string) string { return "outcome-fact-dismissed:" + project }

func factID(outcomeID string, proof tools.VerificationEvidence) string {
	raw, _ := json.Marshal(proof)
	return HashIdentity("fact-candidate-v1\x00" + outcomeID + "\x00" + string(raw))
}

func factKey(proof tools.VerificationEvidence) string {
	raw, _ := json.Marshal(struct {
		Command string                   `json:"command"`
		Files   []tools.VerificationFile `json:"files"`
	}{proof.Command, proof.Files})
	return "verified-command:" + HashIdentity(string(raw))
}

func observedCommand(proof tools.VerificationEvidence) bool {
	// A literal local Go test command is narrow enough to describe without
	// parsing shell syntax or assigning meaning to arbitrary tool output.
	command := proof.Command
	if len(command) > 512 || strings.TrimSpace(command) != command {
		return false
	}
	parts := strings.Fields(command)
	if len(parts) < 3 || command != strings.Join(parts, " ") || parts[0] != "go" || parts[1] != "test" {
		return false
	}
	packages := 0
	for _, part := range parts[2:] {
		if part == "-race" || part == "-short" {
			continue
		}
		if strings.HasPrefix(part, "-tags=") && asciiToken(strings.TrimPrefix(part, "-tags="), "_,") {
			continue
		}
		if strings.HasPrefix(part, "-count=") && asciiDigits(strings.TrimPrefix(part, "-count=")) {
			continue
		}
		if strings.HasPrefix(part, "-run=") && asciiToken(strings.TrimPrefix(part, "-run="), "_./-") {
			continue
		}
		if !strings.HasPrefix(part, "./") || part == "./.." || strings.Contains(part, "../") || !asciiToken(strings.TrimPrefix(part, "./"), "_./-") {
			return false
		}
		packages++
		if packages > 1 {
			return false
		}
	}
	return packages == 1
}

func asciiToken(value, punctuation string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune(punctuation, r)) {
			return false
		}
	}
	return true
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RecordFactCandidates derives reviewable observations from one opted-in run.
// Only fully checkable file snapshots can be promoted automatically.
func RecordFactCandidates(ctx context.Context, store *memory.Store, project, root string, outcome Outcome, proofs []tools.VerificationEvidence, cfg ObservationConfig) error {
	if !cfg.Enabled || !cfg.FactMemoryEnabled || outcome.Status != "completed" {
		return nil
	}
	if err := outcome.validate(); err != nil {
		return err
	}
	if len(proofs) > 16 {
		proofs = proofs[:16]
	}
	for _, proof := range proofs {
		if !observedCommand(proof) || len(proof.Files)+len(proof.Directories) == 0 {
			continue
		}
		candidate := FactCandidate{ID: factID(outcome.ID, proof), OutcomeID: outcome.ID, Command: proof.Command, Proof: proof, CreatedAt: time.Now().Unix(), Status: "review-only"}
		dismissed, err := store.Current(ctx, factDismissalScope(project), candidate.ID, "")
		if err != nil {
			return err
		}
		if dismissed != nil {
			continue
		}
		if initial, err := json.Marshal(candidate); err != nil {
			return err
		} else if len(initial) > 7600 {
			proofRaw, _ := json.Marshal(proof)
			candidate.Proof = tools.VerificationEvidence{}
			candidate.EvidenceHash = HashIdentity(string(proofRaw))
			candidate.Reason = "verification proof exceeds 8 KiB review limit"
			if err := storeFactCandidate(ctx, store, project, candidate); err != nil {
				return err
			}
			continue
		}
		if sources, reason := factSourcesForPromotion(ctx, root, proof, cfg.FactReadAllowed); reason != "" {
			candidate.Reason = reason
		} else {
			key := factKey(proof)
			text := fmt.Sprintf("Observed successful verification command: %s", proof.Command)
			evidence := fmt.Sprintf("Successful Bash command in opted-in outcome %s; call %s; declared file snapshot rechecked", outcome.ID, proof.CallID)
			stored, err := store.PutIfAbsent(ctx, memory.Record{Scope: project, Key: key, Kind: "procedure", Text: text, Evidence: evidence, Sources: sources, Expires: time.Now().Add(30 * 24 * time.Hour).Unix()})
			if err != nil {
				candidate.Reason = "memory write failed"
			} else if stored {
				candidate.Status = "stored"
				candidate.Reason = "verified and stored with source attribution"
			} else {
				candidate.Reason = "existing memory record or user correction preserved"
			}
		}
		if err := storeFactCandidate(ctx, store, project, candidate); err != nil {
			return err
		}
	}
	return nil
}

func storeFactCandidate(ctx context.Context, store *memory.Store, project string, candidate FactCandidate) error {
	raw, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	if len(raw) > 8192 {
		return fmt.Errorf("fact candidate exceeds review storage limit")
	}
	_, err = store.PutUniquePruned(ctx, memory.Record{Scope: factScope(project), Key: candidate.ID, Kind: "procedure", Text: string(raw), Evidence: "Observed successful Bash verification in opted-in outcome " + candidate.OutcomeID, Expires: time.Now().Add(7 * 24 * time.Hour).Unix()}, 100)
	return err
}

func factSourcesForPromotion(ctx context.Context, root string, proof tools.VerificationEvidence, allowRead func(string) bool) ([]memory.Source, string) {
	if len(proof.Directories) != 0 {
		return nil, "directory membership cannot be represented by memory sources"
	}
	if len(proof.MissingFiles) != 0 {
		return nil, "missing-file dependencies cannot be represented by memory sources"
	}
	if len(proof.Files) == 0 {
		return nil, "no declared regular file sources"
	}
	if len(proof.Files) > 8 {
		return nil, "more than eight declared files exceed memory source limit"
	}
	if allowRead == nil {
		return nil, "current Read permission unavailable"
	}
	paths := make([]string, len(proof.Files))
	for i, file := range proof.Files {
		paths[i] = file.Path
		if !allowRead(file.Path) {
			return nil, "current Read permission denied"
		}
	}
	if !tools.VerificationFreshContext(ctx, root, proof, allowRead) {
		for _, path := range paths {
			if !allowRead(path) {
				return nil, "current Read permission denied"
			}
		}
		return nil, "declared input snapshot changed or unavailable"
	}
	for _, path := range paths {
		if !allowRead(path) {
			return nil, "current Read permission denied"
		}
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil || !info.Mode().IsRegular() {
			return nil, "source file unavailable or not regular"
		}
		if info.Size() > 1<<20 {
			return nil, "source file exceeds 1 MiB memory source limit"
		}
	}
	sources, err := memory.CaptureSourcesWithRead(root, paths, allowRead)
	if errors.Is(err, memory.ErrSourceReadDenied) {
		return nil, "current Read permission denied during source capture"
	}
	if err != nil {
		return nil, "source capture failed or source exceeded memory limit"
	}
	for i, source := range sources {
		if source.SHA256 != proof.Files[i].SHA256 || !source.ModeKnown || source.Mode != proof.Files[i].Mode {
			return nil, "source snapshot changed during capture"
		}
	}
	if !tools.VerificationFreshContext(ctx, root, proof, allowRead) {
		return nil, "declared input snapshot changed or current Read permission denied"
	}
	for _, path := range paths {
		if !allowRead(path) {
			return nil, "current Read permission denied"
		}
	}
	return sources, ""
}

// ListFactCandidates includes stale observations for review, without applying them.
func ListFactCandidates(ctx context.Context, store *memory.Store, project, root string) ([]FactCandidate, error) {
	records, err := store.Recent(ctx, factScope(project), 100)
	if err != nil {
		return nil, err
	}
	out := make([]FactCandidate, 0, len(records))
	for _, record := range records {
		var candidate FactCandidate
		if err := json.Unmarshal([]byte(record.Text), &candidate); err != nil {
			return nil, err
		}
		if candidate.ID != record.Key || !validDigest(candidate.OutcomeID) || (candidate.EvidenceHash == "" && candidate.ID != factID(candidate.OutcomeID, candidate.Proof)) || (candidate.EvidenceHash != "" && (!validDigest(candidate.EvidenceHash) || candidate.Status != "review-only")) {
			return nil, fmt.Errorf("invalid stored fact candidate")
		}
		if candidate.Status == "" {
			candidate.Status, candidate.Reason = "review-only", "legacy candidate has no recorded promotion result"
		}
		if candidate.Status == "stored" {
			present, err := store.DerivedFactStored(ctx, project, factKey(candidate.Proof), candidate.OutcomeID)
			if err != nil {
				return nil, err
			}
			if !present {
				candidate.Status, candidate.Reason = "review-only", "derived fact expired, corrected, or pruned"
			}
		}
		out = append(out, candidate)
	}
	return out, nil
}

func DismissFactCandidate(ctx context.Context, store *memory.Store, project, id string) error {
	if !validDigest(id) {
		return fmt.Errorf("invalid fact candidate ID")
	}
	candidate, err := store.Current(ctx, factScope(project), id, "")
	if err != nil {
		return err
	}
	if candidate == nil {
		return fmt.Errorf("fact candidate not found")
	}
	_, err = store.PutUniquePruned(ctx, memory.Record{Scope: factDismissalScope(project), Key: id, Kind: "procedure", Text: "dismissed", Evidence: "Explicit user command"}, 1000)
	if err != nil {
		return err
	}
	return store.Forget(ctx, factScope(project), id)
}
