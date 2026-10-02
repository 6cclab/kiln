package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tool input is read in two different ways, and they must never disagree.
//
// The permission gate, the PreToolUse hooks and the TUI read a call's
// input as a map, by exact key ("path", "command", ...). The built-in
// tools decode the same JSON into structs with encoding/json, which
// matches a field name case-insensitively and lets the last of several
// matching keys win. So {"PATH": "~/.zshrc"} reaches the write tool as
// path "~/.zshrc" while the gate sees no path at all, and the workspace
// boundary, Read/Edit rules and Bash rules judge something other than
// what runs.
//
// CheckArgs is the one check that closes that gap. The turn loop
// (internal/harness beginTool) runs it on every call's input before any
// hook, the gate or the tool sees it, and again on input a PreToolUse
// hook rewrote. Input it accepts has, at every level, keys that are
// pairwise distinct under case folding and spelled exactly as the schema
// declares them, so an exact-key map lookup and a case-insensitive struct
// decode select the same value.

// PathKeys are the keys the permission gate reads a call's target path
// from (permission.PathArgOf), in the order it reads them.
var PathKeys = []string{"path", "file_path", "filePath"}

// PrimaryKeys are the keys the permission gate reads a call's identifying
// argument from (permission.PrimaryArgOf), in the order it reads them.
// Permission rules match on this value.
var PrimaryKeys = []string{"command", "path", "file_path", "filePath", "pattern", "query", "url"}

// ArgsPolicy says how strictly CheckArgs treats keys the schema does not
// declare.
type ArgsPolicy int

const (
	// ArgsStrict refuses a top-level key the gate reads (PathKeys,
	// PrimaryKeys) that the schema does not declare. A built-in tool's
	// struct drops such a key, but the gate would still judge the call on
	// it: a "path" sent to web_fetch would be what Bash/WebFetch rules
	// match (PrimaryArgOf reads "path" before "url") while the tool
	// fetches the url. Other undeclared keys are left alone: kiln's own
	// schemas do not list every key the tools and the TUI accept (bash's
	// "description", task's "model"), and the gate never reads them.
	ArgsStrict ArgsPolicy = iota
	// ArgsPassThrough allows every undeclared key: the input is forwarded
	// to something else (an MCP server) that owns its own schema and may
	// read a "path" itself, so the gate reading it too is correct.
	ArgsPassThrough
)

// ArgsError is CheckArgs's refusal. Its message is a clause written for
// the model, meant to follow "The call to <tool> did not run: ".
type ArgsError struct{ msg string }

func (e *ArgsError) Error() string { return e.msg }

func argsErrorf(format string, a ...any) error { return &ArgsError{msg: fmt.Sprintf(format, a...)} }

// CheckArgs reports whether raw is input the gate and the tool will read
// identically. It refuses:
//
//   - input that is not a JSON object;
//   - an object, at any depth, with two keys equal under Unicode case
//     folding, exact duplicates included (scanned from the raw tokens,
//     since decoding into a map would hide them);
//   - a key that equals a property the schema declares, or one of the keys
//     the gate reads (PathKeys, PrimaryKeys), only under case folding;
//   - under ArgsStrict, a top-level key the gate reads that the schema
//     does not declare.
//
// schema is the tool's JSON Schema (Tool.Parameters). Nested "properties"
// and array "items" are followed, so edits[].oldText is held to its exact
// spelling too. A schema that does not parse skips the schema checks but
// never the duplicate check.
func CheckArgs(raw []byte, schema json.RawMessage, policy ArgsPolicy) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		kind := "nothing"
		if len(trimmed) > 0 {
			kind = jsonKind(trimmed[0])
		}
		return argsErrorf("its input must be a JSON object, but it was %s", kind)
	}
	var root *schemaNode
	if len(schema) > 0 {
		var n schemaNode
		if err := json.Unmarshal(schema, &n); err == nil {
			root = &n
		}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	w := argWalker{dec: dec, policy: policy}
	if err := w.value(root, "", true); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return argsErrorf("its input is not a single JSON object")
	}
	return nil
}

// CheckArgsMap is CheckArgs for input already decoded into a map, the form
// msg.ToolCall carries. A nil map is the empty object: a provider leaves
// Arguments nil for a call that sent no input.
func CheckArgsMap(args map[string]any, schema json.RawMessage, policy ArgsPolicy) error {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return argsErrorf("its input could not be encoded as JSON: %v", err)
	}
	return CheckArgs(raw, schema, policy)
}

