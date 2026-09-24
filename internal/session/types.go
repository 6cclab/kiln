package session

import (
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/msg"
)

// FormatVersion is the JSONL header "v" field pi writes today.
const FormatVersion = 4

// StorageVersion is the JSONL header "storageVersion" field.
const StorageVersion = 1

// EntryType discriminates the Entry union.
type EntryType string

const (
	EntryMessage       EntryType = "message"
	EntryCompaction    EntryType = "compaction"
	EntryBranchSummary EntryType = "branch_summary"
	EntryCustom        EntryType = "custom"
)

// Entry is one node in a session's conversation tree, as materialized in
// memory and as it appears (flattened with "kind":"entry") on the wire
// inside a transaction. Fields are grouped by which EntryType they apply to;
// see doc.go.
type Entry struct {
	ID         string
	ParentID   *string // nil means root
	Seq        int64
	Timestamp  int64
	Type       EntryType
	CustomType string // set when Type == EntryCustom (and, rarely, otherwise)

	// EntryMessage
	Message   msg.Message
	Terminate bool

	// EntryCompaction
	Summary      string
	RetainedTail []msg.Message
	TokensBefore int64
	FromHook     bool

	// EntryCompaction, EntryBranchSummary
	Details json.RawMessage
	Usage   *msg.Usage

	// EntryBranchSummary
	FromID *string // nil means the branch root

	// EntryCustom
	Data json.RawMessage
}

// entryWire is the flat on-disk shape of one entry write, kind included.
type entryWire struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	ParentID     *string           `json:"parentId"`
	Seq          int64             `json:"seq,omitempty"`
	Timestamp    int64             `json:"timestamp,omitempty"`
	Type         string            `json:"type"`
	CustomType   string            `json:"customType,omitempty"`
	Message      json.RawMessage   `json:"message,omitempty"`
	Terminate    *bool             `json:"terminate,omitempty"`
	Summary      string            `json:"summary,omitempty"`
	RetainedTail []json.RawMessage `json:"retainedTail,omitempty"`
	TokensBefore *int64            `json:"tokensBefore,omitempty"`
	Details      json.RawMessage   `json:"details,omitempty"`
	Usage        json.RawMessage   `json:"usage,omitempty"`
	FromHook     *bool             `json:"fromHook,omitempty"`
	FromID       json.RawMessage   `json:"fromId,omitempty"`
	Data         json.RawMessage   `json:"data,omitempty"`
}

// MarshalJSON encodes the entry, including "kind":"entry".
func (e Entry) MarshalJSON() ([]byte, error) {
	w := entryWire{
		ID:        e.ID,
		Kind:      "entry",
		ParentID:  e.ParentID,
		Seq:       e.Seq,
		Timestamp: e.Timestamp,
		Type:      string(e.Type),
	}
	if e.Type == EntryCustom || e.CustomType != "" {
		w.CustomType = e.CustomType
	}
	switch e.Type {
	case EntryMessage:
		if e.Message != nil {
			raw, err := json.Marshal(e.Message)
			if err != nil {
				return nil, fmt.Errorf("marshal entry %s message: %w", e.ID, err)
			}
			w.Message = raw
		}
		if e.Terminate {
			t := true
			w.Terminate = &t
		}
	case EntryCompaction:
		w.Summary = e.Summary
		tail := make([]json.RawMessage, len(e.RetainedTail))
		for i, m := range e.RetainedTail {
			raw, err := json.Marshal(m)
			if err != nil {
				return nil, fmt.Errorf("marshal entry %s retainedTail[%d]: %w", e.ID, i, err)
			}
			tail[i] = raw
		}
		if len(tail) > 0 {
			w.RetainedTail = tail
		} else {
			w.RetainedTail = []json.RawMessage{}
		}
		w.TokensBefore = &e.TokensBefore
		w.Details = e.Details
		if e.Usage != nil {
			raw, err := json.Marshal(e.Usage)
			if err != nil {
				return nil, err
			}
			w.Usage = raw
		}
		fh := e.FromHook
		w.FromHook = &fh
	case EntryBranchSummary:
		w.Summary = e.Summary
		w.Details = e.Details
		if e.Usage != nil {
			raw, err := json.Marshal(e.Usage)
			if err != nil {
				return nil, err
			}
			w.Usage = raw
		}
		fh := e.FromHook
		w.FromHook = &fh
		fromID, err := json.Marshal(e.FromID)
		if err != nil {
			return nil, err
		}
		w.FromID = fromID
	case EntryCustom:
		w.Data = e.Data
	}
	return json.Marshal(w)
}

