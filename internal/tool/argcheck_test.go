package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

var writeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string"},
		"content": {"type": "string"}
	},
	"required": ["path", "content"]
}`)

var editSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string"},
		"edits": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"oldText": {"type": "string"},
					"newText": {"type": "string"}
				}
			}
		}
	}
}`)

func TestCheckArgs(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		schema  json.RawMessage
		policy  ArgsPolicy
		wantErr string // "" means accepted
	}{
		{"exact keys", `{"path":"a.txt","content":"x"}`, writeSchema, ArgsStrict, ""},
		{"empty object", `{}`, writeSchema, ArgsStrict, ""},
		{"upper-case key", `{"PATH":"/etc/x","content":"x"}`, writeSchema, ArgsStrict, `"PATH", which differs only in case from the parameter "path"`},
		{"mixed-case key", `{"Path":"/etc/x","content":"x"}`, writeSchema, ArgsStrict, `"Path", which differs only in case from the parameter "path"`},
		{"exact duplicate", `{"path":"a.txt","path":"/etc/x","content":"x"}`, writeSchema, ArgsStrict, `"path" more than once`},
		{"case-variant duplicate", `{"path":"a.txt","PATH":"/etc/x","content":"x"}`, writeSchema, ArgsStrict, `both "path" and "PATH"`},
		{"case-variant duplicate, variant first", `{"PATH":"/etc/x","path":"a.txt"}`, writeSchema, ArgsStrict, `"PATH", which differs only in case from the parameter "path"`},
		{"duplicate with no schema", `{"x":1,"X":2}`, nil, ArgsPassThrough, `both "x" and "X"`},
		{"Kelvin sign folds to k", "{\"Key\":1,\"key\":2}", nil, ArgsPassThrough, "differ only in case"},
		{"array", `[{"path":"a"}]`, writeSchema, ArgsStrict, "must be a JSON object, but it was an array"},
		{"string", `"path"`, writeSchema, ArgsStrict, "but it was a string"},
		{"null", `null`, writeSchema, ArgsStrict, "but it was null"},
		{"number", `12`, writeSchema, ArgsStrict, "but it was a number"},
		{"empty", ``, writeSchema, ArgsStrict, "but it was nothing"},
		{"trailing value", `{"path":"a"} {"path":"b"}`, writeSchema, ArgsStrict, "not a single JSON object"},
		{"invalid JSON", `{"path":}`, writeSchema, ArgsStrict, "not valid JSON"},
		{"undeclared gate key, strict", `{"path":"a","url":"x"}`, writeSchema, ArgsStrict, `"url", which is not a parameter of this tool (parameters: content, path)`},
		{"undeclared gate key smuggled to web_fetch", `{"url":"https://evil.example","path":"inside.txt"}`, json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"offset":{"type":"number"}}}`), ArgsStrict, `"path", which is not a parameter`},
		{"undeclared gate key, pass-through", `{"path":"a","url":"x"}`, writeSchema, ArgsPassThrough, ""},
		{"undeclared other key, strict", `{"command":"ls","description":"list files"}`, json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`), ArgsStrict, ""},
		{"nested case variant", `{"path":"a","edits":[{"oldText":"a","NEWTEXT":"b"}]}`, editSchema, ArgsStrict, `"edits[0].NEWTEXT", which differs only in case from the parameter "edits[0].newText"`},
		{"nested duplicate", `{"path":"a","edits":[{"oldText":"a","newText":"b","newtext":"c"}]}`, editSchema, ArgsStrict, `both "edits[0].newText" and "edits[0].newtext"`},
		{"nested exact", `{"path":"a","edits":[{"oldText":"a","newText":"b"}]}`, editSchema, ArgsStrict, ""},
		// Pass-through (MCP): undeclared keys are the server's business,
		// but a case variant of a key the gate reads is still refused.
		{"gate key variant, pass-through", `{"COMMAND":"rm -rf /"}`, json.RawMessage(`{"type":"object","properties":{}}`), ArgsPassThrough, `"COMMAND", which differs only in case from "command"`},
		{"gate key variant, no schema", `{"File_Path":"/etc/x"}`, nil, ArgsPassThrough, `differs only in case from "file_path"`},
		{"declared spelling wins over gate key", `{"Path":"x"}`, json.RawMessage(`{"type":"object","properties":{"Path":{"type":"string"}}}`), ArgsPassThrough, ""},
		{"unparsable schema still checks duplicates", `{"a":1,"A":2}`, json.RawMessage(`{`), ArgsStrict, "differ only in case"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckArgs([]byte(tc.raw), tc.schema, tc.policy)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckArgs(%s) = %v, want accepted", tc.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckArgs(%s) accepted it, want an error containing %q", tc.raw, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CheckArgs(%s) = %q, want it to contain %q", tc.raw, err.Error(), tc.wantErr)
			}
		})
	}
}

