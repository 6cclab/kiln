package mods

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

// The prompt events: what kiln raises where the person types, submits and
// completes, where it reads an @mention, injects a message for the model on
// its own, and renders the system prompt. Each call site is in kiln's TUI or
// cli; these are the typed entry points they use. Every one is a no-op (the
// input passes through) when no mod hooks the event, and a chain that fails
// or outlasts promptBound never blocks the prompt: kiln uses its own answer.

// promptBound caps one prompt event for a call site that is on the way to the
// model (submit, mention, attachment, system prompt).
const promptBound = 10 * time.Second

// PromptBox is the person's prompt box: the draft and the caret's offset into
// it in UTF-16 code units.
type PromptBox struct {
	Text   string
	Cursor int
}

// PromptFillAnswer is what the box did with a fill: Refusal is "no_composer"
// where kiln draws no box (a -p run) and "dialog" while a dialog holds the
// keys; Box is the draft afterwards.
type PromptFillAnswer struct {
	Filled  bool
	Refusal string
	Box     PromptBox
}

// PromptAttachment is one non-text item of a submitted prompt. ID is set by
// the engine for the submission in flight; Data is the item's bytes.
type PromptAttachment struct {
	ID        string
	Type      string
	MediaType string
	Filename  string
	Data      []byte
}

// PromptSubmitIn is a prompt about to start a turn.
type PromptSubmitIn struct {
	Text        string
	Origin      map[string]any // nil: the person's own Enter (composer)
	Attachments []PromptAttachment
	// Command is the slash command whose expansion this is ({name, args}), nil
	// for typed text: a rewrite of Text is then not applied.
	Command *PromptCommand
	Wait    bool
	TurnID  string
	Context []string
}

// PromptCommand is the slash command a submission runs.
type PromptCommand struct{ Name, Args string }

// PromptSubmitOut is the prompt as the hooks left it.
type PromptSubmitOut struct {
	Text    string
	Context []string
	// Drop is a hook's reason for stopping the prompt; empty when it enters.
	Drop string
}

// PromptSuggestion is one row a prompt.autocomplete hook adds to the typeahead.
type PromptSuggestion struct{ Text, Label, Description string }

// PromptSection is one section of the system prompt: Scope is "shared" or
// "session".
type PromptSection struct{ ID, Text, Scope string }

// MentionResult is what the prompt.mention hooks decided for one @path.
type MentionResult struct {
	// Path is the file to read (the hooks may have redirected it).
	Path string
	// Context is extra text the model reads after the mentioned files.
	Context []string
	// Deny is a hook's reason for reading nothing; Skip is set when a hook
	// answered without reading the file, so nothing is attached.
	Deny string
	Skip bool
}

var mediaSeq atomic.Int64

// mediaStore holds the attachments of the submissions in flight, for
// $.prompt.media.
type mediaStore struct {
	mu    sync.Mutex
	items map[string]PromptAttachment
}

func (s *mediaStore) put(a PromptAttachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]PromptAttachment{}
	}
	s.items[a.ID] = a
}

func (s *mediaStore) drop(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.items, id)
	}
}

func (s *mediaStore) get(id string) (PromptAttachment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.items[id]
	return a, ok
}

// mediaSlice is the most one $.prompt.media call answers (4 MiB).
const mediaSlice = 4 << 20

// Media answers $.prompt.media: a slice of an attachment of a submission in
// flight, as claude-code.d.ts's PromptMedia.
func (e *Engine) media(id string, offset int) map[string]any {
	a, ok := e.mediaItems.get(id)
	if !ok {
		return map[string]any{"deny": fmt.Sprintf("no prompt submission in flight carries an item %q", id)}
	}
	if len(a.Data) == 0 {
		return map[string]any{"unreadable": "kiln holds no bytes for this item"}
	}
	if offset < 0 || offset >= len(a.Data) {
		return map[string]any{"deny": fmt.Sprintf("offset %d is past the end of the item (%d bytes)", offset, len(a.Data))}
	}
	end := offset + mediaSlice
	if end > len(a.Data) {
		end = len(a.Data)
	}
	out := map[string]any{
		"type": a.Type, "mediaType": a.MediaType, "size": len(a.Data),
		"base64": base64.StdEncoding.EncodeToString(a.Data[offset:end]),
	}
	if end < len(a.Data) {
		out["next"] = end
	}
	return out
}

func bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, promptBound)
}

func strList(v any) []string {
	var out []string
	switch l := v.(type) {
	case []any:
		for _, x := range l {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, s := range l {
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// PromptSubmit raises prompt.submit for a prompt about to start a turn and
// returns it as the hooks left it. The attachments' bytes are readable with
// $.prompt.media until the chain has settled.
func (e *Engine) PromptSubmit(ctx context.Context, in PromptSubmitIn) PromptSubmitOut {
	pass := PromptSubmitOut{Text: in.Text, Context: in.Context}
	if e == nil || !e.Has("prompt.submit") {
		return pass
	}
	ev := map[string]any{"text": in.Text, "wait": in.Wait}
	if in.Origin == nil {
		ev["origin"] = map[string]any{"kind": "composer"}
	} else {
		ev["origin"] = in.Origin
	}
	if in.TurnID != "" {
		ev["turnId"] = in.TurnID
	}
	if len(in.Context) > 0 {
		ev["context"] = in.Context
	}
	if in.Command != nil {
		ev["command"] = map[string]any{"name": in.Command.Name, "args": in.Command.Args}
	}
	var ids []string
	if len(in.Attachments) > 0 {
		rows := make([]any, 0, len(in.Attachments))
		for _, a := range in.Attachments {
			a.ID = fmt.Sprintf("att_%d", mediaSeq.Add(1))
			ids = append(ids, a.ID)
			e.mediaItems.put(a)
			row := map[string]any{"type": a.Type, "id": a.ID}
			if a.MediaType != "" {
				row["mediaType"] = a.MediaType
			}
			if a.Filename != "" {
				row["filename"] = a.Filename
			}
			rows = append(rows, row)
		}
		ev["attachments"] = rows
		defer e.mediaItems.drop(ids)
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	res, err := e.Raise(ctx, "prompt.submit", ev, OpResult(func(got map[string]any) (map[string]any, error) {
		out := map[string]any{"text": got["text"], "origin": got["origin"]}
		if c := strList(got["context"]); len(c) > 0 {
			out["context"] = c
		}
		return out, nil
	}))
	if err != nil {
		e.log.Warn("mods: prompt.submit chain failed; the prompt goes as typed", "err", err)
		return pass
	}
	if d, ok := res["drop"].(string); ok {
		if d == "" {
			d = "a mod stopped the prompt"
		}
		return PromptSubmitOut{Drop: d}
	}
	out := PromptSubmitOut{Text: in.Text, Context: strList(res["context"])}
	if s, ok := res["text"].(string); ok {
		out.Text = s
	}
	if in.Command != nil {
		out.Text = in.Text // a command's line is not rewritten
	}
	return out
}

// PromptEdit raises prompt.edit after the person edited the box: before is
// the draft and caret (UTF-16) it had, [start,end) the span replaced and
// inputText what went in. Observe-only: the edit is already in the box, so a
// hook's rewrite of it is not applied.
func (e *Engine) PromptEdit(ctx context.Context, before PromptBox, start, end int, inputText string) {
	if e == nil || !e.Has("prompt.edit") {
		return
	}
	ev := map[string]any{"origin": map[string]any{"kind": "composer"}, "text": before.Text,
		"cursor": before.Cursor, "start": start, "end": end, "inputText": inputText}
	_, err := e.Raise(ctx, "prompt.edit", ev, OpResult(func(got map[string]any) (map[string]any, error) {
		text, _ := got["text"].(string)
		s, _ := got["start"].(float64)
		en, _ := got["end"].(float64)
		in, _ := got["inputText"].(string)
		after := splice16(text, int(s), int(en), in)
		return map[string]any{"text": after, "cursor": int(s) + u16len(in)}, nil
	}))
	if err != nil {
		e.log.Debug("mods: prompt.edit chain failed", "err", err)
	}
}

// PromptChange raises prompt.change after the box's text changed by something
// other than an edit. origin is claude-code.d.ts's PromptChangeOrigin.
func (e *Engine) PromptChange(ctx context.Context, box PromptBox, origin map[string]any) {
	if e == nil || !e.Has("prompt.change") {
		return
	}
	ev := map[string]any{"text": box.Text, "cursor": box.Cursor, "origin": origin}
	_, err := e.Raise(ctx, "prompt.change", ev, OpResult(func(got map[string]any) (map[string]any, error) {
		return map[string]any{"text": got["text"]}, nil
	}))
	if err != nil {
		e.log.Debug("mods: prompt.change chain failed", "err", err)
	}
}

// PromptAutocomplete raises prompt.autocomplete for the token at the cursor
// and returns the rows the hooks add to the typeahead.
func (e *Engine) PromptAutocomplete(ctx context.Context, box PromptBox, token string, start int) []PromptSuggestion {
	if e == nil || !e.Has("prompt.autocomplete") {
		return nil
	}
	ev := map[string]any{"text": box.Text, "cursor": box.Cursor, "token": token, "start": start}
	res, err := e.Raise(ctx, "prompt.autocomplete", ev, OpResult(func(map[string]any) (map[string]any, error) {
		return map[string]any{"suggestions": []any{}}, nil
	}))
	if err != nil {
		e.log.Debug("mods: prompt.autocomplete chain failed", "err", err)
		return nil
	}
	rows, _ := res["suggestions"].([]any)
	var out []PromptSuggestion
	for _, r := range rows {
		m, _ := r.(map[string]any)
		if t := strOf(m, "text"); t != "" {
			out = append(out, PromptSuggestion{Text: t, Label: strOf(m, "label"), Description: strOf(m, "description")})
		}
	}
	return out
}

// PromptSuggest raises prompt.suggest for the engine's own guess at the next
// prompt and returns the text to show ("" when a hook dropped it).
func (e *Engine) PromptSuggest(ctx context.Context, text string) string {
	if e == nil || !e.Has("prompt.suggest") {
		return text
	}
	shown := text
	res, err := e.Raise(ctx, "prompt.suggest", map[string]any{"text": text, "origin": map[string]any{"kind": "suggestion"}},
		OpResult(func(got map[string]any) (map[string]any, error) {
			shown = strOf(got, "text")
			return map[string]any{"isShown": true}, nil
		}))
	if err != nil {
		e.log.Warn("mods: prompt.suggest chain failed; showing the suggestion as it is", "err", err)
		return text
	}
	if b, ok := res["isShown"].(bool); ok && !b {
		return ""
	}
	return shown
}

// PromptMention raises prompt.mention for one @path before kiln reads it.
func (e *Engine) PromptMention(ctx context.Context, mention, path string) MentionResult {
	pass := MentionResult{Path: path}
	if e == nil || !e.Has("prompt.mention") {
		return pass
	}
	read := false
	readPath := path
	ctx, cancel := bounded(ctx)
	defer cancel()
	res, err := e.Raise(ctx, "prompt.mention", map[string]any{"mention": mention, "path": path},
		OpResult(func(got map[string]any) (map[string]any, error) {
			read = true
			readPath = strOf(got, "path")
			return map[string]any{"type": "file"}, nil
		}))
	if err != nil {
		e.log.Warn("mods: prompt.mention chain failed; reading the file", "mention", mention, "err", err)
		return pass
	}
	if d, ok := res["deny"].(string); ok {
		if d == "" {
			d = "a mod refused the mention"
		}
		return MentionResult{Path: path, Deny: d}
	}
	if !read {
		return MentionResult{Path: path, Skip: true}
	}
	if readPath == "" {
		readPath = path
	}
	return MentionResult{Path: readPath, Context: strList(res["context"])}
}

// PromptAttachment raises prompt.attachment for a message kiln injects for
// the model on its own and returns the text to carry; keep is false when a
// hook answered { text: null }.
func (e *Engine) PromptAttachment(ctx context.Context, typ, text string, origin map[string]any) (string, bool) {
	if e == nil || !e.Has("prompt.attachment") {
		return text, true
	}
	if origin == nil {
		origin = map[string]any{"kind": "engine"}
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	res, err := e.Raise(ctx, "prompt.attachment", map[string]any{"type": typ, "text": text, "origin": origin},
		OpResult(func(got map[string]any) (map[string]any, error) {
			return map[string]any{"text": got["text"]}, nil
		}))
	if err != nil {
		e.log.Warn("mods: prompt.attachment chain failed; the message goes as it is", "type", typ, "err", err)
		return text, true
	}
	v, has := res["text"]
	if has && v == nil {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	return text, true
}

func sectionMaps(list []PromptSection) []any {
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, map[string]any{"id": s.ID, "text": s.Text, "scope": s.Scope})
	}
	return out
}

// sectionsOf reads a prompt.compose answer; ok is false for a list the engine
// refuses (an empty or repeated id, a bad scope, a shared section after a
// session one).
func sectionsOf(v any) (list []PromptSection, ok bool) {
	rows, isList := v.([]any)
	if !isList {
		return nil, false
	}
	seen := map[string]bool{}
	sawSession := false
	for _, r := range rows {
		m, _ := r.(map[string]any)
		s := PromptSection{ID: strOf(m, "id"), Text: strOf(m, "text"), Scope: strOf(m, "scope")}
		if s.ID == "" || seen[s.ID] || (s.Scope != "shared" && s.Scope != "session") {
			return nil, false
		}
		if s.Scope == "shared" && sawSession {
			return nil, false
		}
		if s.Scope == "session" {
			sawSession = true
		}
		seen[s.ID] = true
		list = append(list, s)
	}
	return list, true
}

// PromptSectionsOnly runs each section through prompt.section (a hook may
// rewrite its text, or drop it with text: null) and returns what is left.
func (e *Engine) PromptSectionsOnly(ctx context.Context, sections []PromptSection) []PromptSection {
	if e == nil || !e.Has("prompt.section") {
		return sections
	}
	var out []PromptSection
	for _, s := range sections {
		cctx, cancel := bounded(ctx)
		res, err := e.Raise(cctx, "prompt.section", map[string]any{"name": s.ID, "text": s.Text},
			OpResult(func(got map[string]any) (map[string]any, error) {
				return map[string]any{"text": got["text"]}, nil
			}))
		cancel()
		if err != nil {
			e.log.Warn("mods: prompt.section chain failed; the section stands", "section", s.ID, "err", err)
			out = append(out, s)
			continue
		}
		v, has := res["text"]
		if has && v == nil {
			continue
		}
		if t, ok := v.(string); ok {
			s.Text = t
		}
		out = append(out, s)
	}
	return out
}

// PromptCompose raises prompt.compose over sections (the engine's own
// composition, after prompt.section) with the facts it was composed for, and
// returns the list the hooks settled on.
func (e *Engine) PromptCompose(ctx context.Context, facts map[string]any, sections []PromptSection) []PromptSection {
	if e == nil || !e.Has("prompt.compose") {
		return sections
	}
	ctx, cancel := bounded(ctx)
	defer cancel()
	res, err := e.Raise(ctx, "prompt.compose", facts, OpResult(func(map[string]any) (map[string]any, error) {
		return map[string]any{"sections": sectionMaps(sections)}, nil
	}))
	if err != nil {
		e.log.Warn("mods: prompt.compose chain failed; the system prompt stands", "err", err)
		return sections
	}
	if list, ok := sectionsOf(res["sections"]); ok {
		return list
	}
	e.log.Warn("mods: prompt.compose answered a list the engine refuses; the system prompt stands")
	return sections
}

// ComposePrompt renders the system prompt's sections through both events.
func (e *Engine) ComposePrompt(ctx context.Context, facts map[string]any, sections []PromptSection) []PromptSection {
	return e.PromptCompose(ctx, facts, e.PromptSectionsOnly(ctx, sections))
}

// u16len is the length of s in UTF-16 code units.
func u16len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// splice16 replaces [start,end) of text, in UTF-16 code units, with in.
func splice16(text string, start, end int, in string) string {
	u := utf16.Encode([]rune(text))
	if start < 0 {
		start = 0
	}
	if end > len(u) {
		end = len(u)
	}
	if start > end {
		start = end
	}
	out := append(append(append([]uint16{}, u[:start]...), utf16.Encode([]rune(in))...), u[end:]...)
	return string(utf16.Decode(out))
}

// RuneToU16 converts an offset in runes of s to UTF-16 code units.
func RuneToU16(s string, runes int) int {
	n, i := 0, 0
	for _, r := range s {
		if i >= runes {
			break
		}
		i++
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// U16ToRune converts an offset in UTF-16 code units of s to runes.
func U16ToRune(s string, units int) int {
	n, i := 0, 0
	for _, r := range s {
		if n >= units {
			break
		}
		i++
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return i
}
