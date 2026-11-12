package learning

import (
	"fmt"
	"testing"
)

func TestClassifyRunShape(t *testing.T) {
	for _, tc := range []struct {
		edits, checks int
		want          string
	}{
		{0, 0, RunShapeQuestion},
		{0, 2, RunShapeQuestion},
		{1, 1, RunShapeCodeChange},
		{3, 0, RunShapeCodeChangeUnverified},
	} {
		if got := ClassifyRunShape(tc.edits, tc.checks); got != tc.want {
			t.Errorf("ClassifyRunShape(%d, %d) = %q, want %q", tc.edits, tc.checks, got, tc.want)
		}
	}
}

// Runs that changed files without a check produce a reviewable suggestion
// once they are at least half of the file-changing runs; records without a
// shape (saved before shapes existed) are left out of the count.
func TestSuggestionsUnverifiedChanges(t *testing.T) {
	shaped := func(n int, shape string) []Outcome {
		out := make([]Outcome, n)
		for i := range out {
			out[i] = sampleOutcome(fmt.Sprintf("%s-%d", shape, i))
			out[i].Shape = shape
		}
		return out
	}
	find := func(s []Suggestion) *Suggestion {
		for i := range s {
			if s[i].Kind == "unverified-changes" {
				return &s[i]
			}
		}
		return nil
	}
	mixed := append(shaped(3, RunShapeCodeChangeUnverified), shaped(2, RunShapeCodeChange)...)
	got := find(Suggestions(mixed, 5))
	if got == nil || got.AffectedRuns != 3 || got.Samples != 5 {
		t.Fatalf("suggestion = %+v, want 3 of 5 file-changing runs", got)
	}
	if err := mixed[0].validate(); err != nil {
		t.Fatalf("shaped outcome invalid: %v", err)
	}
	if find(Suggestions(append(shaped(3, RunShapeCodeChangeUnverified), shaped(4, RunShapeCodeChange)...), 5)) != nil {
		t.Error("suggested while most file-changing runs ran a check")
	}
	if find(Suggestions(append(shaped(3, RunShapeCodeChangeUnverified), shaped(5, "")...), 5)) != nil {
		t.Error("unshaped records counted as file-changing runs")
	}
	bad := sampleOutcome("bad")
	bad.Shape = "feature"
	if bad.validate() == nil {
		t.Error("unknown run shape accepted")
	}
}
