package jsonl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// ErrLegacyV3Unsupported is returned by UpgradeLegacyV3 (and therefore by
// Open) when a file's line-1 header is a legacy v3 header but the body
// cannot be turned into a v4 file: a record with an unsupported "type", a
// dangling/forward parent, a duplicate id, or (see the deviation note below)
// a "custom_message" record. It is never returned for a file that is
// neither v4 nor v3 at all — ParseHeader's own "unsupported session header"
// error covers that case.
var ErrLegacyV3Unsupported = errors.New("legacy v3 session: file is not a well-formed legacy v3 session")

// --- Deviation from pi (read before trusting this file matches legacy-v3.js) ---
//
// pi never rewrites a legacy v3 file just because it was opened. JsonlStorage
// .openLegacyV3 (storage.js) replays the file into memory via
// LegacyV3Source.read/writes and returns a working, read-only-until-you-
// commit Storage; the on-disk rewrite (JsonlStorage.upgradeLegacyV3ToV4,
// storage.js) only happens inside applyCommit, lazily, on the FIRST commit
// that carries at least one caller write — and that rewrite's trailing
// transaction line is [usage-adjustment-row, ...callerWrites] together.
// Forking an *open* v3 session is explicitly refused by
// repo.js:resolveForkInput ("commit a non-empty transaction to upgrade it to
// format 4 first"); forking a *closed* v3 session reads LegacyV3Source fresh
// from its path and uses it directly as fork input, again never touching
// the original file.
//
// This Go port does not have (and, per this task's instructions, must not
// grow inside internal/session) the storage.js-level distinction between "a
// v3-backed Storage that hasn't upgraded yet" and "a v4-backed Storage" —
// Storage here is v4-only. So Open (storage.go) eagerly calls
// UpgradeLegacyV3 the moment it sees a v3 header, then re-opens the
// resulting v4 file, and every caller that reaches a v3 file through Open
// (Fork included — internal/agent/session.go calls jsonl.Open before
// jsonl.Fork) gets it upgraded as a side effect of opening. The trailing
// transaction this writes is the usage-adjustment row alone (there is no
// caller commit riding along, because there is no caller commit at all at
// Open time) rather than pi's [usage-adjustment, ...callerWrites].
//
// A second deviation, forced by this task's file-scope restriction
// (internal/msg may not be touched): pi represents three v3 record shapes as
// msg.Message values that don't exist in this Go port's message role set
// (RoleSystem/User/Assistant/ToolResult only, see internal/msg/msg.go):
//
//   - "custom_message" (a v3 record wrapping a Message with role "custom")
//     is REJECTED here with an error, rather than becoming a v4 "message"
//     entry whose message has role "custom" (legacy-v3.js:140-146,
//     importedCustomMessage at legacy-v3.js:61-72). It is not silently
//     dropped: the file is left untouched and UpgradeLegacyV3 returns an
//     error identifying the record.
//   - inside a compaction's retainedTail, a "branch_summary" or "compaction"
//     ancestor is projected to nothing (silently omitted from the tail)
//     rather than to a role:"branchSummary" or role:"compactionSummary"
//     message (legacy-v3.js:95-115, createBranchSummaryMessage /
//     createCompactionSummaryMessage in messages.js). The v3 "custom" record
//     type is unaffected — pi already projects it to nothing too
//     (legacy-v3.js:107, the "custom" case in projectContextMessage).
//
// Everything else — id remapping (fresh uuidv7 per retained node,
// non-retained nodes passing their mapped id straight through to their own
// children), seq assignment (only retained nodes consume one), the
// session-name/label/branch-tip/lane-config/lane-state value derivation, the
// nearest-model_change/thinking_level_change/active_tools_change walk for
// configuration, and importedUsage accumulation — mirrors legacy-v3.js
// line-for-line; see the function comments below for exact line references.

