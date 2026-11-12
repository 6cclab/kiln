package harness

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
)

// progressLoopGuard retains only digests from the current operation. It is
// deliberately conservative about tool effects: unknown tools break a read
// sequence, and file cycles are recognized only for small regular local files.
type progressLoopGuard struct {
	limit                 int
	cwd                   string
	maxFileBytes          int64 // file-cycle hash cap; zero means defaultLoopFileBytes
	reads                 [40][sha256.Size]byte
	readNext, readCount   int
	states                [40]fileState
	stateNext, stateCount int
	warned                bool
	warnedSignature       [sha256.Size]byte
}

const (
	defaultLoopFileBytes int64 = 1 << 20
	// MaxLoopFileBytes is the largest cap a setting may request.
	MaxLoopFileBytes int64 = 16 << 20
)

func (g *progressLoopGuard) fileCap() int64 {
	switch {
	case g.maxFileBytes <= 0:
		return defaultLoopFileBytes
	case g.maxFileBytes > MaxLoopFileBytes:
		return MaxLoopFileBytes
	}
	return g.maxFileBytes
}

type fileState struct {
	path   [sha256.Size]byte
	digest [sha256.Size]byte
	valid  bool
	after  bool
}

type progressLoopEvent struct {
	Notice string
	Err    error
}

func (g *progressLoopGuard) enabled() bool { return g.limit >= 2 }

func (g *progressLoopGuard) before(call msg.ToolCall) fileState {
	if !g.enabled() || (call.Name != "edit" && call.Name != "write") {
		return fileState{}
	}
	return g.snapshot(call)
}

func (g *progressLoopGuard) snapshot(call msg.ToolCall) fileState {
	path, ok := call.Arguments["path"].(string)
	if !ok || path == "" {
		return fileState{}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(g.cwd, path)
	}
	path = filepath.Clean(path)
	// Never inspect a path outside the working tree. A symlink is followed
	// only when its final target also stays inside it; the state is keyed by
	// that target so a link and its target are one file. The content is
	// streamed through a hash (nothing retained) up to fileCap bytes.
	rel, err := filepath.Rel(g.cwd, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// A lexically outside path can still be a link back in; it is not
		// tracked, only links that start inside are.
		return fileState{}
	}
	cwdReal, cwdErr := filepath.EvalSymlinks(g.cwd)
	pathReal, pathErr := filepath.EvalSymlinks(path)
	if cwdErr != nil || pathErr != nil {
		return fileState{}
	}
	rel, err = filepath.Rel(cwdReal, pathReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fileState{}
	}
	limit := g.fileCap()
	info, err := os.Lstat(pathReal)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return fileState{}
	}
	f, err := os.Open(pathReal)
	if err != nil {
		return fileState{}
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, limit+1))
	if err != nil || n > limit {
		return fileState{}
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return fileState{path: sha256.Sum256([]byte(pathReal)), digest: digest, valid: true}
}

func (g *progressLoopGuard) observe(call msg.ToolCall, result msg.ToolResultMessage, before fileState) progressLoopEvent {
	if !g.enabled() {
		return progressLoopEvent{}
	}
	if result.IsError {
		g.readCount = 0
		return progressLoopEvent{}
	}
	if call.Name == "edit" || call.Name == "write" {
		g.readCount = 0
		after := g.snapshot(call)
		if !before.valid || !after.valid || before.path != after.path || before.digest == after.digest {
			return progressLoopEvent{}
		}
		g.pushState(before)
		after.after = true
		for i := 0; i < g.stateCount; i++ {
			prior := g.states[(g.stateNext-1-i+len(g.states))%len(g.states)]
			if prior.path != after.path || prior.digest != after.digest {
				continue
			}
			transitions := 1
			for j := 0; j < i; j++ {
				entry := g.states[(g.stateNext-1-j+len(g.states))%len(g.states)]
				if entry.path == after.path && entry.after {
					transitions++
				}
			}
			if transitions >= g.limit {
				g.pushState(after)
				return g.trigger("the file returned to an earlier state after repeated edits", sha256.Sum256(append(after.path[:], after.digest[:]...)))
			}
			break
		}
		g.pushState(after)
		return progressLoopEvent{}
	}
	if !readOnlyForLoop(call.Name) && call.Name != "bash" {
		g.readCount = 0
		return progressLoopEvent{}
	}
	fingerprint, err := successfulToolFingerprint(call, result)
	if err != nil {
		g.readCount = 0
		return progressLoopEvent{}
	}
	if call.Name == "bash" {
		command, ok := call.Arguments["command"].(string)
		if !ok || !settings.IsReadOnlyCommand(command) {
			g.readCount = 0
			return progressLoopEvent{}
		}
		state, known := g.workspaceDigest()
		if !known {
			// An incomplete snapshot cannot establish unchanged local state.
			g.readCount = 0
			return progressLoopEvent{}
		}
		fingerprint = sha256.Sum256(append(fingerprint[:], state[:]...))
	}
	g.reads[g.readNext] = fingerprint
	g.readNext = (g.readNext + 1) % len(g.reads)
	if g.readCount < len(g.reads) {
		g.readCount++
	}
	if g.readCount >= g.limit {
		match := true
		for i := 1; i < g.limit; i++ {
			if g.readFromEnd(i) != fingerprint {
				match = false
				break
			}
		}
		if match {
			reason := "the same successful read returned unchanged content repeatedly"
			if call.Name == "bash" {
				reason = "the same successful local command returned unchanged content and workspace state repeatedly"
			}
			return g.trigger(reason, fingerprint)
		}
	}
	for period := 2; period <= 4; period++ {
		if period*g.limit > g.readCount {
			continue
		}
		match := true
		for i := 0; i < period && match; i++ {
			for repetition := 1; repetition < g.limit; repetition++ {
				if g.readFromEnd(i) != g.readFromEnd(repetition*period+i) {
					match = false
					break
				}
			}
		}
		if match && g.distinctReadPeriod(period) {
			return g.trigger(fmt.Sprintf("the same %d-call read-only sequence repeated without a changed result", period), g.readPeriodFingerprint(period))
		}
	}
	return progressLoopEvent{}
}

