package tool

import (
	"strings"
	"testing"
)

// TestCheckArgs_MalformedInputIsRefused: input the decoder cannot finish
// reading is refused with a message, never let through.
func TestCheckArgs_MalformedInputIsRefused(t *testing.T) {
	cases := map[string]string{
		"unterminated array":   `{"edits":[{"oldText":"a"}`,
		"unterminated object":  `{"path":"a"`,
		"trailing comma":       `{"path":"a",}`,
		"missing value":        `{"path":}`,
		"boolean":              `true`,
		"false":                `false`,
		"unterminated nested":  `{"edits":[1,`,
		"object not closed":    `{"path":"a","content":"b"`,
		"array of one element": `{"edits":[`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if err := CheckArgs([]byte(raw), editSchema, ArgsStrict); err == nil {
				t.Fatalf("CheckArgs(%s) = nil, want a refusal", raw)
			}
		})
	}
	if err := CheckArgs([]byte(`true`), editSchema, ArgsStrict); err == nil || !strings.Contains(err.Error(), "a boolean") {
		t.Errorf("CheckArgs(true) = %v, want it to name a boolean", err)
	}
}

// TestCheckArgs_InvalidUTF8Key: a key with invalid UTF-8 decodes to
// U+FFFD; folding must keep it distinct from a real parameter and still
// catch it duplicated.
func TestCheckArgs_InvalidUTF8Key(t *testing.T) {
	if err := CheckArgs([]byte("{\"pa\xffth\":\"x\",\"path\":\"a\",\"content\":\"b\"}"), writeSchema, ArgsPassThrough); err != nil {
		t.Errorf("a distinct invalid-UTF-8 key was refused: %v", err)
	}
	if err := CheckArgs([]byte("{\"\xff\":1,\"\xfe\":2}"), writeSchema, ArgsPassThrough); err == nil {
		t.Error("two keys that both decode to U+FFFD were accepted")
	}
}

// TestCheckArgsMap_Unencodable: a map holding a value JSON cannot encode
// is refused rather than passed through unchecked.
func TestCheckArgsMap_Unencodable(t *testing.T) {
	err := CheckArgsMap(map[string]any{"path": make(chan int)}, writeSchema, ArgsStrict)
	if err == nil || !strings.Contains(err.Error(), "could not be encoded") {
		t.Fatalf("CheckArgsMap(chan) = %v, want an encoding refusal", err)
	}
}

// TestCheckArgsMapFor_UsesTheToolsSchemaAndPolicy: the per-tool wrapper
// checks against the tool's own schema, and a pass-through tool forwards
// undeclared keys a strict one refuses.
func TestCheckArgsMapFor_UsesTheToolsSchemaAndPolicy(t *testing.T) {
	strict := &Tool{Name: "write", Parameters: writeSchema}
	if err := CheckArgsMapFor(strict, map[string]any{"PATH": "x", "content": "c"}); err == nil {
		t.Error("strict tool accepted a case-variant of its own parameter")
	}
	if err := CheckArgsMapFor(strict, map[string]any{"path": "a", "content": "c"}); err != nil {
		t.Errorf("strict tool refused exact input: %v", err)
	}
	if err := CheckArgsMapFor(strict, nil); err != nil {
		t.Errorf("nil input (no arguments) was refused: %v", err)
	}
	if err := CheckArgsMapFor(strict, map[string]any{"path": "a", "url": "u"}); err == nil {
		t.Error("strict tool accepted an undeclared gate-read key")
	}
	mcp := &Tool{Name: "mcp__x__y", Parameters: writeSchema, PassThroughArgs: true}
	if err := CheckArgsMapFor(mcp, map[string]any{"path": "a", "url": "u"}); err != nil {
		t.Errorf("pass-through tool refused an undeclared key: %v", err)
	}
}
