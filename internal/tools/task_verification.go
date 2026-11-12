package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/andrepato/harness/internal/tool"
	"mvdan.cc/sh/v3/syntax"
)

const maxVerificationFiles = 16
const maxVerificationFileBytes = 32 << 20
const maxVerificationTotalBytes = 64 << 20
const maxVerificationCommandBytes = 4096
const maxVerificationDirectories = 8
const maxVerificationDirectoryFiles = 512
const defaultVerificationDirectoryFiles = 128
const maxVerificationDirectoryBytes = 64 << 20

var errVerificationBounds = errors.New("verification directory exceeds limits")

// VerificationFile binds one permitted project file to its content at command execution.
type VerificationFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

// VerificationDirectory binds a tree's regular files and directory membership.
type VerificationDirectory struct {
	Path           string                     `json:"path"`
	Mode           uint32                     `json:"mode"`
	Files          []VerificationFile         `json:"files"`
	Subdirectories []VerificationSubdirectory `json:"subdirectories"`
}

// VerificationSubdirectory records the presence and permissions of a nested directory.
type VerificationSubdirectory struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

// VerificationEvidence records a successful Bash command with unchanged declared inputs.
// It does not claim the command was a meaningful test of those files.
type VerificationEvidence struct {
	CallID             string                  `json:"callId"`
	Command            string                  `json:"command"`
	Discovery          string                  `json:"discovery,omitempty"`
	Env                map[string]string       `json:"env,omitempty"`
	Files              []VerificationFile      `json:"files"`
	MissingFiles       []string                `json:"missingFiles,omitempty"`
	Directories        []VerificationDirectory `json:"directories,omitempty"`
	DirectoryFileLimit int                     `json:"directoryFileLimit,omitempty"`
	DirectoryByteLimit int64                   `json:"directoryByteLimit,omitempty"`
}

