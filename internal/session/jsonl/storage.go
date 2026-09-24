package jsonl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/andrepato/harness/internal/session"
)

// Storage is the JSONL-file-backed session.Storage implementation. It
// mirrors JsonlStorage in storage.js: an in-memory session.State replayed
// from (or written alongside) an append-only file.
type Storage struct {
	path   string
	header session.Header
	state  *session.State
	now    func() time.Time
	closed bool
}

// Header returns the storage's line-1 header as currently known in memory
// (Create/Open do not mutate it further; a legacy-v3 upgrade would, but that
// path is not implemented — see legacy_v3.go).
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
// except to complete a torn (unterminated) final line by rewriting it away.
// It mirrors JsonlStorage.open/openV4 in storage.js.
func Open(path string, now func() time.Time) (*Storage, error) {
	if now == nil {
		now = time.Now
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("jsonl: failed to read %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("jsonl: failed to read %s: %w", path, err)
		}
		return nil, fmt.Errorf("jsonl: invalid storage %s: missing header", path)
	}
	parsed, err := ParseHeader(scanner.Text())
	if err != nil {
		return nil, fmt.Errorf("jsonl: invalid storage %s: invalid header: %w", path, err)
	}
	if parsed.Format == FormatV3Legacy {
		return nil, ErrLegacyV3Unsupported
	}
	header := *parsed.V4
	if header.StorageVersion != session.StorageVersion {
		return nil, fmt.Errorf("jsonl: session %s uses unsupported storage version %d", header.ID, header.StorageVersion)
	}

	state := session.NewState()
	lineNumber := 1
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		writes, err := ParseTransaction(line)
		if err != nil {
			return nil, fmt.Errorf("jsonl: invalid storage %s: line %d: %w", path, lineNumber, err)
		}
		if err := state.ValidateCommitted(writes); err != nil {
			return nil, fmt.Errorf("jsonl: invalid storage %s: line %d: %w", path, lineNumber, err)
		}
		state.ApplyValidated(writes)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("jsonl: failed to read %s: %w", path, err)
	}
	if header.NextSeq != 0 {
		if err := state.AdvanceNextSeq(header.NextSeq); err != nil {
			return nil, err
		}
	}
	return &Storage{path: path, header: header, state: state, now: now}, nil
}

// Commit appends one transaction line for writes and applies it in memory.
// It mirrors JsonlStorage.commit/applyCommit in storage.js (minus the
// legacy-v3-upgrade path, which is not implemented).
func (s *Storage) Commit(writes []session.Write) (session.CommitResult, error) {
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
func (s *Storage) GetEntries(ids []string) map[string]session.Entry { return s.state.GetEntries(ids) }

// GetEntry returns one entry by id.
func (s *Storage) GetEntry(id string) (session.Entry, bool) { return s.state.GetEntry(id) }

// GetValue returns the current raw value at (namespace, key).
func (s *Storage) GetValue(namespace, key string) (json.RawMessage, int64, bool) {
	v, ok := s.state.GetValue(namespace, key)
	if !ok {
		return nil, 0, false
	}
	return v.Value, v.Seq, true
}

// ScanValues returns every current value under namespace whose key has the
// given prefix, ordered by key.
func (s *Storage) ScanValues(namespace, keyPrefix string) []session.StoredRaw {
	return s.state.ScanValues(namespace, keyPrefix)
}

// ReadList returns a list's elements, oldest first.
func (s *Storage) ReadList(namespace, key string) []session.ListElementRaw {
	return s.state.ReadList(namespace, key)
}

// ScanBranch walks a branch from an entry to the root.
func (s *Storage) ScanBranch(query session.BranchScan) ([]session.Entry, error) {
	return s.state.ScanBranch(query)
}

// ScanEntries returns entries in commit order (or reverse).
func (s *Storage) ScanEntries(query session.EntryScan) []session.Entry {
	return s.state.ScanEntries(query)
}

// ScanUsage returns usage rows ordered by seq (or reverse).
func (s *Storage) ScanUsage(query session.UsageScan) []session.UsageRow {
	return s.state.ScanUsage(query)
}

// GetStats returns the current session totals.
func (s *Storage) GetStats() session.SessionStats { return s.state.GetStats() }

// NextSeq returns the sequence the next commit will start at. Used by
// Fork to capture a boundary on an open source without racing its commits.
func (s *Storage) NextSeq() int64 { return s.state.NextSeq() }

// Close marks the storage closed. Further Commit/Get*/Scan* calls fail.
func (s *Storage) Close() error {
	s.closed = true
	return nil
}

var _ session.Storage = (*Storage)(nil)
