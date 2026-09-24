// Package ollama is the Ollama provider: discovery over /api/tags, /api/ps
// and /api/show, and resolution of the context window a model is actually
// being served with (which is often far smaller than its training context).
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
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/api"
)

// ProviderID is the registry id this provider registers under.
const ProviderID = "ollama"

// DefaultURL is the bare-Ollama default host.
const DefaultURL = "http://127.0.0.1:11434"

// fallbackContext is used when a model's serving context cannot be
// determined.
//
// Ollama's own default is 4096, which every tier rejects as unrunnable.
// Guessing high would be worse: an over-large window silently selects a
// bigger tier and the model truncates mid-turn with no error. 8192 is the
// smallest window that runs, so an unknown model starts conservative and is
// corrected the moment /api/ps reports the truth.
const fallbackContext = 8_192

type tag struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

type tagsResponse struct {
	Models []tag `json:"models"`
}

type psEntry struct {
	Model string `json:"model"`
	// ContextLength is the effective serving context of the loaded
	// instance. Authoritative when present.
	ContextLength int `json:"context_length"`
}

type psResponse struct {
	Models []psEntry `json:"models"`
}

type showResponse struct {
	Parameters   string   `json:"parameters"`
	Capabilities []string `json:"capabilities"`
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
	PSContextLength int
	ShowParameters  string
	// ServerDefault is the server-wide OLLAMA_CONTEXT_LENGTH, if known.
	ServerDefault int
}

// ResolveContextWindow resolves the window a model will actually be served
// with.
//
// Ordering matters, and getting it wrong is not a small error: a model can
// report a training context far larger than what it is actually served at
// (a Modelfile-pinned num_ctx). Priority: /api/ps context_length (only
// Ollama knows this, and only while the model is resident) > num_ctx pinned
// in the Modelfile (via /api/show parameters) > server-wide default >
// fallbackContext. Deliberately NOT model_info.*.context_length -- that is
// the training context and is wildly larger than what gets served.
func ResolveContextWindow(args ResolveContextWindowArgs) int {
	if args.PSContextLength > 0 {
		return args.PSContextLength
	}
	if n, ok := numCtxFromParameters(args.ShowParameters); ok {
		return n
	}
	if args.ServerDefault > 0 {
		return args.ServerDefault
	}
	return fallbackContext
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
		Api:           provider.ApiOpenAICompletions,
		Provider:      ProviderID,
		BaseURL:       baseURL,
		Reasoning:     reasoning,
		Input:         input,
		ContextWindow: contextWindow,
		MaxTokens:     contextWindow,
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
		// handled by internal/provider's reasoning.go instead.
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
	HTTPClient   *http.Client
}

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

func (o Options) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// DiscoverModels discovers models and their true serving windows.
func DiscoverModels(ctx context.Context, opts Options) ([]provider.Model, error) {
	base := opts.baseURL()
	client := opts.client()

	var tags tagsResponse
	if err := getJSON(ctx, client, base, "/api/tags", opts.APIKey, "", nil, &tags); err != nil {
		return nil, err
	}

	// Loaded models report their real context window; unloaded ones cannot
	// without being loaded, which would be a rude side effect of listing.
	loaded := map[string]psEntry{}
	var ps psResponse
	if err := getJSON(ctx, client, base, "/api/ps", opts.APIKey, "", nil, &ps); err == nil {
		for _, e := range ps.Models {
			loaded[e.Model] = e
		}
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

		window := ResolveContextWindow(ResolveContextWindowArgs{
			PSContextLength: loaded[t.Model].ContextLength,
			ShowParameters:  show.Parameters,
			ServerDefault:   opts.ServerDefaultContext,
		})
		out = append(out, toModel(t.Model, base+"/v1", window, show.Capabilities))
	}
	return out, nil
}

// Provider is the Ollama provider.Provider implementation.
type Provider struct {
	opts   Options
	models []provider.Model
	client *api.OpenAICompletionsClient
}

// New builds an Ollama provider. Models are empty until RefreshModels is
// called (matching pi's getModels() returning [] before the first refresh).
func New(opts Options) *Provider {
	return &Provider{opts: opts, client: &api.OpenAICompletionsClient{HTTPClient: opts.client()}}
}

func (p *Provider) ID() string   { return ProviderID }
func (p *Provider) Name() string { return "Ollama" }

func (p *Provider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindAPIKey, EnvVars: []string{"OLLAMA_HOST"}}
}

func (p *Provider) Models() []provider.Model { return p.models }

// RefreshModels re-discovers models via /api/tags, /api/ps and /api/show.
func (p *Provider) RefreshModels(ctx context.Context) error {
	models, err := DiscoverModels(ctx, p.opts)
	if err != nil {
		return err
	}
	p.models = models
	return nil
}

// Login validates the given host by hitting /api/tags, matching pi's
// login() which fails at login rather than at first turn.
func Login(ctx context.Context, url, apiKey string) error {
	base := strings.TrimRight(url, "/")
	client := &http.Client{Timeout: 15 * time.Second}
	var tags tagsResponse
	return getJSON(ctx, client, base, "/api/tags", apiKey, "", nil, &tags)
}

// Stream drives a completion through the openai-completions client at
// {base}/v1, using the placeholder key "local" (Ollama ignores it; the
// gateway requires a real bearer token supplied via Options.APIKey).
func (p *Provider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	key := p.opts.APIKey
	if key == "" {
		key = "local"
	}
	// Reasoning suppression: some Qwen models on Ollama ignore every
	// transport-level "stop reasoning" field, so the /no_think suffix goes
	// on the last user message instead (internal/provider/reasoning.go).
	suppression := provider.SuppressionFor(model)
	if suppression.Suffix != "" {
		transcript = provider.ApplySuppression(transcript, provider.SuppressionNoThinkSuffix)
	}
	return p.client.Stream(ctx, model, transcript, opts, api.Auth{APIKey: key})
}