// legacyV3Record is the generic wire shape of one legacy v3 JSONL record
// (line 2+ of a v3 file). Only the fields relevant to its "type" are
// populated; see parseLegacyV3Line.
type legacyV3Record struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`
	Type      string  `json:"type"`

	// type == "message"
	Message json.RawMessage `json:"message,omitempty"`

	// type == "custom_message" is rejected outright (see the deviation note
	// above) as soon as its "type" is seen, so its customType/content/
	// display fields are never decoded here.

	// type == "custom"
	CustomType string          `json:"customType,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`

	// type == "branch_summary" | "compaction" (shared)
	Summary  string          `json:"summary,omitempty"`
	Details  json.RawMessage `json:"details,omitempty"`
	Usage    json.RawMessage `json:"usage,omitempty"`
	FromHook *bool           `json:"fromHook,omitempty"`

	// type == "branch_summary"
	FromID *string `json:"fromId,omitempty"`

	// type == "compaction"
	FirstKeptEntryID string `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int64  `json:"tokensBefore,omitempty"`

	// type == "model_change"
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"modelId,omitempty"`

	// type == "thinking_level_change"
	ThinkingLevel string `json:"thinkingLevel,omitempty"`

	// type == "active_tools_change"
	ActiveToolNames []string `json:"activeToolNames,omitempty"`

	// type == "session_info"
	Name string `json:"name,omitempty"`

	// type == "label"
	TargetID *string `json:"targetId,omitempty"`
	Label    string  `json:"label,omitempty"`
}

// isRetainedLegacyV3Type mirrors isRetainedEntry, legacy-v3.js:73-79: every
// record type becomes a v4 entry of its own except the five configuration/
// bookkeeping record types, which fold into derived current values instead.
func isRetainedLegacyV3Type(t string) bool {
	switch t {
	case "model_change", "thinking_level_change", "active_tools_change", "session_info", "label":
		return false
	default:
		return true
	}
}

// legacyV3RecordTypes are the record types parseLegacyV3Line accepts,
// mirroring the type guard in parseLegacyV3Entry, legacy-v3.js:46-58.
var legacyV3RecordTypes = map[string]bool{
	"message":               true,
	"custom":                true,
	"custom_message":        true,
	"branch_summary":        true,
	"compaction":            true,
	"model_change":          true,
	"thinking_level_change": true,
	"active_tools_change":   true,
	"session_info":          true,
	"label":                 true,
}

// parseLegacyV3Line decodes and type-checks one legacy v3 record line. It
// mirrors parseLegacyV3Entry, legacy-v3.js:38-60.
func parseLegacyV3Line(line []byte) (legacyV3Record, error) {
	var r legacyV3Record
	if err := json.Unmarshal(line, &r); err != nil {
		return legacyV3Record{}, fmt.Errorf("%w: invalid legacy v3 JSONL record: not valid JSON: %v", ErrLegacyV3Unsupported, err)
	}
	if !legacyV3RecordTypes[r.Type] {
		return legacyV3Record{}, fmt.Errorf("%w: unsupported legacy v3 record type: %q", ErrLegacyV3Unsupported, r.Type)
	}
	if r.ID == "" {
		return legacyV3Record{}, fmt.Errorf("%w: legacy v3 record has no id", ErrLegacyV3Unsupported)
	}
	if r.Type == "custom_message" {
		// Deviation: see the file-level comment. Fail before any output is
		// produced, so the source file is never touched.
		return legacyV3Record{}, fmt.Errorf("%w: legacy v3 record %s is a custom_message; this port has no message role to represent it (internal/msg has no \"custom\" role) and does not upgrade files containing one", ErrLegacyV3Unsupported, r.ID)
	}
	return r, nil
}

// parseLegacyV3Timestamp parses one record's or the header's ISO-8601
// "timestamp" field strictly (unlike repo.go's lenient parseLegacyTimestamp,
// used only for directory-listing metadata): a legacy v3 file with an
// unparseable timestamp is corrupt and UpgradeLegacyV3 must refuse it rather
// than silently treating it as epoch 0. Mirrors Date.parse's role in
// legacy-v3.js wherever it appears (e.g. line 18, 68, 106, 135, 239).
func parseLegacyV3Timestamp(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid timestamp %q: %v", ErrLegacyV3Unsupported, s, err)
	}
	return t.UnixMilli(), nil
}

// legacyEntry is one indexed v3 record: indexLegacyV3Entry's "structure"
// plus enough of the raw record to materialize its v4 form later.
// mappedID is nil exactly when this entry maps to the root: either it is a
// non-retained (pass-through) entry whose own parent is the root, or (for a
// resolved reference elsewhere) it denotes "no parent"/"no ancestor". A
// retained entry's mappedID is never nil (indexLegacyV3Entry mints a fresh
// id for every retained entry, legacy-v3.js:237-239).
type legacyEntry struct {
	id          string
	parentID    *string
	typ         string
	timestampMS int64
	raw         legacyV3Record

	mappedID *string
	seq      int64
	retained bool
}

