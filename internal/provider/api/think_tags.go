package api

import "strings"

// thinkTags are the inline reasoning wrappers open models put in the
// content field when the server does not split reasoning out into
// reasoning_content (DeepSeek-R1 distills, QwQ and Qwen3 on a llama.cpp or
// vLLM without a reasoning parser). Idea from hermes-agent's
// agent/think_scrubber.py (NousResearch, MIT).
var thinkTags = []string{"think", "thinking", "reasoning", "thought"}

// thinkSplitter separates a leading inline reasoning block from a streamed
// reply's content, across delta boundaries. Only a tag that opens the
// content (after whitespace) counts: models emit their reasoning first,
// and a tag later in a reply (a code sample about prompting, say) is the
// answer's text.
type thinkSplitter struct {
	state    int // splitUndecided, splitThinking or splitText
	closeTag string
	// held is content not yet emitted: the start of the reply while it may
	// still be an open tag, or a tail that may be the start of the close tag.
	held string
	// trimLead drops the whitespace a model puts between its close tag and
	// the answer.
	trimLead bool
}

const (
	splitUndecided = iota
	splitThinking
	splitText
)

// feed takes one content delta and returns what of it is reasoning and
// what is reply text.
func (s *thinkSplitter) feed(delta string) (thinking, text string) {
	switch s.state {
	case splitUndecided:
		s.held += delta
		lead := strings.TrimLeft(s.held, " \t\r\n")
		if lead == "" {
			return "", ""
		}
		for _, tag := range thinkTags {
			open := "<" + tag + ">"
			if strings.HasPrefix(lead, open) {
				s.state, s.closeTag, s.held = splitThinking, "</"+tag+">", ""
				return s.feed(lead[len(open):])
			}
			if strings.HasPrefix(open, lead) {
				return "", "" // may still become this tag
			}
		}
		s.state = splitText
		text, s.held = s.held, ""
		return "", text
	case splitThinking:
		buf := s.held + delta
		s.held = ""
		if i := strings.Index(buf, s.closeTag); i >= 0 {
			s.state, s.trimLead = splitText, true
			_, text = s.feed(buf[i+len(s.closeTag):])
			return buf[:i], text
		}
		// Hold back a tail that could be the start of the close tag.
		for n := len(s.closeTag) - 1; n > 0; n-- {
			if strings.HasSuffix(buf, s.closeTag[:n]) {
				s.held = buf[len(buf)-n:]
				return buf[:len(buf)-n], ""
			}
		}
		return buf, ""
	default:
		if s.trimLead {
			delta = strings.TrimLeft(delta, " \t\r\n")
			s.trimLead = delta == ""
		}
		return "", delta
	}
}

// flush returns whatever is still held when the stream ends: an unclosed
// reasoning block stays reasoning (the model was cut off mid-thought).
func (s *thinkSplitter) flush() (thinking, text string) {
	held := s.held
	s.held = ""
	if s.state == splitThinking {
		return held, ""
	}
	return "", held
}
