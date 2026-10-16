package router

import (
	"strings"

	"github.com/andrepato/harness/internal/provider"
)

// LocalCandidate is one router slot's or the classifier's fast role's
// resolved model, considered for the one-model-per-host collapse
// (CollapseLocal). Slot is "plan", "planComplex", "implement", "direct"
// or "fast" -- used only to decide priority and to label the note; it is
// not itself compared.
type LocalCandidate struct {
	Slot  string
	Model provider.Model
}

// localOrder ranks LocalCandidate.Slot for CollapseLocal's tie-break: the
// first resident candidate in this order wins, and so does the first
// candidate overall when none is resident. Implement is first because it
// is the model most requests end up answering on once a session reaches
// the implementation phase; fast is last since it is not itself a router
// slot, only the classifier's own role.
var localOrder = map[string]int{
	"implement":   0,
	"plan":        1,
	"planComplex": 2,
	"direct":      3,
	"fast":        4,
}

func localRank(slot string) int {
	if r, ok := localOrder[slot]; ok {
		return r
	}
	return len(localOrder)
}

// LocalCollapse is CollapseLocal's verdict for one self-hosted server.
type LocalCollapse struct {
	// Chosen is the one model every candidate on this host moves to.
	// The zero Model when a caller ignores the returned ok.
	Chosen provider.Model
	// AlreadyLoaded reports that Chosen was in the resident set
	// CollapseLocal was given -- the startup note's "(already loaded)".
	AlreadyLoaded bool
	// Others is every other distinct candidate model this host had,
	// in candidate order, for the note's "instead of X and Y".
	Others []provider.Model
}

// normalizeModelID trims a trailing ":latest" tag, so a candidate's id
// compares equal to a /api/ps-reported id whichever form either endpoint
// used for the same model.
func normalizeModelID(id string) string {
	return strings.TrimSuffix(id, ":latest")
}

func sameModel(a, b provider.Model) bool {
	return a.Provider == b.Provider && a.ID == b.ID
}

// CollapseLocal decides whether candidates -- every router slot and the
// classifier's fast role that resolved on one self-hosted server --
// should collapse onto one model, given resident, the model ids (in
// Model.ID's own format; CollapseLocal normalises a trailing ":latest"
// itself) that server currently has loaded. resident nil or empty means
// residency could not be determined (e.g. the host's Resident call
// errored), and is treated the same as "nothing resident".
//
// ok is false when there is nothing to collapse: fewer than two distinct
// candidate models, or every one of them is already resident (the
// opt-out -- the server provably holds them all at once, so routing
// between them costs no swap).
//
// Candidates need not already be in priority order: CollapseLocal sorts
// a copy by Slot's priority (implement, plan, planComplex, direct, fast)
// before picking, so a caller may pass them in any order.
func CollapseLocal(candidates []LocalCandidate, resident []string) (LocalCollapse, bool) {
	residentSet := make(map[string]bool, len(resident))
	for _, id := range resident {
		residentSet[normalizeModelID(id)] = true
	}

	ordered := make([]LocalCandidate, len(candidates))
	copy(ordered, candidates)
	// A stable insertion sort by rank: candidates is short (at most five
	// entries -- one per router slot plus fast), so this is simpler than
	// pulling in sort.SliceStable for five elements, and preserves
	// relative order of equal-rank entries just the same.
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && localRank(ordered[j].Slot) < localRank(ordered[j-1].Slot); j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}

	seen := map[string]bool{}
	var distinct []provider.Model
	for _, c := range ordered {
		key := c.Model.Provider + "/" + c.Model.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		distinct = append(distinct, c.Model)
	}
	if len(distinct) < 2 {
		return LocalCollapse{}, false
	}

	allResident := true
	for _, m := range distinct {
		if !residentSet[normalizeModelID(m.ID)] {
			allResident = false
			break
		}
	}
	if allResident {
		return LocalCollapse{}, false
	}

	var chosen provider.Model
	loaded := false
	for _, c := range ordered {
		if residentSet[normalizeModelID(c.Model.ID)] {
			chosen, loaded = c.Model, true
			break
		}
	}
	if !loaded {
		chosen = ordered[0].Model
	}

	var others []provider.Model
	for _, m := range distinct {
		if sameModel(m, chosen) {
			continue
		}
		others = append(others, m)
	}

	return LocalCollapse{Chosen: chosen, AlreadyLoaded: loaded, Others: others}, true
}