// legacyV3Inventory is readLegacyV3Inventory's result: every indexed
// record, in file order, plus the derived scalars normalizeLegacyV3Values
// needs. Mirrors legacy-v3.js:264-289.
type legacyV3Inventory struct {
	order         []string // legacy ids, file order
	entries       map[string]*legacyEntry
	nextSeq       int64
	importedUsage msg.Usage
	name          string
	finalID       *string
}

// readLegacyV3Inventory scans every complete (newline-terminated) v3 record
// after the header, indexing each one (indexLegacyV3Entry, legacy-v3.js:
// 209-250) and folding it into the running inventory (legacy-v3.js:264-289).
// lines must already have any torn final line dropped (see loadLegacyV3).
func readLegacyV3Inventory(lines [][]byte) (*legacyV3Inventory, error) {
	inv := &legacyV3Inventory{entries: map[string]*legacyEntry{}, nextSeq: 1}
	for i, lineBytes := range lines {
		lineNumber := i + 2 // line 1 is the header
		if len(bytes.TrimSpace(lineBytes)) == 0 {
			continue
		}
		rec, err := parseLegacyV3Line(lineBytes)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if _, exists := inv.entries[rec.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate legacy v3 entry id: %s", ErrLegacyV3Unsupported, rec.ID)
		}

		var mappedParentID *string
		if rec.ParentID != nil {
			parent, ok := inv.entries[*rec.ParentID]
			if !ok {
				return nil, fmt.Errorf("%w: legacy v3 entry %s has a missing or forward parent at line %d: %s", ErrLegacyV3Unsupported, rec.ID, lineNumber, *rec.ParentID)
			}
			mappedParentID = parent.mappedID
		}

		ts, err := parseLegacyV3Timestamp(rec.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("legacy v3 entry %s: %w", rec.ID, err)
		}

		e := &legacyEntry{id: rec.ID, parentID: rec.ParentID, typ: rec.Type, timestampMS: ts, raw: rec}
		if isRetainedLegacyV3Type(rec.Type) {
			mintedID, err := uuidv7At(ts)
			if err != nil {
				return nil, err
			}
			e.mappedID = &mintedID
			e.seq = inv.nextSeq
			e.retained = true
		} else {
			e.mappedID = mappedParentID
			e.retained = false
		}

		inv.entries[rec.ID] = e
		inv.order = append(inv.order, rec.ID)
		if e.retained {
			inv.nextSeq++
		}
		id := rec.ID
		inv.finalID = &id
		if rec.Type == "session_info" {
			inv.name = rec.Name
		}

		usage, ok, err := legacyEntryUsage(rec)
		if err != nil {
			return nil, fmt.Errorf("legacy v3 entry %s: %w", rec.ID, err)
		}
		if ok {
			inv.importedUsage = inv.importedUsage.Add(usage)
		}
	}
	return inv, nil
}

// legacyEntryUsage extracts the Usage a record contributes to importedUsage,
// mirroring legacyEntryUsage, legacy-v3.js:251-263.
func legacyEntryUsage(rec legacyV3Record) (msg.Usage, bool, error) {
	switch rec.Type {
	case "message":
		m, err := msg.UnmarshalMessage(rec.Message)
		if err != nil {
			return msg.Usage{}, false, fmt.Errorf("message: %w", err)
		}
		switch mm := m.(type) {
		case msg.AssistantMessage:
			return mm.Usage, true, nil
		case msg.ToolResultMessage:
			if mm.Usage != nil {
				return *mm.Usage, true, nil
			}
		}
		return msg.Usage{}, false, nil
	case "compaction", "branch_summary":
		if len(rec.Usage) == 0 {
			return msg.Usage{}, false, nil
		}
		var u msg.Usage
		if err := json.Unmarshal(rec.Usage, &u); err != nil {
			return msg.Usage{}, false, err
		}
		return u, true, nil
	default:
		return msg.Usage{}, false, nil
	}
}

// resolveLegacyID mirrors createLegacyIdResolver's returned closure,
// legacy-v3.js:81-90: null (nil) maps to null; any other id must already be
// indexed.
func resolveLegacyID(entries map[string]*legacyEntry, legacyID *string) (*string, error) {
	if legacyID == nil {
		return nil, nil
	}
	e, ok := entries[*legacyID]
	if !ok {
		return nil, fmt.Errorf("%w: missing legacy v3 entry reference: %s", ErrLegacyV3Unsupported, *legacyID)
	}
	return e.mappedID, nil
}