// CheckArgsFor runs CheckArgs against t's own schema and policy.
func CheckArgsFor(t *Tool, raw []byte) error {
	return CheckArgs(raw, t.Parameters, t.argsPolicy())
}

// CheckArgsMapFor runs CheckArgsMap against t's own schema and policy.
func CheckArgsMapFor(t *Tool, args map[string]any) error {
	return CheckArgsMap(args, t.Parameters, t.argsPolicy())
}

func (t *Tool) argsPolicy() ArgsPolicy {
	if t.PassThroughArgs {
		return ArgsPassThrough
	}
	return ArgsStrict
}

// schemaNode is the part of a JSON Schema CheckArgs follows.
type schemaNode struct {
	Properties map[string]*schemaNode `json:"properties"`
	Items      *schemaNode            `json:"items"`
}

type argWalker struct {
	dec    *json.Decoder
	policy ArgsPolicy
}

// value consumes one JSON value. s is its schema (nil when unknown), at
// the location the value sits, top whether it is the input object itself.
func (w *argWalker) value(s *schemaNode, at string, top bool) error {
	tok, err := w.dec.Token()
	if err != nil {
		return argsErrorf("its input is not valid JSON: %v", err)
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		return w.object(s, at, top)
	case '[':
		var items *schemaNode
		if s != nil {
			items = s.Items
		}
		for i := 0; w.dec.More(); i++ {
			if err := w.value(items, fmt.Sprintf("%s[%d]", at, i), false); err != nil {
				return err
			}
		}
		if _, err := w.dec.Token(); err != nil { // ']'
			return argsErrorf("its input is not valid JSON: %v", err)
		}
	}
	return nil
}

func (w *argWalker) object(s *schemaNode, at string, top bool) error {
	var props map[string]*schemaNode
	if s != nil {
		props = s.Properties
	}
	declared := map[string]string{} // folded -> declared spelling
	for name := range props {
		declared[foldKey(name)] = name
	}
	seen := map[string]string{} // folded -> spelling seen first
	for w.dec.More() {
		tok, err := w.dec.Token()
		if err != nil {
			return argsErrorf("its input is not valid JSON: %v", err)
		}
		key, ok := tok.(string)
		if !ok {
			return argsErrorf("its input is not valid JSON")
		}
		where := key
		if at != "" {
			where = at + "." + key
		}
		folded := foldKey(key)
		if prev, dup := seen[folded]; dup {
			if prev == key {
				return argsErrorf("its input has the key %q more than once", where)
			}
			return argsErrorf("its input has both %q and %q, keys that differ only in case", qualify(at, prev), where)
		}
		seen[folded] = key

		var child *schemaNode
		if _, exact := props[key]; exact {
			child = props[key]
		} else if name, near := declared[folded]; near {
			return argsErrorf("its input has the key %q, which differs only in case from the parameter %q; use the exact name", where, qualify(at, name))
		} else if top {
			if gate, near := gateKey(folded); near {
				if gate != key {
					return argsErrorf("its input has the key %q, which differs only in case from %q; use the exact name", where, gate)
				}
				if w.policy == ArgsStrict {
					return argsErrorf("its input has the key %q, which is not a parameter of this tool (parameters: %s)", where, strings.Join(sortedNames(props), ", "))
				}
			}
		}
		if err := w.value(child, where, false); err != nil {
			return err
		}
	}
	if _, err := w.dec.Token(); err != nil { // '}'
		return argsErrorf("its input is not valid JSON: %v", err)
	}
	return nil
}

// gateKey reports the gate-read key folded matches, if any.
func gateKey(folded string) (string, bool) {
	for _, list := range [][]string{PathKeys, PrimaryKeys} {
		for _, k := range list {
			if foldKey(k) == folded {
				return k, true
			}
		}
	}
	return "", false
}

func qualify(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

func sortedNames(props map[string]*schemaNode) []string {
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func jsonKind(first byte) string {
	switch first {
	case '[':
		return "an array"
	case '"':
		return "a string"
	case 'n':
		return "null"
	case 't', 'f':
		return "a boolean"
	default:
		return "a number"
	}
}

// foldKey maps s to a canonical form such that foldKey(a) == foldKey(b)
// exactly when strings.EqualFold(a, b): every rune becomes the smallest
// rune of its unicode.SimpleFold orbit. It is the same folding
// encoding/json uses to match keys to struct fields.
func foldKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == utf8.RuneError {
			b.WriteRune(r)
			continue
		}
		for {
			r2 := unicode.SimpleFold(r)
			if r2 <= r {
				r = r2
				break
			}
			r = r2
		}
		b.WriteRune(r)
	}
	return b.String()
}
