package tool

import (
	"encoding/json"
	"strconv"
	"strings"
)

// CoerceArgs fixes the commonest type slips weaker models make in a tool
// call's top-level arguments, against the tool's JSON Schema: a number or
// boolean sent as a string ("42", "true"), an array or object sent as a
// JSON-encoded string, and a number or boolean where a string is declared.
// Anything else is left for the tool to reject. It returns the arguments
// to use (args itself when nothing changed) and the names it changed.
// Adapted from hermes-agent's argument coercion (NousResearch, MIT).
func CoerceArgs(schema json.RawMessage, args map[string]any) (map[string]any, []string) {
	var s struct {
		Properties map[string]struct {
			Type json.RawMessage `json:"type"`
		} `json:"properties"`
	}
	if len(args) == 0 || json.Unmarshal(schema, &s) != nil || len(s.Properties) == 0 {
		return args, nil
	}
	var out map[string]any
	var changed []string
	for name, v := range args {
		prop, ok := s.Properties[name]
		if !ok {
			continue
		}
		fixed, ok := coerce(v, declaredType(prop.Type))
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(args))
			for k, v := range args {
				out[k] = v
			}
		}
		out[name] = fixed
		changed = append(changed, name)
	}
	if out == nil {
		return args, nil
	}
	return out, changed
}

// declaredType is a schema's single type: "string", or ["string", "null"]
// read as "string". Anything less definite is "".
func declaredType(raw json.RawMessage) string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return ""
	}
	t := ""
	for _, m := range many {
		if m == "null" {
			continue
		}
		if t != "" {
			return ""
		}
		t = m
	}
	return t
}

// coerce converts v to typ when it is a recognisable slip; ok is false
// when v already fits or cannot be fixed safely.
func coerce(v any, typ string) (any, bool) {
	switch typ {
	case "integer", "number":
		s, isStr := v.(string)
		if !isStr {
			return nil, false
		}
		s = strings.TrimSpace(s)
		if typ == "integer" {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return float64(n), true
			}
			return nil, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	case "boolean":
		if s, isStr := v.(string); isStr {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true":
				return true, true
			case "false":
				return false, true
			}
		}
	case "array", "object":
		s, isStr := v.(string)
		if !isStr {
			return nil, false
		}
		s = strings.TrimSpace(s)
		if (typ == "array" && !strings.HasPrefix(s, "[")) || (typ == "object" && !strings.HasPrefix(s, "{")) {
			return nil, false
		}
		var parsed any
		if json.Unmarshal([]byte(s), &parsed) == nil {
			return parsed, true
		}
	case "string":
		switch x := v.(type) {
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64), true
		case bool:
			return strconv.FormatBool(x), true
		}
	}
	return nil, false
}