// resolveBranchSummaryFromID mirrors resolveBranchSummaryFromId,
// legacy-v3.js:91-94: the legacy "root" sentinel means the branch root, not
// an entry reference.
func resolveBranchSummaryFromID(entries map[string]*legacyEntry, fromID *string) (*string, error) {
	if fromID != nil && *fromID == "root" {
		return nil, nil
	}
	return resolveLegacyID(entries, fromID)
}

// retainedTailStructure walks the physical (legacy) ancestry from a
// compaction's parent through its firstKeptEntryId, inclusive, nearest
// first. It mirrors retainedTailStructure, legacy-v3.js:116-128.
func retainedTailStructure(entries map[string]*legacyEntry, compaction *legacyEntry) ([]*legacyEntry, error) {
	var chain []*legacyEntry
	currentID := compaction.parentID
	for currentID != nil {
		e, ok := entries[*currentID]
		if !ok {
			return nil, fmt.Errorf("%w: legacy v3 compaction %s has a broken parent chain at %s", ErrLegacyV3Unsupported, compaction.id, *currentID)
		}
		chain = append(chain, e)
		if *currentID == compaction.raw.FirstKeptEntryID {
			return chain, nil
		}
		currentID = e.parentID
	}
	return nil, fmt.Errorf("%w: legacy v3 compaction %s firstKeptEntryId is not on its parent branch: %s", ErrLegacyV3Unsupported, compaction.id, compaction.raw.FirstKeptEntryID)
}

// projectContextMessage turns one ancestor entry into the msg.Message its
// v4 form would carry inside a later compaction's retainedTail, or reports
// it produces none. It mirrors projectContextMessage, legacy-v3.js:95-115,
// except the "branch_summary" and "compaction" cases: see the file-level
// deviation note (internal/msg has no branchSummary/compactionSummary role).
func projectContextMessage(e *legacyEntry) (msg.Message, bool, error) {
	switch e.typ {
	case "message":
		m, err := msg.UnmarshalMessage(e.raw.Message)
		if err != nil {
			return nil, false, fmt.Errorf("legacy v3 entry %s message: %w", e.id, err)
		}
		return m, true, nil
	default:
		return nil, false, nil
	}
}

// buildRetainedTail materializes one compaction's retainedTail (oldest
// first), mirroring the retainedTail construction inside writes(),
// legacy-v3.js:158-169 (the reverse() at the end of the surrounding loop).
func buildRetainedTail(entries map[string]*legacyEntry, compaction *legacyEntry) ([]msg.Message, error) {
	chain, err := retainedTailStructure(entries, compaction)
	if err != nil {
		return nil, err
	}
	var tail []msg.Message
	for _, ancestor := range chain {
		canProduce := isRetainedLegacyV3Type(ancestor.typ) && ancestor.typ != "custom"
		if !canProduce {
			continue
		}
		m, ok, err := projectContextMessage(ancestor)
		if err != nil {
			return nil, err
		}
		if ok {
			tail = append(tail, m)
		}
	}
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	return tail, nil
}

