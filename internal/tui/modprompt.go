package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/msg"
)

// The prompt box's side of the mod API: the box the mods read ($.prompt.read),
// what they write into it ($.prompt.fill/suggest/submit), and the events that
// follow the person's typing (prompt.edit, prompt.change, prompt.autocomplete).

// PromptSubmission is a prompt about to start a turn, for prompt.submit.
type PromptSubmission struct {
	Text    string
	Command *mods.PromptCommand
	Images  []msg.ImageContent
	// Busy is set for a prompt typed while a turn runs: it fires at Enter
	// with that turn's id.
	Busy bool
}

// PromptSubmitFunc raises prompt.submit and returns the prompt as the hooks
// left it, or the reason one dropped it.
type PromptSubmitFunc func(ctx context.Context, in PromptSubmission) (text string, context []string, drop string)

// AttachmentFunc raises prompt.attachment for a message kiln injects for the
// model; keep is false when a hook left it out.
type AttachmentFunc func(ctx context.Context, typ, text string, origin map[string]any) (out string, keep bool)

// modsReplyWait is how long a mod's call into the box waits for the TUI.
const modsReplyWait = 3 * time.Second

// MsgModsFill asks the box to take a plugin's text.
type MsgModsFill struct {
	Text, Mode, Plugin string
	Reply              chan mods.PromptFillAnswer
}

// MsgModsSuggest asks the box to show a plugin's dim suggestion.
type MsgModsSuggest struct {
	Text  string
	Reply chan bool
}

// MsgModsSubmit hands the TUI a plugin's prompt to run as a turn once idle.
type MsgModsSubmit struct {
	Text    string
	Context []string
	Plugin  string
	AsUser  bool
	Reply   chan error
}

type msgModsAutocomplete struct {
	text      string
	cursor    int // rune offset
	tokenRune int // runes in the token
	rows      []mods.PromptSuggestion
}

type pluginPrompt struct {
	text    string
	context []string
	plugin  string
	asUser  bool
}

// promptSnap is the box at one moment.
type promptSnap struct {
	text   string
	cursor int // runes
}

func (m Model) promptSnap() promptSnap {
	return promptSnap{text: m.editor.Value(), cursor: m.editor.CursorOffset()}
}

func (s promptSnap) box() mods.PromptBox {
	return mods.PromptBox{Text: s.text, Cursor: mods.RuneToU16(s.text, s.cursor)}
}

// splice is the edit that turns before into after: the span of before it
// replaces and what went in, both in UTF-16 code units.
func splice(before, after string) (start, end int, input string) {
	b, a := []rune(before), []rune(after)
	p := 0
	for p < len(b) && p < len(a) && b[p] == a[p] {
		p++
	}
	s := 0
	for s < len(b)-p && s < len(a)-p && b[len(b)-1-s] == a[len(a)-1-s] {
		s++
	}
	return mods.RuneToU16(before, p), mods.RuneToU16(before, len(b)-s), string(a[p : len(a)-s])
}

// Prompt box published to the mods.

// SetBox records the box for $.prompt.read; called from the TUI goroutine.
func (d *ModsDriver) SetBox(b mods.PromptBox) {
	d.mu.Lock()
	d.box = b
	d.mu.Unlock()
}

// Box is the prompt box as it stands; ok is false until the TUI is running.
func (d *ModsDriver) Box() (mods.PromptBox, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.box, d.send != nil
}

// queue runs f on the driver's one event goroutine, in order; an event is
// dropped (never blocks the TUI) when the mods have fallen far behind.
func (d *ModsDriver) queue(f func(ctx context.Context)) {
	select {
	case d.events <- f:
	default:
	}
}

func (d *ModsDriver) runEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case f := <-d.events:
			f(ctx)
		}
	}
}

// PromptChanged raises prompt.edit or prompt.change for a change of the box
// the person's last message made. cause is the editor's (Cause); key says the
// message was a key or a paste.
func (d *ModsDriver) PromptChanged(before, after promptSnap, cause string, key bool) {
	if before.text == after.text {
		return
	}
	switch {
	case cause == "" && key:
		start, end, in := splice(before.text, after.text)
		d.queue(func(ctx context.Context) { d.engine.PromptEdit(ctx, before.box(), start, end, in) })
	default:
		origin := map[string]any{"kind": "engine"}
		switch {
		case cause == "history", cause == "clear":
			origin = map[string]any{"kind": cause}
		case strings.HasPrefix(cause, "plugin:"):
			origin = map[string]any{"kind": "plugin", "name": strings.TrimPrefix(cause, "plugin:")}
		}
		d.queue(func(ctx context.Context) { d.engine.PromptChange(ctx, after.box(), origin) })
	}
}

