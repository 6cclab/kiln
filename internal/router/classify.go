package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Streamer is the model-calling surface LLMClassifier needs; every
// provider.Provider satisfies it.
type Streamer interface {
	Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error))
}

// DefaultClassifierTimeout bounds one classifier call (jev trusts only a
// clean stop, with no timeout of its own; kiln adds one so a stuck
// provider never stalls the turn).
const DefaultClassifierTimeout = 20 * time.Second

// classifierMaxTokens caps the classifier's reply: one short JSON object.
const classifierMaxTokens = 64

// classifierInstructions and the two criteria are jev-router.ts:76-80,
// reused word for word.
const (
	classifierInstructions = "How demanding is the software engineering work requested in `prompt`?"
	classifierStandardCrit = "Ordinary features, fixes, reviews, or questions"
	classifierComplexCrit  = "Subtle design, cross-cutting changes, or hard debugging"
)

// LLMClassifier rates a request's complexity with one call on a model, as
// jev's classify call (jev-router.ts:69-87, trusting only
// stopReason === "stop"). It implements Auto.Classifier.
type LLMClassifier struct {
	// Resolve picks the model for one call.
	Resolve func() (Streamer, provider.Model, error)
	// Timeout bounds one call; zero means DefaultClassifierTimeout.
	Timeout time.Duration
	// OnUsage, if set, receives every call's usage, answered or not: the
	// tokens are spent either way, and the session's cost must show them.
	OnUsage func(model provider.Model, usage msg.Usage)
}

var _ Classifier = (*LLMClassifier)(nil)

// Request builds the system prompt and user message for prompt, exported
// so tests can assert on exactly what the model is sent.
func (c *LLMClassifier) Request(prompt string) (system, user string) {
	return classifierSystemPrompt(), classifierUserMessage(prompt)
}

func classifierSystemPrompt() string {
	return "You rate one thing about `prompt`.\n\n" +
		classifierInstructions + "\n\n" +
		"- standard: " + classifierStandardCrit + "\n" +
		"- complex: " + classifierComplexCrit + "\n\n" +
		`Answer with exactly one JSON object and nothing else: {"complexity":"standard"} or {"complexity":"complex"}.`
}

// classifierUserMessage wraps prompt, JSON-encoded, in a <prompt> tag, so
// the model reads it as data rather than as instructions.
func classifierUserMessage(prompt string) string {
	encoded, _ := json.Marshal(prompt) // a string never fails to encode
	return "<prompt>" + string(encoded) + "</prompt>"
}

// Classify sends prompt to the classifier model. Any failure — no model, a
// provider error, the timeout, an answer that is not the expected JSON —
// is an error; Auto.rate treats it as no answer and falls back, except
// when ctx itself was cancelled (Esc), which it propagates instead.
func (c *LLMClassifier) Classify(ctx context.Context, prompt string) (Complexity, error) {
	start := time.Now()
	var (
		modelName string
		usage     msg.Usage
		result    Complexity
		err       error
	)
	defer func() {
		decision := string(result)
		if err != nil {
			decision = "error"
		}
		diag.L().Info("model router classifier", "decision", decision, "model", modelName,
			"ms", time.Since(start).Milliseconds(),
			"input_tokens", usage.Input, "output_tokens", usage.Output,
			"cache_read", usage.CacheRead, "cache_write", usage.CacheWrite,
		)
	}()

	if c.Resolve == nil {
		err = errors.New("no classifier model")
		return result, err
	}
	streamer, model, err := c.Resolve()
	if err != nil {
		return result, err
	}
	modelName = model.Provider + "/" + model.ID

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultClassifierTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	system, user := c.Request(prompt)
	maxTokens := classifierMaxTokens
	if model.MaxTokens > 0 && model.MaxTokens < maxTokens {
		maxTokens = model.MaxTokens
	}
	ch, wait := streamer.Stream(callCtx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(user)}, Timestamp: time.Now().UnixMilli()},
	}, provider.StreamOptions{SystemPrompt: system, MaxTokens: maxTokens, ThinkingLevel: provider.ThinkingOff})
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
		err = fmt.Errorf("model error: %s", firstNonEmptyStr(am.ErrorMessage, string(am.StopReason)))
	case am.StopReason != msg.StopStop:
		// Cut off (max tokens) or ended any other way: a prefix can still
		// parse as an answer, but it is not the model's whole answer, and
		// jev only trusts a clean stop.
		err = fmt.Errorf("answer did not finish (stop reason %q)", am.StopReason)
	}
	if am != nil {
		usage = am.Usage
		if c.OnUsage != nil && (usage.Input != 0 || usage.Output != 0 || usage.CacheRead != 0 || usage.CacheWrite != 0) {
			c.OnUsage(model, usage)
		}
	}
	if err != nil {
		return result, err
	}
	result, err = ParseComplexity(msg.TextOf(am.Content))
	return result, err
}

// ParseComplexity reads the classifier's reply: one JSON object,
// {"complexity":"standard"} or {"complexity":"complex"}, and nothing
// else. Strict: a duplicate key (in any letter case), trailing text after
// the object, or an unknown value are all errors, never a guess.
func ParseComplexity(text string) (Complexity, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	fields, ok := singleJSONObject(s)
	unreadable := fmt.Errorf("unreadable classifier answer %q", clipText(text, 200))
	if !ok {
		return "", unreadable
	}
	raw, ok := fields["complexity"]
	if !ok {
		return "", unreadable
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", unreadable
	}
	switch Complexity(strings.ToLower(strings.TrimSpace(value))) {
	case Standard:
		return Standard, nil
	case Complex:
		return Complex, nil
	}
	return "", fmt.Errorf("classifier answered complexity %q", value)
}

// singleJSONObject reads s as exactly one JSON object and nothing after
// it but whitespace, keyed by lower-cased name. A key given twice, in any
// letter case, makes it unreadable: which one counts must not be a guess.
func singleJSONObject(s string) (map[string]json.RawMessage, bool) {
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

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ClassifierRegistry is the part of provider.Registry Resolver needs.
type ClassifierRegistry interface {
	GetModel(providerID, modelID string) (provider.Model, bool)
	Provider(id string) (provider.Provider, bool)
}

// ClassifierResolver resolves the classifier's model once at setup: the
// "fast" modelRoles value, which must name a model the registry knows.
// Unlike automode.Resolver there is no session fallback — the classifier
// only ever runs before a new session's first request, outside any lane.
func ClassifierResolver(reg ClassifierRegistry, model provider.Model) func() (Streamer, provider.Model, error) {
	return func() (Streamer, provider.Model, error) {
		p, ok := reg.Provider(model.Provider)
		if !ok {
			return nil, provider.Model{}, fmt.Errorf("unknown provider %q", model.Provider)
		}
		return p, model, nil
	}
}
