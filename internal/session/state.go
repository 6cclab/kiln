package session

import (
	"fmt"
	"sort"
)

func physicalKey(namespace, key string) string { return namespace + "\x00" + key }

type storedScalar struct {
	namespace string
	key       string
	value     StoredRaw
}

type listElement struct {
	seq   int64
	value []byte
}

type storedList struct {
	namespace string
	key       string
	elements  []listElement
}

// State is the complete in-memory materialization of one session's entries,
// values, lists and usage ledger. It mirrors InMemoryStorageState in
// in-memory-storage-state.js and is intentionally unsuited to sessions too
// large to hold in memory.
type State struct {
	entries      map[string]Entry
	entriesBySeq []Entry
	scalars      map[string]storedScalar
	lists        map[string]*storedList
	usage        map[string]UsageRow
	stats        SessionStats
	nextSeq      int64
}

// NewState returns an empty State with nextSeq starting at 1.
func NewState() *State {
	return &State{
		entries: map[string]Entry{},
		scalars: map[string]storedScalar{},
		lists:   map[string]*storedList{},
		usage:   map[string]UsageRow{},
		nextSeq: 1,
	}
}

func (s *State) hasEntryOrUsageID(id string) bool {
	if _, ok := s.entries[id]; ok {
		return true
	}
	_, ok := s.usage[id]
	return ok
}

func (s *State) hasEntryID(id string) bool {
	_, ok := s.entries[id]
	return ok
}

// PrepareCommit assigns seq/timestamp to writes and validates them against
// current state, without applying them.
func (s *State) PrepareCommit(writes []Write, timestamp int64) ([]CommittedWrite, CommitResult, error) {
	committed, result := PrepareCommit(writes, s.nextSeq, timestamp)
	if err := s.ValidateCommitted(committed); err != nil {
		return nil, CommitResult{}, err
	}
	return committed, result, nil
}

// ValidateCommitted checks writes already produced by PrepareCommit.
func (s *State) ValidateCommitted(writes []CommittedWrite) error {
	return ValidateCommittedWrites(writes, s.nextSeq, s)
}

// ApplyValidated applies writes already accepted by ValidateCommitted and
// returns the post-apply totals.
func (s *State) ApplyValidated(writes []CommittedWrite) SessionStats {
	for _, w := range writes {
		switch w.Kind {
		case "entry":
			e := *w.Entry
			s.entries[e.ID] = e
			s.entriesBySeq = append(s.entriesBySeq, e)
			if e.Type == EntryMessage {
				s.stats.MessageCount++
			}
		case "usage":
			r := *w.Usage
			s.usage[r.ID] = r
			s.stats.Usage = s.stats.Usage.Add(r.Usage)
		case "value":
			if w.Value.Op == "delete" {
				delete(s.scalars, physicalKey(w.Value.Namespace, w.Value.Key))
			} else {
				s.applySet(*w.Value)
			}
		case "list":
			if w.Value.Op == "delete" {
				delete(s.lists, physicalKey(w.Value.Namespace, w.Value.Key))
			} else {
				s.applySet(*w.Value)
			}
		}
		s.nextSeq = w.Seq() + 1
	}
	return s.stats
}

func (s *State) applySet(w ValueWrite) {
	key := physicalKey(w.Namespace, w.Key)
	if w.Kind == "value" {
		s.scalars[key] = storedScalar{
			namespace: w.Namespace,
			key:       w.Key,
			value:     StoredRaw{Namespace: w.Namespace, Key: w.Key, Value: w.Value, Seq: w.Seq},
		}
		return
	}
	el := listElement{seq: w.Seq, value: w.Value}
	l, ok := s.lists[key]
	if !ok {
		l = &storedList{namespace: w.Namespace, key: w.Key}
		s.lists[key] = l
	}
	l.elements = append(l.elements, el)
}

// AdvanceNextSeq raises nextSeq to at least nextSeq, matching a header's
// "nextSeq" high-water mark on open.
func (s *State) AdvanceNextSeq(nextSeq int64) error {
	if nextSeq < 1 {
		return fmt.Errorf("session: invalid storage sequence high-water mark: %d", nextSeq)
	}
	if nextSeq > s.nextSeq {
		s.nextSeq = nextSeq
	}
	return nil
}

// NextSeq returns the sequence the next commit will start at.
func (s *State) NextSeq() int64 { return s.nextSeq }

// GetEntries returns the entries with the given ids that exist.
func (s *State) GetEntries(ids []string) map[string]Entry {
	out := map[string]Entry{}
	for _, id := range ids {
		if e, ok := s.entries[id]; ok {
			out[id] = e
		}
	}
	return out
}

// GetEntry returns one entry by id.
func (s *State) GetEntry(id string) (Entry, bool) {
	e, ok := s.entries[id]
	return e, ok
}