// VerificationAttempt identifies a Bash result, including failed or unbound
// runs, so a later attempt supersedes older evidence for the same command.
type VerificationAttempt struct {
	CallID               string `json:"callId"`
	Command              string `json:"command"`
	DiscoveryUnsupported string `json:"discoveryUnsupported,omitempty"`
	// ExitStatusUnreliable names the shell construct that lets the command
	// exit 0 while its check failed; no evidence is recorded for it.
	ExitStatusUnreliable string `json:"exitStatusUnreliable,omitempty"`
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
	properties["verificationFiles"] = json.RawMessage(`{"type":"array","maxItems":16,"items":{"type":"string"},"description":"Project files to fingerprint before and after this command. Evidence is recorded only when the command succeeds and files remain unchanged. The command must be one command or an && chain: a pipeline, ||, ; or & can exit 0 after a failed check, so it records no evidence."}`)
	properties["verificationDiscover"] = json.RawMessage(`{"type":"string","enum":["go"],"description":"Opt in to a partial bounded input snapshot for a literal go test ./path, go test ./path/..., or go test ./... command. Includes in-module and go.work module imports (every file in each scanned directory, regardless of build constraints), local go.mod/go.work replacement targets, Go module/workspace metadata, go.sum, and the GOFLAGS, GOOS, GOARCH, CGO_ENABLED, GOEXPERIMENT, GOTOOLCHAIN and GOWORK environment values. Shell expansion, flags and compound commands are unsupported; the module cache and the go binary are not read; unchanged evidence never means safe to skip verification. Unsupported or unavailable discovery records no evidence."}`)
	properties["verificationDirectories"] = json.RawMessage(`{"type":"array","maxItems":8,"items":{"type":"string"},"description":"Project directories to fingerprint recursively, including regular file contents, permissions and membership. .git is excluded. Evidence requires a successful command and unchanged inputs."}`)
	properties["verificationDirectoryFileLimit"] = json.RawMessage(`{"type":"integer","minimum":1,"maximum":512,"description":"Maximum regular files across all declared files and directories (default 128, hard ceiling 512). Tree entries, including subdirectories, also have a shared hard ceiling of 512. Above limit, evidence is unavailable."}`)
	properties["verificationDirectoryByteLimit"] = json.RawMessage(`{"type":"integer","minimum":1,"maximum":67108864,"description":"Maximum total bytes across all declared files and directories (default and hard ceiling 67108864). Above limit, evidence is unavailable."}`)
	schema["properties"], _ = json.Marshal(properties)
	wrapped.Parameters, _ = json.Marshal(schema)
	wrapped.Execute = func(ctx context.Context, raw json.RawMessage, update tool.Update, inv tool.Invocation) (tool.Result, error) {
		var args struct {
			Command                 string   `json:"command"`
			VerificationDiscover    string   `json:"verificationDiscover"`
			VerificationFiles       []string `json:"verificationFiles"`
			VerificationDirectories []string `json:"verificationDirectories"`
			DirectoryFileLimit      int      `json:"verificationDirectoryFileLimit"`
			DirectoryByteLimit      int64    `json:"verificationDirectoryByteLimit"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return base.Execute(ctx, raw, update, inv)
		}
		requestedFiles := append([]string(nil), args.VerificationFiles...)
		requestedDirs := append([]string(nil), args.VerificationDirectories...)
		var inferred discoveredVerificationInputs
		var discoveryReason string
		discoveryValid := args.VerificationDiscover == ""
		if args.VerificationDiscover == "go" {
			var discoverErr error
			inferred, discoverErr = discoverGoVerificationInputs(inv.Cwd, args.Command, allowRead)
			discoveryValid = discoverErr == nil
			if discoverErr != nil {
				discoveryReason = discoverErr.Error()
			}
			if discoveryValid {
				requestedFiles = appendUniqueVerificationPaths(requestedFiles, inferred.files)
				requestedDirs = appendUniqueVerificationPaths(requestedDirs, inferred.directories)
			}
		}
		var before []VerificationFile
		var beforeDirs []VerificationDirectory
		if discoveryValid && len(requestedFiles) > 0 && len(requestedFiles)+len(inferred.missing) <= maxVerificationFiles {
			var err error
			before, err = fingerprintFiles(ctx, inv.Cwd, requestedFiles, allowRead)
			if err != nil {
				before = nil
				if args.VerificationDiscover == "go" {
					discoveryReason = err.Error()
				}
			}
		}
		fileLimit := effectiveDirectoryFileLimit(args.DirectoryFileLimit)
		byteLimit := effectiveDirectoryByteLimit(args.DirectoryByteLimit)
		validLimits := fileLimit > 0 && fileLimit <= maxVerificationDirectoryFiles && byteLimit > 0 && byteLimit <= maxVerificationDirectoryBytes
		if len(before) > 0 && len(requestedDirs) > 0 {
			fileLimit -= len(before)
			var used int64
			for _, file := range before {
				info, statErr := os.Lstat(filepath.Join(inv.Cwd, file.Path))
				if statErr != nil || !info.Mode().IsRegular() || info.Size() < 0 {
					fileLimit = 0
					break
				}
				used += info.Size()
			}
			byteLimit -= used
		}
		if discoveryValid && len(requestedDirs) > 0 && len(requestedDirs) <= maxVerificationDirectories && (len(requestedFiles) == 0 || len(before) > 0) && validLimits && fileLimit > 0 && byteLimit > 0 {
			var err error
			beforeDirs, err = fingerprintDirectories(ctx, inv.Cwd, requestedDirs, allowRead, fileLimit, byteLimit, args.VerificationDiscover == "go")
			if err != nil && args.VerificationDiscover == "go" {
				discoveryReason = err.Error()
			}
		}
		exitMasked := verificationExitMasked(args.Command)
		result, err := base.Execute(ctx, raw, update, inv)
		if err == nil && args.Command != "" && len(args.Command) <= maxVerificationCommandBytes {
			var details map[string]json.RawMessage
			if len(result.Details) == 0 || json.Unmarshal(result.Details, &details) == nil {
				if details == nil {
					details = make(map[string]json.RawMessage)
				}
				attempt, marshalErr := json.Marshal(VerificationAttempt{CallID: inv.ToolCallID, Command: args.Command, DiscoveryUnsupported: discoveryReason, ExitStatusUnreliable: exitMasked})
				if marshalErr == nil {
					details["taskVerificationAttempt"] = attempt
					result.Details, _ = json.Marshal(details)
				}
			}
		}
		if err != nil || result.IsError || exitMasked != "" || !discoveryValid || len(before)+len(beforeDirs) == 0 || len(args.Command) > maxVerificationCommandBytes || (len(requestedFiles) > 0 && len(before) == 0) || (len(requestedDirs) > 0 && len(beforeDirs) == 0) || !verificationMissingFiles(inv.Cwd, inferred.missing, allowRead) {
			return result, err
		}
		if args.VerificationDiscover == "go" {
			afterInference, discoverErr := discoverGoVerificationInputs(inv.Cwd, args.Command, allowRead)
			if discoverErr != nil || !slices.Equal(inferred.files, afterInference.files) || !slices.Equal(inferred.missing, afterInference.missing) || !slices.Equal(inferred.directories, afterInference.directories) || !maps.Equal(inferred.env, afterInference.env) {
				return result, nil
			}
		}
		var after []VerificationFile
		var hashErr error
		if len(before) > 0 {
			after, hashErr = fingerprintFiles(ctx, inv.Cwd, requestedFiles, allowRead)
		}
		if hashErr != nil || !sameFingerprints(before, after) {
			return result, nil
		}
		var afterDirs []VerificationDirectory
		if len(beforeDirs) > 0 {
			afterDirs, hashErr = fingerprintDirectories(ctx, inv.Cwd, requestedDirs, allowRead, fileLimit, byteLimit, args.VerificationDiscover == "go")
		}
		if hashErr != nil || !sameDirectories(beforeDirs, afterDirs) {
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
		provenance := ""
		if args.VerificationDiscover == "go" {
			provenance = goPartialDiscovery
		}
		proof, marshalErr := json.Marshal(VerificationEvidence{CallID: inv.ToolCallID, Command: args.Command, Discovery: provenance, Env: inferred.env, Files: before, MissingFiles: inferred.missing, Directories: beforeDirs, DirectoryFileLimit: fileLimit, DirectoryByteLimit: byteLimit})
		if marshalErr != nil {
			return result, nil
		}
		details["taskVerification"] = proof
		result.Details, _ = json.Marshal(details)
		return result, nil
	}
	return &wrapped
}

// verificationExitMasked names the shell construct that can make command exit
// 0 although its check failed, or returns "" when the exit status is the
// check's own. kiln's shell does not set pipefail, so a pipeline reports its
// last stage (`go test ./... | tail` passes when tests fail); `||`, a later
// statement, `&` and `!` likewise detach the status from the check. Only
// simple commands, alone or joined by &&, qualify. The text of a nested
// `sh -c` string and command substitutions are not inspected.
func verificationExitMasked(command string) string {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return "unparseable shell syntax"
	}
	if len(file.Stmts) != 1 {
		return "more than one statement"
	}
	var check func(*syntax.Stmt) string
	check = func(s *syntax.Stmt) string {
		if s.Background || s.Coprocess || s.Disown {
			return "background command"
		}
		if s.Negated {
			return "negated command"
		}
		switch c := s.Cmd.(type) {
		case *syntax.CallExpr:
			return ""
		case *syntax.BinaryCmd:
			switch c.Op {
			case syntax.AndStmt:
				if reason := check(c.X); reason != "" {
					return reason
				}
				return check(c.Y)
			case syntax.OrStmt:
				return "|| fallback"
			default:
				return "pipeline without pipefail"
			}
		default:
			return "compound command"
		}
	}
	return check(file.Stmts[0])
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
	if json.Unmarshal(details, &envelope) != nil || strings.TrimSpace(envelope.Evidence.Command) == "" || len(envelope.Evidence.Command) > maxVerificationCommandBytes || envelope.Evidence.CallID == "" || len(envelope.Evidence.Files)+len(envelope.Evidence.Directories) == 0 || len(envelope.Evidence.Files)+len(envelope.Evidence.MissingFiles) > maxVerificationFiles || len(envelope.Evidence.Directories) > maxVerificationDirectories || envelope.Evidence.Discovery != "" && !isGoPartialDiscovery(envelope.Evidence.Discovery) {
		return VerificationEvidence{}, false
	}
	if !validVerificationDirectories(envelope.Evidence) {
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
	seenMissing := make(map[string]bool)
	if envelope.Evidence.Discovery == "" && len(envelope.Evidence.MissingFiles) > 0 {
		return VerificationEvidence{}, false
	}
	for _, path := range envelope.Evidence.MissingFiles {
		if !validRelativeVerificationPath(path) || seenMissing[path] {
			return VerificationEvidence{}, false
		}
		seenMissing[path] = true
		for _, file := range envelope.Evidence.Files {
			if file.Path == path {
				return VerificationEvidence{}, false
			}
		}
	}
	if isGoPartialDiscovery(envelope.Evidence.Discovery) && !validGoDiscoveryEvidence(envelope.Evidence) {
		return VerificationEvidence{}, false
	}
	return envelope.Evidence, true
}

// VerificationFresh checks that every declared input still matches its execution snapshot.
func VerificationFresh(cwd string, proof VerificationEvidence, allowRead func(string) bool) bool {
	return VerificationFreshContext(context.Background(), cwd, proof, allowRead)
}

// VerificationFreshContext rechecks a bounded snapshot with cancellation.
func VerificationFreshContext(ctx context.Context, cwd string, proof VerificationEvidence, allowRead func(string) bool) bool {
	if len(proof.Files)+len(proof.Directories) == 0 || len(proof.Files)+len(proof.MissingFiles) > maxVerificationFiles || len(proof.Directories) > maxVerificationDirectories || !validVerificationDirectories(proof) || allowRead == nil || !verificationMissingFiles(cwd, proof.MissingFiles, allowRead) {
		return false
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
	if err != nil || !sameFingerprints(proof.Files, current) {
		return false
	}
	if len(proof.Directories) == 0 {
		return true
	}
	currentDirs, err := fingerprintDirectories(ctx, cwd, directoryPaths(proof.Directories), allowRead, proof.DirectoryFileLimit, proof.DirectoryByteLimit, isGoPartialDiscovery(proof.Discovery))
	return err == nil && sameDirectories(proof.Directories, currentDirs) && len(changedGoVerificationEnv(proof)) == 0
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

func fingerprintFiles(ctx context.Context, cwd string, paths []string, allowRead func(string) bool, external ...bool) ([]VerificationFile, error) {
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
	allowExternal := len(external) > 0 && external[0]
	for _, given := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		outside := validGoExternalVerificationPath(given)
		if !validRelativeVerificationPath(given) && !(allowExternal && outside) {
			return nil, fmt.Errorf("verification path must be relative")
		}
		clean := filepath.Clean(given)
		if clean == "." || outside && !allowExternal || seen[clean] {
			return nil, fmt.Errorf("invalid verification path")
		}
		seen[clean] = true
		if !allowRead(clean) {
			return nil, fmt.Errorf("verification file is not readable")
		}
		full := verificationResolvedPath(root, clean)
		stop := root
		if outside {
			stop = verificationCommonAncestor(root, full)
		}
		if err := verificationWalkAncestors(ctx, full, stop, filepath.Dir, func(cursor string) error {
			info, err := os.Lstat(cursor)
			if err != nil || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("verification path contains a symlink or is missing")
			}
			if cursor == full && !info.Mode().IsRegular() {
				return fmt.Errorf("verification file is not regular")
			}
			return nil
		}); err != nil {
			return nil, err
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

func effectiveDirectoryFileLimit(n int) int {
	if n == 0 {
		return defaultVerificationDirectoryFiles
	}
	return n
}

func effectiveDirectoryByteLimit(n int64) int64 {
	if n == 0 {
		return maxVerificationDirectoryBytes
	}
	return n
}

func validRelativeVerificationPath(path string) bool {
	if path == "" || len(path) > 4096 || filepath.IsAbs(path) || filepath.Clean(path) != path || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return false
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".git" {
			return false
		}
	}
	return true
}

func validVerificationDirectories(proof VerificationEvidence) bool {
	if len(proof.Directories) == 0 {
		return true
	}
	if proof.DirectoryFileLimit < 1 || proof.DirectoryFileLimit > maxVerificationDirectoryFiles || proof.DirectoryByteLimit < 1 || proof.DirectoryByteLimit > maxVerificationDirectoryBytes {
		return false
	}
	files := 0
	entries := 0
	seen := make(map[string]bool)
	for _, dir := range proof.Directories {
		if !validVerificationDirectoryPath(dir.Path, proof.Discovery) || seen[dir.Path] || dir.Mode > 0777 {
			return false
		}
		if len(dir.Subdirectories) > maxVerificationDirectoryFiles {
			return false
		}
		seen[dir.Path] = true
		last := ""
		for _, sub := range dir.Subdirectories {
			entries++
			if !validVerificationDirectoryChildPath(sub.Path, dir.Path, proof.Discovery) || sub.Mode > 0777 || last != "" && sub.Path <= last {
				return false
			}
			last = sub.Path
		}
		last = ""
		for _, file := range dir.Files {
			entries++
			if !validVerificationDirectoryChildPath(file.Path, dir.Path, proof.Discovery) || file.Mode > 0777 || len(file.SHA256) != 64 || last != "" && file.Path <= last {
				return false
			}
			if _, err := hex.DecodeString(file.SHA256); err != nil {
				return false
			}
			last = file.Path
			files++
		}
	}
	return files <= proof.DirectoryFileLimit && entries <= maxVerificationDirectoryFiles
}

func validVerificationDirectoryPath(path, discovery string) bool {
	return path == "." || validRelativeVerificationPath(path) || isGoPartialDiscovery(discovery) && validGoExternalVerificationPath(path)
}

func validVerificationDirectoryChildPath(path, parent, discovery string) bool {
	if parent == "." {
		return validRelativeVerificationPath(path)
	}
	if !strings.HasPrefix(path, parent+string(filepath.Separator)) {
		return false
	}
	return validRelativeVerificationPath(path) || isGoPartialDiscovery(discovery) && validGoExternalVerificationPath(path)
}

func verificationCommonAncestor(root, full string) string {
	for {
		rel, err := filepath.Rel(root, full)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			return root
		}
		root = parent
	}
}

// verificationWalkAncestors stops with unavailable evidence if the target
// reaches its own volume root before the source's common ancestor. This can
// happen for absolute local replacements on another Windows drive or share.
func verificationWalkAncestors(ctx context.Context, full, stop string, parent func(string) string, inspect func(string) error) error {
	for cursor := full; cursor != stop; {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := inspect(cursor); err != nil {
			return err
		}
		next := parent(cursor)
		if next == cursor {
			return fmt.Errorf("verification ancestor unavailable across volumes")
		}
		cursor = next
	}
	return nil
}

func verificationResolvedPath(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func directoryPaths(dirs []VerificationDirectory) []string {
	out := make([]string, len(dirs))
	for i, dir := range dirs {
		out[i] = dir.Path
	}
	return out
}

func sameDirectories(a, b []VerificationDirectory) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Mode != b[i].Mode || !slices.Equal(a[i].Files, b[i].Files) || !slices.Equal(a[i].Subdirectories, b[i].Subdirectories) {
			return false
		}
	}
	return true
}

func fingerprintDirectories(ctx context.Context, cwd string, paths []string, allowRead func(string) bool, fileLimit int, byteLimit int64, external ...bool) ([]VerificationDirectory, error) {
	fileLimit = effectiveDirectoryFileLimit(fileLimit)
	byteLimit = effectiveDirectoryByteLimit(byteLimit)
	if len(paths) == 0 || len(paths) > maxVerificationDirectories || fileLimit < 1 || fileLimit > maxVerificationDirectoryFiles || byteLimit < 1 || byteLimit > maxVerificationDirectoryBytes || cwd == "" || allowRead == nil {
		return nil, fmt.Errorf("invalid verification directory set")
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]VerificationDirectory, 0, len(paths))
	var totalBytes int64
	fileCount := 0
	entryCount := 0
	allowExternal := len(external) > 0 && external[0]
	discovery := ""
	if allowExternal {
		discovery = goPartialDiscovery
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		outside := validGoExternalVerificationPath(path)
		if !validVerificationDirectoryPath(path, discovery) || seen[path] || !allowRead(path) {
			return nil, fmt.Errorf("verification directory unavailable")
		}
		seen[path] = true
		full := verificationResolvedPath(root, path)
		stop := root
		if outside {
			stop = verificationCommonAncestor(root, full)
		}
		if err := verificationWalkAncestors(ctx, full, stop, filepath.Dir, func(cursor string) error {
			info, err := os.Lstat(cursor)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("verification directory unavailable")
			}
			return nil
		}); err != nil {
			return nil, err
		}
		rootInfo, err := os.Lstat(full)
		if err != nil {
			return nil, err
		}
		dir := VerificationDirectory{Path: path, Mode: uint32(rootInfo.Mode().Perm()), Files: []VerificationFile{}, Subdirectories: []VerificationSubdirectory{}}
		var walk func(string) error
		walk = func(parent string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			folder, err := os.Open(parent)
			if err != nil {
				return err
			}
			remaining := maxVerificationDirectoryFiles - entryCount
			if remaining < 0 {
				_ = folder.Close()
				return errVerificationBounds
			}
			names, readErr := folder.Readdirnames(remaining + 1)
			closeErr := folder.Close()
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			if len(names) > remaining {
				return errVerificationBounds
			}
			entryCount += len(names)
			slices.Sort(names)
			for _, name := range names {
				if err := ctx.Err(); err != nil {
					return err
				}
				if name == ".git" {
					continue
				}
				walkPath := filepath.Join(parent, name)
				rel, err := filepath.Rel(root, walkPath)
				if filepath.IsAbs(path) {
					rel, err = walkPath, nil
				}
				if err != nil || !validVerificationDirectoryChildPath(rel, path, discovery) || !allowRead(rel) {
					return fmt.Errorf("verification path unavailable")
				}
				info, err := os.Lstat(walkPath)
				if err != nil {
					return err
				}
				if info.IsDir() {
					dir.Subdirectories = append(dir.Subdirectories, VerificationSubdirectory{Path: rel, Mode: uint32(info.Mode().Perm())})
					if err := walk(walkPath); err != nil {
						return err
					}
					continue
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("verification nonregular file")
				}
				fileCount++
				if fileCount > fileLimit || info.Size() < 0 || info.Size() > maxVerificationFileBytes || totalBytes+info.Size() > byteLimit {
					return errVerificationBounds
				}
				fingerprints, err := fingerprintFiles(ctx, cwd, []string{rel}, allowRead, allowExternal)
				if err != nil {
					return err
				}
				updated, err := os.Lstat(walkPath)
				if err != nil || !updated.Mode().IsRegular() || updated.Size() != info.Size() {
					return fmt.Errorf("verification file changed during fingerprint")
				}
				totalBytes += info.Size()
				dir.Files = append(dir.Files, fingerprints[0])
			}
			return nil
		}
		err = walk(full)
		if err != nil {
			return nil, err
		}
		// Bound each directory read, then sort the bounded snapshot deterministically.
		slices.SortFunc(dir.Files, func(a, b VerificationFile) int { return strings.Compare(a.Path, b.Path) })
		slices.SortFunc(dir.Subdirectories, func(a, b VerificationSubdirectory) int { return strings.Compare(a.Path, b.Path) })
		out = append(out, dir)
	}
	return out, nil
}
