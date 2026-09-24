package catalog

import "sort"

// providers.go tables every provider id in the vendored catalog (data/*.json)
// against its auth configuration, ported from pi-ai's providers/*.js
// (each provider's own `auth: {...}` block) and dist/env-api-keys.js's
// getApiKeyEnvVars(). This is data, not behavior: internal/provider/builtin
// reads it to register a provider.Provider per id; this package has no
// dependency on internal/auth/oauth (that would cycle back into
// internal/provider, which catalog already imports for provider.Model).

// AuthKind is the flavor of credential a catalog provider's own OAuth/API-key
// flow produces, matching provider.AuthKind's two real values (this package
// never sees AuthKindNone).
type AuthKind string

const (
	AuthAPIKey AuthKind = "api_key"
	AuthOAuth  AuthKind = "oauth"
)

// HeaderOverride is one static header a provider's requests must carry (a
// value to set, or an unset to remove a header the API client would
// otherwise send), matching pi's per-provider `auth.resolve()` header
// blocks (Cloudflare AI Gateway's cf-aig-authorization swap is the one
// vendored example: it sets a bearer header and blanks Authorization and
// x-api-key so the underlying API client's own auth injection is
// overridden).
type HeaderOverride struct {
	Name  string
	Value string
	Unset bool
}

// ProviderConfig is one catalog provider id's auth configuration.
type ProviderConfig struct {
	ID   string
	Name string
	// AuthKind is which flow `harness login <id>` should prefer: AuthOAuth
	// when the provider has a login/refresh flow in internal/auth/oauth
	// (regardless of whether it also accepts a bare API key), else
	// AuthAPIKey.
	AuthKind AuthKind
	// EnvVars are environment variable names checked, in order, for an API
	// key, matching env-api-keys.js's getApiKeyEnvVars(id). Anthropic's
	// first entry (ANTHROPIC_AUTH_TOKEN) is sent as a Bearer header rather
	// than an api key -- builtin special-cases that one provider the same
	// way env-api-keys.js does.
	EnvVars []string
	// IsSubscription marks OAuth providers backed by a plan rather than
	// metered API usage, matching pi-ai's oauth flow objects'
	// `isSubscription: true`.
	IsSubscription bool
	// StaticHeaders are additional headers every request for this provider
	// carries verbatim (GitHub Copilot's Editor-Version/User-Agent/etc,
	// OpenAI Codex's originator are per-request and not modeled here).
	StaticHeaders map[string]string
	// HeaderOverrides are header set/unset instructions applied after
	// StaticHeaders and after the API client's own auth injection,
	// matching Cloudflare AI Gateway's auth.resolve() header block.
	HeaderOverrides []HeaderOverride
	// RequiresAccountID marks providers whose api-key login also needs an
	// account id (Cloudflare Workers AI / AI Gateway: CLOUDFLARE_ACCOUNT_ID).
	RequiresAccountID bool
	// RequiresGatewayID additionally marks Cloudflare AI Gateway's gateway
	// id requirement (CLOUDFLARE_GATEWAY_ID).
	RequiresGatewayID bool
}

// anthropicEnvVars mirrors env-api-keys.js's getApiKeyEnvVars("anthropic"):
// ANTHROPIC_AUTH_TOKEN participates in discovery but must be sent as
// Authorization: Bearer, not an api key.
var anthropicEnvVars = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

// githubCopilotHeaders mirrors pi-ai/dist/auth/oauth/github-copilot.js's
// COPILOT_HEADERS, sent on both the device/token exchange and (once the
// api client for this shape exists) every chat completion.
var githubCopilotHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

// cloudflareAIGatewayHeaderOverrides mirrors
// pi-ai/dist/providers/cloudflare-auth.js's cloudflareAIGatewayAuth()
// resolve(): the API key travels as `cf-aig-authorization: Bearer <key>`,
// with Authorization and x-api-key explicitly unset so the API client's own
// auth injection does not also send the key under the wrong header.
var cloudflareAIGatewayHeaderOverrides = []HeaderOverride{
	{Name: "Authorization", Unset: true},
	{Name: "x-api-key", Unset: true},
}