// GetValue returns the current raw value at (namespace, key).
func (s *State) GetValue(namespace, key string) (StoredRaw, bool) {
	v, ok := s.scalars[physicalKey(namespace, key)]
	return v.value, ok
}

// ScanValues returns every current value under namespace whose key has the
// given prefix, ordered by key (Unicode code point order, matching pi).
func (s *State) ScanValues(namespace, keyPrefix string) []StoredRaw {
	var out []StoredRaw
	for _, v := range s.scalars {
		if v.namespace != namespace {
			continue
		}
		if len(keyPrefix) > 0 && !hasPrefixRunes(v.key, keyPrefix) {
			continue
		}
		out = append(out, v.value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func hasPrefixRunes(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// ReadList returns the elements of one list, oldest first, matching pi's
// default readList options (order asc, limit 1000, no cursor).
func (s *State) ReadList(namespace, key string) []ListElementRaw {
	l, ok := s.lists[physicalKey(namespace, key)]
	if !ok {
		return nil
	}
	out := make([]ListElementRaw, len(l.elements))
	for i, el := range l.elements {
		out[i] = ListElementRaw{Value: el.value, Seq: el.seq}
	}
	return out
}

// ScanBranch walks the parentId chain from query.Start to the root (or to a
// stop condition), then filters and orders it. It mirrors scanBranch in
// in-memory-storage-state.js.
func (s *State) ScanBranch(query BranchScan) ([]Entry, error) {
	start, ok := s.entries[query.Start]
	if !ok {
		return nil, fmt.Errorf("session: unknown branch start: %s", query.Start)
	}
	var path []Entry
	entry := start
	for {
		path = append(path, entry)
		if entry.ParentID == nil {
			break
		}
		next, ok := s.entries[*entry.ParentID]
		if !ok {
			return nil, fmt.Errorf("session: corrupt branch: missing parent")
		}
		entry = next
	}
	// path is tip -> root ("newestFirst"); reverse for "oldestFirst".
	if query.Order == "oldestFirst" {
		for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
			path[i], path[j] = path[j], path[i]
		}
	}
	var stopped []Entry
	for _, candidate := range path {
		stopped = append(stopped, candidate)
		if (query.StopAtID != "" && candidate.ID == query.StopAtID) ||
			(query.StopAtType != "" && candidate.Type == query.StopAtType) {
			break
		}
	}
	var out []Entry
	for _, candidate := range stopped {
		if query.Type != "" && candidate.Type != query.Type {
			continue
		}
		if query.CustomType != "" && candidate.CustomType != query.CustomType {
			continue
		}
		if query.Cursor != nil {
			if query.Order == "oldestFirst" {
				if candidate.Seq <= query.Cursor.Seq {
					continue
				}
			} else if candidate.Seq >= query.Cursor.Seq {
				continue
			}
		}
		out = append(out, candidate)
	}
	if query.Limit > 0 && len(out) > query.Limit {
		out = out[:query.Limit]
	}
	return out, nil
}

// ScanEntries returns entries in commit order (or reverse), filtered and
// limited. It mirrors scanEntries in in-memory-storage-state.js.
func (s *State) ScanEntries(query EntryScan) []Entry {
	limit := len(s.entriesBySeq)
	if query.Limit > 0 && query.Limit < limit {
		limit = query.Limit
	}
	var out []Entry
	descending := query.Order == "desc"
	n := len(s.entriesBySeq)
	for i := 0; i < n && len(out) < limit; i++ {
		idx := i
		if descending {
			idx = n - 1 - i
		}
		e := s.entriesBySeq[idx]
		if query.Type != "" && e.Type != query.Type {
			continue
		}
		if query.CustomType != "" && e.CustomType != query.CustomType {
			continue
		}
		if query.FromSeq != 0 && e.Seq < query.FromSeq {
			continue
		}
		if query.ToSeq != 0 && e.Seq > query.ToSeq {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ScanUsage returns usage rows ordered by seq (or reverse), filtered and
// limited.
func (s *State) ScanUsage(query UsageScan) []UsageRow {
	rows := make([]UsageRow, 0, len(s.usage))
	for _, r := range s.usage {
		if query.FromSeq != 0 && r.Seq < query.FromSeq {
			continue
		}
		if query.ToSeq != 0 && r.Seq > query.ToSeq {
			continue
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if query.Order == "desc" {
			return rows[i].Seq > rows[j].Seq
		}
		return rows[i].Seq < rows[j].Seq
	})
	if query.Limit > 0 && len(rows) > query.Limit {
		rows = rows[:query.Limit]
	}
	return rows
}

// GetStats returns the current session totals.
func (s *State) GetStats() SessionStats { return s.stats }
