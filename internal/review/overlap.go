package review

import (
	"os"
	"strings"
	"unicode"
)

// A lesson lives in one place. A memory note whose words mostly appear in
// what a skill proposal adds (or in a note already kept) says the same
// thing twice: the skill, which carries the task's context, keeps it.
const (
	duplicateShare    = 0.6 // share of a note's words found elsewhere
	duplicateMinWords = 4   // shorter notes are never judged duplicates
)

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true, "from": true, "into": true,
	"are": true, "was": true, "were": true, "not": true, "but": true, "all": true, "any": true, "use": true,
	"when": true, "then": true, "than": true, "here": true, "there": true, "its": true, "it's": true, "you": true,
	"they": true, "them": true, "their": true, "has": true, "have": true, "had": true, "will": true, "should": true,
	"must": true, "only": true, "also": true, "always": true, "never": true, "before": true, "after": true, "repo": true,
	"project": true, "person": true, "user": true, "said": true, "says": true, "wants": true, "prefers": true,
}

// words is text's significant words: lowercase, three or more letters or
// digits, without stop words.
func words(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '\''
	}) {
		w = strings.Trim(w, "-'")
		if len(w) >= 3 && !stopWords[w] {
			out[w] = true
		}
	}
	return out
}

// sameLesson reports whether note's words mostly appear in text.
func sameLesson(note, text string) bool {
	nw := words(note)
	if len(nw) < duplicateMinWords {
		return false
	}
	tw := words(text)
	found := 0
	for w := range nw {
		if tw[w] {
			found++
		}
	}
	return float64(found) >= duplicateShare*float64(len(nw))
}

// addedText is the lines of next that are not in prev: what a skill
// proposal adds.
func addedText(prev, next string) string {
	had := map[string]bool{}
	for _, l := range strings.Split(prev, "\n") {
		had[strings.TrimSpace(l)] = true
	}
	var b strings.Builder
	for _, l := range strings.Split(next, "\n") {
		if t := strings.TrimSpace(l); t != "" && !had[t] {
			b.WriteString(t)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// duplicateOf names where note's lesson already is: a skill proposal in
// ps, or a kept memory note; "" when nowhere.
func duplicateOf(note Proposal, ps proposals, in Input) string {
	text := note.Text
	for _, p := range ps {
		if p.Kind != KindSkill {
			continue
		}
		prev := ""
		if p.Base != "" {
			if data, err := readSkillFile(p.SkillPath(in.SkillsDir)); err == nil {
				prev = data
			}
		}
		if sameLesson(text, addedText(prev, p.Content)) {
			return "the proposal for skill " + p.Target()
		}
	}
	for _, r := range in.Memory {
		if r.Key != note.Key && sameLesson(text, r.Text) {
			return "the memory note " + r.Key
		}
	}
	return ""
}

// dropDuplicateNotes removes memory proposals that repeat a skill
// proposal or a kept note, and names them.
func (ps *proposals) dropDuplicateNotes(in Input) []string {
	var kept proposals
	var dropped []string
	for _, p := range *ps {
		if p.Kind == KindMemory {
			if where := duplicateOf(p, *ps, in); where != "" {
				dropped = append(dropped, p.Key+" (repeats "+where+")")
				continue
			}
		}
		kept = append(kept, p)
	}
	*ps = kept
	return dropped
}

func readSkillFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
