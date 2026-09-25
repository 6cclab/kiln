package tools

import "testing"

// FuzzFuzzyFindText feeds arbitrary (content, oldText) pairs to
// fuzzyFindText, the anchor/matching logic edit-diff.js's fuzzyFindText
// mirrors: exact match first, falling back to fuzzy matching with
// trailing whitespace, smart quotes, Unicode dashes and special spaces
// normalized away. It must never panic, including on unicode combining
// characters, mixed tabs/spaces, empty strings, and CRLF vs LF content.
func FuzzFuzzyFindText(f *testing.F) {
	seeds := []struct{ content, oldText string }{
		{"say 'hi' now", "say ‘hi’ now"},
		{"say “hi” now", `say "hi" now`},
		{"a—b", "a-b"},
		{"a–b", "a-b"},
		{"a b", "a b"},
		{"line one   \nline two", "line one\nline two"},
		{"", ""},
		{"abc", ""},
		{"", "abc"},
		{"line1\r\nline2\r\nline3", "line2"},
		{"line1\nline2\nline3", "line1\r\nline2"},
		{"tabs\t\tindent", "tabs  indent"},
		// Combining characters: "e" + combining acute vs precomposed "é".
		{"café", "café"},
		{"foo\nfoo\nfoo", "foo"},
		{"\t \t\n \t", "\t \t"},
		{"abcdef", "cdef"},
		{"abcdef", "abcd"},
	}
	for _, s := range seeds {
		f.Add(s.content, s.oldText)
	}
	f.Fuzz(func(t *testing.T, content, oldText string) {
		_ = fuzzyFindText(content, oldText)
		_ = countOccurrences(content, oldText)
	})
}
