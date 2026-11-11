package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/andrepato/harness/internal/tool"
)

const maxVerificationRecords = 16
const maxVerificationStatusResults = 8

type verificationStatusArgs struct {
	Command string   `json:"command"`
	Files   []string `json:"files"`
}

type verificationStatusRecord struct {
	CallID  string   `json:"callId"`
	Command string   `json:"command,omitempty"`
	Status  string   `json:"status"`
	Reason  string   `json:"reason"`
	Files   []string `json:"files,omitempty"`
}

// VerificationStatusTool reports current file state for successful Bash
// evidence supplied by the caller's committed, selected branch. It cannot
// execute checks or infer whether a command tested the right behavior.
func VerificationStatusTool(cwd string, allowRead func(string) bool, proofs func() []VerificationEvidence, tokens int) *tool.Tool {
	return &tool.Tool{
		Name: "verification_status", Label: "Check verification evidence",
		Description: "Inspect recent successful Bash command evidence on this branch. Recheck only its declared file dependencies; unchanged means those bytes and permissions still match, not that the task is correct. Optional exact command or file filters narrow results. This tool never runs commands.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Exact command to filter"},"files":{"type":"array","maxItems":16,"items":{"type":"string"},"description":"Show evidence associated with any of these project files"}}}`),
		Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var args verificationStatusArgs
			if err := json.Unmarshal(raw, &args); err != nil || len(args.Files) > maxVerificationFiles {
				return tool.Errorf("verification_status: invalid arguments"), nil
			}
			// A denied filter must not become an existence or evidence oracle.
			for _, path := range args.Files {
				if path == "" || len(path) > 4096 || filepath.IsAbs(path) || filepath.Clean(path) != path || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) || allowRead == nil || !allowRead(path) {
					return tool.Errorf("verification_status: file filter unavailable"), nil
				}
			}
			if proofs == nil {
				return tool.Text("No saved verification evidence on this branch."), nil
			}
			limit := tokens * 4
			if limit < 512 {
				limit = 512
			} else if limit > 32768 {
				limit = 32768
			}
			all := proofs()
			if len(all) > maxVerificationRecords {
				all = all[:maxVerificationRecords]
			}
			records := make([]verificationStatusRecord, 0, maxVerificationStatusResults)
			used := 0
			outputFull := false
			for _, proof := range all {
				if err := ctx.Err(); err != nil {
					return tool.Errorf("verification_status: cancelled"), nil
				}
				if args.Command != "" && args.Command != proof.Command {
					continue
				}
				if len(args.Files) > 0 && !verificationFileIntersection(proof.Files, args.Files) {
					continue
				}
				used++
				if outputFull || len(records) >= maxVerificationStatusResults {
					continue
				}
				record := checkVerificationProof(ctx, cwd, proof, allowRead)
				candidate := append(records, record)
				encoded, err := json.Marshal(struct {
					Scope   string                     `json:"scope"`
					Records []verificationStatusRecord `json:"records"`
				}{Scope: "declared file snapshots only; no task correctness claim", Records: candidate})
				if err != nil {
					return tool.Result{}, err
				}
				if len(encoded)+32 > limit {
					outputFull = true
					continue
				}
				records = candidate
			}
			if used == 0 {
				return tool.Text("No matching saved verification evidence on this branch."), nil
			}
			output, err := json.Marshal(struct {
				Scope   string                     `json:"scope"`
				Records []verificationStatusRecord `json:"records"`
				Omitted int                        `json:"omitted"`
			}{Scope: "declared file snapshots only; no task correctness claim", Records: records, Omitted: used - len(records)})
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Text(string(output)), nil
		},
	}
}

func verificationFileIntersection(files []VerificationFile, requested []string) bool {
	for _, file := range files {
		for _, path := range requested {
			if file.Path == path {
				return true
			}
		}
	}
	return false
}

func checkVerificationProof(ctx context.Context, cwd string, proof VerificationEvidence, allowRead func(string) bool) verificationStatusRecord {
	record := verificationStatusRecord{CallID: proof.CallID}
	if len(proof.Files) == 0 || len(proof.Files) > maxVerificationFiles || allowRead == nil {
		record.Status, record.Reason = "unavailable", "saved dependency set cannot be checked"
		return record
	}
	for _, file := range proof.Files {
		if !allowRead(file.Path) {
			record.Status, record.Reason = "unavailable", "one or more dependencies cannot be read"
			return record
		}
	}
	paths := make([]string, 0, len(proof.Files))
	for _, file := range proof.Files {
		paths = append(paths, file.Path)
	}
	current, err := fingerprintFiles(ctx, cwd, paths, allowRead)
	if err != nil {
		record.Status, record.Reason = "unavailable", "one or more dependencies cannot be checked"
		return record
	}
	record.Command = proof.Command
	if len(record.Command) > 256 {
		record.Command = record.Command[:256]
		for !utf8.ValidString(record.Command) {
			record.Command = record.Command[:len(record.Command)-1]
		}
	}
	if sameFingerprints(proof.Files, current) {
		record.Status, record.Reason = "unchanged", "declared file bytes and permissions match the successful command snapshot"
		return record
	}
	record.Status, record.Reason = "changed", "one or more declared file snapshots differ"
	for i, file := range proof.Files {
		if file != current[i] {
			record.Files = append(record.Files, strings.Clone(file.Path))
		}
	}
	return record
}
