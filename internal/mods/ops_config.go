package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/dop251/goja"
)

// $.config.* and $.telemetry.*. kiln's /config panel is a read-only report and
// kiln writes none of Claude Code's settings files, so no row is writable from
// a mod: $.config.set runs the config.set hooks and then answers { deny }
// (docs/mods-gap.md). Nothing leaves the machine for $.telemetry.*: kiln has no
// analytics sink, so the hooks see the record and the call resolves.

// ConfigRow is one built-in /config row kiln supplies for $.config.list.
// Kind is boolean, choice, text or number. Via names the command that changes
// it ("/model"): $.config.set denies, pointing there.
type ConfigRow struct {
	Key, Label, Description string
	Kind                    string
	Value                   any
	Options                 []string
	Via                     string
}

// configRow is a ConfigRow plus who owns it and whether a trusted source does.
type configRow struct {
	ConfigRow
	provider map[string]any
	locked   bool
}

var configKinds = map[string]bool{"boolean": true, "choice": true, "text": true, "number": true}

// pluginConfigRows are the rows for the userConfig fields a plugin's manifest
// declares, valued from the plugin's options. kiln reads those values from
// Claude Code's settings (pluginConfigs) and never writes them, so each row is
// locked. A sensitive field is left out: its value is a secret.
func pluginConfigRows(plugin, root string, opts map[string]any) []configRow {
	data, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err != nil {
		return nil
	}
	var manifest struct {
		UserConfig map[string]struct {
			Type        string   `json:"type"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			Sensitive   bool     `json:"sensitive"`
			Multiple    bool     `json:"multiple"`
			Options     []string `json:"options"`
		} `json:"userConfig"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return nil
	}
	keys := make([]string, 0, len(manifest.UserConfig))
	for k := range manifest.UserConfig {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows []configRow
	for _, k := range keys {
		f := manifest.UserConfig[k]
		if f.Sensitive {
			continue
		}
		r := configRow{
			ConfigRow: ConfigRow{Key: plugin + "." + k, Label: f.Title, Description: f.Description, Kind: "text", Value: ""},
			provider:  map[string]any{"plugin": plugin, "tier": "user"},
			locked:    true,
		}
		if r.Label == "" {
			r.Label = k
		}
		switch f.Type {
		case "boolean":
			r.Kind, r.Value = "boolean", false
		case "number":
			r.Kind, r.Value = "number", float64(0)
		default:
			if len(f.Options) > 0 {
				r.Kind, r.Options = "choice", f.Options
			}
		}
		if v, ok := opts[k]; ok {
			r.Value = v
		} else if f.Multiple {
			r.Value = []any{}
		}
		rows = append(rows, r)
	}
	return rows
}

// configRows is every row: kiln's own, then each loaded plugin's. Worker-only.
func (e *Engine) configRows() []configRow {
	var rows []configRow
	if e.opts.Services.Config != nil {
		for _, r := range e.opts.Services.Config() {
			if !configKinds[r.Kind] {
				r.Kind = "text"
			}
			rows = append(rows, configRow{ConfigRow: r, provider: coreOrigin()})
		}
	}
	for _, p := range e.plugins {
		rows = append(rows, p.rows...)
	}
	return rows
}

// fitsKind reports whether v is a value a row of that kind takes.
func fitsKind(r configRow, v any) bool {
	switch r.Kind {
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		switch v.(type) {
		case float64, int64:
			return true
		}
		return false
	case "choice":
		s, ok := v.(string)
		for _, o := range r.Options {
			if ok && o == s {
				return true
			}
		}
		return false
	}
	switch t := v.(type) {
	case string:
		return true
	case []any:
		for _, x := range t {
			if _, ok := x.(string); !ok {
				return false
			}
		}
		return true
	}
	return false
}

const noConfigWriter = "kiln writes no /config row: its /config panel is read-only and kiln never writes Claude Code's settings files"

func (e *Engine) answerConfig(f *frame, name string, arg func(int) goja.Value) (out any, handled bool, err error) {
	switch name {
	case "config.list":
		rows := e.configRows()
		return e.goAsync(func() (any, error) {
			list := []any{}
			for _, r := range rows {
				ev := map[string]any{"key": r.Key, "label": r.Label, "isHidden": false, "provider": r.provider}
				if r.Description != "" {
					ev["description"] = r.Description
				}
				res, err := e.Raise(context.Background(), "config.describe", ev, OpResult(func(in map[string]any) (map[string]any, error) {
					return map[string]any{"label": in["label"], "description": in["description"], "isHidden": in["isHidden"]}, nil
				}))
				if err != nil {
					res = ev // a hook that fails passes the row through
				}
				if hidden, _ := res["isHidden"].(bool); hidden {
					continue
				}
				row := map[string]any{"key": r.Key, "label": r.Label, "kind": r.Kind, "value": r.Value,
					"provider": r.provider, "isLocked": r.locked}
				if s, ok := res["label"].(string); ok {
					row["label"] = s
				}
				if s, ok := res["description"].(string); ok && s != "" {
					row["description"] = s
				}
				if len(r.Options) > 0 {
					row["options"] = r.Options
				}
				list = append(list, row)
			}
			return list, nil
		}), true, nil

	case "config.set":
		args := exportMap(arg(0))
		key := strOf(args, "key")
		value, hasValue := args["value"]
		if key == "" || !hasValue {
			return nil, true, fmt.Errorf("takes { key, value }")
		}
		var row *configRow
		for _, r := range e.configRows() {
			if r.Key == key {
				r := r
				row = &r
				break
			}
		}
		if row == nil {
			return nil, true, fmt.Errorf("no /config row is named %q", key)
		}
		r := *row
		plugin := f.plugin.name
		return e.opCall(f, "config.set",
			func(context.Context) (map[string]any, error) {
				return map[string]any{"key": key, "value": value, "previous": r.Value, "provider": r.provider,
					"origin": map[string]any{"kind": "plugin", "name": plugin}}, nil
			},
			func(context.Context) OpCore {
				return OpResult(func(ev map[string]any) (map[string]any, error) {
					switch {
					case r.locked:
						return map[string]any{"deny": "kiln reads " + key + " from Claude Code's settings and never writes them; change it there"}, nil
					case !fitsKind(r, ev["value"]):
						return map[string]any{"deny": fmt.Sprintf("%v does not fit the %s row %s", ev["value"], r.Kind, key)}, nil
					case r.Via != "":
						return map[string]any{"deny": "only " + r.Via + " changes " + key}, nil
					}
					return map[string]any{"deny": noConfigWriter}, nil
				})
			},
			func(res map[string]any) (any, error) {
				// kiln writes no row, so a { value } a hook answered without
				// calling next() was not written: never report it as written.
				if s := strOf(res, "deny"); s != "" {
					return map[string]any{"deny": s}, nil
				}
				return map[string]any{"deny": noConfigWriter}, nil
			}), true, nil

	case "telemetry.log":
		ev, err := telemetryLogEvent(exportMap(arg(0)), e.clock.Now())
		if err != nil {
			return nil, true, err
		}
		return e.telemetryCall(f, "telemetry.log", ev), true, nil

	case "telemetry.mark":
		ev, err := telemetryMarkEvent(exportMap(arg(0)))
		if err != nil {
			return nil, true, err
		}
		return e.telemetryCall(f, "telemetry.mark", ev), true, nil
	}
	return e.answerUI(f, name, arg)
}

// telemetryCall raises a telemetry event for a $ call. kiln has no analytics
// sink, so core only notes (at debug) that nothing was sent; a hook's { deny }
// rejects the caller.
func (e *Engine) telemetryCall(f *frame, event string, ev map[string]any) goja.Value {
	return e.opCall(f, event,
		func(context.Context) (map[string]any, error) { return ev, nil },
		func(context.Context) OpCore {
			return OpResult(func(in map[string]any) (map[string]any, error) {
				e.log.Debug("mods: "+event+" reached kiln's core: kiln has no telemetry sink, nothing is sent", "plugin", f.plugin.name)
				return map[string]any{}, nil
			})
		},
		func(res map[string]any) (any, error) {
			if s := strOf(res, "deny"); s != "" {
				return nil, fmt.Errorf("%s", s)
			}
			return nil, nil
		})
}

var featureName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// checkProps validates TelemetryProp values: a number, a boolean, or
// { value, of } with value one of the strings in of.
func checkProps(v any) error {
	if v == nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("props is an object of name: number | boolean | { value, of }")
	}
	for k, p := range m {
		switch t := p.(type) {
		case bool, float64, int64:
		case map[string]any:
			val, _ := t["value"].(string)
			of, _ := t["of"].([]any)
			found := false
			for _, o := range of {
				if o == val {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("props.%s: %q is not in its \"of\" list", k, val)
			}
		default:
			return fmt.Errorf("props.%s is a number, a boolean or { value, of }", k)
		}
	}
	return nil
}

// telemetryLogEvent is the telemetry.log event for $.telemetry.log's entry: a
// first-party row ({ to?: "anthropic", event, props? }) or a collector record
// ({ to: "collector", event, attributes, loggedAt, span? }). `to` is always
// present on the event; a record's loggedAt is filled when left out.
func telemetryLogEvent(entry map[string]any, now time.Time) (map[string]any, error) {
	if entry == nil {
		return nil, fmt.Errorf("takes { to?, event, props? | attributes, loggedAt }")
	}
	if strOf(entry, "event") == "" {
		return nil, fmt.Errorf("takes an event name")
	}
	to := strOf(entry, "to")
	if _, set := entry["to"]; !set {
		to = "anthropic"
	}
	ev := map[string]any{"to": to, "event": entry["event"]}
	switch to {
	case "anthropic":
		if err := checkProps(entry["props"]); err != nil {
			return nil, err
		}
		if p, ok := entry["props"]; ok {
			ev["props"] = p
		}
	case "collector":
		attrs, ok := entry["attributes"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("a collector record takes attributes: an object")
		}
		ev["attributes"] = attrs
		ev["loggedAt"] = strOf(entry, "loggedAt")
		if ev["loggedAt"] == "" {
			ev["loggedAt"] = now.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		if sp, ok := entry["span"]; ok {
			ev["span"] = sp
		}
	default:
		return nil, fmt.Errorf(`to is "anthropic" or "collector", not %q`, to)
	}
	return ev, nil
}

// telemetryMarkEvent is the telemetry.mark event for $.telemetry.mark's entry.
func telemetryMarkEvent(entry map[string]any) (map[string]any, error) {
	feature := strOf(entry, "feature")
	if !featureName.MatchString(feature) {
		return nil, fmt.Errorf("feature is a snake_case name")
	}
	kind := strOf(entry, "kind")
	if kind != "ok" && kind != "sad" && kind != "bad" {
		return nil, fmt.Errorf(`kind is "ok", "sad" or "bad"`)
	}
	if err := checkProps(entry["props"]); err != nil {
		return nil, err
	}
	ev := map[string]any{"feature": feature, "kind": kind}
	if s := strOf(entry, "reason"); s != "" {
		ev["reason"] = s
	}
	if p, ok := entry["props"]; ok {
		ev["props"] = p
	}
	return ev, nil
}

// SkillPromptInput is a skill's prompt at the moment kiln expands it for the
// model (skill.prompt's input).
type SkillPromptInput struct {
	// Skill is the name the skill was invoked by.
	Skill string
	Text  string
	// Frontmatter is what the SKILL.md declares above its text; nil is {}.
	Frontmatter map[string]any
	// Source is a SkillSource word (userSettings, projectSettings, plugin,
	// built-in); "" reads built-in.
	Source string
	// Plugin is the installed plugin the skill comes from; Path its file.
	Plugin, Path string
}

// SkillPrompt raises skill.prompt for one expansion and returns the text the
// model should read: the hooks' rewrite, or in.Text when none changed it or
// the chain failed. kiln raises it where the Skill tool and a /skill command
// expand a skill's body.
func (e *Engine) SkillPrompt(ctx context.Context, in SkillPromptInput) string {
	if e == nil || !e.Has("skill.prompt") {
		return in.Text
	}
	fm := map[string]any{}
	problem := ""
	for k, v := range in.Frontmatter {
		switch k {
		case "__proto__", "constructor", "prototype":
			problem = "the frontmatter key " + k + " is left out"
		default:
			fm[k] = v
		}
	}
	src := in.Source
	if src == "" {
		src = "built-in"
	}
	ev := map[string]any{"skill": in.Skill, "text": in.Text, "frontmatter": fm, "source": src}
	if problem != "" {
		ev["frontmatterProblem"] = problem
	}
	if in.Plugin != "" {
		ev["pluginName"] = in.Plugin
	}
	if in.Path != "" {
		ev["path"] = in.Path
	}
	res, err := e.Raise(ctx, "skill.prompt", ev, OpResult(func(c map[string]any) (map[string]any, error) {
		return map[string]any{"text": c["text"]}, nil
	}))
	if err != nil {
		e.log.Warn("mods: skill.prompt chain failed; the skill reads as written", "skill", in.Skill, "err", err)
		return in.Text
	}
	if s, ok := res["text"].(string); ok {
		return s
	}
	return in.Text
}