// buildV4Entry materializes one retained legacy record as a v4
// session.Entry, mirroring normalizeRetainedEntry, legacy-v3.js:129-176.
func buildV4Entry(entries map[string]*legacyEntry, e *legacyEntry) (session.Entry, error) {
	var parentID *string
	if e.parentID != nil {
		parent, ok := entries[*e.parentID]
		if !ok {
			return session.Entry{}, fmt.Errorf("%w: missing legacy v3 entry reference: %s", ErrLegacyV3Unsupported, *e.parentID)
		}
		parentID = parent.mappedID
	}
	out := session.Entry{
		ID:        *e.mappedID,
		ParentID:  parentID,
		Seq:       e.seq,
		Timestamp: e.timestampMS,
	}
	switch e.typ {
	case "message":
		m, err := msg.UnmarshalMessage(e.raw.Message)
		if err != nil {
			return session.Entry{}, fmt.Errorf("legacy v3 entry %s message: %w", e.id, err)
		}
		out.Type = session.EntryMessage
		out.Message = m
	case "branch_summary":
		out.Type = session.EntryBranchSummary
		fromID, err := resolveBranchSummaryFromID(entries, e.raw.FromID)
		if err != nil {
			return session.Entry{}, err
		}
		out.FromID = fromID
		out.Summary = e.raw.Summary
		out.Details = e.raw.Details
		if len(e.raw.Usage) > 0 {
			var u msg.Usage
			if err := json.Unmarshal(e.raw.Usage, &u); err != nil {
				return session.Entry{}, err
			}
			out.Usage = &u
		}
		if e.raw.FromHook != nil {
			out.FromHook = *e.raw.FromHook
		}
	case "compaction":
		out.Type = session.EntryCompaction
		out.Summary = e.raw.Summary
		out.TokensBefore = e.raw.TokensBefore
		out.Details = e.raw.Details
		if len(e.raw.Usage) > 0 {
			var u msg.Usage
			if err := json.Unmarshal(e.raw.Usage, &u); err != nil {
				return session.Entry{}, err
			}
			out.Usage = &u
		}
		if e.raw.FromHook != nil {
			out.FromHook = *e.raw.FromHook
		}
		tail, err := buildRetainedTail(entries, e)
		if err != nil {
			return session.Entry{}, err
		}
		out.RetainedTail = tail
	case "custom":
		out.Type = session.EntryCustom
		out.CustomType = e.raw.CustomType
		out.Data = e.raw.Data
	default:
		return session.Entry{}, fmt.Errorf("%w: legacy v3 entry %s: unexpected retained type %q", ErrLegacyV3Unsupported, e.id, e.typ)
	}
	return out, nil
}

// selectedConfiguration walks the physical (legacy) parent chain from
// selectedID looking for the nearest model_change, thinking_level_change and
// active_tools_change, mirroring selectedConfiguration, legacy-v3.js:
// 177-208.
func selectedConfiguration(entries map[string]*legacyEntry, selectedID *string) (session.LaneConfiguration, bool, error) {
	remaining := map[string]bool{"model_change": true, "thinking_level_change": true, "active_tools_change": true}
	var model *session.ModelRef
	var thinkingLevel string
	var hasThinkingLevel bool
	var activeToolNames []string
	var hasActiveToolNames bool

	currentID := selectedID
	for currentID != nil && len(remaining) != 0 {
		e, ok := entries[*currentID]
		if !ok {
			return session.LaneConfiguration{}, false, fmt.Errorf("%w: missing legacy v3 entry reference: %s", ErrLegacyV3Unsupported, *currentID)
		}
		if remaining[e.typ] {
			delete(remaining, e.typ)
			switch e.typ {
			case "model_change":
				m := session.ModelRef{Provider: e.raw.Provider, ModelID: e.raw.ModelID}
				model = &m
			case "thinking_level_change":
				thinkingLevel = e.raw.ThinkingLevel
				hasThinkingLevel = true
			case "active_tools_change":
				activeToolNames = append([]string{}, e.raw.ActiveToolNames...)
				hasActiveToolNames = true
			}
		}
		currentID = e.parentID
	}
	if model == nil || !hasThinkingLevel {
		return session.LaneConfiguration{}, false, nil
	}
	if !hasActiveToolNames {
		activeToolNames = []string{}
	}
	return session.LaneConfiguration{Model: *model, ThinkingLevel: thinkingLevel, ActiveToolNames: activeToolNames}, true, nil
}

