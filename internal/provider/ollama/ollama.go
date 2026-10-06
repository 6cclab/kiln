// Package ollama is the Ollama provider: discovery over /api/tags and
// /api/show, resolution of the context window kiln will ask Ollama to
// serve, and a native-protocol streaming client (/api/chat) that sends that
// window on every request.
//
// Ollama's OpenAI-compatible endpoint (/v1/chat/completions) cannot carry
// num_ctx: the server just serves its own default (or whatever a previous
// caller happened to load the model with), regardless of what this harness
// budgets for. Measured against a live Ollama 0.34.0: /v1 with
// options.num_ctx is silently ignored; a native /api/chat request with
// options.num_ctx is honored and reloads the model at that window. So the
// only reliable way to make the served window match the budgeted one is for
// kiln to speak the native protocol and set num_ctx itself -- see
// client.go.
//
// This is a Go port of harness/src/provider/ollama.ts.
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/api"
)

// ProviderID is the registry id this provider registers under.
const ProviderID = "ollama"

// DefaultURL is the bare-Ollama default host.
const DefaultURL = "http://127.0.0.1:11434"

// ApiOllamaNative is this provider's Api tag. provider.Api is deliberately
// not a closed enum (pi's Api type is KnownApi | string) for exactly this
// case: Ollama speaks its own native protocol (client.go), not one of
// pi-ai's ten catalog shapes, and nothing downstream switches on Model.Api
// for an Ollama model -- Provider.Stream below is this provider's own
// method, never routed through internal/provider/builtin's
// implementedAPIs/switch.
const ApiOllamaNative provider.Api = "ollama-native"

// fallbackContext is used when a model's serving context cannot be
// determined.
//
// Ollama's own default is 4096, which every tier rejects as unrunnable.
// Guessing high would be worse: an over-large window asks Ollama to serve
// more than the box may have VRAM for, which fails the request outright
// rather than truncating anything. 8192 is the smallest window that runs,
// so an unknown model starts conservative; a Modelfile num_ctx,
// OLLAMA_CONTEXT_LENGTH or a known training context (ResolveContextWindow)
// all take priority over this guess.
const fallbackContext = 8_192

type tag struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

type tagsResponse struct {
	Models []tag `json:"models"`
}

type showResponse struct {
	Parameters   string   `json:"parameters"`
	Capabilities []string `json:"capabilities"`
	// ModelInfo is Ollama's per-architecture metadata blob, keyed like
	// "qwen3.context_length", "llama.context_length": the model's TRAINING
	// context, not what it will be served at. Read only by
	// trainingContextFromModelInfo, and only as the last resort below
	// fallbackContext.
	ModelInfo map[string]any `json:"model_info"`
}

// trainingContextFromModelInfo finds the training-context entry in a
// /api/show response's model_info blob. The key is architecture-prefixed
// (e.g. "qwen3.context_length") so it is matched by suffix rather than a
// fixed name. Returns 0, false if no such key is present or it does not
// decode as a positive number.
func trainingContextFromModelInfo(modelInfo map[string]any) (int, bool) {
	for k, v := range modelInfo {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		switch n := v.(type) {
		case float64:
			if n > 0 {
				return int(n), true
			}
		case json.Number:
			if f, err := n.Float64(); err == nil && f > 0 {
				return int(f), true
			}
		}
	}
	return 0, false
}

// controlCharPattern scrubs raw control characters Ollama occasionally
// emits inside strings (seen in `parameters`), which strict JSON decoding
// rejects.
var controlCharPattern = regexp.MustCompile("[\x00-\x08\x0B\x0C\x0E-\x1F]")

func getJSON(ctx context.Context, client *http.Client, base, path, key string, method string, body []byte, out any) error {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s -> HTTP %d", path, resp.StatusCode)
	}
	scrubbed := controlCharPattern.ReplaceAllString(string(raw), " ")
	return json.Unmarshal([]byte(scrubbed), out)
}

// numCtxPattern matches "num_ctx 32768" inside Ollama's whitespace-aligned
// `parameters` blob.
var numCtxPattern = regexp.MustCompile(`(?m)^\s*num_ctx\s+(\d+)\s*$`)

