package jsonl

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/andrepato/harness/internal/session"
)

// ForkOptions selects what Fork copies from a source session.
type ForkOptions struct {
	Scope session.ForkScope

	// Scope == ForkScopeBranch
	Branch   string
	EntryID  string
	Position string // "before" | "at" (default "at")

	// Destination
	ID string // "" generates a fresh uuidv7
}

type forkIndex struct {
	currentScalarSeqs      map[string]int64
	branchTips             map[string]*string
	firstSurvivingListSeqs map[string]int64
	entryParents           map[string]*string
	copiedEntryIDs         map[string]bool
	laneConfigs            map[string]bool
	laneStates             map[string]bool
}

func newForkIndex() *forkIndex {
	return &forkIndex{
		currentScalarSeqs:      map[string]int64{},
		branchTips:             map[string]*string{},
		firstSurvivingListSeqs: map[string]int64{},
		entryParents:           map[string]*string{},
		copiedEntryIDs:         map[string]bool{},
		laneConfigs:            map[string]bool{},
		laneStates:             map[string]bool{},
	}
}

func (idx *forkIndex) applyWrites(writes []session.CommittedWrite) {
	for _, w := range writes {
		switch w.Kind {
		case "entry":
			idx.entryParents[w.Entry.ID] = w.Entry.ParentID
		case "value":
			key := physicalForkKey(w.Value.Namespace, w.Value.Key)
			if w.Value.Op == "delete" {
				delete(idx.currentScalarSeqs, key)
			} else {
				idx.currentScalarSeqs[key] = w.Value.Seq
			}
			idx.applyLaneValue(*w.Value)
		case "list":
			key := physicalForkKey(w.Value.Namespace, w.Value.Key)
			if w.Value.Op == "delete" {
				delete(idx.firstSurvivingListSeqs, key)
			} else if _, ok := idx.firstSurvivingListSeqs[key]; !ok {
				idx.firstSurvivingListSeqs[key] = w.Value.Seq
			}
		}
	}
}

func (idx *forkIndex) applyLaneValue(w session.ValueWrite) {
	present := w.Op == "set"
	switch w.Namespace {
	case session.NamespaceBranchTip:
		if present {
			var v *string
			_ = jsonUnmarshalNullableString(w.Value, &v)
			idx.branchTips[w.Key] = v
		} else {
			delete(idx.branchTips, w.Key)
		}
	case session.NamespaceLaneConfig:
		if present {
			idx.laneConfigs[w.Key] = true
		} else {
			delete(idx.laneConfigs, w.Key)
		}
	case session.NamespaceLaneState:
		if present {
			idx.laneStates[w.Key] = true
		} else {
			delete(idx.laneStates, w.Key)
		}
	}
}

func jsonUnmarshalNullableString(raw []byte, out **string) error {
	s := strings.TrimSpace(string(raw))
	if s == "null" || s == "" {
		*out = nil
		return nil
	}
	unquoted := strings.Trim(s, `"`)
	v := unquoted
	*out = &v
	return nil
}

func physicalForkKey(namespace, key string) string { return namespace + "\x00" + key }

func (idx *forkIndex) hasCompleteLane(branch string) bool {
	return idx.laneConfigs[branch] && idx.laneStates[branch]
}

func (idx *forkIndex) getCurrentScalarSeq(namespace, key string) (int64, bool) {
	seq, ok := idx.currentScalarSeqs[physicalForkKey(namespace, key)]
	return seq, ok
}

func (idx *forkIndex) isSurvivingListElement(namespace, key string, seq int64) bool {
	first, ok := idx.firstSurvivingListSeqs[physicalForkKey(namespace, key)]
	return ok && seq >= first
}