// Fill puts text in the box by mode, as the TUI answers it.
func (d *ModsDriver) Fill(ctx context.Context, text, mode, plugin string) mods.PromptFillAnswer {
	d.mu.Lock()
	send := d.send
	d.mu.Unlock()
	if send == nil {
		return mods.PromptFillAnswer{Refusal: "no_composer"}
	}
	reply := make(chan mods.PromptFillAnswer, 1)
	send(MsgModsFill{Text: text, Mode: mode, Plugin: plugin, Reply: reply})
	select {
	case a := <-reply:
		return a
	case <-ctx.Done():
	case <-time.After(modsReplyWait):
	}
	return mods.PromptFillAnswer{}
}

// Suggest proposes text as the box's dim suggestion.
func (d *ModsDriver) Suggest(ctx context.Context, text string) bool {
	d.mu.Lock()
	send := d.send
	d.mu.Unlock()
	if send == nil {
		return false
	}
	reply := make(chan bool, 1)
	send(MsgModsSuggest{Text: text, Reply: reply})
	select {
	case ok := <-reply:
		return ok
	case <-ctx.Done():
	case <-time.After(modsReplyWait):
	}
	return false
}

// Submit hands the TUI a plugin's prompt; it returns once the turn started or
// the prompt is queued behind the running one.
func (d *ModsDriver) Submit(ctx context.Context, text string, blocks []string, plugin string, asUser bool) error {
	d.mu.Lock()
	send := d.send
	d.mu.Unlock()
	if send == nil {
		return fmt.Errorf("kiln's interface is not up")
	}
	reply := make(chan error, 1)
	send(MsgModsSubmit{Text: text, Context: blocks, Plugin: plugin, AsUser: asUser, Reply: reply})
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(modsReplyWait):
		return fmt.Errorf("kiln's interface did not take the prompt")
	}
}

// Autocomplete asks the mods for rows for the token at the cursor, off the
// TUI goroutine; nil when there is nothing to ask.
func (m Model) modAutocompleteCmd() tea.Cmd {
	d := m.cfg.Mods
	if d == nil || d.engine == nil {
		return nil
	}
	snap := m.promptSnap()
	runes := []rune(snap.text)
	start := snap.cursor
	for start > 0 && !isSeparator(runes[start-1]) {
		start--
	}
	if start == snap.cursor {
		return nil
	}
	token := string(runes[start:snap.cursor])
	box := snap.box()
	start16 := mods.RuneToU16(snap.text, start)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows := d.engine.PromptAutocomplete(ctx, box, token, start16)
		if len(rows) == 0 {
			return nil
		}
		return msgModsAutocomplete{text: snap.text, cursor: snap.cursor, tokenRune: snap.cursor - start, rows: rows}
	}
}

// applyModAutocomplete adds the mods' rows beneath the engine's own, unless
// the person has typed on since they were asked for.
func (m Model) applyModAutocomplete(msg msgModsAutocomplete) Model {
	if m.editor.Value() != msg.text || m.editor.CursorOffset() != msg.cursor {
		return m
	}
	if m.popup != nil {
		for _, it := range m.popup.Items {
			if it.Mod {
				return m
			}
		}
	}
	_, col := m.editor.CursorLine()
	start := col - msg.tokenRune
	if start < 0 {
		start = 0
	}
	var items []AutocompleteItem
	for _, r := range msg.rows {
		items = append(items, AutocompleteItem{Value: r.Text, Label: r.Label, Description: r.Description,
			Mod: true, ModStart: start, ModEnd: col})
	}
	if m.popup == nil {
		m.popup = &Popup{Kind: KindArgument, Start: start, End: col}
	}
	m.popup.Items = append(m.popup.Items, items...)
	m.editor.PopupActive = true
	return m
}

func (m Model) promptDialogOpen() bool {
	return m.dialog != nil || m.prompt.Active()
}

