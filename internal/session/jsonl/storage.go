package jsonl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/session"
)

// Storage is the JSONL-file-backed session.Storage implementation. It
// mirrors JsonlStorage in storage.js: an in-memory session.State replayed
// from (or written alongside) an append-only file.
//
// mu serializes every access to state and to the backing file: Commit
// (append + apply) and every Get*/Scan* read. The turn loop's own
// documentation (turn.go's executeConcurrentRun) assumed Commit was only
// ever called from one goroutine at a time, but Lane.Steer can now be
// called from a different goroutine (the TUI's input handler) while a
// turn is in flight on the lane's own goroutine, racing both the map
// writes inside session.State and AppendTransaction's file write. mu
// makes both safe; it does not change ordering semantics beyond making
// concurrent calls linearize in whatever order they arrive.
type Storage struct {
	path   string
	header session.Header
	state  *session.State
	now    func() time.Time
	closed bool

	mu sync.Mutex
}

// Header returns the storage's line-1 header as currently known in memory.
// Open never returns a Storage still backed by a legacy v3 file: it
// upgrades one to v4 first (UpgradeLegacyV3, legacy_v3.go) and reopens it.
func (s *Storage) Header() session.Header { return s.header }

// Path returns the file path backing this storage.
func (s *Storage) Path() string { return s.path }