// readSourceTransactions reads and parses every complete transaction after
// the header, stopping (without including) the first transaction whose
// first write's seq is >= stopBeforeSeq (0 means read to EOF). It never
// splits a transaction at the boundary — like fork.js, a straddling
// transaction is an error, which cannot occur for a boundary captured via
// Storage.NextSeq on the same file.
func readSourceTransactions(path string, stopBeforeSeq int64, yield func(writes []session.CommittedWrite) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("jsonl: failed to open fork source %s: %w", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	if !scanner.Scan() {
		return fmt.Errorf("jsonl: fork source %s has no header", path)
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		writes, err := ParseTransaction(line)
		if err != nil {
			return err
		}
		if len(writes) > 0 && stopBeforeSeq > 0 {
			first, last := writes[0].Seq(), writes[len(writes)-1].Seq()
			if first >= stopBeforeSeq {
				break
			}
			if last >= stopBeforeSeq {
				return fmt.Errorf("jsonl: transaction crosses fork sequence boundary %d", stopBeforeSeq)
			}
		}
		if err := yield(writes); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// Fork creates a new session at destPath by copying source (never modifying
// it) according to opts. It mirrors runJsonlFork in fork.js and only ever
// sees a v4 sourcePath: callers reach Fork by first calling Open on the
// source, and Open upgrades a legacy v3 file to v4 (as a side effect, before
// Fork ever reads it) rather than Fork special-casing v3 itself. See
// legacy_v3.go for how that differs from pi, which forks a closed v3
// source directly from LegacyV3Source without touching the original file.
func Fork(sourcePath string, sourceHeader session.Header, sourceNextSeq int64, destPath string, opts ForkOptions, now func() int64) error {
	idx := newForkIndex()
	if err := readSourceTransactions(sourcePath, sourceNextSeq, func(writes []session.CommittedWrite) error {
		idx.applyWrites(writes)
		return nil
	}); err != nil {
		return err
	}

	var plan session.ForkPlan
	if opts.Scope == session.ForkScopeTree {
		plan = session.ForkPlan{Scope: session.ForkScopeTree}
	} else {
		tip, tipKnown := idx.branchTips[opts.Branch]
		p, err := session.SelectBranchFork(session.BranchForkOptions{
			Branch:   opts.Branch,
			EntryID:  opts.EntryID,
			Position: opts.Position,
		}, forkBranchSource(idx, tip, tipKnown))
		if err != nil {
			return err
		}
		if !idx.hasCompleteLane(opts.Branch) {
			return fmt.Errorf("jsonl: source branch %q is not a configured AgentLane", opts.Branch)
		}
		plan = p
	}
	isEntryCopied := func(entryID string) bool {
		if plan.Scope == session.ForkScopeTree {
			return true
		}
		return idx.copiedEntryIDs[entryID]
	}

	destHeader := sourceHeader
	destHeader.ID = opts.ID
	if destHeader.ID == "" {
		id, err := uuidv7At(now())
		if err != nil {
			return err
		}
		destHeader.ID = id
	}
	destHeader.CreatedAt = now()
	destHeader.ParentSessionID = sourceHeader.ID
	destHeader.NextSeq = sourceNextSeq

	return PublishJSONL(destPath, destHeader, func(append func(writes []session.CommittedWrite) error) error {
		return readSourceTransactions(sourcePath, sourceNextSeq, func(writes []session.CommittedWrite) error {
			for _, w := range writes {
				projected, err := projectForkWrite(w, idx, plan, isEntryCopied)
				if err != nil {
					return err
				}
				if projected == nil {
					continue
				}
				if err := append([]session.CommittedWrite{*projected}); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func forkBranchSource(idx *forkIndex, tip *string, tipKnown bool) session.BranchForkOptionsSource {
	return session.BranchForkOptionsSource{
		Tip:      tip,
		TipKnown: tipKnown,
		GetParent: func(entryID string) (*string, bool) {
			p, ok := idx.entryParents[entryID]
			return p, ok
		},
		SelectEntry: func(entryID string) { idx.copiedEntryIDs[entryID] = true },
	}
}

func projectForkWrite(w session.CommittedWrite, idx *forkIndex, plan session.ForkPlan, isEntryCopied func(string) bool) (*session.CommittedWrite, error) {
	switch w.Kind {
	case "entry":
		if !isEntryCopied(w.Entry.ID) {
			return nil, nil
		}
		return &w, nil
	case "value":
		if w.Value.Op != "set" {
			return nil, nil
		}
		seq, ok := idx.getCurrentScalarSeq(w.Value.Namespace, w.Value.Key)
		if !ok || seq != w.Value.Seq {
			return nil, nil
		}
		projected, err := session.ProjectForkCurrentStateWrite(*w.Value, plan, isEntryCopied)
		if err != nil || projected == nil {
			return nil, err
		}
		return &session.CommittedWrite{Kind: "value", Value: projected}, nil
	case "list":
		if w.Value.Op != "append" {
			return nil, nil
		}
		if !idx.isSurvivingListElement(w.Value.Namespace, w.Value.Key, w.Value.Seq) {
			return nil, nil
		}
		projected, err := session.ProjectForkCurrentStateWrite(*w.Value, plan, isEntryCopied)
		if err != nil || projected == nil {
			return nil, err
		}
		return &session.CommittedWrite{Kind: "list", Value: projected}, nil
	case "usage":
		return nil, nil
	}
	return nil, nil
}
