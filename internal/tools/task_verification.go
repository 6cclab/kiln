package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrepato/harness/internal/tool"
)

const maxVerificationFiles = 16
const maxVerificationFileBytes = 32 << 20
const maxVerificationTotalBytes = 64 << 20
const maxVerificationCommandBytes = 4096

// VerificationFile binds one permitted project file to its content at command execution.
type VerificationFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

// VerificationEvidence records a successful Bash command with unchanged file inputs.
// It does not claim the command was a meaningful test of those files.
type VerificationEvidence struct {
	CallID  string             `json:"callId"`
	Command string             `json:"command"`
	Files   []VerificationFile `json:"files"`
}

// VerificationAttempt identifies a Bash result, including failed or unbound
// runs, so a later attempt supersedes older evidence for the same command.
type VerificationAttempt struct {
	CallID  string `json:"callId"`
	Command string `json:"command"`
}

// VerificationBashTool wraps a Bash tool without changing its permission gate.
// allowRead must check the effective Read permission for each file. The wrapper
// itself never runs a command or returns file contents to the model.
func VerificationBashTool(base *tool.Tool, allowRead func(path string) bool) *tool.Tool {
	wrapped := *base
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(base.Parameters, &schema); err != nil {
		panic("invalid built-in bash schema: " + err.Error())
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(schema["properties"], &properties); err != nil {
		panic("invalid built-in bash properties: " + err.Error())
	}
	properties["verificationFiles"] = json.RawMessage(`{"type":"array","maxItems":16,"items":{"type":"string"},"description":"Project files to fingerprint before and after this command. Evidence is recorded only when the command succeeds and files remain unchanged."}`)
	schema["properties"], _ = json.Marshal(properties)
	wrapped.Parameters, _ = json.Marshal(schema)
	wrapped.Execute = func(ctx context.Context, raw json.RawMessage, update tool.Update, inv tool.Invocation) (tool.Result, error) {
		var args struct {
			Command           string   `json:"command"`
			VerificationFiles []string `json:"verificationFiles"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return base.Execute(ctx, raw, update, inv)
		}
		var before []VerificationFile
		if len(args.VerificationFiles) > 0 && len(args.VerificationFiles) <= maxVerificationFiles {
			var err error
			before, err = fingerprintFiles(ctx, inv.Cwd, args.VerificationFiles, allowRead)
			if err != nil {
				before = nil
			}
		}
		result, err := base.Execute(ctx, raw, update, inv)
		if err == nil && args.Command != "" && len(args.Command) <= maxVerificationCommandBytes {
			var details map[string]json.RawMessage
			if len(result.Details) == 0 || json.Unmarshal(result.Details, &details) == nil {
				if details == nil {
					details = make(map[string]json.RawMessage)
				}
				attempt, marshalErr := json.Marshal(VerificationAttempt{CallID: inv.ToolCallID, Command: args.Command})
				if marshalErr == nil {
					details["taskVerificationAttempt"] = attempt
					result.Details, _ = json.Marshal(details)
				}
			}
		}
		if err != nil || result.IsError || len(before) == 0 || len(args.Command) > maxVerificationCommandBytes {
			return result, err
		}
		after, hashErr := fingerprintFiles(ctx, inv.Cwd, args.VerificationFiles, allowRead)
		if hashErr != nil || !sameFingerprints(before, after) {
			return result, nil
		}
		var details map[string]json.RawMessage
		if len(result.Details) != 0 {
			if json.Unmarshal(result.Details, &details) != nil {
				return result, nil
			}
		}
		if details == nil {
			details = make(map[string]json.RawMessage)
		}
		proof, marshalErr := json.Marshal(VerificationEvidence{CallID: inv.ToolCallID, Command: args.Command, Files: before})
		if marshalErr != nil {
			return result, nil
		}
		details["taskVerification"] = proof
		result.Details, _ = json.Marshal(details)
		return result, nil
	}
	return &wrapped
}

// VerificationAttemptFromResult reads the wrapper's command identity.
func VerificationAttemptFromResult(details json.RawMessage) (VerificationAttempt, bool) {
	var envelope struct {
		Attempt VerificationAttempt `json:"taskVerificationAttempt"`
	}
	if json.Unmarshal(details, &envelope) != nil || envelope.Attempt.CallID == "" || strings.TrimSpace(envelope.Attempt.Command) == "" || len(envelope.Attempt.Command) > maxVerificationCommandBytes {
		return VerificationAttempt{}, false
	}
	return envelope.Attempt, true
}

// VerificationFromResult extracts trusted wrapper evidence, never model text.
func VerificationFromResult(details json.RawMessage) (VerificationEvidence, bool) {
	var envelope struct {
		Evidence VerificationEvidence `json:"taskVerification"`
	}
	if json.Unmarshal(details, &envelope) != nil || strings.TrimSpace(envelope.Evidence.Command) == "" || len(envelope.Evidence.Command) > maxVerificationCommandBytes || envelope.Evidence.CallID == "" || len(envelope.Evidence.Files) == 0 || len(envelope.Evidence.Files) > maxVerificationFiles {
		return VerificationEvidence{}, false
	}
	for _, file := range envelope.Evidence.Files {
		if file.Path == "" || len(file.Path) > 4096 || filepath.IsAbs(file.Path) || filepath.Clean(file.Path) != file.Path || file.Path == ".." || strings.HasPrefix(file.Path, ".."+string(filepath.Separator)) || len(file.SHA256) != 64 || file.Mode > 0777 {
			return VerificationEvidence{}, false
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return VerificationEvidence{}, false
		}
	}
	return envelope.Evidence, true
}

// VerificationFresh checks that every recorded file still matches its execution snapshot.
func VerificationFresh(cwd string, proof VerificationEvidence, allowRead func(string) bool) bool {
	return VerificationFreshContext(context.Background(), cwd, proof, allowRead)
}

// VerificationFreshContext rechecks a bounded snapshot with cancellation.
func VerificationFreshContext(ctx context.Context, cwd string, proof VerificationEvidence, allowRead func(string) bool) bool {
	paths := make([]string, 0, len(proof.Files))
	for _, file := range proof.Files {
		paths = append(paths, file.Path)
	}
	current, err := fingerprintFiles(ctx, cwd, paths, allowRead)
	return err == nil && sameFingerprints(proof.Files, current)
}

func sameFingerprints(a, b []VerificationFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func fingerprintFiles(ctx context.Context, cwd string, paths []string, allowRead func(string) bool) ([]VerificationFile, error) {
	if len(paths) == 0 || len(paths) > maxVerificationFiles || cwd == "" || allowRead == nil {
		return nil, fmt.Errorf("invalid verification file set")
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	out := make([]VerificationFile, 0, len(paths))
	seen := map[string]bool{}
	var totalBytes int64
	for _, given := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if filepath.IsAbs(given) || given == "" || len(given) > 4096 {
			return nil, fmt.Errorf("verification path must be relative")
		}
		clean := filepath.Clean(given)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || seen[clean] {
			return nil, fmt.Errorf("invalid verification path")
		}
		seen[clean] = true
		if !allowRead(clean) {
			return nil, fmt.Errorf("verification file is not readable")
		}
		full := filepath.Join(root, clean)
		for cursor := full; cursor != root; cursor = filepath.Dir(cursor) {
			info, err := os.Lstat(cursor)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("verification path contains a symlink or is missing")
			}
			if cursor == full && !info.Mode().IsRegular() {
				return nil, fmt.Errorf("verification file is not regular")
			}
		}
		file, err := os.Open(full)
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxVerificationFileBytes || totalBytes+info.Size() > maxVerificationTotalBytes {
			if closeErr := file.Close(); closeErr != nil {
				return nil, closeErr
			}
			return nil, fmt.Errorf("verification file is not a bounded regular file")
		}
		h := sha256.New()
		buf := make([]byte, 32*1024)
		var fileBytes int64
		for {
			if err = ctx.Err(); err != nil {
				break
			}
			var n int
			n, err = file.Read(buf)
			if n > 0 {
				fileBytes += int64(n)
				if fileBytes > maxVerificationFileBytes || totalBytes+fileBytes > maxVerificationTotalBytes {
					err = fmt.Errorf("verification file grew beyond limit")
					break
				}
				if _, err = h.Write(buf[:n]); err != nil {
					break
				}
			}
			if n == 0 && err == nil {
				err = io.ErrNoProgress
			}
			if err != nil {
				break
			}
		}
		closeErr := file.Close()
		if err == io.EOF {
			err = nil
		}
		if err != nil || closeErr != nil {
			return nil, fmt.Errorf("read verification file: %v %v", err, closeErr)
		}
		totalBytes += fileBytes
		out = append(out, VerificationFile{Path: clean, SHA256: hex.EncodeToString(h.Sum(nil)), Mode: uint32(info.Mode().Perm())})
	}
	return out, nil
}
