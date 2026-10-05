package jsonl

import (
	"bufio"
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

	// repair is non-nil when Open found the file's last line torn
	// (unterminated and unparsable) or unterminated-but-valid. Open never
	// writes to the file itself (see its doc comment); this records the
	// fix so this Storage's own first Commit can apply it, guarded by a
	// check that the file has not changed size since Open read it.
	repair *tornRepair

	mu sync.Mutex
}

// tornRepairMode discriminates the two fixes a torn/unterminated final
// line can need; see tornRepair.
type tornRepairMode int

const (
	// repairTruncate drops an unterminated, unparsable final line by
	// truncating the file to truncateTo, the offset of its first byte.
	repairTruncate tornRepairMode = iota
	// repairAppendNewline completes an unterminated-but-valid final line
	// (already applied to in-memory state) by appending its missing "\n".
	repairAppendNewline
)

// tornRepair is the on-disk fix Open found necessary but deferred to
// Commit, because Open itself must never write to a file another process
// might still be appending to. sizeAtOpen is the file's exact size as Open
// read it; Commit refuses to apply the repair (and refuses to write at
// all) if the file's size has since changed.
type tornRepair struct {
	sizeAtOpen int64
	mode       tornRepairMode
	truncateTo int64 // meaningful only when mode == repairTruncate
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
// detected and rejected (see legacy_v3.go; its eager on-disk upgrade
// rewrite is a separate, pre-existing exception to the rule below, not
// something this port revisits here).
//
// Open itself never writes to the file — not even to repair a torn or
// unterminated last line. Commits are a plain append with no fsync
// (doc.go), so another kiln process can still hold this same file open
// for append while this one calls Open: `kiln session inspect`
// (cmd/kiln/main.go), forkSession's read of a live session's header
// (internal/agent/session.go), and the eval runner's read of final stats
// (internal/eval/runner.go) can all run against a session the TUI or -p
// process is still writing. If Open repaired a torn tail on the spot, it
// could instead be racing that other process's own in-flight append and
// corrupt the file out from under it. So any repair Open finds necessary
// is only ever recorded in memory (on the returned *Storage) and applied
// later, by that Storage's own first Commit — see Commit's doc comment. A
// read-only caller, one that never calls Commit on the Storage it opened,
// never touches the file at all, however torn its tail is:
//
//   - final line unterminated (no trailing "\n") and unparsable: the write
//     never completed. It is dropped from memory now; the first Commit
//     will truncate the file to drop it from disk too.
//   - final line unterminated but parses and validates fine (the write
//     landed, only the trailing newline did not): it is kept in memory
//     now; the first Commit will append the missing "\n".
//   - final line terminated (a complete "\n"-ended line) but unparsable or
//     failing validation: not repaired, ever. A line the writer finished
//     is corruption, not a torn write.
//   - any non-final line that is unparsable or fails validation: always an
//     error, regardless of termination.
//
// pi's storage.js takes a different tradeoff: JsonlSessionStorage.load
// rewrites the file (publishFileAtomically), or appends the missing
// newline, immediately, inside load itself — it assumes a session file is
// never opened by more than one process at a time. This port does not
// make that assumption (see the read-only callers above), so it defers
// the on-disk fix to the one call that is actually about to write.
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
		f.Close()
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

	// Two cheap, read-only probes (no whole-file buffering): the file's
	// exact size, to compare against later in Commit, and its very last
	// byte, to learn whether the file as a whole — and so whichever line
	// the scan loop below ends up calling "last" — ends with "\n". Every
	// line but the true final one is, by construction, followed by "\n"
	// inside the file (that's what makes it not the last line), so this
	// one check is all isLast's termination ever depends on.
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("jsonl: failed to read %s: %w", path, err)
	}
	sizeAtOpen := info.Size()
	fileEndsWithNewline := true
	if sizeAtOpen > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], sizeAtOpen-1); err != nil {
			return nil, fmt.Errorf("jsonl: failed to read %s: %w", path, err)
		}
		fileEndsWithNewline = last[0] == '\n'
	}

	state := session.NewState()
	lineNumber := 1
	var repair *tornRepair

	// One-line lookahead: `current` is only known to be the last body line
	// once the next Scan() comes back false, so each iteration processes
	// the line buffered on the *previous* iteration.
	haveLine := scanner.Scan()
	var pending string
	if haveLine {
		pending = scanner.Text()
	}
	for haveLine {
		lineNumber++
		current := pending
		haveLine = scanner.Scan()
		isLast := !haveLine
		if haveLine {
			pending = scanner.Text()
		}
		lineTerminated := !isLast || fileEndsWithNewline

		writes, perr := ParseTransaction([]byte(current))
		if perr != nil {
			if isLast && !lineTerminated {
				// Torn tail: scanner.Text() returned exactly the file's
				// unterminated raw trailing bytes (ScanLines strips only a
				// real delimiter, and there isn't one here), so the
				// offset to truncate to is simply the file size minus
				// this line's length.
				repair = &tornRepair{sizeAtOpen: sizeAtOpen, mode: repairTruncate, truncateTo: sizeAtOpen - int64(len(current))}
				break
			}
			return nil, fmt.Errorf("jsonl: invalid storage %s: line %d: %w", path, lineNumber, perr)
		}
		if verr := state.ValidateCommitted(writes); verr != nil {
			return nil, fmt.Errorf("jsonl: invalid storage %s: line %d: %w", path, lineNumber, verr)
		}
		state.ApplyValidated(writes)
		if isLast && !lineTerminated {
			repair = &tornRepair{sizeAtOpen: sizeAtOpen, mode: repairAppendNewline}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("jsonl: failed to read %s: %w", path, err)
	}
	if header.NextSeq != 0 {
		if err := state.AdvanceNextSeq(header.NextSeq); err != nil {
			return nil, err
		}
	}
	return &Storage{path: path, header: header, state: state, now: now, repair: repair}, nil
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
// Before its very first append, Commit applies any repair Open deferred
// (see Open's doc comment) — truncating away a torn last line, or
// completing an unterminated-but-valid one with its missing "\n" — but
// only if the file's size still matches exactly what Open saw. If it has
// changed (another process appended to, or otherwise modified, the file
// since this Storage was opened), Commit refuses to write at all: the
// file may be mid-append elsewhere, and writing over that guess would be
// exactly the corruption Open's deferral exists to avoid. It mirrors
// JsonlStorage.commit/applyCommit in storage.js (minus the
// legacy-v3-upgrade path, which is not implemented, and the immediate
// torn-tail repair inside load, which this port defers — see Open).
func (s *Storage) Commit(writes []session.Write) (session.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return session.CommitResult{}, fmt.Errorf("jsonl: storage is closed")
	}
	if s.repair != nil {
		if err := s.applyRepair(); err != nil {
			return session.CommitResult{}, err
		}
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

// applyRepair performs the on-disk fix Open deferred (s.repair), guarded
// by a check that the file is still exactly the size Open saw — see
// Open's and Commit's doc comments. Called with s.mu already held, and
// only clears s.repair on success, so a failed attempt (e.g. the size
// check) keeps refusing every subsequent Commit rather than silently
// treating the file as fine.
func (s *Storage) applyRepair() error {
	info, err := os.Stat(s.path)
	if err != nil {
		return fmt.Errorf("jsonl: failed to check %s before repair: %w", s.path, err)
	}
	if info.Size() != s.repair.sizeAtOpen {
		return fmt.Errorf("jsonl: session file changed since it was opened: %s", s.path)
	}
	switch s.repair.mode {
	case repairTruncate:
		if err := os.Truncate(s.path, s.repair.truncateTo); err != nil {
			return fmt.Errorf("jsonl: failed to repair torn tail %s: %w", s.path, err)
		}
	case repairAppendNewline:
		if err := appendNewline(s.path); err != nil {
			return fmt.Errorf("jsonl: failed to repair unterminated tail %s: %w", s.path, err)
		}
	}
	s.repair = nil
	return nil
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