func numCtxFromParameters(parameters string) (int, bool) {
	m := numCtxPattern.FindStringSubmatch(parameters)
	if m == nil {
		return 0, false
	}
	var n int
	_, err := fmt.Sscanf(m[1], "%d", &n)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ResolveContextWindowArgs are the inputs to ResolveContextWindow.
type ResolveContextWindowArgs struct {
	ShowParameters string
	// ServerDefault is the server-wide OLLAMA_CONTEXT_LENGTH, if known. As
	// of the native /api/chat client, this is kiln's own ask (what it will
	// send as options.num_ctx when nothing more specific pins a window),
	// not just a guess at the server's behavior.
	ServerDefault int
	// TrainingContext is model_info.*.context_length from /api/show: the
	// context length the model was TRAINED with, which can be far larger
	// than anything worth asking Ollama to serve (loading a model at its
	// full training window can take far more VRAM than the box has, and is
	// usually unnecessary). Used only as a last resort, capped at
	// maxAutoContext.
	TrainingContext int
}

// maxAutoContext caps how much of a model's training context
// ResolveContextWindow will ask Ollama to serve when nothing else (a
// Modelfile pin or OLLAMA_CONTEXT_LENGTH) says what to ask for. Chosen as a
// window every machine that can load the model at all can plausibly also
// serve at, without either the operator or kiln ever naming a number.
const maxAutoContext = 32_768

// ResolveContextWindow resolves the window kiln will ask Ollama to serve
// via options.num_ctx on every /api/chat request (client.go) -- by
// construction, the window kiln budgets for (internal/budget.TierForWindow)
// is the window Ollama actually serves, since kiln is the one asking for
// it.
//
// Ordering, most authoritative first: num_ctx pinned in the Modelfile (via
// /api/show parameters -- an operator's explicit choice for this model) >
// OLLAMA_CONTEXT_LENGTH (kiln's own env-configured ask, same variable
// Ollama's server reads for its own default) > the model's training
// context (model_info.*.context_length), capped at maxAutoContext so an
// unconfigured 1M-context model doesn't get asked to serve a window no
// consumer box can hold > fallbackContext when nothing is known at all.
// /api/ps's context_length (a resident model's CURRENT serving window) is
// deliberately not consulted: now that every request carries its own
// num_ctx, the next request is what sets the window, not whatever an
// earlier caller happened to load the model with.
func ResolveContextWindow(args ResolveContextWindowArgs) int {
	if n, ok := numCtxFromParameters(args.ShowParameters); ok {
		return capAtTraining(n, args.TrainingContext)
	}
	if args.ServerDefault > 0 {
		return capAtTraining(args.ServerDefault, args.TrainingContext)
	}
	if args.TrainingContext > 0 {
		return min(args.TrainingContext, maxAutoContext)
	}
	return fallbackContext
}

// capAtTraining caps an asked-for window at the model's training context
// when that is known: Ollama silently serves a num_ctx above it at the
// training context, so asking for more would budget a window that is not
// served (OLLAMA_CONTEXT_LENGTH=49152 on qwen3:8b serves 40960).
func capAtTraining(n, training int) int {
	if training > 0 && n > training {
		return training
	}
	return n
}

// thinkingLevelMapOff maps ThinkingOff -> "off" and every other level to
// unsupported (nil), matching toPiModel: Phase 0 found qwen3-cc and
// qwen3:30b-a3b ignore Ollama's think:false -- they keep reasoning, move it
// from `thinking` into `content`, and exhaust max_tokens without ever
// emitting the tool call. "off" must therefore be a reachable level; medium
// is the one budget level left reachable, matching the TS thinkingLevelMap.
func thinkingLevelMap() provider.ThinkingLevelMap {
	off := "off"
	medium := "medium"
	return provider.ThinkingLevelMap{
		provider.ThinkingOff:     &off,
		provider.ThinkingMinimal: nil,
		provider.ThinkingLow:     nil,
		provider.ThinkingMedium:  &medium,
		provider.ThinkingHigh:    nil,
		provider.ThinkingXHigh:   nil,
	}
}

func toModel(id, baseURL string, contextWindow int, capabilities []string) provider.Model {
	reasoning := containsStr(capabilities, "thinking")
	vision := containsStr(capabilities, "vision")

	input := []string{"text"}
	if vision {
		input = []string{"text", "image"}
	}

	m := provider.Model{
		ID:            id,
		Name:          id,
		Api:           ApiOllamaNative,
		Provider:      ProviderID,
		BaseURL:       baseURL,
		Reasoning:     reasoning,
		Input:         input,
		ContextWindow: contextWindow,
		// MaxTokens caps the model's own output (options.num_predict),
		// not a request-dependent value: unchanged from the /v1 path,
		// which also set it to the full window. budget/tier.go never
		// reads Model.MaxTokens -- only ContextWindow -- so this does not
		// affect compaction/tiering; it only means an unbounded turn's
		// output is capped by the window, same as before this fix.
		MaxTokens: contextWindow,
		// Self-hosted: no per-token cost. Keeps cost reporting honest rather
		// than inventing a number.
		Cost: provider.ModelCost{},
	}
	if reasoning {
		m.ThinkingLevelMap = thinkingLevelMap()
	}
	compat := provider.OpenAICompletionsCompat{
		SupportsStore:            boolPtr(false),
		SupportsDeveloperRole:    boolPtr(false),
		SupportsReasoningEffort:  boolPtr(false), // verified in Phase 0: accepted and ignored
		SupportsUsageInStreaming: boolPtr(true),
		SupportsStrictMode:       boolPtr(false),
		MaxTokensField:           "max_tokens",
		// thinkingFormat is deliberately NOT set: Ollama 0.32.15 silently
		// drops chat_template_kwargs / enable_thinking / thinking_budget_tokens
		// (measured byte-identical output on qwen3-cc). Suppression is
		// handled by internal/provider's reasoning.go instead. Kept even
		// though the native client (client.go) no longer reads this
		// compat struct: Model.MaxTokensField()/SupportsStrictMode() etc
		// still decode it, and other code may still call those accessors
		// on an Ollama model.
	}
	raw, _ := json.Marshal(compat)
	m.Compat = raw
	return m
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool { return &b }

// Options configures a Provider.
type Options struct {
	// URL is the base URL of the Ollama host or gateway.
	URL string
	// APIKey is a bearer token, for the gateway. A bare Ollama host needs
	// none.
	APIKey string
	// ServerDefaultContext is the server-wide OLLAMA_CONTEXT_LENGTH, used
	// for models that pin no num_ctx.
	ServerDefaultContext int
	// RequireTools drops models that cannot call tools. Default true: the
	// agent loop needs them.
	RequireTools *bool
	// HTTPClient carries generation requests. Leave it nil for
	// api.NewStreamingClient, which bounds connecting but not the
	// response: a whole-request Timeout here cut off a slow local model
	// mid-answer. Discovery calls always get discoveryTimeout on top.
	HTTPClient *http.Client
}

// discoveryTimeout bounds each model-listing call (/api/tags, /api/show):
// they answer from metadata, without loading a model.
const discoveryTimeout = 15 * time.Second

func (o Options) requireTools() bool {
	if o.RequireTools == nil {
		return true
	}
	return *o.RequireTools
}

func (o Options) baseURL() string {
	u := o.URL
	if u == "" {
		u = DefaultURL
	}
	return strings.TrimRight(u, "/")
}

// client is the discovery client: the configured transport, with
// discoveryTimeout per call.
func (o Options) client() *http.Client {
	c := &http.Client{Timeout: discoveryTimeout}
	if o.HTTPClient != nil {
		c.Transport = o.HTTPClient.Transport
	}
	return c
}

// streamClient carries generation requests. It has no total deadline: the
// harness ends a request that has gone quiet (internal/harness/stall.go),
// sized to the prompt, and Esc ends any request.
func (o Options) streamClient() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return api.NewStreamingClient()
}

// DiscoverModels discovers models and the context window kiln will ask
// each one to be served at (ResolveContextWindow).
func DiscoverModels(ctx context.Context, opts Options) ([]provider.Model, error) {
	base := opts.baseURL()
	client := opts.client()

	var tags tagsResponse
	if err := getJSON(ctx, client, base, "/api/tags", opts.APIKey, "", nil, &tags); err != nil {
		return nil, err
	}

	var out []provider.Model
	for _, t := range tags.Models {
		var show showResponse
		body, _ := json.Marshal(map[string]string{"model": t.Model})
		_ = getJSON(ctx, client, base, "/api/show", opts.APIKey, http.MethodPost, body, &show)

		// An Ollama host serves embedding models (nomic-embed-text) alongside
		// chat models, and they cannot hold a conversation at all. Offering
		// one in the model picker produces a baffling failure at first turn.
		if !containsStr(show.Capabilities, "completion") {
			continue
		}
		// The agent loop is tool calls. A chat-only model would connect,
		// respond in prose, and never act -- worse than refusing it up
		// front.
		if opts.requireTools() && !containsStr(show.Capabilities, "tools") {
			continue
		}

		trainingContext, _ := trainingContextFromModelInfo(show.ModelInfo)
		window := ResolveContextWindow(ResolveContextWindowArgs{
			ShowParameters:  show.Parameters,
			ServerDefault:   opts.ServerDefaultContext,
			TrainingContext: trainingContext,
		})
		out = append(out, toModel(t.Model, base, window, show.Capabilities))
	}
	return out, nil
}

// Provider is the Ollama provider.Provider implementation.
type Provider struct {
	opts Options
	// modelsMu guards models: RefreshModels replaces it from the UI
	// goroutine (a /model switch) while a turn reads it through
	// Registry.GetModel on the lane's goroutine. The slice is never
	// mutated after it is published, only replaced, so readers may keep
	// what Models returned.
	modelsMu sync.RWMutex
	models   []provider.Model
	client   *nativeClient
}

// New builds an Ollama provider. Models are empty until RefreshModels is
// called (matching pi's getModels() returning [] before the first refresh).
func New(opts Options) *Provider {
	return &Provider{opts: opts, client: &nativeClient{HTTPClient: opts.streamClient()}}
}

func (p *Provider) ID() string   { return ProviderID }
func (p *Provider) Name() string { return "Ollama" }

func (p *Provider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindAPIKey, EnvVars: []string{"OLLAMA_HOST"}}
}

