package session

import (
	"testing"
)

// branchGraph is a tiny fake parent-chain source for SelectBranchFork tests:
// root -> a -> b -> c (tip).
type branchGraph struct {
	tip      *string
	tipKnown bool
	parents  map[string]*string // entryID -> parentID (nil = root)
	selected []string
}

func newBranchGraph() *branchGraph {
	root := "a"
	a := "a"
	b := "b"
	c := "c"
	_ = root
	tip := c
	return &branchGraph{
		tip:      &tip,
		tipKnown: true,
		parents: map[string]*string{
			"a": nil,
			"b": &a,
			"c": &b,
		},
	}
}

func (g *branchGraph) source() BranchForkOptionsSource {
	return BranchForkOptionsSource{
		Tip:      g.tip,
		TipKnown: g.tipKnown,
		GetParent: func(entryID string) (*string, bool) {
			p, ok := g.parents[entryID]
			return p, ok
		},
		SelectEntry: func(entryID string) {
			g.selected = append(g.selected, entryID)
		},
	}
}

func TestSelectBranchForkFoundAtTip(t *testing.T) {
	g := newBranchGraph()
	plan, err := SelectBranchFork(BranchForkOptions{Branch: "main"}, g.source())
	if err != nil {
		t.Fatalf("SelectBranchFork: %v", err)
	}
	if plan.Scope != ForkScopeBranch || plan.Branch != "main" {
		t.Fatalf("plan = %+v, want scope=branch branch=main", plan)
	}
	if plan.DestinationTip == nil || *plan.DestinationTip != "c" {
		t.Fatalf("DestinationTip = %v, want c", plan.DestinationTip)
	}
	// Copies the whole chain c, b, a (tip to root, in that walk order).
	wantSelected := []string{"c", "b", "a"}
	if !equalStrSlices(g.selected, wantSelected) {
		t.Fatalf("selected = %v, want %v", g.selected, wantSelected)
	}
}

func TestSelectBranchForkFoundViaExplicitEntryIDAt(t *testing.T) {
	g := newBranchGraph()
	plan, err := SelectBranchFork(BranchForkOptions{Branch: "main", EntryID: "b", Position: "at"}, g.source())
	if err != nil {
		t.Fatalf("SelectBranchFork: %v", err)
	}
	if plan.DestinationTip == nil || *plan.DestinationTip != "b" {
		t.Fatalf("DestinationTip = %v, want b", plan.DestinationTip)
	}
	wantSelected := []string{"b", "a"}
	if !equalStrSlices(g.selected, wantSelected) {
		t.Fatalf("selected = %v, want %v", g.selected, wantSelected)
	}
}

func TestSelectBranchForkPositionBefore(t *testing.T) {
	g := newBranchGraph()
	plan, err := SelectBranchFork(BranchForkOptions{Branch: "main", EntryID: "b", Position: "before"}, g.source())
	if err != nil {
		t.Fatalf("SelectBranchFork: %v", err)
	}
	if plan.DestinationTip == nil || *plan.DestinationTip != "a" {
		t.Fatalf("DestinationTip = %v, want a (b's parent)", plan.DestinationTip)
	}
	// The walk is tip (c) -> b -> a. "before" matches at "b" without
	// selecting it, then selects everything walked afterward (toward the
	// root): just "a". "c" is walked before the match and is never
	// selected because found flips true only once the match is reached.
	wantSelected := []string{"a"}
	if !equalStrSlices(g.selected, wantSelected) {
		t.Fatalf("selected = %v, want %v", g.selected, wantSelected)
	}
}

func TestSelectBranchForkEntryNotOnBranch(t *testing.T) {
	g := newBranchGraph()
	_, err := SelectBranchFork(BranchForkOptions{Branch: "main", EntryID: "not-on-branch"}, g.source())
	if err == nil {
		t.Fatalf("SelectBranchFork with unknown entry id: got nil error, want error")
	}
}

func TestSelectBranchForkUnknownSourceBranch(t *testing.T) {
	g := newBranchGraph()
	g.tipKnown = false
	_, err := SelectBranchFork(BranchForkOptions{Branch: "ghost"}, g.source())
	if err == nil {
		t.Fatalf("SelectBranchFork with unknown source branch: got nil error, want error")
	}
}

func TestSelectBranchForkCorruptParent(t *testing.T) {
	g := newBranchGraph()
	delete(g.parents, "b") // "c"'s parent chain now hits a missing lookup at "b"
	_, err := SelectBranchFork(BranchForkOptions{Branch: "main"}, g.source())
	if err == nil {
		t.Fatalf("SelectBranchFork with missing parent lookup: got nil error, want error")
	}
}

func TestSelectBranchForkEmptyBranchTipNil(t *testing.T) {
	g := &branchGraph{tip: nil, tipKnown: true, parents: map[string]*string{}}
	plan, err := SelectBranchFork(BranchForkOptions{Branch: "empty"}, g.source())
	if err != nil {
		t.Fatalf("SelectBranchFork on empty branch: %v", err)
	}
	if plan.DestinationTip != nil {
		t.Fatalf("DestinationTip = %v, want nil for empty branch", plan.DestinationTip)
	}
	if len(g.selected) != 0 {
		t.Fatalf("selected = %v, want empty for empty branch", g.selected)
	}
}

func equalStrSlices(a, b []string) bool {
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

// --- ProjectForkCurrentStateWrite ---------------------------------------

func alwaysCopied(string) bool { return true }
func neverCopied(string) bool  { return false }

func TestProjectForkSessionNameAlwaysCopied(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceSessionName, Key: "", Value: []byte(`"x"`)}
	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main"}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got == nil || got.Value == nil {
		t.Fatalf("session name write dropped, want kept")
	}
}

