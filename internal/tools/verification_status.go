package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/andrepato/harness/internal/tool"
)

const maxVerificationRecords = 16
const maxVerificationStatusResults = 8

type verificationStatusArgs struct {
	Command     string   `json:"command"`
	Files       []string `json:"files"`
	Directories []string `json:"directories"`
	Mode        string   `json:"mode"`
}

type verificationStatusRecord struct {
	CallID      string   `json:"callId"`
	Discovery   string   `json:"discovery,omitempty"`
	Command     string   `json:"command,omitempty"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason"`
	Files       []string `json:"files,omitempty"`
	Directories []string `json:"directories,omitempty"`
	Action      string   `json:"action,omitempty"`
}

// VerificationStatusTool reports current file state for successful Bash
// evidence supplied by the caller's committed, selected branch. It cannot
// execute checks or infer whether a command tested the right behavior.
func VerificationStatusTool(cwd string, allowRead func(string) bool, proofs func() []VerificationEvidence, tokens int) *tool.Tool {
	return &tool.Tool{
		Name: "verification_status", Label: "Check verification evidence",
		Description: "Inspect recent successful Bash command evidence on this branch. Recheck declared file and directory dependencies; unchanged means only those declared inputs match, never that skipping verification is safe. mode plan recommends exact recorded commands for changed inputs and flags unavailable evidence for review. This tool never runs commands.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Exact command to filter"},"files":{"type":"array","maxItems":16,"items":{"type":"string"},"description":"Show evidence associated with these project files"},"directories":{"type":"array","maxItems":8,"items":{"type":"string"},"description":"Show evidence associated with these project directories"},"mode":{"type":"string","enum":["status","plan"],"description":"status (default) or conservative rerun plan"}}}`),
		Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var args verificationStatusArgs
			if err := json.Unmarshal(raw, &args); err != nil || len(args.Files) > maxVerificationFiles || len(args.Directories) > maxVerificationDirectories || args.Mode != "" && args.Mode != "status" && args.Mode != "plan" {
				return tool.Errorf("verification_status: invalid arguments"), nil
			}
			// A denied filter must not become an existence or evidence oracle.
			for _, path := range append(args.Files, args.Directories...) {
				if !validRelativeVerificationPath(path) || allowRead == nil || !allowRead(path) {
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
			matched := 0
			outputFull := false
			for _, proof := range all {
				if err := ctx.Err(); err != nil {
					return tool.Errorf("verification_status: cancelled"), nil
				}
				if args.Command != "" && args.Command != proof.Command {
					continue
				}
				if len(args.Files) > 0 && !verificationFileIntersection(proof.Files, proof.MissingFiles, proof.Directories, args.Files) {
					continue
				}
				if len(args.Directories) > 0 && !verificationDirectoryIntersection(proof.Directories, args.Directories) {
					continue
				}
				matched++
				record := checkVerificationProof(ctx, cwd, proof, allowRead)
				if args.Mode == "plan" {
					if record.Status == "unchanged" {
						continue
					}
					if record.Command != "" && record.Status != "unchanged" {
						record.Command = proof.Command
						record.Action = "rerun"
					} else {
						record.Action = "review"
					}
				}
				used++
				if outputFull || len(records) >= maxVerificationStatusResults {
					continue
				}
				candidate := append(records, record)
				encoded, err := json.Marshal(struct {
					Scope   string                     `json:"scope"`
					Records []verificationStatusRecord `json:"records"`
				}{Scope: verificationScope(candidate), Records: candidate})
				if err != nil {
					return tool.Result{}, err
				}
				if len(encoded)+32 > limit {
					outputFull = true
					continue
				}
				records = candidate
			}
			if matched == 0 {
				return tool.Text("No matching saved verification evidence on this branch."), nil
			}
			output, err := json.Marshal(struct {
				Scope   string                     `json:"scope"`
				Records []verificationStatusRecord `json:"records"`
				Omitted int                        `json:"omitted"`
			}{Scope: verificationScope(records), Records: records, Omitted: used - len(records)})
			if err != nil {
				return tool.Result{}, err
			}
			return tool.Text(string(output)), nil
		},
	}
}

func verificationFileIntersection(files []VerificationFile, missing []string, dirs []VerificationDirectory, requested []string) bool {
	for _, file := range files {
		for _, path := range requested {
			if file.Path == path {
				return true
			}
		}
	}
	for _, path := range missing {
		for _, target := range requested {
			if path == target {
				return true
			}
		}
	}
	for _, dir := range dirs {
		for _, path := range requested {
			if strings.HasPrefix(path, dir.Path+string(filepath.Separator)) {
				return true
			}
		}
		for _, file := range dir.Files {
			for _, path := range requested {
				if file.Path == path {
					return true
				}
			}
		}
	}
	return false
}

func verificationDirectoryIntersection(dirs []VerificationDirectory, requested []string) bool {
	for _, dir := range dirs {
		for _, path := range requested {
			if dir.Path == path {
				return true
			}
		}
	}
	return false
}

func checkVerificationProof(ctx context.Context, cwd string, proof VerificationEvidence, allowRead func(string) bool) verificationStatusRecord {
	record := verificationStatusRecord{CallID: proof.CallID, Discovery: proof.Discovery}
	if len(proof.Files)+len(proof.Directories) == 0 || len(proof.Files)+len(proof.MissingFiles) > maxVerificationFiles || len(proof.Directories) > maxVerificationDirectories || !validVerificationDirectories(proof) || allowRead == nil {
		record.Status, record.Reason = "unavailable", "saved dependency set cannot be checked"
		return record
	}
	for _, file := range proof.Files {
		if !allowRead(file.Path) {
			record.Status, record.Reason = "unavailable", "one or more dependencies cannot be read"
			return record
		}
	}
	for _, path := range proof.MissingFiles {
		if !validRelativeVerificationPath(path) || !allowRead(path) {
			record.Status, record.Reason = "unavailable", "one or more dependencies cannot be read"
			return record
		}
	}
	for _, dir := range proof.Directories {
		if !allowRead(dir.Path) {
			record.Status, record.Reason = "unavailable", "one or more dependencies cannot be read"
			return record
		}
		for _, sub := range dir.Subdirectories {
			if !allowRead(sub.Path) {
				record.Status, record.Reason = "unavailable", "one or more dependencies cannot be read"
				return record
			}
		}
		for _, file := range dir.Files {
			if !allowRead(file.Path) {
				record.Status, record.Reason = "unavailable", "one or more dependencies cannot be read"
				return record
			}
		}
	}
	paths := make([]string, 0, len(proof.Files))
	for _, file := range proof.Files {
		paths = append(paths, file.Path)
	}
	var current []VerificationFile
	var err error
	if len(paths) > 0 {
		current, err = fingerprintFiles(ctx, cwd, paths, allowRead)
	}
	if err != nil {
		record.Status, record.Reason = "unavailable", "one or more dependencies cannot be checked"
		return record
	}
	for _, path := range proof.MissingFiles {
		_, statErr := os.Lstat(filepath.Join(cwd, path))
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			record.Status, record.Reason = "unavailable", "one or more dependencies cannot be checked"
			return record
		}
		record.Command = proof.Command
		record.Status, record.Reason = "changed", "a previously absent metadata file now exists"
		record.Files = append(record.Files, path)
		return record
	}
	var currentDirs []VerificationDirectory
	remainingDirs := make([]string, 0, len(proof.Directories))
	for _, dir := range proof.Directories {
		_, statErr := os.Lstat(filepath.Join(cwd, dir.Path))
		if os.IsNotExist(statErr) {
			record.Directories = append(record.Directories, dir.Path)
		} else {
			remainingDirs = append(remainingDirs, dir.Path)
		}
	}
	if len(remainingDirs) > 0 {
		currentDirs, err = fingerprintDirectories(ctx, cwd, remainingDirs, allowRead, proof.DirectoryFileLimit, proof.DirectoryByteLimit)
	}
	if err != nil {
		record.Status, record.Reason = "unavailable", "one or more dependencies cannot be checked"
		return record
	}
	if len(record.Directories) > 0 {
		record.Command = proof.Command
		record.Status, record.Reason = "changed", "a declared directory was removed"
		return record
	}
	record.Command = proof.Command
	if len(record.Command) > 256 {
		record.Command = record.Command[:256]
		for !utf8.ValidString(record.Command) {
			record.Command = record.Command[:len(record.Command)-1]
		}
	}
	if sameFingerprints(proof.Files, current) && sameDirectories(proof.Directories, currentDirs) {
		record.Status, record.Reason = "unchanged", "declared input bytes, permissions and membership match the successful command snapshot"
		return record
	}
	record.Status, record.Reason = "changed", "one or more declared input snapshots differ"
	for i, file := range proof.Files {
		if file != current[i] {
			record.Files = append(record.Files, strings.Clone(file.Path))
		}
	}
	for i, dir := range proof.Directories {
		if !sameDirectories([]VerificationDirectory{dir}, []VerificationDirectory{currentDirs[i]}) {
			record.Directories = append(record.Directories, dir.Path)
		}
	}
	return record
}

// Keep ordinary evidence concise; inferred inputs need the additional scope limit.
func verificationScope(records []verificationStatusRecord) string {
	for _, record := range records {
		if record.Discovery != "" {
			return "recorded inputs only; partial Go discovery omits imports, replacements, toolchain and environment; dependency graph incomplete; no task correctness or safe-to-skip claim"
		}
	}
	return "declared inputs only; no task correctness or safe-to-skip claim"
}
