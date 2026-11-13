package mods

import (
	"context"
	"fmt"

	"github.com/dop251/goja"
)

// The session-meta $ methods: the transcript, who is signed in, the surfaces
// the session draws on, and messages to other sessions. Where kiln has no
// counterpart (remote surfaces, peers, a first-party login a mod may spend)
// each answers with kiln's real state; docs/mods-gap.md says which.

// maxSessionMessages is how many entries $.session.messages returns, the
// newest (claude-code.d.ts: "the newest 4096 entries").
const maxSessionMessages = 4096

// capMessages keeps the newest maxSessionMessages rows. In the API form the
// list opens on a user message, as the Messages API requires.
func capMessages(rows []any, api bool) []any {
	if len(rows) > maxSessionMessages {
		rows = rows[len(rows)-maxSessionMessages:]
	}
	if api {
		for len(rows) > 0 {
			if m, _ := rows[0].(map[string]any); strOf(m, "role") == "user" {
				break
			}
			rows = rows[1:]
		}
	}
	return rows
}

// sendRecipient spells a $.session.send recipient as the SendMessage tool
// does: a string, or { sessionId } / { agentId } as that id.
func sendRecipient(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, t != ""
	case map[string]any:
		if s := strOf(t, "sessionId"); s != "" {
			return s, true
		}
		if s := strOf(t, "agentId"); s != "" {
			return s, true
		}
	}
	return "", false
}

func (e *Engine) answerSession(f *frame, name string, arg func(int) goja.Value) (out any, handled bool, err error) {
	svc := e.opts.Services
	switch name {
	case "session.messages":
		if svc.Messages == nil {
			return nil, true, noService(name)
		}
		args := exportMap(arg(0))
		agentID := strOf(args, "agentId")
		as := strOf(args, "as")
		if as != "" && as != "api" {
			return nil, true, fmt.Errorf(`takes { agentId?, as?: "api" }`)
		}
		return e.goAsync(func() (any, error) {
			rows, err := svc.Messages(withoutHook(context.Background(), f.h), agentID, as == "api")
			if err != nil {
				if agentID != "" {
					// An agent the session cannot read is a deny, never the main one.
					return map[string]any{"deny": err.Error()}, nil
				}
				return nil, err
			}
			return capMessages(rows, as == "api"), nil
		}), true, nil

	case "session.identity":
		// Only a mod the organization's managed settings install may read
		// the account, and kiln never installs a mod that way: it holds no
		// login and reads no managed mod source.
		return e.promiseOf(e.mustJS(map[string]any{
			"deny": "kiln holds no login and installs no mod from managed settings, so it answers no identity",
		})), true, nil

	case "session.authorize":
		// A handle lets a mod spend the person's Anthropic credential at a
		// first-party host. kiln runs on the models the person configured and
		// lends no credential to a mod: it answers the d.ts's "no first-party
		// credential" (null), whatever keys the session holds. $.http.fetch's
		// `auth` is then never given a handle.
		return e.promiseOf(goja.Null()), true, nil

	case "session.surfaces", "session.surface":
		return e.goAsync(func() (any, error) {
			list := []any{}
			if svc.Surfaces != nil {
				for _, s := range svc.Surfaces() {
					list = append(list, s)
				}
			}
			if name == "session.surface" {
				if len(list) == 0 {
					return nil, nil
				}
				return list[0], nil
			}
			return list, nil
		}), true, nil

	case "session.send":
		args := exportMap(arg(0))
		to, ok := sendRecipient(args["to"])
		text, hasText := args["text"].(string)
		if !ok || !hasText {
			return nil, true, fmt.Errorf("takes { to: string | { sessionId } | { agentId }, text }")
		}
		plugin := f.plugin.name
		return e.opCall(f, "session.send",
			func(context.Context) (map[string]any, error) {
				return map[string]any{"to": to, "text": text, "origin": map[string]any{"kind": "plugin", "name": plugin}}, nil
			},
			func(context.Context) OpCore {
				return OpResult(func(ev map[string]any) (map[string]any, error) { return undelivered(strOf(ev, "to")), nil })
			}, nil), true, nil
	}
	return e.answerPrompt(f, name, arg)
}

// undelivered is kiln's answer to a send: it has no SendMessage tool and no
// other session or agent inbox, so nothing is ever queued at a recipient.
func undelivered(to string) map[string]any {
	return map[string]any{"isDelivered": false, "reason": fmt.Sprintf("nobody by the name %q: kiln runs one session and has no inbox to deliver a message to another agent or session", to)}
}
