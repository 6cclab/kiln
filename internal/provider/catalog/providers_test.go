package catalog

import "testing"

// TestProviderConfigCoversEveryVendoredProvider asserts every provider id
// present in the vendored catalog (data/*.json, computed via ProviderIDs(),
// not hardcoded to 41) has a Providers entry, and vice versa: no
// ProviderConfig for an id absent from the vendored data.
func TestProviderConfigCoversEveryVendoredProvider(t *testing.T) {
	want := ProviderIDs() // sorted, data-driven
	got := ProviderConfigIDs()

	if len(want) != len(got) {
		t.Fatalf("ProviderConfigIDs() has %d entries, ProviderIDs() (vendored data) has %d", len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("provider id mismatch at index %d: catalog data has %q, Providers table has %q", i, want[i], got[i])
		}
	}
}

// TestProviderConfigFieldsPopulated asserts every entry has a non-empty
// name and auth kind, and that api-key providers list at least one env var
// unless they are explicitly credential-source-free (amazon-bedrock, whose
// many AWS credential sources env-api-keys.js checks ad hoc rather than via
// a single env var list).
func TestProviderConfigFieldsPopulated(t *testing.T) {
	// env-api-keys.js's getApiKeyEnvVars() has no entry for these ids:
	// amazon-bedrock checks several AWS credential sources ad hoc rather
	// than a single env var, and openai-codex is OAuth-only.
	noEnvVarsOK := map[string]bool{"amazon-bedrock": true, "openai-codex": true}
	for _, p := range Providers {
		if p.Name == "" {
			t.Errorf("%s: empty Name", p.ID)
		}
		if p.AuthKind != AuthAPIKey && p.AuthKind != AuthOAuth {
			t.Errorf("%s: invalid AuthKind %q", p.ID, p.AuthKind)
		}
		if len(p.EnvVars) == 0 && !noEnvVarsOK[p.ID] {
			t.Errorf("%s: no EnvVars and not in the no-env-vars allowlist", p.ID)
		}
	}
}

// TestProviderConfigAuthKindSpotCheck pins ten providers' auth kind and env
// vars against pi-ai's own providers/*.js source, quoting the exact line
// each assertion is ported from (grep the vendored
// node_modules/@earendil-works/pi-ai/dist/providers/<id>.js to re-verify).
func TestProviderConfigAuthKindSpotCheck(t *testing.T) {
	cases := []struct {
		id       string
		wantKind AuthKind
		wantEnv  []string
		wantSub  bool
		piQuote  string
	}{
		{
			id: "anthropic", wantKind: AuthOAuth, wantEnv: anthropicEnvVars, wantSub: true,
			piQuote: `auth: { apiKey: anthropicApiKeyAuth(), oauth: lazyOAuth({ ... isSubscription: true ... }) }`,
		},
		{
			id: "openai", wantKind: AuthAPIKey, wantEnv: []string{"OPENAI_API_KEY"},
			piQuote: `auth: { apiKey: envApiKeyAuth("OpenAI API key", ["OPENAI_API_KEY"]) },`,
		},
		{
			id: "openai-codex", wantKind: AuthOAuth, wantSub: true,
			piQuote: `auth: { oauth: lazyOAuth({ name: "OpenAI (ChatGPT Plus/Pro)", isSubscription: true, ... }) },`,
		},
		{
			id: "github-copilot", wantKind: AuthOAuth, wantEnv: []string{"COPILOT_GITHUB_TOKEN"}, wantSub: true,
			piQuote: `apiKey: envApiKeyAuth("GitHub Copilot token", ["COPILOT_GITHUB_TOKEN"]), oauth: lazyOAuth({ name: "GitHub Copilot", isSubscription: true, ... })`,
		},
		{
			id: "kimi-coding", wantKind: AuthOAuth, wantEnv: []string{"KIMI_API_KEY"}, wantSub: true,
			piQuote: `export const kimiCodingOAuth = { name: "Kimi Code (subscription)", isSubscription: true, ... }`,
		},
		{
			id: "openrouter", wantKind: AuthOAuth, wantEnv: []string{"OPENROUTER_API_KEY"},
			piQuote: `auth: { apiKey: envApiKeyAuth("OpenRouter API key", ["OPENROUTER_API_KEY"]), oauth: lazyOAuth({ name: "OpenRouter OAuth", ... }) },`,
		},
		{
			id: "xai", wantKind: AuthOAuth, wantEnv: []string{"XAI_API_KEY"}, wantSub: true,
			piQuote: `export const xaiOAuth = { name: "xAI (Grok/X subscription)", isSubscription: true, ... }`,
		},
		{
			id: "radius", wantKind: AuthOAuth, wantEnv: []string{"RADIUS_API_KEY"},
			piQuote: `auth: { apiKey: envApiKeyAuth("Radius API key", ["RADIUS_API_KEY"]), oauth: lazyOAuth({ name, load: () => loadRadiusOAuth(...) }) },`,
		},
		{
			id: "vercel-ai-gateway", wantKind: AuthAPIKey, wantEnv: []string{"AI_GATEWAY_API_KEY"},
			piQuote: `auth: { apiKey: envApiKeyAuth("Vercel AI Gateway API key", ["AI_GATEWAY_API_KEY"]) },`,
		},
		{
			id: "cloudflare-ai-gateway", wantKind: AuthAPIKey, wantEnv: []string{"CLOUDFLARE_API_KEY"},
			piQuote: `auth: { apiKey: cloudflareAIGatewayAuth() }` + " -- cloudflare-auth.js: `cf-aig-authorization`: `Bearer ${apiKey}`, Authorization: null, x-api-key: null",
		},
	}

	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			p, ok := ProviderConfigByID(c.id)
			if !ok {
				t.Fatalf("no ProviderConfig for %q", c.id)
			}
			if p.AuthKind != c.wantKind {
				t.Errorf("%s: AuthKind = %q, want %q (pi: %s)", c.id, p.AuthKind, c.wantKind, c.piQuote)
			}
			if p.IsSubscription != c.wantSub {
				t.Errorf("%s: IsSubscription = %v, want %v (pi: %s)", c.id, p.IsSubscription, c.wantSub, c.piQuote)
			}
			if c.wantEnv != nil {
				if len(p.EnvVars) != len(c.wantEnv) {
					t.Fatalf("%s: EnvVars = %v, want %v (pi: %s)", c.id, p.EnvVars, c.wantEnv, c.piQuote)
				}
				for i := range c.wantEnv {
					if p.EnvVars[i] != c.wantEnv[i] {
						t.Errorf("%s: EnvVars[%d] = %q, want %q (pi: %s)", c.id, i, p.EnvVars[i], c.wantEnv[i], c.piQuote)
					}
				}
			}
		})
	}
}

