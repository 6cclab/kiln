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