// buildLegacyV3Values derives the current-value writes (session name,
// labels, branch tip, lane config/state) that follow the retained entries in
// the upgraded file, mirroring normalizeLegacyV3Values, legacy-v3.js:
// 291-327. It returns the writes (each with its own seq, already following
// on from inv.nextSeq) and the next seq after all of them.
func buildLegacyV3Values(inv *legacyV3Inventory) ([]session.CommittedWrite, int64, error) {
	nextSeq := inv.nextSeq
	var out []session.CommittedWrite

	valueWrite := func(namespace, key string, value any) (session.CommittedWrite, error) {
		raw, err := json.Marshal(value)
		if err != nil {
			return session.CommittedWrite{}, err
		}
		w := &session.ValueWrite{Kind: "value", Op: "set", Seq: nextSeq, Namespace: namespace, Key: key, Value: raw}
		nextSeq++
		return session.CommittedWrite{Kind: "value", Value: w}, nil
	}

	// session name (legacy-v3.js:296-298: only if truthy).
	if inv.name != "" {
		w, err := valueWrite(session.NamespaceSessionName, "", inv.name)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, w)
	}

	// labels (legacy-v3.js:299-314): last-write-wins per target, in the
	// order each target id was first mentioned; a falsy label deletes.
	var labelOrder []string
	labelPresent := map[string]bool{}
	labelValue := map[string]string{}
	for _, id := range inv.order {
		e := inv.entries[id]
		if e.typ != "label" {
			continue
		}
		targetID, err := resolveLegacyID(inv.entries, e.raw.TargetID)
		if err != nil {
			return nil, 0, err
		}
		if targetID == nil {
			continue
		}
		if !labelPresent[*targetID] {
			labelOrder = append(labelOrder, *targetID)
		}
		if e.raw.Label != "" {
			labelPresent[*targetID] = true
			labelValue[*targetID] = e.raw.Label
		} else {
			labelPresent[*targetID] = false
			delete(labelValue, *targetID)
		}
	}
	for _, target := range labelOrder {
		if !labelPresent[target] {
			continue
		}
		w, err := valueWrite(session.NamespaceEntryLabel, target, labelValue[target])
		if err != nil {
			return nil, 0, err
		}
		out = append(out, w)
	}

	// branch tip (legacy-v3.js:315-316).
	finalMapped, err := resolveLegacyID(inv.entries, inv.finalID)
	if err != nil {
		return nil, 0, err
	}
	tipWrite, err := valueWrite(session.NamespaceBranchTip, "main", finalMapped)
	if err != nil {
		return nil, 0, err
	}
	out = append(out, tipWrite)

	// configuration (legacy-v3.js:317-325).
	cfg, ok, err := selectedConfiguration(inv.entries, inv.finalID)
	if err != nil {
		return nil, 0, err
	}
	if ok {
		cfgWrite, err := valueWrite(session.NamespaceLaneConfig, "main", cfg)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, cfgWrite)
		stateWrite, err := valueWrite(session.NamespaceLaneState, "main", session.LaneState{
			CurrentOperationID: nil,
			LastOperationID:    nil,
			Inbox:              []session.InboxItem{},
		})
		if err != nil {
			return nil, 0, err
		}
		out = append(out, stateWrite)
	}

	return out, nextSeq, nil
}

// legacyV3ParentSessionID mirrors resolveLegacyV3ParentSessionId,
// legacy-v3.js:8-14: best-effort — a missing or unparseable parent file
// means "no parent session id" rather than an error.
func legacyV3ParentSessionID(parentPath string) (string, bool) {
	f, err := os.Open(parentPath)
	if err != nil {
		return "", false
	}
	defer f.Close()
	line, err := readFirstLine(f)
	if err != nil {
		return "", false
	}
	parsed, err := ParseHeader(line)
	if err != nil {
		return "", false
	}
	if parsed.Format == FormatV4 {
		return parsed.V4.ID, true
	}
	return parsed.V3Legacy.ID, true
}

// buildLegacyV3Header derives the v4 header for an upgraded file, mirroring
// metadataFromLegacyV3Header + normalizeLegacyV3Header, legacy-v3.js:15-37.
func buildLegacyV3Header(v3 LegacyV3Header) (session.Header, error) {
	createdAt, err := parseLegacyV3Timestamp(v3.Timestamp)
	if err != nil {
		return session.Header{}, err
	}
	h := session.Header{
		V:              session.FormatVersion,
		Kind:           "header",
		ID:             v3.ID,
		StorageVersion: session.StorageVersion,
		CreatedAt:      createdAt,
		Cwd:            v3.Cwd,
	}
	if v3.ParentSession != "" {
		if id, ok := legacyV3ParentSessionID(v3.ParentSession); ok {
			h.ParentSessionID = id
		} else {
			h.LegacyParentSessionPath = v3.ParentSession
		}
	}
	return h, nil
}

// splitTerminatedLines splits data into complete (newline-terminated) lines,
// discarding a final unterminated fragment. It mirrors splitCompleteLines,
// storage.js:8-13 (used there for v4; used here identically for v3, since
// LegacyV3Source.read tolerates the same "reader still catching up to a
// writer" torn tail: legacy-v3.js:270-273, "if (!line.terminated) break").
func splitTerminatedLines(data []byte) [][]byte {
	if !bytes.HasSuffix(data, []byte("\n")) {
		if idx := bytes.LastIndexByte(data, '\n'); idx >= 0 {
			data = data[:idx+1]
		} else {
			return nil
		}
	}
	trimmed := data[:len(data)-1]
	if len(trimmed) == 0 {
		return [][]byte{[]byte("")}
	}
	return bytes.Split(trimmed, []byte("\n"))
}