// workspaceDigest observes a bounded set of regular worktree files after a
// conservatively read-only Bash command. Large, unreadable, linked or changing
// trees are unknown and cannot trigger a progress claim. Git metadata is not
// part of this file-state signal; command output remains in the fingerprint.
func (g *progressLoopGuard) workspaceDigest() ([sha256.Size]byte, bool) {
	var zero [sha256.Size]byte
	if g.cwd == "" {
		return zero, false
	}
	const maxNodes = 256
	const maxBytes = 1 << 20
	h := sha256.New()
	nodes, bytesRead := 0, int64(0)
	var walk func(string) bool
	walk = func(path string) bool {
		nodes++
		if nodes > maxNodes {
			return false
		}
		info, err := os.Lstat(path)
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return false
		}
		rel, err := filepath.Rel(g.cwd, path)
		if err != nil {
			return false
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00", rel, info.Mode(), info.Size())
		if info.IsDir() {
			entries, known := boundedDirEntries(path, maxNodes-nodes)
			if !known {
				return false
			}
			for _, entry := range entries {
				if path == g.cwd && entry.Name() == ".git" && entry.IsDir() {
					continue
				}
				if !walk(filepath.Join(path, entry.Name())) {
					return false
				}
			}
			return true
		}
		if info.Size() > maxBytes-bytesRead {
			return false
		}
		file, err := os.Open(path)
		if err != nil {
			return false
		}
		n, copyErr := io.Copy(h, io.LimitReader(file, maxBytes-bytesRead+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() {
			return false
		}
		bytesRead += n
		return true
	}
	if !walk(g.cwd) {
		return zero, false
	}
	copy(zero[:], h.Sum(nil))
	return zero, true
}

// boundedDirEntries reads at most one entry beyond the remaining node budget.
// os.ReadDir(path) and filepath.WalkDir sort the entire directory first, so
// neither can enforce a memory bound for a single very wide directory.
func boundedDirEntries(path string, remaining int) ([]os.DirEntry, bool) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer dir.Close()
	entries, err := dir.ReadDir(remaining + 1)
	if err != nil && err != io.EOF || len(entries) > remaining {
		return entries, false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, true
}

func (g *progressLoopGuard) distinctReadPeriod(period int) bool {
	first := g.readFromEnd(0)
	for i := 1; i < period; i++ {
		if g.readFromEnd(i) != first {
			return true
		}
	}
	return false
}

func (g *progressLoopGuard) readPeriodFingerprint(period int) [sha256.Size]byte {
	h := sha256.New()
	for i := period - 1; i >= 0; i-- {
		fingerprint := g.readFromEnd(i)
		_, _ = h.Write(fingerprint[:])
	}
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func readOnlyForLoop(name string) bool {
	switch name {
	case "read", "glob", "grep", "ls", "verification_status":
		return true
	default:
		return false
	}
}

func successfulToolFingerprint(call msg.ToolCall, result msg.ToolResultMessage) ([sha256.Size]byte, error) {
	payload, err := json.Marshal(struct {
		Name    string         `json:"name"`
		Args    map[string]any `json:"args"`
		Content msg.Blocks     `json:"content"`
	}{call.Name, call.Arguments, result.Content})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(payload), nil
}

func (g *progressLoopGuard) readFromEnd(offset int) [sha256.Size]byte {
	return g.reads[(g.readNext-1-offset+len(g.reads))%len(g.reads)]
}

func (g *progressLoopGuard) pushState(state fileState) {
	g.states[g.stateNext] = state
	g.stateNext = (g.stateNext + 1) % len(g.states)
	if g.stateCount < len(g.states) {
		g.stateCount++
	}
}

func (g *progressLoopGuard) trigger(reason string, signature [sha256.Size]byte) progressLoopEvent {
	g.readCount = 0
	g.stateCount = 0
	if g.warned {
		if signature == g.warnedSignature {
			return progressLoopEvent{Err: fmt.Errorf("progress loop continued after corrective notice: %s", reason)}
		}
		return progressLoopEvent{}
	}
	g.warned = true
	g.warnedSignature = signature
	return progressLoopEvent{Notice: "Progress loop detected: " + reason + ". Stop repeating these calls; inspect what changed and choose a different next step."}
}
