package api

import (
	"encoding/json"
	"errors"
)

// strict_schema.go ports pi-ai's JSON-schema strict transform
// (dist/api/constrained-sampling.js: makeStrictJsonSchema /
// makeJsonSchemaNodeStrict). OpenAI's Responses API (and the ChatGPT Codex
// backend) reject a tool sent with "strict": true unless its parameter schema
// is in the strict subset: every object must set "additionalProperties": false
// and list every property in "required", with optional properties made
// nullable. pi transforms the schema before sending and, when a schema cannot
// be expressed in the strict subset, falls back to sending that tool
// non-strict (resolveJsonSchemaStrictSampling's "prefer" path). kiln's tools
// carry no per-tool strict preference, so they are all treated as "prefer":
// strict when the schema allows, non-strict otherwise.

// errUnsupportedStrictSchema mirrors pi's UnsupportedStrictJsonSchemaError: a
// schema that cannot be represented in the strict subset. It signals a
// per-tool fallback to non-strict, never a hard failure.
var errUnsupportedStrictSchema = errors.New("unsupported strict json schema")

// unsupportedStrictSchemaKeys are JSON-schema keywords pi rejects for strict
// mode (UNSUPPORTED_STRICT_SCHEMA_KEYS).
var unsupportedStrictSchemaKeys = []string{
	"$ref", "$defs", "definitions", "allOf", "oneOf", "patternProperties",
	"dependentSchemas", "dependencies", "unevaluatedProperties",
	"propertyNames", "contains", "prefixItems", "not", "if", "then", "else",
}

// makeStrictJSONSchema returns schemaJSON transformed into the strict subset,
// or errUnsupportedStrictSchema if it cannot be. The root must be an object
// schema. The input is not mutated (it is re-decoded here).
func makeStrictJSONSchema(schemaJSON json.RawMessage) (json.RawMessage, error) {
	var node any
	if err := json.Unmarshal(schemaJSON, &node); err != nil {
		return nil, errUnsupportedStrictSchema
	}
	root, ok := node.(map[string]any)
	if !ok {
		return nil, errUnsupportedStrictSchema // "root schema must have type object"
	}
	if err := makeJSONSchemaNodeStrict(root); err != nil {
		return nil, err
	}
	if root["type"] != "object" {
		return nil, errUnsupportedStrictSchema
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, errUnsupportedStrictSchema
	}
	return out, nil
}

// makeJSONSchemaNodeStrict mutates schema in place into the strict subset,
// recursing into anyOf variants, array items, and object properties. It ports
// makeJsonSchemaNodeStrict.
func makeJSONSchemaNodeStrict(schema map[string]any) error {
	for _, key := range unsupportedStrictSchemaKeys {
		if _, ok := schema[key]; ok {
			return errUnsupportedStrictSchema
		}
	}

	if av, ok := schema["anyOf"]; ok {
		variants, ok := av.([]any)
		if !ok || len(variants) == 0 {
			return errUnsupportedStrictSchema
		}
		for _, v := range variants {
			vm, ok := v.(map[string]any)
			if !ok {
				return errUnsupportedStrictSchema // boolean schema
			}
			if isStructuredSchema(vm) {
				return errUnsupportedStrictSchema // object and array unions are unsupported
			}
			if err := makeJSONSchemaNodeStrict(vm); err != nil {
				return err
			}
		}
	}

	if iv, ok := schema["items"]; ok {
		if _, isArr := iv.([]any); isArr {
			return errUnsupportedStrictSchema // tuple schemas are unsupported
		}
		im, ok := iv.(map[string]any)
		if !ok {
			return errUnsupportedStrictSchema
		}
		if err := makeJSONSchemaNodeStrict(im); err != nil {
			return err
		}
	}

	isObject := schema["type"] == "object"
	_, hasProps := schema["properties"]
	if hasProps && !isObject {
		return errUnsupportedStrictSchema // properties require type object
	}
	if !isObject {
		return nil
	}

	if ap, ok := schema["additionalProperties"]; ok {
		if b, isBool := ap.(bool); !isBool || b {
			return errUnsupportedStrictSchema // schema-valued or true additionalProperties is unsupported
		}
	}

	properties, _ := schema["properties"].(map[string]any)
	if hasProps && properties == nil {
		return errUnsupportedStrictSchema // object properties must be a schema map
	}

	required := map[string]bool{}
	if rv, ok := schema["required"]; ok {
		rl, ok := rv.([]any)
		if !ok {
			return errUnsupportedStrictSchema // object required must be a string array
		}
		for _, r := range rl {
			rs, ok := r.(string)
			if !ok {
				return errUnsupportedStrictSchema
			}
			required[rs] = true
		}
	}
	for k := range required {
		if _, ok := properties[k]; !ok {
			return errUnsupportedStrictSchema // required contains an unknown property
		}
	}

	propertyNames := make([]string, 0, len(properties))
	for k, pv := range properties {
		propertyNames = append(propertyNames, k)
		pm, ok := pv.(map[string]any)
		if !ok {
			return errUnsupportedStrictSchema // boolean schema
		}
		if err := makeJSONSchemaNodeStrict(pm); err != nil {
			return err
		}
		// A property not in `required` and not already nullable is wrapped as
		// {anyOf: [property, {type: null}]} so strict mode can list it in
		// `required` while still allowing it to be absent (as null).
		if !required[k] && !schemaAllowsNull(pm) {
			properties[k] = map[string]any{"anyOf": []any{pm, map[string]any{"type": "null"}}}
		}
	}

	// Strict mode: every property is required, and no extra properties.
	reqOut := make([]any, len(propertyNames))
	for i, n := range propertyNames {
		reqOut[i] = n
	}
	schema["required"] = reqOut
	schema["additionalProperties"] = false
	return nil
}

// isStructuredSchema reports whether schema describes an object or array (ports
// isStructuredSchema): used to reject object/array members of an anyOf union.
func isStructuredSchema(schema map[string]any) bool {
	for _, t := range schemaTypes(schema) {
		if t == "object" || t == "array" {
			return true
		}
	}
	_, hasProps := schema["properties"]
	_, hasItems := schema["items"]
	return hasProps || hasItems
}

// schemaAllowsNull ports schemaAllowsNull: whether schema already permits null.
func schemaAllowsNull(schema map[string]any) bool {
	for _, t := range schemaTypes(schema) {
		if t == "null" {
			return true
		}
	}
	if c, ok := schema["const"]; ok && c == nil {
		return true
	}
	if ev, ok := schema["enum"].([]any); ok {
		for _, e := range ev {
			if e == nil {
				return true
			}
		}
	}
	if av, ok := schema["anyOf"].([]any); ok {
		for _, v := range av {
			if vm, ok := v.(map[string]any); ok && schemaAllowsNull(vm) {
				return true
			}
		}
	}
	return false
}

// schemaTypes returns the schema's "type" as a slice (it may be a string or an
// array of strings).
func schemaTypes(schema map[string]any) []string {
	switch t := schema["type"].(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