func (p *Provider) Models() []provider.Model {
	p.modelsMu.RLock()
	defer p.modelsMu.RUnlock()
	return p.models
}

// RefreshModels re-discovers models via /api/tags and /api/show.
// Discovery runs outside the lock; only the swap is guarded.
func (p *Provider) RefreshModels(ctx context.Context) error {
	models, err := DiscoverModels(ctx, p.opts)
	if err != nil {
		return err
	}
	p.modelsMu.Lock()
	p.models = models
	p.modelsMu.Unlock()
	return nil
}

// Login validates the given host by hitting /api/tags, matching pi's
// login() which fails at login rather than at first turn.
func Login(ctx context.Context, url, apiKey string) error {
	base := strings.TrimRight(url, "/")
	client := &http.Client{Timeout: discoveryTimeout}
	var tags tagsResponse
	return getJSON(ctx, client, base, "/api/tags", apiKey, "", nil, &tags)
}

// Stream drives a completion through the native /api/chat client
// (client.go), using the placeholder key "local" (a bare Ollama host
// ignores the Authorization header entirely; a gateway in front of Ollama
// needs a real bearer token, supplied via Options.APIKey -- see client.go's
// nativeHeaders, which sends it exactly as the discovery calls above
// already do).
func (p *Provider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	key := p.opts.APIKey
	if key == "" {
		key = "local"
	}
	// Reasoning suppression: some Qwen models ignore think:false over
	// every transport this harness has tried, native /api/chat included
	// -- see internal/provider/reasoning.go. The /no_think suffix on the
	// last user message is the one channel that reaches them.
	suppression := provider.SuppressionFor(model)
	if suppression.Suffix != "" {
		transcript = provider.ApplySuppression(transcript, provider.SuppressionNoThinkSuffix)
	}
	return p.client.Stream(ctx, model, transcript, opts, api.Auth{APIKey: key})
}