// UnmarshalJSON decodes one entry write. It requires "kind":"entry".
func (e *Entry) UnmarshalJSON(data []byte) error {
	var w entryWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.Kind != "entry" {
		return fmt.Errorf("session: not an entry write (kind=%q)", w.Kind)
	}
	*e = Entry{
		ID:         w.ID,
		ParentID:   w.ParentID,
		Seq:        w.Seq,
		Timestamp:  w.Timestamp,
		Type:       EntryType(w.Type),
		CustomType: w.CustomType,
		Details:    w.Details,
	}
	switch e.Type {
	case EntryMessage:
		if len(w.Message) > 0 {
			m, err := msg.UnmarshalMessage(w.Message)
			if err != nil {
				return fmt.Errorf("entry %s message: %w", e.ID, err)
			}
			e.Message = m
		}
		if w.Terminate != nil {
			e.Terminate = *w.Terminate
		}
	case EntryCompaction:
		e.Summary = w.Summary
		e.RetainedTail = make([]msg.Message, len(w.RetainedTail))
		for i, raw := range w.RetainedTail {
			m, err := msg.UnmarshalMessage(raw)
			if err != nil {
				return fmt.Errorf("entry %s retainedTail[%d]: %w", e.ID, i, err)
			}
			e.RetainedTail[i] = m
		}
		if w.TokensBefore != nil {
			e.TokensBefore = *w.TokensBefore
		}
		if len(w.Usage) > 0 {
			var u msg.Usage
			if err := json.Unmarshal(w.Usage, &u); err != nil {
				return err
			}
			e.Usage = &u
		}
		if w.FromHook != nil {
			e.FromHook = *w.FromHook
		}
	case EntryBranchSummary:
		e.Summary = w.Summary
		if len(w.Usage) > 0 {
			var u msg.Usage
			if err := json.Unmarshal(w.Usage, &u); err != nil {
				return err
			}
			e.Usage = &u
		}
		if w.FromHook != nil {
			e.FromHook = *w.FromHook
		}
		if len(w.FromID) > 0 {
			var id *string
			if err := json.Unmarshal(w.FromID, &id); err != nil {
				return err
			}
			e.FromID = id
		}
	case EntryCustom:
		e.Data = w.Data
	default:
		return fmt.Errorf("session: unknown entry type %q", w.Type)
	}
	return nil
}

// EntryStructure is the shape-only projection of an Entry (no payload).
type EntryStructure struct {
	ID         string
	ParentID   *string
	Seq        int64
	Timestamp  int64
	Type       EntryType
	CustomType string
}

// UsageRow is one usage ledger line.
type UsageRow struct {
	ID         string
	Seq        int64
	Usage      msg.Usage
	EntryID    string
	Adjustment bool
	Details    json.RawMessage
}

type usageWire struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Seq        int64           `json:"seq,omitempty"`
	Usage      json.RawMessage `json:"usage"`
	EntryID    string          `json:"entryId,omitempty"`
	Adjustment bool            `json:"adjustment"`
	Details    json.RawMessage `json:"details,omitempty"`
}

// MarshalJSON encodes the usage row, including "kind":"usage".
func (u UsageRow) MarshalJSON() ([]byte, error) {
	raw, err := json.Marshal(u.Usage)
	if err != nil {
		return nil, err
	}
	return json.Marshal(usageWire{
		ID:         u.ID,
		Kind:       "usage",
		Seq:        u.Seq,
		Usage:      raw,
		EntryID:    u.EntryID,
		Adjustment: u.Adjustment,
		Details:    u.Details,
	})
}