func TestProjectForkEntryLabelCopiedOrDropped(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceEntryLabel, Key: "entry-1", Value: []byte(`"label"`)}

	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main"}, alwaysCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got == nil {
		t.Fatalf("entry label write dropped when entry was copied, want kept")
	}

	got2, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main"}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got2 != nil {
		t.Fatalf("entry label write kept when entry was not copied, want dropped")
	}
}

func TestProjectForkBranchTipTreeScope(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceBranchTip, Key: "other-branch", Value: []byte(`"tip-id"`)}
	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeTree}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got == nil || string(got.Value) != `"tip-id"` {
		t.Fatalf("tree-scope branch tip write = %v, want unchanged copy", got)
	}
}

func TestProjectForkBranchTipBranchScopeMatchingBranch(t *testing.T) {
	tip := "new-tip"
	w := ValueWrite{Namespace: NamespaceBranchTip, Key: "main", Value: []byte(`"old-tip"`)}
	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main", DestinationTip: &tip}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got == nil {
		t.Fatalf("branch-scope branch tip write dropped, want rewritten")
	}
	if string(got.Value) != `"new-tip"` {
		t.Fatalf("branch tip value = %s, want rewritten to destination tip", got.Value)
	}
}

func TestProjectForkBranchTipBranchScopeMatchingBranchNilDestination(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceBranchTip, Key: "main", Value: []byte(`"old-tip"`)}
	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main", DestinationTip: nil}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got == nil {
		t.Fatalf("branch-scope branch tip write dropped, want rewritten")
	}
	if string(got.Value) != "null" {
		t.Fatalf("branch tip value = %s, want null for nil destination tip", got.Value)
	}
}

func TestProjectForkBranchTipBranchScopeOtherBranchDropped(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceBranchTip, Key: "other", Value: []byte(`"old-tip"`)}
	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main"}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got != nil {
		t.Fatalf("branch tip for other branch kept, want dropped: %+v", got)
	}
}

func TestProjectForkLaneConfigTreeAndBranchScope(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceLaneConfig, Key: "main", Value: []byte(`{}`)}

	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeTree}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got == nil {
		t.Fatalf("lane config dropped in tree scope, want kept")
	}

	got2, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main"}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got2 == nil {
		t.Fatalf("lane config dropped for matching branch, want kept")
	}

	got3, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "other"}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got3 != nil {
		t.Fatalf("lane config kept for non-matching branch, want dropped: %+v", got3)
	}
}

func TestProjectForkLaneStateResetToFixedJSON(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceLaneState, Key: "main", Value: []byte(`{"currentOperationId":"op-1","lastOperationId":"op-0","inbox":[{"entryId":"e1","kind":"x"}]}`)}
	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main"}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got == nil {
		t.Fatalf("lane state dropped, want kept with reset value")
	}
	want := `{"currentOperationId":null,"lastOperationId":null,"inbox":[]}`
	if string(got.Value) != want {
		t.Fatalf("lane state value = %s, want %s", got.Value, want)
	}
}

func TestProjectForkLaneStateOtherBranchDropped(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceLaneState, Key: "other", Value: []byte(`{}`)}
	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main"}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got != nil {
		t.Fatalf("lane state for other branch kept, want dropped: %+v", got)
	}
}

func TestProjectForkResultAlwaysDropped(t *testing.T) {
	w := ValueWrite{Namespace: NamespaceResult, Key: "op-1", Value: []byte(`{}`)}
	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeTree}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got != nil {
		t.Fatalf("pi.result write kept, want always dropped: %+v", got)
	}
}

func TestProjectForkOpAndPendingPrefixesDropped(t *testing.T) {
	cases := []string{
		NamespaceOpMeta, NamespaceOpState, NamespaceOpToolArgs, NamespaceOpToolMemo, NamespaceOpPreparation,
		NamespacePendingEntry, NamespacePendingToolOutput, NamespacePendingAssistantFrame,
	}
	for _, ns := range cases {
		w := ValueWrite{Namespace: ns, Key: "k", Value: []byte(`{}`)}
		got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeTree}, neverCopied)
		if err != nil {
			t.Fatalf("ProjectForkCurrentStateWrite(%s): %v", ns, err)
		}
		if got != nil {
			t.Errorf("ProjectForkCurrentStateWrite(%s) kept, want dropped (pi.op./pi.pending. prefix)", ns)
		}
	}
}

func TestProjectForkUnknownPiPrefixErrors(t *testing.T) {
	w := ValueWrite{Namespace: "pi.something.unknown", Key: "k", Value: []byte(`{}`)}
	_, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeTree}, neverCopied)
	if err == nil {
		t.Fatalf("ProjectForkCurrentStateWrite(unknown pi.* namespace): got nil error, want error")
	}
}

func TestProjectForkBarePiNamespaceErrors(t *testing.T) {
	w := ValueWrite{Namespace: "pi", Key: "k", Value: []byte(`{}`)}
	_, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeTree}, neverCopied)
	if err == nil {
		t.Fatalf("ProjectForkCurrentStateWrite(bare \"pi\" namespace): got nil error, want error")
	}
}

func TestProjectForkNonPiNamespaceTreeVsBranchScope(t *testing.T) {
	w := ValueWrite{Namespace: "app.custom", Key: "k", Value: []byte(`{}`)}

	got, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeTree}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got == nil {
		t.Fatalf("non-pi namespace dropped in tree scope, want kept")
	}

	got2, err := ProjectForkCurrentStateWrite(w, ForkPlan{Scope: ForkScopeBranch, Branch: "main"}, neverCopied)
	if err != nil {
		t.Fatalf("ProjectForkCurrentStateWrite: %v", err)
	}
	if got2 != nil {
		t.Fatalf("non-pi namespace kept in branch scope, want dropped: %+v", got2)
	}
}