// loadLegacyV3 reads path (without modifying it) and returns its derived v4
// header and its full record inventory. It mirrors LegacyV3Source.read,
// legacy-v3.js:358-372.
func loadLegacyV3(path string) (session.Header, *legacyV3Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return session.Header{}, nil, fmt.Errorf("jsonl: failed to read %s: %w", path, err)
	}
	lines := splitTerminatedLines(data)
	if len(lines) == 0 || len(lines[0]) == 0 {
		return session.Header{}, nil, fmt.Errorf("jsonl: invalid storage %s: missing header", path)
	}
	parsed, err := ParseHeader(string(lines[0]))
	if err != nil {
		return session.Header{}, nil, fmt.Errorf("jsonl: invalid storage %s: invalid header: %w", path, err)
	}
	if parsed.Format != FormatV3Legacy {
		return session.Header{}, nil, fmt.Errorf("jsonl: invalid legacy v3 storage %s: expected format 3 header", path)
	}
	header, err := buildLegacyV3Header(*parsed.V3Legacy)
	if err != nil {
		return session.Header{}, nil, fmt.Errorf("jsonl: invalid legacy v3 storage %s: %w", path, err)
	}
	inv, err := readLegacyV3Inventory(lines[1:])
	if err != nil {
		return session.Header{}, nil, fmt.Errorf("jsonl: invalid legacy v3 storage %s: %w", path, err)
	}
	return header, inv, nil
}

// UpgradeLegacyV3 rewrites the legacy v3 session file at path into a v4
// session file, atomically (through "<path>.tmp" then rename, via
// PublishJSONL/PublishFileAtomically — the same primitive Create and Fork
// use). On any error the source file is left completely untouched: every
// v3 record is read and validated (loadLegacyV3) before anything is
// written.
//
// It mirrors the combination of LegacyV3Source (structural/value
// derivation) and JsonlStorage.upgradeLegacyV3ToV4 (the atomic rewrite,
// storage.js) — with the deviations documented at the top of this file: it
// runs eagerly (called from Open, not lazily from the first commit), and it
// writes the usage-adjustment row alone as the trailing transaction line
// (pi always has at least one caller write to include alongside it, because
// pi only upgrades from inside a non-empty commit; UpgradeLegacyV3 has none).
//
// A file that is already v4, or whose header is neither v4 nor v3, is an
// error (not silently accepted) — callers (Open) are expected to check the
// header format themselves and only call this for a confirmed v3 header,
// but loadLegacyV3 re-validates regardless.
func UpgradeLegacyV3(path string, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	header, inv, err := loadLegacyV3(path)
	if err != nil {
		return err
	}

	var entryWrites []session.CommittedWrite
	for _, id := range inv.order {
		e := inv.entries[id]
		if !e.retained {
			continue
		}
		entry, err := buildV4Entry(inv.entries, e)
		if err != nil {
			return err
		}
		entryWrites = append(entryWrites, session.CommittedWrite{Kind: "entry", Entry: &entry})
	}

	valueWrites, nextSeqAfterValues, err := buildLegacyV3Values(inv)
	if err != nil {
		return err
	}

	usageID, err := uuidv7At(now().UnixMilli())
	if err != nil {
		return err
	}
	usageRow := session.UsageRow{
		ID:         usageID,
		Seq:        nextSeqAfterValues,
		Usage:      inv.importedUsage,
		Adjustment: true,
		Details:    json.RawMessage(`{"source":"v3-import"}`),
	}

	header.NextSeq = usageRow.Seq + 1

	return PublishJSONL(path, header, func(appendTx func(writes []session.CommittedWrite) error) error {
		for _, w := range entryWrites {
			if err := appendTx([]session.CommittedWrite{w}); err != nil {
				return err
			}
		}
		for _, w := range valueWrites {
			if err := appendTx([]session.CommittedWrite{w}); err != nil {
				return err
			}
		}
		return appendTx([]session.CommittedWrite{{Kind: "usage", Usage: &usageRow}})
	})
}
