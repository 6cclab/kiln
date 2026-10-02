// Package automode is kiln's auto mode classifier: the model that reviews
// an action auto mode would otherwise run without asking
// (internal/claude/permission's Classifier). It builds the request — what
// the classifier may see is fixed in transcript.go — sends it through the
// provider layer on the "fast" model role, or the session model, and reads
// back an allow or a block.
package automode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Streamer is the model-calling surface the classifier needs; every
// provider.Provider satisfies it.
type Streamer interface {
	Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error))
}

// DefaultTimeout bounds one classifier call. Past it the call fails, and a
// failed call asks the user.
const DefaultTimeout = 60 * time.Second

// maxAnswerTokens caps the classifier's reply: one short JSON object.
const maxAnswerTokens = 1024

// Classifier is the model-backed permission.Classifier.
type Classifier struct {
	// Resolve picks the model for one call (Resolver).
	Resolve func() (Streamer, provider.Model, error)
	// Config is the user's autoMode settings.
	Config settings.AutoModeConfig
	// Memory is the CLAUDE.md text the session loaded, shown to the
	// classifier as the user's configuration.
	Memory string
	// Timeout bounds one call; zero means DefaultTimeout.
	Timeout time.Duration
}

var _ permission.Classifier = (*Classifier)(nil)

// Request builds the classifier's system prompt and its one user message
// for req. Exported so tests can assert on exactly what the model is sent.
func (c *Classifier) Request(req permission.ClassifyRequest) (system, user string, err error) {
	action, err := actionJSON(req.ToolName, req.PrimaryArg, req.Args)
	if err != nil {
		return "", "", err
	}
	lines := transcriptLines(req.UserHistory, req.History, req.Delegated, req.CallID)
	return systemPrompt(c.Config), userPrompt(c.Memory, lines, action), nil
}

// Classify sends req to the classifier model. Any failure — no model, a
// provider error, the timeout, an answer that is not the expected JSON — is
// an error, which the gate turns into a question for the user.
func (c *Classifier) Classify(ctx context.Context, req permission.ClassifyRequest) (permission.Verdict, error) {
	start := time.Now()
	var (
		modelName string
		usage     msg.Usage
		verdict   permission.Verdict
		err       error
	)
	defer func() {
		decision := "allow"
		switch {
		case err != nil:
			decision = "error"
		case verdict.Block:
			decision = "block"
		}
		kv := []any{
			"tool", req.ToolName, "decision", decision, "model", modelName,
			"ms", time.Since(start).Milliseconds(),
			"input_tokens", usage.Input, "output_tokens", usage.Output,
			"cache_read", usage.CacheRead, "cache_write", usage.CacheWrite,
		}
		if verdict.Reason != "" {
			kv = append(kv, "reason", verdict.Reason)
		}
		if err != nil {
			kv = append(kv, "error", err.Error())
		}
		diag.L().Info("auto mode classifier", kv...)
	}()

	system, user, err := c.Request(req)
	if err != nil {
		return verdict, err
	}
	if c.Resolve == nil {
		err = errors.New("no classifier model")
		return verdict, err
	}
	streamer, model, err := c.Resolve()
	if err != nil {
		return verdict, err
	}
	modelName = model.Provider + "/" + model.ID

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxTokens := maxAnswerTokens
	if model.MaxTokens > 0 && model.MaxTokens < maxTokens {
		maxTokens = model.MaxTokens
	}
	ch, wait := streamer.Stream(callCtx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(user)}, Timestamp: time.Now().UnixMilli()},
	}, provider.StreamOptions{SystemPrompt: system, MaxTokens: maxTokens})
	for range ch {
	}
	am, werr := wait()
	switch {
	case callCtx.Err() != nil && ctx.Err() == nil:
		err = fmt.Errorf("timed out after %s", timeout)
	case werr != nil:
		err = werr
	case am == nil:
		err = errors.New("no response")
	case am.StopReason == msg.StopError || am.StopReason == msg.StopAborted:
		err = fmt.Errorf("model error: %s", firstNonEmpty(am.ErrorMessage, string(am.StopReason)))
	}
	if am != nil {
		usage = am.Usage
	}
	if err != nil {
		return verdict, err
	}
	verdict, err = ParseAnswer(msg.TextOf(am.Content))
	return verdict, err
}

// ParseAnswer reads the classifier's reply: one JSON object with
// "decision" "allow" or "block", optionally in a code fence. Anything else
// is an error, never a default.
func ParseAnswer(text string) (permission.Verdict, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	fields, ok := singleObject(s)
	unreadable := fmt.Errorf("unreadable classifier answer %q", clip(text, 200))
	if !ok {
		return permission.Verdict{}, unreadable
	}
	var decision, reason string
	raw, ok := fields["decision"]
	if !ok || json.Unmarshal(raw, &decision) != nil {
		return permission.Verdict{}, unreadable
	}
	if r, ok := fields["reason"]; ok && json.Unmarshal(r, &reason) != nil {
		return permission.Verdict{}, unreadable
	}
	reason = strings.TrimSpace(reason)
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "allow":
		return permission.Verdict{Reason: reason}, nil
	case "block":
		return permission.Verdict{Block: true, Reason: reason}, nil
	}
	return permission.Verdict{}, fmt.Errorf("classifier answered decision %q", decision)
}

// singleObject reads s as exactly one JSON object and nothing after it but
// whitespace, keyed by lower-cased name. A key given twice, in any letter
// case, makes it unreadable: which one counts must not be a guess.
func singleObject(s string) (map[string]json.RawMessage, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		key, isKey := tok.(string)
		if err != nil || !isKey {
			return nil, false
		}
		key = strings.ToLower(key)
		if _, dup := fields[key]; dup {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		fields[key] = v
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Registry is the part of provider.Registry Resolver needs.
type Registry interface {
	GetModel(providerID, modelID string) (provider.Model, bool)
	Provider(id string) (provider.Provider, bool)
}

// Resolver picks the classifier's model for each call: the "fast" model
// role when settings.json's modelRoles names one the registry knows, else
// the session's current model, read at call time so /model applies.
func Resolver(reg Registry, roles map[string]string, session func() provider.Model) func() (Streamer, provider.Model, error) {
	return func() (Streamer, provider.Model, error) {
		if v := roles["fast"]; v != "" {
			if i := strings.IndexByte(v, '/'); i > 0 && i < len(v)-1 {
				if m, ok := reg.GetModel(v[:i], v[i+1:]); ok {
					if p, ok := reg.Provider(m.Provider); ok {
						return p, m, nil
					}
				}
			}
		}
		m := session()
		p, ok := reg.Provider(m.Provider)
		if !ok {
			return nil, provider.Model{}, fmt.Errorf("unknown provider %q", m.Provider)
		}
		return p, m, nil
	}
}
