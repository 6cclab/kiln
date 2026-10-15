package mods

import (
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/dop251/goja"
)

// MaxTreeText is how much text one tree draws: Claude Code draws the first
// 100,000 characters of a tree and drops the rest.
const MaxTreeText = 100_000

// maxTreeDepth bounds decoding of a pathological (or cyclic) tree.
const maxTreeDepth = 256

// decoder turns the value a ui.render hook returned into an Element tree.
// Function props become Callbacks registered through addCallback.
type decoder struct {
	vm          *goja.Runtime
	elementSym  *goja.Symbol
	textLeft    int
	addCallback func(fn goja.Callable) Callback
}

// decodeTree decodes a hook's return value: nil for null/undefined (the
// hook drew nothing), an error for anything that is not an element.
func (d *decoder) decodeTree(v goja.Value) (*Element, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil, nil
	}
	el, err := d.decodeNode(v, 0)
	if err != nil {
		return nil, err
	}
	if el.Type == "" {
		// A bare string drawn on its own is a Text.
		el = Element{Type: "Text", Children: []Element{el}}
	}
	return &el, nil
}

func (d *decoder) decodeNode(v goja.Value, depth int) (Element, error) {
	if depth > maxTreeDepth {
		return Element{}, fmt.Errorf("tree nested deeper than %d", maxTreeDepth)
	}
	switch x := v.Export().(type) {
	case string:
		return Element{Text: d.takeText(x)}, nil
	case int64:
		return Element{Text: d.takeText(fmt.Sprint(x))}, nil
	case float64:
		return Element{Text: d.takeText(formatNumber(x))}, nil
	}
	obj, ok := v.(*goja.Object)
	if !ok || obj.GetSymbol(d.elementSym) == nil || !obj.GetSymbol(d.elementSym).ToBoolean() {
		return Element{}, fmt.Errorf("a %s is not an element: draw with the constructors $.ui.resolve(e) returns", describe(v))
	}
	el := Element{Type: obj.Get("type").String()}
	if k := obj.Get("key"); k != nil && !goja.IsUndefined(k) {
		el.Key = k.String()
	}
	if p, ok := obj.Get("props").(*goja.Object); ok {
		keys := p.Keys()
		if len(keys) > 0 {
			el.Props = make(map[string]any, len(keys))
			for _, k := range keys {
				pv, err := d.decodeValue(p.Get(k), depth+1)
				if err != nil {
					return Element{}, fmt.Errorf("%s prop %s: %w", el.Type, k, err)
				}
				if pv != nil {
					el.Props[k] = pv
				}
			}
		}
	}
	if ch, ok := obj.Get("children").(*goja.Object); ok {
		n := int(ch.Get("length").ToInteger())
		for i := 0; i < n; i++ {
			c, err := d.decodeNode(ch.Get(fmt.Sprint(i)), depth+1)
			if err != nil {
				return Element{}, err
			}
			if c.Type == "" && c.Text == "" {
				continue
			}
			el.Children = append(el.Children, c)
		}
	}
	return el, nil
}

// decodeValue converts a prop value to JSON-shaped Go data, a function to a
// Callback. undefined decodes to nil (the prop is left out).
func (d *decoder) decodeValue(v goja.Value, depth int) (any, error) {
	if depth > maxTreeDepth {
		return nil, fmt.Errorf("value nested deeper than %d", maxTreeDepth)
	}
	if v == nil || goja.IsUndefined(v) {
		return nil, nil
	}
	if goja.IsNull(v) {
		return nil, nil
	}
	if fn, ok := goja.AssertFunction(v); ok {
		return d.addCallback(fn), nil
	}
	obj, isObj := v.(*goja.Object)
	if !isObj {
		switch x := v.Export().(type) {
		case string:
			return d.takeText(x), nil
		case int64:
			return float64(x), nil
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return nil, nil
			}
			return x, nil
		case bool:
			return x, nil
		default:
			return nil, nil
		}
	}
	if obj.GetSymbol(d.elementSym) != nil && obj.GetSymbol(d.elementSym).ToBoolean() {
		el, err := d.decodeNode(v, depth)
		if err != nil {
			return nil, err
		}
		return el, nil
	}
	if obj.ClassName() == "Array" {
		n := int(obj.Get("length").ToInteger())
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			e, err := d.decodeValue(obj.Get(fmt.Sprint(i)), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
		return out, nil
	}
	out := map[string]any{}
	for _, k := range obj.Keys() {
		e, err := d.decodeValue(obj.Get(k), depth+1)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out[k] = e
		}
	}
	return out, nil
}

// takeText charges s against the tree's text allowance and returns what of
// it is drawn.
func (d *decoder) takeText(s string) string {
	if d.textLeft <= 0 {
		return ""
	}
	n := utf8.RuneCountInString(s)
	if n <= d.textLeft {
		d.textLeft -= n
		return s
	}
	cut := 0
	for i := range s {
		if cut == d.textLeft {
			d.textLeft = 0
			return s[:i]
		}
		cut++
	}
	d.textLeft = 0
	return s
}

func formatNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprint(f)
}

func describe(v goja.Value) string {
	if o, ok := v.(*goja.Object); ok {
		return o.ClassName()
	}
	return fmt.Sprintf("%T", v.Export())
}