// Create writes a brand-new v4 session file: the header, then one
// transaction for initialWrites (if any), and returns the opened Storage.
// It mirrors JsonlStorage.create in storage.js.
func Create(path string, header session.Header, initialWrites []session.Write, now func() time.Time) (*Storage, error) {
	if now == nil {
		now = time.Now
	}
	state := session.NewState()
	var committed []session.CommittedWrite
	var result session.CommitResult
	if len(initialWrites) > 0 {
		var err error
		committed, result, err = state.PrepareCommit(initialWrites, now().UnixMilli())
		if err != nil {
			return nil, err
		}
		_ = result
	}
	err := PublishJSONL(path, header, func(append func(writes []session.CommittedWrite) error) error {
		if len(committed) != 0 {
			return append(committed)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	state.ApplyValidated(committed)
	return &Storage{path: path, header: header, state: state, now: now}, nil
}

// Open replays an existing v4 session file into memory. Legacy v3 files are
// detected and rejected (see legacy_v3.go); Open never modifies the file
// except to repair the very last line when it was torn by a kill/crash mid
// AppendTransaction (commits are a plain append with no fsync: doc.go):
//
//   - final line unterminated (no trailing "\n") and unparsable: the write
//     never completed. The file is truncated to drop exactly that line,
//     leaving every earlier, complete transaction intact.
//   - final line unterminated but parses and validates fine (the write
//     landed, only the trailing newline did not): the line is kept and the
//     missing "\n" is appended.
//   - final line terminated (a complete "\n"-ended line) but unparsable or
//     failing validation: not repaired. A line the writer finished is
//     corruption, not a torn write, so Open still errors.
//   - any non-final line that is unparsable or fails validation: always an
//     error, regardless of termination.
//
// It mirrors JsonlStorage.open/openV4 in storage.js, which drops an
// unparsable final line and completes an unterminated-but-valid one the
// same way; this port additionally refuses to auto-repair a *terminated*
// final line that is invalid, treating that as real corruption rather than
// a crash artifact.
func Open(path string, now func() time.Time) (*Storage, error) {
	if now == nil {
		now = time.Now
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jsonl: failed to read %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("jsonl: invalid storage %s: missing header", path)
	}

	var headerText []byte
	var rest []byte
	if idx := bytes.IndexByte(data, '\n'); idx == -1 {
		headerText = data
	} else {
		headerText = data[:idx]
		rest = data[idx+1:]
	}
	parsed, err := ParseHeader(string(headerText))
	if err != nil {
		return nil, fmt.Errorf("jsonl: invalid storage %s: invalid header: %w", path, err)
	}
	if parsed.Format == FormatV3Legacy {
		// See legacy_v3.go's file-level comment for how this eager upgrade
		// (on Open, rather than lazily on the first commit) deviates from
		// pi. UpgradeLegacyV3 leaves path untouched on any error.
		if err := UpgradeLegacyV3(path, now); err != nil {
			return nil, err
		}
		return Open(path, now)
	}
	header := *parsed.V4
	if header.StorageVersion != session.StorageVersion {
		return nil, fmt.Errorf("jsonl: session %s uses unsupported storage version %d", header.ID, header.StorageVersion)
	}

	state := session.NewState()
	lines, restTerminated := splitBodyLines(rest)
	lineNumber := 1
	appendMissingNewline := false
	for i, lineBytes := range lines {
		lineNumber++
		isLast := i == len(lines)-1
		lineTerminated := restTerminated || !isLast
		writes, perr := ParseTransaction(lineBytes)
		if perr != nil {
			if isLast && !lineTerminated {
				// Torn tail: drop it by truncating the file to just before
				// it. The preceding line's own trailing "\n" is already
				// the new EOF, so the truncate offset is simply the file
				// length minus this line's byte length.
				if err := os.Truncate(path, int64(len(data)-len(lineBytes))); err != nil {
					return nil, fmt.Errorf("jsonl: failed to repair torn tail %s: %w", path, err)
				}
				break
			}
			return nil, fmt.Errorf("jsonl: invalid storage %s: line %d: %w", path, lineNumber, perr)
		}
		if verr := state.ValidateCommitted(writes); verr != nil {
			return nil, fmt.Errorf("jsonl: invalid storage %s: line %d: %w", path, lineNumber, verr)
		}
		state.ApplyValidated(writes)
		if isLast && !lineTerminated {
			appendMissingNewline = true
		}
	}
	if appendMissingNewline {
		if err := appendNewline(path); err != nil {
			return nil, fmt.Errorf("jsonl: failed to repair unterminated tail %s: %w", path, err)
		}
	}
	if header.NextSeq != 0 {
		if err := state.AdvanceNextSeq(header.NextSeq); err != nil {
			return nil, err
		}
	}
	return &Storage{path: path, header: header, state: state, now: now}, nil
}

// splitBodyLines splits the bytes after the header line into individual
// transaction lines, reporting whether the body (and so its final line) was
// newline-terminated. A nil/empty rest yields no lines.
func splitBodyLines(rest []byte) (lines [][]byte, terminated bool) {
	if len(rest) == 0 {
		return nil, true
	}
	terminated = rest[len(rest)-1] == '\n'
	trimmed := rest
	if terminated {
		trimmed = rest[:len(rest)-1]
	}
	if len(trimmed) == 0 {
		return nil, terminated
	}
	return bytes.Split(trimmed, []byte("\n")), terminated
}

// appendNewline appends a single "\n" to path, completing a final line that
// parsed and validated fine but was missing its trailing newline.
func appendNewline(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write([]byte("\n"))
	return err
}

// Commit appends one transaction line for writes and applies it in memory.
// It mirrors JsonlStorage.commit/applyCommit in storage.js (minus the
// legacy-v3-upgrade path, which is not implemented).
func (s *Storage) Commit(writes []session.Write) (session.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return session.CommitResult{}, fmt.Errorf("jsonl: storage is closed")
	}
	committed, result, err := s.state.PrepareCommit(writes, s.now().UnixMilli())
	if err != nil {
		return session.CommitResult{}, err
	}
	if len(committed) != 0 {
		if err := AppendTransaction(s.path, committed); err != nil {
			return session.CommitResult{}, err
		}
	}
	stats := s.state.ApplyValidated(committed)
	result.Stats = stats
	return result, nil
}

// GetEntries returns the entries with the given ids that exist.
func (s *Storage) GetEntries(ids []string) map[string]session.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.GetEntries(ids)
}

// GetEntry returns one entry by id.
func (s *Storage) GetEntry(id string) (session.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.GetEntry(id)
}

// GetValue returns the current raw value at (namespace, key).
func (s *Storage) GetValue(namespace, key string) (json.RawMessage, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.state.GetValue(namespace, key)
	if !ok {
		return nil, 0, false
	}
	return v.Value, v.Seq, true
}

// ScanValues returns every current value under namespace whose key has the
// given prefix, ordered by key.
func (s *Storage) ScanValues(namespace, keyPrefix string) []session.StoredRaw {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ScanValues(namespace, keyPrefix)
}

// ReadList returns a list's elements, oldest first.
func (s *Storage) ReadList(namespace, key string) []session.ListElementRaw {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ReadList(namespace, key)
}

// ScanBranch walks a branch from an entry to the root.
func (s *Storage) ScanBranch(query session.BranchScan) ([]session.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ScanBranch(query)
}

// ScanEntries returns entries in commit order (or reverse).
func (s *Storage) ScanEntries(query session.EntryScan) []session.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ScanEntries(query)
}

// ScanUsage returns usage rows ordered by seq (or reverse).
func (s *Storage) ScanUsage(query session.UsageScan) []session.UsageRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ScanUsage(query)
}

// GetStats returns the current session totals.
func (s *Storage) GetStats() session.SessionStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.GetStats()
}

// NextSeq returns the sequence the next commit will start at. Used by
// Fork to capture a boundary on an open source without racing its commits.
func (s *Storage) NextSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.NextSeq()
}

// Close marks the storage closed. Further Commit/Get*/Scan* calls fail.
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

var _ session.Storage = (*Storage)(nil)