// UnmarshalJSON decodes one usage row. It requires "kind":"usage".
func (u *UsageRow) UnmarshalJSON(data []byte) error {
	var w usageWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.Kind != "usage" {
		return fmt.Errorf("session: not a usage write (kind=%q)", w.Kind)
	}
	var usage msg.Usage
	if len(w.Usage) > 0 {
		if err := json.Unmarshal(w.Usage, &usage); err != nil {
			return err
		}
	}
	*u = UsageRow{
		ID:         w.ID,
		Seq:        w.Seq,
		Usage:      usage,
		EntryID:    w.EntryID,
		Adjustment: w.Adjustment,
		Details:    w.Details,
	}
	return nil
}

// ValueWrite is one "value" or "list" transaction line: a set/delete on a
// scalar, or an append/delete on a list. Kind is "value" or "list"; Op is
// "set"/"delete" for a value, "append"/"delete" for a list.
type ValueWrite struct {
	Kind      string // "value" | "list"
	Op        string // "set" | "delete" | "append"
	Seq       int64
	Namespace string
	Key       string
	Value     json.RawMessage // absent (nil) on delete
}

type valueWire struct {
	Kind      string          `json:"kind"`
	Op        string          `json:"op"`
	Seq       int64           `json:"seq,omitempty"`
	Namespace string          `json:"namespace"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value,omitempty"`
}

// MarshalJSON encodes the value/list write.
func (v ValueWrite) MarshalJSON() ([]byte, error) {
	return json.Marshal(valueWire(v))
}

// UnmarshalJSON decodes one value/list write. Kind must be "value" or "list".
func (v *ValueWrite) UnmarshalJSON(data []byte) error {
	var w valueWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.Kind != "value" && w.Kind != "list" {
		return fmt.Errorf("session: not a value/list write (kind=%q)", w.Kind)
	}
	*v = ValueWrite(w)
	return nil
}

// CommittedWrite is one transaction line item: exactly one of Entry, Usage,
// or Value is set, matching Kind.
type CommittedWrite struct {
	Kind  string // "entry" | "usage" | "value" | "list"
	Entry *Entry
	Usage *UsageRow
	Value *ValueWrite
}

// Seq returns the write's assigned sequence number.
func (w CommittedWrite) Seq() int64 {
	switch w.Kind {
	case "entry":
		return w.Entry.Seq
	case "usage":
		return w.Usage.Seq
	case "value", "list":
		return w.Value.Seq
	}
	return 0
}

// MarshalJSON encodes the write in whichever flat shape its Kind implies.
func (w CommittedWrite) MarshalJSON() ([]byte, error) {
	switch w.Kind {
	case "entry":
		return json.Marshal(w.Entry)
	case "usage":
		return json.Marshal(w.Usage)
	case "value", "list":
		return json.Marshal(w.Value)
	default:
		return nil, fmt.Errorf("session: unknown write kind %q", w.Kind)
	}
}

type kindProbe struct {
	Kind string `json:"kind"`
}

// UnmarshalJSON decodes one transaction line item by its "kind".
func (w *CommittedWrite) UnmarshalJSON(data []byte) error {
	var probe kindProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch probe.Kind {
	case "entry":
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			return err
		}
		*w = CommittedWrite{Kind: "entry", Entry: &e}
	case "usage":
		var u UsageRow
		if err := json.Unmarshal(data, &u); err != nil {
			return err
		}
		*w = CommittedWrite{Kind: "usage", Usage: &u}
	case "value", "list":
		var v ValueWrite
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*w = CommittedWrite{Kind: probe.Kind, Value: &v}
	default:
		return fmt.Errorf("session: unknown write kind %q", probe.Kind)
	}
	return nil
}

// Write is an uncommitted write, as passed to Storage.Commit. Concrete
// types: EntryWrite, UsageWrite, ValueWrite (op set/delete), ListWrite (op
// append/delete). ValueWrite doubles as its own uncommitted form: Seq is
// simply ignored (and left zero) until commit assigns it.
type Write interface {
	writeKind() string
}

