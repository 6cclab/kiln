package session

import (
	"fmt"
	"strings"
)

// ForkScope selects how much of a session Fork copies.
type ForkScope string

const (
	ForkScopeTree   ForkScope = "tree"
	ForkScopeBranch ForkScope = "branch"
)

// BranchForkOptions selects one branch's ancestry to fork.
type BranchForkOptions struct {
	Branch   string
	EntryID  string // "" means the branch's current tip
	Position string // "before" | "at" (default "at")
}

// ForkPlan is the resolved outcome of a fork request: either the whole tree,
// or one branch's copied entries plus its destination tip.
type ForkPlan struct {
	Scope          ForkScope
	Branch         string
	DestinationTip *string // nil means the branch root
}

// BranchForkOptionsSource is the lookup surface SelectBranchFork needs.
type BranchForkOptionsSource struct {
	Tip         *string
	TipKnown    bool
	GetParent   func(entryID string) (parentID *string, ok bool)
	SelectEntry func(entryID string)
}

// SelectBranchFork walks a branch from its tip to the root, selecting the
// entries to copy and identifying the destination tip. It mirrors
// selectBranchFork in fork-policy.js.
func SelectBranchFork(options BranchForkOptions, source BranchForkOptionsSource) (ForkPlan, error) {
	if !source.TipKnown {
		return ForkPlan{}, fmt.Errorf("session: unknown source branch: %s", options.Branch)
	}
	// options.EntryID == "" means "unspecified": default to the branch's
	// current tip (which may itself be nil, meaning the branch is empty).
	var requested *string
	if options.EntryID != "" {
		id := options.EntryID
		requested = &id
	} else {
		requested = source.Tip
	}
	found := requested == nil
	var destinationTip *string
	entryID := source.Tip

	for entryID != nil {
		parentID, ok := source.GetParent(*entryID)
		if !ok {
			return ForkPlan{}, fmt.Errorf("session: corrupt source branch: missing parent %s", *entryID)
		}
		if requested != nil && *entryID == *requested {
			found = true
			if options.Position == "before" {
				destinationTip = parentID
			} else {
				destinationTip = entryID
				source.SelectEntry(*entryID)
			}
		} else if found {
			source.SelectEntry(*entryID)
		}
		entryID = parentID
	}
	if !found {
		return ForkPlan{}, fmt.Errorf("session: fork entry %v is not on source branch %q", requested, options.Branch)
	}
	return ForkPlan{Scope: ForkScopeBranch, Branch: options.Branch, DestinationTip: destinationTip}, nil
}

// ProjectForkCurrentStateWrite decides what one current scalar/list write
// becomes in the destination, or nil to drop it. It mirrors
// projectForkCurrentStateWrite in fork-policy.js.
func ProjectForkCurrentStateWrite(write ValueWrite, plan ForkPlan, isEntryCopied func(entryID string) bool) (*ValueWrite, error) {
	switch write.Namespace {
	case NamespaceSessionName:
		w := write
		return &w, nil
	case NamespaceEntryLabel:
		if isEntryCopied(write.Key) {
			w := write
			return &w, nil
		}
		return nil, nil
	case NamespaceBranchTip:
		if plan.Scope == ForkScopeTree {
			w := write
			return &w, nil
		}
		if write.Key == plan.Branch {
			w := write
			raw, err := marshalNullableString(plan.DestinationTip)
			if err != nil {
				return nil, err
			}
			w.Value = raw
			return &w, nil
		}
		return nil, nil
	case NamespaceLaneConfig:
		if plan.Scope == ForkScopeTree || write.Key == plan.Branch {
			w := write
			return &w, nil
		}
		return nil, nil
	case NamespaceLaneState:
		if plan.Scope == ForkScopeTree || write.Key == plan.Branch {
			w := write
			w.Value = []byte(`{"currentOperationId":null,"lastOperationId":null,"inbox":[]}`)
			return &w, nil
		}
		return nil, nil
	case NamespaceResult:
		return nil, nil
	}
	if strings.HasPrefix(write.Namespace, "pi.op.") || strings.HasPrefix(write.Namespace, "pi.pending.") {
		return nil, nil
	}
	if write.Namespace == "pi" || strings.HasPrefix(write.Namespace, "pi.") {
		return nil, fmt.Errorf("session: unknown reserved fork namespace: %s", write.Namespace)
	}
	if plan.Scope == ForkScopeTree {
		w := write
		return &w, nil
	}
	return nil, nil
}

func marshalNullableString(s *string) ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	return []byte(`"` + jsonEscape(*s) + `"`), nil
}

func jsonEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
