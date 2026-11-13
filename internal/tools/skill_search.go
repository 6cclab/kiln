package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"github.com/andrepato/harness/internal/tool"
)

// SearchSkills uses local lexical matching; discovery never needs a model call.
// Empty queries browse the catalog in stable name order. Bodies are not indexed.
func SearchSkills(records []SkillRecord, query string, limit int) []SkillRecord {
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	type hit struct {
		record SkillRecord
		score  int
	}
	var hits []hit
	for _, r := range records {
		score := 0
		name, description := strings.ToLower(r.Name), strings.ToLower(r.Description)
		seen := map[string]bool{}
		for _, term := range terms {
			if seen[term] {
				continue
			}
			seen[term] = true
			if strings.Contains(name, term) {
				score += 4
			}
			if strings.Contains(description, term) {
				score++
			}
		}
		if len(terms) == 0 || score > 0 {
			hits = append(hits, hit{r, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].record.Name < hits[j].record.Name
	})
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	var out []SkillRecord
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.record)
	}
	return out
}

// SkillSearchText bounds results independently of catalog size, including names.
func SkillSearchText(records []SkillRecord, query string, tokens int) string {
	return skillLines(SearchSkills(records, query, 20), tokens)
}

// SuggestSkills is SearchSkills for the suggestions kiln adds on its own
// to a typed request, with SuggestCapabilities' stricter matching.
func SuggestSkills(records []SkillRecord, query string, limit int) []SkillRecord {
	terms := suggestionTerms(query)
	if len(terms) == 0 {
		return nil
	}
	type hit struct {
		record SkillRecord
		score  int
	}
	var hits []hit
	for _, r := range records {
		if score, _ := suggestionScore(r.Name, r.Description, terms); score > 0 {
			hits = append(hits, hit{r, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].record.Name < hits[j].record.Name
	})
	var out []SkillRecord
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.record)
	}
	return out
}

// SkillSuggestionText is SkillSearchText over SuggestSkills.
func SkillSuggestionText(records []SkillRecord, query string, tokens int) string {
	return skillLines(SuggestSkills(records, query, 20), tokens)
}

// skillLines renders records as "name: description" lines within tokens.
func skillLines(records []SkillRecord, tokens int) string {
	if tokens <= 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range records {
		description := []rune(strings.Join(strings.Fields(r.Description), " "))
		if len(description) > 240 {
			description = append(description[:237], '.', '.', '.')
		}
		line := r.Name + ": " + string(description) + "\n"
		if b.Len()+len(line) > tokens*4 {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

func SkillSearchTool(records []SkillRecord, budget int) *tool.Tool {
	return &tool.Tool{Name: "skill_search", Label: "Skill search", Description: "Find installed skills by topic or name. Empty query browses names. Invoke a result with skill to load its instructions.", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`), Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		var a struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return tool.Result{}, err
		}
		text := SkillSearchText(records, a.Query, budget)
		if text == "" {
			text = "No matching skills fit the discovery budget. Try a narrower query or invoke an exact name with skill."
		}
		return tool.Text(text), nil
	}}
}