// Providers is every provider id in the vendored catalog (data/*.json),
// with its auth configuration ported from pi-ai's providers/*.js. Order
// matches pi's rough registration order in providers/all.js; ProviderIDs()
// (data-driven) is the source of truth for "is every id present",
// catalog_test.go's table test asserts this list covers exactly that set.
var Providers = []ProviderConfig{
	{ID: "anthropic", Name: "Anthropic", AuthKind: AuthOAuth, EnvVars: anthropicEnvVars, IsSubscription: true},
	{ID: "openai", Name: "OpenAI", AuthKind: AuthAPIKey, EnvVars: []string{"OPENAI_API_KEY"}},
	{ID: "openai-codex", Name: "OpenAI (ChatGPT Plus/Pro)", AuthKind: AuthOAuth, IsSubscription: true},
	{ID: "github-copilot", Name: "GitHub Copilot", AuthKind: AuthOAuth, EnvVars: []string{"COPILOT_GITHUB_TOKEN"}, IsSubscription: true, StaticHeaders: githubCopilotHeaders},
	{ID: "kimi-coding", Name: "Kimi Code (subscription)", AuthKind: AuthOAuth, EnvVars: []string{"KIMI_API_KEY"}, IsSubscription: true},
	{ID: "openrouter", Name: "OpenRouter", AuthKind: AuthOAuth, EnvVars: []string{"OPENROUTER_API_KEY"}},
	{ID: "xai", Name: "xAI", AuthKind: AuthOAuth, EnvVars: []string{"XAI_API_KEY"}, IsSubscription: true},
	{ID: "radius", Name: "Radius", AuthKind: AuthOAuth, EnvVars: []string{"RADIUS_API_KEY"}},
	{ID: "meta", Name: "Meta", AuthKind: AuthOAuth, EnvVars: []string{"META_API_KEY"}},
	{ID: "vercel-ai-gateway", Name: "Vercel AI Gateway", AuthKind: AuthAPIKey, EnvVars: []string{"AI_GATEWAY_API_KEY"}},
	{ID: "cloudflare-workers-ai", Name: "Cloudflare Workers AI", AuthKind: AuthAPIKey, EnvVars: []string{"CLOUDFLARE_API_KEY"}, RequiresAccountID: true},
	{ID: "cloudflare-ai-gateway", Name: "Cloudflare AI Gateway", AuthKind: AuthAPIKey, EnvVars: []string{"CLOUDFLARE_API_KEY"}, RequiresAccountID: true, RequiresGatewayID: true, HeaderOverrides: cloudflareAIGatewayHeaderOverrides},
	{ID: "opencode", Name: "OpenCode", AuthKind: AuthAPIKey, EnvVars: []string{"OPENCODE_API_KEY"}},
	{ID: "opencode-go", Name: "OpenCode Go", AuthKind: AuthAPIKey, EnvVars: []string{"OPENCODE_API_KEY"}},
	{ID: "google", Name: "Google", AuthKind: AuthAPIKey, EnvVars: []string{"GEMINI_API_KEY"}},
	{ID: "google-vertex", Name: "Google Vertex", AuthKind: AuthAPIKey, EnvVars: []string{"GOOGLE_CLOUD_API_KEY"}},
	{ID: "amazon-bedrock", Name: "Amazon Bedrock", AuthKind: AuthAPIKey},
	{ID: "azure-openai-responses", Name: "Azure OpenAI", AuthKind: AuthAPIKey, EnvVars: []string{"AZURE_OPENAI_API_KEY"}},
	{ID: "mistral", Name: "Mistral", AuthKind: AuthAPIKey, EnvVars: []string{"MISTRAL_API_KEY"}},
	{ID: "deepseek", Name: "DeepSeek", AuthKind: AuthAPIKey, EnvVars: []string{"DEEPSEEK_API_KEY"}},
	{ID: "groq", Name: "Groq", AuthKind: AuthAPIKey, EnvVars: []string{"GROQ_API_KEY"}},
	{ID: "cerebras", Name: "Cerebras", AuthKind: AuthAPIKey, EnvVars: []string{"CEREBRAS_API_KEY"}},
	{ID: "nvidia", Name: "NVIDIA", AuthKind: AuthAPIKey, EnvVars: []string{"NVIDIA_API_KEY"}},
	{ID: "huggingface", Name: "Hugging Face", AuthKind: AuthAPIKey, EnvVars: []string{"HF_TOKEN"}},
	{ID: "fireworks", Name: "Fireworks", AuthKind: AuthAPIKey, EnvVars: []string{"FIREWORKS_API_KEY"}},
	{ID: "together", Name: "Together", AuthKind: AuthAPIKey, EnvVars: []string{"TOGETHER_API_KEY"}},
	{ID: "baseten", Name: "Baseten", AuthKind: AuthAPIKey, EnvVars: []string{"BASETEN_API_KEY"}},
	{ID: "zai", Name: "Z.AI", AuthKind: AuthAPIKey, EnvVars: []string{"ZAI_API_KEY"}},
	{ID: "zai-coding-cn", Name: "Z.AI Coding CN", AuthKind: AuthAPIKey, EnvVars: []string{"ZAI_CODING_CN_API_KEY"}},
	{ID: "moonshotai", Name: "Moonshot AI", AuthKind: AuthAPIKey, EnvVars: []string{"MOONSHOT_API_KEY"}},
	{ID: "moonshotai-cn", Name: "Moonshot AI CN", AuthKind: AuthAPIKey, EnvVars: []string{"MOONSHOT_API_KEY"}},
	{ID: "minimax", Name: "MiniMax", AuthKind: AuthAPIKey, EnvVars: []string{"MINIMAX_API_KEY"}},
	{ID: "minimax-cn", Name: "MiniMax CN", AuthKind: AuthAPIKey, EnvVars: []string{"MINIMAX_CN_API_KEY"}},
	{ID: "ant-ling", Name: "Ant Ling", AuthKind: AuthAPIKey, EnvVars: []string{"ANT_LING_API_KEY"}},
	{ID: "qwen-token-plan", Name: "Qwen Token Plan", AuthKind: AuthAPIKey, EnvVars: []string{"QWEN_TOKEN_PLAN_API_KEY"}},
	{ID: "qwen-token-plan-cn", Name: "Qwen Token Plan CN", AuthKind: AuthAPIKey, EnvVars: []string{"QWEN_TOKEN_PLAN_CN_API_KEY"}},
	{ID: "qwen-token-plan-individual", Name: "Qwen Token Plan Individual", AuthKind: AuthAPIKey, EnvVars: []string{"QWEN_TOKEN_PLAN_API_KEY"}},
	{ID: "xiaomi", Name: "Xiaomi", AuthKind: AuthAPIKey, EnvVars: []string{"XIAOMI_API_KEY"}},
	{ID: "xiaomi-token-plan-ams", Name: "Xiaomi Token Plan AMS", AuthKind: AuthAPIKey, EnvVars: []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"}},
	{ID: "xiaomi-token-plan-cn", Name: "Xiaomi Token Plan CN", AuthKind: AuthAPIKey, EnvVars: []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"}},
	{ID: "xiaomi-token-plan-sgp", Name: "Xiaomi Token Plan SGP", AuthKind: AuthAPIKey, EnvVars: []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"}},
}

var providerConfigByID map[string]ProviderConfig

func init() {
	providerConfigByID = make(map[string]ProviderConfig, len(Providers))
	for _, p := range Providers {
		providerConfigByID[p.ID] = p
	}
}

// ProviderConfigByID returns id's auth configuration, if listed.
func ProviderConfigByID(id string) (ProviderConfig, bool) {
	p, ok := providerConfigByID[id]
	return p, ok
}

// ProviderConfigIDs returns every id in Providers, sorted (for diffing
// against ProviderIDs() in tests).
func ProviderConfigIDs() []string {
	out := make([]string, 0, len(Providers))
	for _, p := range Providers {
		out = append(out, p.ID)
	}
	sort.Strings(out)
	return out
}
