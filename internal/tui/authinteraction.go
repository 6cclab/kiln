package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/tools"
)

// errLoginCancelled is what an in-session login prompt returns when the
// user presses Esc (or Ctrl+C) on it.
var errLoginCancelled = errors.New("cancelled")

// authInteraction is the in-session auth.Interaction: /login runs the same
// flow as `kiln login`, with its prompts drawn as ask-user questions above
// the editor and its notices committed to the transcript.
type authInteraction struct{ b *Bridge }

// NewAuthInteraction returns the auth.Interaction /login uses inside the
// TUI. Prompt blocks the calling goroutine (never the event loop) until the
// user answers, cancels, or ctx ends; a prompt whose ctx ends is withdrawn
// from the screen, which is how Anthropic's manual-code prompt goes away
// when the browser callback wins the race.
func NewAuthInteraction(b *Bridge) auth.Interaction { return &authInteraction{b: b} }

func (a *authInteraction) Prompt(ctx context.Context, p auth.Prompt) (string, error) {
	switch p.Type {
	case auth.PromptSelect:
		opts := make([]tools.AskUserOption, len(p.Options))
		for i, o := range p.Options {
			opts[i] = tools.AskUserOption{Label: o.Label, Description: o.Description}
		}
		ans, err := a.ask(ctx, MsgAskUserPrompt{
			Questions: []tools.AskUserQuestion{{Question: p.Message, Header: "login", Options: opts}},
			NoOther:   true,
		})
		if err != nil {
			return "", err
		}
		for _, o := range p.Options {
			if o.Label == ans {
				return o.ID, nil
			}
		}
		return "", fmt.Errorf("unknown choice %q", ans)
	default:
		return a.ask(ctx, MsgAskUserPrompt{
			Questions: []tools.AskUserQuestion{{Question: p.Message}},
			Entry: &TextEntry{
				Header:      "login",
				Secret:      p.Type == auth.PromptSecret,
				Placeholder: p.Placeholder,
				// A blank text field means "the default" (the terminal
				// flow's contract); a secret or pasted code has no default.
				AllowEmpty: p.Type == auth.PromptText,
			},
		})
	}
}

// ask sends the prompt and waits for the one answer.
func (a *authInteraction) ask(ctx context.Context, msg MsgAskUserPrompt) (string, error) {
	reply := make(chan AskUserReply, 1)
	msg.Reply = reply
	a.b.Send(msg)
	select {
	case r := <-reply:
		if r.Cancelled || len(r.Answers) == 0 || len(r.Answers[0].Answers) == 0 {
			return "", errLoginCancelled
		}
		return r.Answers[0].Answers[0], nil
	case <-ctx.Done():
		a.b.Send(MsgPromptWithdrawn{Reply: reply})
		return "", ctx.Err()
	case <-a.b.quit:
		return "", errLoginCancelled
	}
}

func (a *authInteraction) Notify(e auth.Event) {
	switch e.Type {
	case auth.EventInfo:
		lines := []string{e.Message}
		for _, l := range e.Links {
			label := ""
			if l.Label != "" {
				label = l.Label + ": "
			}
			lines = append(lines, "  "+label+l.URL)
		}
		a.b.CommitNote(strings.Join(lines, "\n"))
	case auth.EventAuthURL:
		instructions := e.Instructions
		if instructions == "" {
			instructions = "Open this URL to authorize:"
		}
		a.commitURL(instructions, e.URL)
		a.open(e.URL)
	case auth.EventDeviceCode:
		a.b.CommitNote(fmt.Sprintf("Enter code: %s", e.UserCode))
		a.commitURL("Go to:", e.VerificationURI)
		if e.ExpiresInSeconds > 0 {
			a.b.CommitNote(fmt.Sprintf("(expires in %d min)", (e.ExpiresInSeconds+30)/60))
		}
		a.open(e.VerificationURI)
	case auth.EventProgress:
		a.b.CommitNote("... " + e.Message)
	}
}

// commitURL commits the instruction as a note and the URL on a row of its
// own, unwrapped, so the terminal's own soft wrap keeps a long OAuth URL
// clickable and copyable as one link.
func (a *authInteraction) commitURL(instructions, url string) {
	a.b.CommitNote(instructions)
	a.b.Commit([]string{"  " + Hyperlink(url, url), ""})
}

func (a *authInteraction) open(url string) {
	if !auth.IsHeadless() {
		auth.OpenBrowser(url)
	}
}