// TestCloudflareAIGatewayHeaderOverrides pins the exact header
// set/unset pi's cloudflareAIGatewayAuth().resolve() performs (see
// providers/cloudflare-auth.js): the API key travels as
// `cf-aig-authorization`, with `Authorization` and `x-api-key` unset so an
// API client's own auth injection does not also send it under the wrong
// header.
func TestCloudflareAIGatewayHeaderOverrides(t *testing.T) {
	p, ok := ProviderConfigByID("cloudflare-ai-gateway")
	if !ok {
		t.Fatal("no ProviderConfig for cloudflare-ai-gateway")
	}
	unset := map[string]bool{}
	for _, h := range p.HeaderOverrides {
		if h.Unset {
			unset[h.Name] = true
		}
	}
	if !unset["Authorization"] || !unset["x-api-key"] {
		t.Fatalf("cloudflare-ai-gateway HeaderOverrides = %+v, want Authorization and x-api-key unset", p.HeaderOverrides)
	}
	if !p.RequiresAccountID || !p.RequiresGatewayID {
		t.Fatalf("cloudflare-ai-gateway RequiresAccountID/RequiresGatewayID = %v/%v, want true/true", p.RequiresAccountID, p.RequiresGatewayID)
	}
}

// TestGitHubCopilotStaticHeaders pins pi's COPILOT_HEADERS
// (auth/oauth/github-copilot.js) verbatim.
func TestGitHubCopilotStaticHeaders(t *testing.T) {
	p, ok := ProviderConfigByID("github-copilot")
	if !ok {
		t.Fatal("no ProviderConfig for github-copilot")
	}
	want := map[string]string{
		"User-Agent":             "GitHubCopilotChat/0.35.0",
		"Editor-Version":         "vscode/1.107.0",
		"Editor-Plugin-Version":  "copilot-chat/0.35.0",
		"Copilot-Integration-Id": "vscode-chat",
	}
	for k, v := range want {
		if p.StaticHeaders[k] != v {
			t.Errorf("github-copilot StaticHeaders[%q] = %q, want %q", k, p.StaticHeaders[k], v)
		}
	}
}
