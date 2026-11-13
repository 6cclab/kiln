package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/tui"
)

// The prompt side of the mods runtime: the typed hand-offs from the TUI and
// the cli to the engine's prompt.* events, and the $.prompt.* services that
// reach the TUI's input box through the driver. Each one is a no-op
// (the input passes through) before mods start or when none hooks the event.

// errNoSession is what $.prompt.submit answers in a -p run: the one prompt it
// was given is the whole session.
var errNoSession = errors.New("kiln -p runs the one prompt it was given: there is no idle session to submit into")

// PromptEdit, PromptChange and PromptAutocomplete are tui.ModsEngine.
func (r *modsRuntime) PromptEdit(ctx context.Context, before mods.PromptBox, start, end int, inputText string) {
	if e := r.get(); e != nil {
		e.PromptEdit(ctx, before, start, end, inputText)
	}
}

func (r *modsRuntime) PromptChange(ctx context.Context, box mods.PromptBox, origin map[string]any) {
	if e := r.get(); e != nil {
		e.PromptChange(ctx, box, origin)
	}
}

func (r *modsRuntime) PromptAutocomplete(ctx context.Context, box mods.PromptBox, token string, start int) []mods.PromptSuggestion {
	if e := r.get(); e != nil {
		return e.PromptAutocomplete(ctx, box, token, start)
	}
	return nil
}

// mention raises prompt.mention for one @path; hooked is false when no mod
// took part, so the caller reads the file as it is.
func (r *modsRuntime) mention(mention, path string) (res mods.MentionResult, hooked bool) {
	e := r.get()
	if e == nil || !e.Has("prompt.mention") {
		return mods.MentionResult{Path: path}, false
	}
	return e.PromptMention(context.Background(), mention, path), true
}

// attachment raises prompt.attachment for text kiln injects for the model;
// keep is false when a hook left it out.
func (r *modsRuntime) attachment(typ, text string, origin map[string]any) (string, bool) {
	e := r.get()
	if e == nil {
		return text, true
	}
	return e.PromptAttachment(context.Background(), typ, text, origin)
}

// promptAttachment is tui.AttachmentFunc.
func (r *modsRuntime) promptAttachment(ctx context.Context, typ, text string, origin map[string]any) (string, bool) {
	e := r.get()
	if e == nil {
		return text, true
	}
	return e.PromptAttachment(ctx, typ, text, origin)
}

// submit is tui.PromptSubmitFunc (and the -p path's, with origin sdk).
func (r *modsRuntime) submit(ctx context.Context, in tui.PromptSubmission) (string, []string, string) {
	e := r.get()
	if e == nil || !e.Has("prompt.submit") {
		return in.Text, nil, ""
	}
	req := mods.PromptSubmitIn{Text: in.Text, Command: in.Command}
	if r.print {
		req.Origin = map[string]any{"kind": "sdk"}
	}
	for _, im := range in.Images {
		data, err := base64.StdEncoding.DecodeString(im.Data)
		if err != nil {
			continue
		}
		req.Attachments = append(req.Attachments, mods.PromptAttachment{Type: "image", MediaType: im.MimeType, Data: data})
	}
	if in.Busy {
		req.TurnID, _ = r.mainTurn.Load().(string)
	}
	out := e.PromptSubmit(ctx, req)
	return out.Text, out.Context, out.Drop
}

// suggestNext routes kiln's own next-prompt guess through prompt.suggest;
// "" when a hook dropped it.
func (r *modsRuntime) suggestNext(ctx context.Context, text string) string {
	if e := r.get(); e != nil && text != "" {
		return e.PromptSuggest(ctx, text)
	}
	return text
}

// facts are what the system prompt is composed for, as claude-code.d.ts's
// PromptComposeArgs names them.
func (r *modsRuntime) facts(tools func() []mods.ToolInfo) map[string]any {
	model := r.model
	if r.modelFn != nil {
		model = r.modelFn()
	}
	surfaces := []any{"terminal"}
	traits := []any{}
	if r.print {
		surfaces = []any{}
		traits = []any{"print"}
	}
	names := []any{}
	if tools != nil {
		for _, t := range tools() {
			names = append(names, t.Name)
		}
	}
	return map[string]any{
		"model": model, "promptModel": model,
		"surfaces": surfaces, "tools": names,
		"outputStyle": nil, "traits": traits,
	}
}

// composePrompt renders the system prompt's sections through prompt.section
// and prompt.compose and joins the survivors; with no mods it is the plain
// join of the non-empty sections.
func (r *modsRuntime) composePrompt(sections []mods.PromptSection) string {
	if e := r.get(); e != nil && (e.Has("prompt.section") || e.Has("prompt.compose")) {
		kept := append([]mods.PromptSection(nil), sections...)
		r.lastSections.Store(&kept)
		sections = e.ComposePrompt(context.Background(), r.facts(r.toolInfo), sections)
	}
	var texts []string
	for _, s := range sections {
		if s.Text != "" {
			texts = append(texts, s.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// hasPromptHooks reports whether a mod shapes the system prompt, so the
// session rebuilds it once the mods are up.
func (r *modsRuntime) hasPromptHooks() bool {
	e := r.get()
	return e != nil && (e.Has("prompt.section") || e.Has("prompt.compose"))
}

// promptServices wires the $.prompt.* methods to the TUI's box.
func (r *modsRuntime) promptServices(svc *mods.Services) {
	svc.PromptRead = func() (mods.PromptBox, bool) {
		if d := r.driver.Load(); d != nil {
			return d.Box()
		}
		return mods.PromptBox{}, false
	}
	svc.PromptFill = func(ctx context.Context, text, mode, plugin string) mods.PromptFillAnswer {
		if d := r.driver.Load(); d != nil {
			return d.Fill(ctx, text, mode, plugin)
		}
		return mods.PromptFillAnswer{Refusal: "no_composer"}
	}
	svc.PromptSuggest = func(ctx context.Context, text string) bool {
		if d := r.driver.Load(); d != nil {
			return d.Suggest(ctx, text)
		}
		return false
	}
	svc.PromptSubmit = func(ctx context.Context, text string, blocks []string, plugin string, asUser bool) error {
		if d := r.driver.Load(); d != nil {
			return d.Submit(ctx, text, blocks, plugin, asUser)
		}
		return errNoSession
	}
	tools := svc.Tools
	svc.PromptCompose = func(_ context.Context, args map[string]any) (map[string]any, []mods.PromptSection) {
		facts := r.facts(tools)
		for k, v := range args {
			facts[k] = v
		}
		var sections []mods.PromptSection
		if p := r.lastSections.Load(); p != nil {
			sections = *p
		}
		return facts, sections
	}
}

// toolInfo lists the tools the model has, for the compose facts.
func (r *modsRuntime) toolInfo() []mods.ToolInfo {
	if f := r.toolsFn; f != nil {
		return f()
	}
	return nil
}
