package review

import (
	"encoding/json"
	"errors"
	"strings"
)

// maxDecodeTries bounds how many '{' positions decodeAnswer tries.
const maxDecodeTries = 64

// decodeAnswer finds the {"proposals": [...]} object in a reply. Small
// models put prose, a code fence or a thinking block around it, or start
// with a stray object; the first object that has a proposals list wins.
func decodeAnswer(text string) (answer, error) {
	if i := strings.LastIndex(text, "</think>"); i >= 0 {
		text = text[i+len("</think>"):]
	}
	var firstErr error
	tries := 0
	for i := 0; i < len(text) && tries < maxDecodeTries; i++ {
		if text[i] != '{' {
			continue
		}
		tries++
		var probe map[string]json.RawMessage
		if err := json.NewDecoder(strings.NewReader(text[i:])).Decode(&probe); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		raw, ok := probe["proposals"]
		if !ok {
			continue
		}
		var a answer
		if err := json.Unmarshal(raw, &a.Proposals); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		return a, nil
	}
	if firstErr == nil {
		firstErr = errors.New(`no {"proposals": [...]} object in the reply`)
	}
	return answer{}, firstErr
}
