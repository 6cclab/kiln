package api

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/andrepato/harness/internal/provider"
)

func decodeSchema(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode strict schema: %v (%s)", err, raw)
	}
	return m
}

// TestMakeStrictJSONSchemaTransforms pins the strict transform pi's Responses
// backends require: additionalProperties:false, every property listed in
// required, and an optional (not-required, not-nullable) property wrapped as
// {anyOf:[prop,{type:null}]}. Without the transform the backend rejects the
// request with invalid_function_parameters ("additionalProperties must be
// false for a strict schema").
func TestMakeStrictJSONSchemaTransforms(t *testing.T) {
	in := json.RawMessage(`{
		"type":"object",
		"properties":{"a":{"type":"string"},"b":{"type":"number"}},
		"required":["a"]
	}`)
	out, err := makeStrictJSONSchema(in)
	if err != nil {
		t.Fatalf("makeStrictJSONSchema: %v", err)
	}
	m := decodeSchema(t, out)

	if m["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false", m["additionalProperties"])
	}
	req, _ := m["required"].([]any)
	got := map[string]bool{}
	for _, r := range req {
		got[r.(string)] = true
	}
	if !got["a"] || !got["b"] || len(got) != 2 {
		t.Errorf("required = %v, want both a and b", req)
	}
	props := m["properties"].(map[string]any)
	// a was required: left as-is.
	if a := props["a"].(map[string]any); a["type"] != "string" {
		t.Errorf("property a = %v, want {type:string}", a)
	}
	// b was optional: wrapped to allow null.
	b := props["b"].(map[string]any)
	anyOf, ok := b["anyOf"].([]any)
	if !ok || len(anyOf) != 2 {
		t.Fatalf("property b = %v, want anyOf[prop,null]", b)
	}
	if !reflect.DeepEqual(anyOf[0], map[string]any{"type": "number"}) {
		t.Errorf("anyOf[0] = %v, want {type:number}", anyOf[0])
	}
	if !reflect.DeepEqual(anyOf[1], map[string]any{"type": "null"}) {
		t.Errorf("anyOf[1] = %v, want {type:null}", anyOf[1])
	}
}

// TestMakeStrictJSONSchemaNested checks recursion into nested object properties
// and array items: every nested object also gets additionalProperties:false.
func TestMakeStrictJSONSchemaNested(t *testing.T) {
	in := json.RawMessage(`{
		"type":"object",
		"properties":{
			"items":{"type":"array","items":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}}
		},
		"required":["items"]
	}`)
	out, err := makeStrictJSONSchema(in)
	if err != nil {
		t.Fatalf("makeStrictJSONSchema: %v", err)
	}
	m := decodeSchema(t, out)
	items := m["properties"].(map[string]any)["items"].(map[string]any)
	itemSchema := items["items"].(map[string]any)
	if itemSchema["additionalProperties"] != false {
		t.Errorf("nested array item object additionalProperties = %v, want false", itemSchema["additionalProperties"])
	}
}

// TestMakeStrictJSONSchemaUnsupported verifies unsupported keywords and shapes
// return errUnsupportedStrictSchema (so the caller sends the tool non-strict
// instead of a request the backend rejects).
func TestMakeStrictJSONSchemaUnsupported(t *testing.T) {
	cases := map[string]json.RawMessage{
		"ref at root property": json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"#/d"}},"required":["x"]}`),
		"oneOf at root":        json.RawMessage(`{"type":"object","oneOf":[{"type":"object"}]}`),
		"root not object":      json.RawMessage(`{"type":"string"}`),
		"object union anyOf":   json.RawMessage(`{"type":"object","properties":{"x":{"anyOf":[{"type":"object"},{"type":"string"}]}},"required":["x"]}`),
		"true additionalProps": json.RawMessage(`{"type":"object","additionalProperties":true,"properties":{}}`),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := makeStrictJSONSchema(in); !errors.Is(err, errUnsupportedStrictSchema) {
				t.Fatalf("err = %v, want errUnsupportedStrictSchema", err)
			}
		})
	}
}

// TestCodexToolsStrictAddsAdditionalProperties is the end-to-end regression for
// the reported "error using codex models": a strict-mode tool whose schema
// lacks additionalProperties must be transformed, not passed through. Before
// the fix the schema was sent verbatim and the Codex backend returned
// invalid_function_parameters.
func TestCodexToolsStrictAddsAdditionalProperties(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}`)
	tools := convertCodexTools([]provider.ToolDef{{Name: "bash", Description: "run", Parameters: params}}, true)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0]["strict"] != true {
		t.Fatalf("strict = %v, want true", tools[0]["strict"])
	}
	schema := decodeSchema(t, mustRawMessage(t, tools[0]["parameters"]))
	if schema["additionalProperties"] != false {
		t.Fatalf("parameters.additionalProperties = %v, want false", schema["additionalProperties"])
	}
}

// TestCodexToolsStrictFallsBackForUnsupportedSchema: a tool whose schema cannot
// be made strict is sent non-strict (no "strict" key) with its schema intact,
// rather than failing the whole request.
func TestCodexToolsStrictFallsBackForUnsupportedSchema(t *testing.T) {
	params := json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"#/d"}},"required":["x"]}`)
	tools := convertCodexTools([]provider.ToolDef{{Name: "weird", Description: "d", Parameters: params}}, true)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	if _, ok := tools[0]["strict"]; ok {
		t.Fatalf("strict present (%v); want omitted for an unsupported schema", tools[0]["strict"])
	}
}

// mustRawMessage normalizes a parameters value (json.RawMessage) back to bytes.
func mustRawMessage(t *testing.T, v any) json.RawMessage {
	t.Helper()
	switch x := v.(type) {
	case json.RawMessage:
		return x
	case []byte:
		return x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal parameters: %v", err)
		}
		return b
	}
}