// EntryWrite is an uncommitted entry (Seq and Timestamp are assigned by
// commit and should be left zero).
type EntryWrite struct{ Entry Entry }

func (EntryWrite) writeKind() string { return "entry" }

// UsageWrite is an uncommitted usage row (Seq is assigned by commit).
type UsageWrite struct{ Row UsageRow }

func (UsageWrite) writeKind() string { return "usage" }

// writeKind is a marker only; ValueWrite.Kind (not this method) distinguishes
// a "value" write from a "list" write.
func (ValueWrite) writeKind() string { return "value" }

// LaneConfiguration is the pi.lane.config value shape.
type LaneConfiguration struct {
	Model           ModelRef `json:"model"`
	ThinkingLevel   string   `json:"thinkingLevel"`
	ActiveToolNames []string `json:"activeToolNames"`
}

// ModelRef names a provider/model pair.
type ModelRef struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// LaneState is the pi.lane.state value shape.
type LaneState struct {
	CurrentOperationID *string     `json:"currentOperationId"`
	LastOperationID    *string     `json:"lastOperationId"`
	Inbox              []InboxItem `json:"inbox"`
}

// InboxItem is one lane inbox entry.
type InboxItem struct {
	EntryID string `json:"entryId"`
	Kind    string `json:"kind"`
}

// SessionStats is the aggregate session totals.
type SessionStats struct {
	MessageCount int
	Usage        msg.Usage
}

// EntryCursor paginates a branch or entry scan by seq.
type EntryCursor struct {
	Seq int64
}

// BranchScan queries scanBranch.
type BranchScan struct {
	Start      string
	StopAtType EntryType
	StopAtID   string
	Type       EntryType
	CustomType string
	Order      string // "newestFirst" | "oldestFirst"
	Limit      int
	Cursor     *EntryCursor
}

// EntryScan queries scanEntries.
type EntryScan struct {
	Type       EntryType
	CustomType string
	FromSeq    int64
	ToSeq      int64
	Order      string // "asc" | "desc"
	Limit      int
}

// UsageScan queries scanUsage.
type UsageScan struct {
	FromSeq int64
	ToSeq   int64
	Order   string
	Limit   int
}

// CommitResult is what a successful Commit reports back.
type CommitResult struct {
	FirstSeq  int64
	Seqs      []int64
	Timestamp int64
	Stats     SessionStats
}

// Header is the JSONL storage header (line 1) for format v4.
type Header struct {
	V                       int    `json:"v"`
	Kind                    string `json:"kind"` // "header"
	ID                      string `json:"id"`
	StorageVersion          int    `json:"storageVersion"`
	CreatedAt               int64  `json:"createdAt"`
	Cwd                     string `json:"cwd"`
	ParentSessionID         string `json:"parentSessionId,omitempty"`
	LegacyParentSessionPath string `json:"legacyParentSessionPath,omitempty"`
	NextSeq                 int64  `json:"nextSeq,omitempty"`
}

// Storage is the backend-agnostic session storage interface. See
// internal/session/jsonl for the JSONL-file-backed implementation.
type Storage interface {
	Commit(writes []Write) (CommitResult, error)
	GetEntries(ids []string) map[string]Entry
	GetValue(namespace, key string) (json.RawMessage, int64, bool)
	ScanValues(namespace, keyPrefix string) []StoredRaw
	ReadList(namespace, key string) []ListElementRaw
	ScanBranch(query BranchScan) ([]Entry, error)
	ScanEntries(query EntryScan) []Entry
	ScanUsage(query UsageScan) []UsageRow
	GetStats() SessionStats
	Close() error
}

// StoredRaw is one scanned scalar value with its address and seq.
type StoredRaw struct {
	Namespace string
	Key       string
	Value     json.RawMessage
	Seq       int64
}

// ListElementRaw is one list element with its seq.
type ListElementRaw struct {
	Value json.RawMessage
	Seq   int64
}
