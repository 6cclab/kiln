package tool

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// TestCoerceArgsFixesTypeSlips: values the schema contradicts in a
// recognisable way are converted; values that already fit, unknown keys
// and unfixable values are left for the tool to judge.
func TestCoerceArgsFixesTypeSlips(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{
		"limit":{"type":"integer"}, "ratio":{"type":"number"}, "all":{"type":"boolean"},
		"edits":{"type":"array"}, "opts":{"type":["object","null"]}, "path":{"type":"string"}, "n":{"type":"integer"}}}`)
	args := map[string]any{
		"limit": "42", "ratio": " 0.5 ", "all": "TRUE",
		"edits": `[{"oldText":"a","newText":"b"}]`, "opts": `{"x":1}`, "path": float64(7),
		"n": "seven", "extra": "1",
	}
	got, changed := CoerceArgs(schema, args)
	want := map[string]any{
		"limit": float64(42), "ratio": 0.5, "all": true,
		"edits": []any{map[string]any{"oldText": "a", "newText": "b"}}, "opts": map[string]any{"x": float64(1)}, "path": "7",
		"n": "seven", "extra": "1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CoerceArgs = %#v\nwant %#v", got, want)
	}
	sort.Strings(changed)
	if !reflect.DeepEqual(changed, []string{"all", "edits", "limit", "opts", "path", "ratio"}) {
		t.Errorf("changed = %v", changed)
	}
	if args["limit"] != "42" {
		t.Error("the caller's map was modified")
	}
	fine := map[string]any{"limit": float64(3), "path": "a.go"}
	if got, changed := CoerceArgs(schema, fine); len(changed) != 0 || !reflect.DeepEqual(got, fine) {
		t.Errorf("args that fit were changed: %v %v", got, changed)
	}
}

// TestCoerceArgsLeavesWhatItCannotFix: a type it cannot pin down, a value
// that does not parse, and a schema it cannot read are left alone.
func TestCoerceArgsLeavesWhatItCannotFix(t *testing.T) {
	schema := json.RawMessage(`{"properties":{
		"either":{"type":["string","integer"]}, "odd":{"type":{"x":1}}, "untyped":{},
		"n":{"type":"integer"}, "f":{"type":"number"}, "b":{"type":"boolean"},
		"list":{"type":"array"}, "obj":{"type":"object"}, "s":{"type":"string"}, "nn":{"type":"integer"},
		"either2":{"type":"array"}}}`)
	args := map[string]any{
		"either": "1", "odd": "1", "untyped": "1",
		"n": "1.5", "f": "fast", "b": "yes",
		"list": "a,b", "obj": "{broken", "s": []any{"x"}, "nn": float64(2),
		"either2": float64(1),
	}
	got, changed := CoerceArgs(schema, args)
	if len(changed) != 0 || !reflect.DeepEqual(got, args) {
		t.Errorf("CoerceArgs changed %v: %#v", changed, got)
	}
	if got, changed := CoerceArgs(json.RawMessage(`not json`), map[string]any{"n": "1"}); len(changed) != 0 || got["n"] != "1" {
		t.Errorf("unreadable schema: %v %v", got, changed)
	}
	if got, changed := CoerceArgs(json.RawMessage(`{"properties":{"b":{"type":"boolean"},"s":{"type":"string"}}}`), map[string]any{"b": "false", "s": true}); len(changed) != 2 || got["b"] != false || got["s"] != "true" {
		t.Errorf("false and a bool for a string: %v %v", got, changed)
	}
}