func TestCheckArgsMapNilIsEmptyObject(t *testing.T) {
	if err := CheckArgsMap(nil, writeSchema, ArgsStrict); err != nil {
		t.Fatalf("CheckArgsMap(nil) = %v, want accepted (a call with no input)", err)
	}
}

// A map can hold case variants (they are distinct keys), so the map form
// the turn loop checks must refuse them too.
func TestCheckArgsMapCaseVariants(t *testing.T) {
	err := CheckArgsMap(map[string]any{"path": "ok.txt", "PATH": "/etc/x", "content": "x"}, writeSchema, ArgsStrict)
	if err == nil || !strings.Contains(err.Error(), "only in case") {
		t.Fatalf("CheckArgsMap = %v, want a case-variant refusal", err)
	}
}

func TestCheckArgsForUsesThePassThroughFlag(t *testing.T) {
	raw := []byte(`{"path":"a","command":"x"}`)
	if err := CheckArgsFor(&Tool{Parameters: writeSchema}, raw); err == nil {
		t.Fatalf("built-in tool accepted an undeclared key the gate reads")
	}
	if err := CheckArgsFor(&Tool{Parameters: writeSchema, PassThroughArgs: true}, raw); err != nil {
		t.Fatalf("pass-through tool refused an undeclared key: %v", err)
	}
}

func TestFoldKeyMatchesEqualFold(t *testing.T) {
	words := []string{"path", "PATH", "Path", "pAtH", "file_path", "FILE_PATH", "filePath", "filepath",
		"key", "Key", "KEY", "s", "ſ", "S", "straße", "STRASSE", "σ", "ς", "Σ", "ǅ", "ǆ", "Ǆ", ""}
	for _, a := range words {
		for _, b := range words {
			if got, want := foldKey(a) == foldKey(b), strings.EqualFold(a, b); got != want {
				t.Errorf("foldKey(%q)==foldKey(%q) is %v, strings.EqualFold is %v", a, b, got, want)
			}
		}
	}
}

// FuzzCheckArgsAgreesWithStructDecode is the property the check exists
// for: whenever CheckArgs accepts input, the gate's exact map lookup and
// the tool's case-insensitive struct decode read the same path.
func FuzzCheckArgsAgreesWithStructDecode(f *testing.F) {
	for _, s := range []string{
		`{"path":"a"}`, `{"PATH":"a"}`, `{"path":"a","PATH":"b"}`, `{"Path":"b","path":"a"}`,
		`{"path":"a","path":"b"}`, "{\"patħ\":\"a\"}", `{"content":"x","pAth":"y"}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if CheckArgs([]byte(raw), writeSchema, ArgsStrict) != nil {
			return
		}
		var asMap map[string]any
		var asStruct struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(raw), &asMap) != nil || json.Unmarshal([]byte(raw), &asStruct) != nil {
			return // a type mismatch the tool reports itself
		}
		gate, _ := asMap["path"].(string)
		if gate != asStruct.Path {
			t.Fatalf("accepted %s: gate reads path %q, tool reads %q", raw, gate, asStruct.Path)
		}
	})
}