// modsFill puts a plugin's text in the box.
func (m Model) modsFill(msg MsgModsFill) (Model, tea.Cmd) {
	if m.promptDialogOpen() {
		msg.Reply <- mods.PromptFillAnswer{Refusal: "dialog", Box: m.promptSnap().box()}
		return m, nil
	}
	cur := []rune(m.editor.Value())
	off := m.editor.CursorOffset()
	if off > len(cur) {
		off = len(cur)
	}
	in := []rune(msg.Text)
	var next []rune
	var cursor int
	switch msg.Mode {
	case "append":
		next = append(append([]rune{}, cur...), in...)
		cursor = len(next)
	case "insert":
		next = append(append(append([]rune{}, cur[:off]...), in...), cur[off:]...)
		cursor = off + len(in)
	default:
		next = in
		cursor = len(in)
	}
	m.editor.SetValueCursor(string(next), cursor)
	m.editor.SetCause("plugin:" + msg.Plugin)
	var cmd tea.Cmd
	m, cmd = m.refreshPopup()
	msg.Reply <- mods.PromptFillAnswer{Filled: true, Box: m.promptSnap().box()}
	return m, cmd
}

// modsSuggest shows a plugin's text as the box's dim suggestion when the box
// is empty and idle.
func (m Model) modsSuggest(msg MsgModsSuggest) Model {
	ok := !m.promptDialogOpen() && !m.busy && m.editor.Value() == "" && strings.TrimSpace(msg.Text) != ""
	if ok {
		m.editor.SetSuggestion(strings.TrimSpace(msg.Text))
	}
	msg.Reply <- ok
	return m
}

// modsSubmit runs a plugin's prompt as a turn of its own once the session is
// idle, or queues it behind what runs now.
func (m Model) modsSubmit(msg MsgModsSubmit) (Model, tea.Cmd) {
	m.pluginPrompts = append(m.pluginPrompts, pluginPrompt{text: msg.Text, context: msg.Context, plugin: msg.Plugin, asUser: msg.AsUser})
	msg.Reply <- nil
	return m.drainPluginPrompts()
}

// drainPluginPrompts starts the first queued plugin prompt when the session
// is idle.
func (m Model) drainPluginPrompts() (Model, tea.Cmd) {
	if len(m.pluginPrompts) == 0 || m.busy || m.background != nil || m.promptDialogOpen() {
		return m, nil
	}
	p := m.pluginPrompts[0]
	m.pluginPrompts = m.pluginPrompts[1:]
	prompt := p.text
	if !p.asUser {
		prompt = fmt.Sprintf("The %s plugin sent a message: %s", p.plugin, p.text)
		m.commitNote(fmt.Sprintf("%s plugin sent a prompt: %s", p.plugin, oneLine(p.text)))
	} else {
		m.commit(RenderUserMessage(p.text, m.contentWidth()))
	}
	blocks := p.context
	if len(blocks) > 0 {
		prompt = "<hook-context>\n" + strings.Join(blocks, "\n\n") + "\n</hook-context>\n\n" + prompt
	}
	if m.cfg.TakeModeReminder != nil {
		if pre := m.cfg.TakeModeReminder(); pre != "" {
			prompt = pre + "\n\n" + prompt
		}
	}
	typed := ""
	if p.asUser {
		typed = p.text
	}
	next, cmd := m.beginTurn(prompt, typed, nil)
	return next.(Model), cmd
}

// attach runs hook context blocks of a settings hook event through the
// mods' prompt.attachment, dropping the ones a hook left out.
func (m Model) attach(blocks []string, event string) []string {
	var out []string
	for _, b := range blocks {
		if t, keep := m.cfg.Attachment(context.Background(), "hook_additional_context", b,
			map[string]any{"kind": "hook", "event": event}); keep {
			out = append(out, t)
		}
	}
	return out
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return s
}

// afterPrompt publishes the box and raises the events for what the last
// message changed in it.
func (m Model) afterPrompt(before promptSnap, tm tea.Msg) {
	d := m.cfg.Mods
	if d == nil {
		return
	}
	after := m.promptSnap()
	d.SetBox(after.box())
	var key bool
	switch tm.(type) {
	case tea.KeyPressMsg, tea.PasteMsg:
		key = true
	}
	d.PromptChanged(before, after, m.editor.Cause(), key)
}
