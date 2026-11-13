package tools

import (
	"strings"
	"unicode"
)

// Automatic suggestions (kiln's <skill-suggestions> and
// <capability-suggestions>, added to a typed request without being asked)
// match more strictly than the search tools the model calls on purpose.
// Plain substring matching on every word made "Ok go ahead" suggest
// Grafana on-call tools ("ok" is in "token", "go" in "good"), and an
// export request suggest Grafana provisioning tools on "file" alone.

// suggestionStopwords are words that say nothing about a capability:
// function words and the vocabulary of acknowledgements and follow-ups.
// Terms split at apostrophes and drop under three letters, so contractions
// need no entries.
var suggestionStopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		the and for are but not you your yours with this that these those then than them they there their
		what when where which who why how can could would should will shall may might must have has had
		was were been being does did doing done its into onto from out off over under again also just
		only very too all any some each every both more most much many such own same other another here
		now yes yeah yep okay sure please thanks thank cool great good fine nice right alright continue
		proceed carry ahead keep let lets sounds sound looks look going get got make made want need like
		think know see use using one two way thing things something anything everything about after
		before while until because our ours mine yourself myself still already yet ever never always`) {
		m[w] = true
	}
	return m
}()

// suggestionTerms are the content words of a typed request: at least three
// letters and not a stopword, each once.
func suggestionTerms(query string) []string {
	var terms []string
	for _, t := range capabilityTerms(query) {
		if len([]rune(t)) >= 3 && !suggestionStopwords[t] {
			terms = append(terms, t)
		}
	}
	return terms
}

// suggestionScore matches terms against a name and description by whole
// word (or word prefix, so "dashboard" finds "dashboards"), and keeps an
// entry only when it carries enough of the request: one term for a one-
// or two-word request, half of them up to three for a longer one. The
// score orders kept entries; 0 drops one. The reason lists matched terms.
func suggestionScore(name, description string, terms []string) (int, string) {
	if len(terms) == 0 {
		return 0, ""
	}
	nameWords := words(name)
	descWords := words(description)
	score := 0
	var matched []string
	for _, term := range terms {
		stem := strings.TrimSuffix(term, "s")
		part := 0
		if hasWordPrefix(nameWords, stem) {
			part += 4
		}
		if hasWordPrefix(descWords, stem) {
			part++
		}
		if part > 0 {
			score += part
			matched = append(matched, term)
		}
	}
	if len(matched) < min(3, (len(terms)+1)/2) {
		return 0, ""
	}
	return score, strings.Join(matched, ", ")
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func hasWordPrefix(words []string, stem string) bool {
	for _, w := range words {
		if strings.HasPrefix(w, stem) {
			return true
		}
	}
	return false
}
