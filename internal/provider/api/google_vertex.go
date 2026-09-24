package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Google Vertex AI streaming client, ported from pi-ai's
// dist/api/google-vertex.js. It speaks the same GenerateContentResponse
// wire shape as google-generative-ai.js (both go through the @google/genai
// SDK's `client.models.generateContentStream`; see google_generative_ai.go's
// doc comment) but against the Vertex endpoint shape:
//
//	{baseUrl}/projects/{project}/locations/{location}/publishers/google/models/{model}:streamGenerateContent?alt=sse
//
// Auth (google-vertex.js:42-46, resolveApiKey/createClient/createClientWithApiKey):
//   - If an explicit (non-placeholder, non-marker) API key is present, pi
//     sends it as the `x-goog-api-key` header, exactly like the Generative AI
//     client. This is implemented (see VertexAuthAPIKey below).
//   - Otherwise pi builds `new GoogleGenAI({vertexai: true, project, location,
//     googleAuthOptions: {keyFilename: GOOGLE_APPLICATION_CREDENTIALS}})` and
//     lets google-auth-library mint a bearer access token via Application
//     Default Credentials. This port implements the two ADC credential kinds
//     google-auth-library resolves from a keyFilename or the default ADC
//     search path: a service-account JSON key (self-signed JWT bearer grant,
//     RFC 7523) and an authorized_user JSON key (refresh_token grant), both
//     exchanged at https://oauth2.googleapis.com/token. NOT implemented: the
//     GCE/GKE metadata-server credential source and impersonated/external-
//     account credentials -- google-auth-library supports both, but neither
//     is reachable in this dev environment to verify against, and pi itself
//     does not shell out to `gcloud` (there is no such call in
//     google-vertex.js; ADC resolution is entirely google-auth-library's).
type GoogleVertexClient struct {
	HTTPClient *http.Client
	// Project and Location resolve Vertex's {project}/{location} path
	// segments. Empty values fall back to GOOGLE_CLOUD_PROJECT/
	// GCLOUD_PROJECT and GOOGLE_CLOUD_LOCATION, matching
	// google-vertex.js's resolveProject/resolveLocation.
	Project  string
	Location string
	// APIKeyOverride, if set, is used exactly like resolveApiKey's non-empty,
	// non-placeholder, non-marker API key path (a bearer header is not
	// needed in that case). auth.APIKey doubles for this when set.
}

func (c *GoogleVertexClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *GoogleVertexClient) resolveProject() string {
	if c.Project != "" {
		return c.Project
	}
	if v := os.Getenv("GOOGLE_CLOUD_PROJECT"); v != "" {
		return v
	}
	return os.Getenv("GCLOUD_PROJECT")
}

func (c *GoogleVertexClient) resolveLocation() string {
	if c.Location != "" {
		return c.Location
	}
	return os.Getenv("GOOGLE_CLOUD_LOCATION")
}

// isPlaceholderVertexAPIKey mirrors google-vertex.js's isPlaceholderApiKey
// (`/^<[^>]+>$/`).
func isPlaceholderVertexAPIKey(key string) bool {
	return strings.HasPrefix(key, "<") && strings.HasSuffix(key, ">") && len(key) > 2
}

const gcpVertexCredentialsMarker = "gcp-vertex-credentials"

// resolveVertexAPIKey mirrors google-vertex.js's resolveApiKey.
func resolveVertexAPIKey(auth Auth) string {
	key := strings.TrimSpace(auth.APIKey)
	if key == "" || key == gcpVertexCredentialsMarker || isPlaceholderVertexAPIKey(key) {
		return ""
	}
	return key
}

// --- ADC access token resolution ---

// serviceAccountKey is the subset of a GCP service-account JSON key file
// google-auth-library reads to mint a self-signed JWT bearer assertion.
type serviceAccountKey struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// authorizedUserKey is the subset of an ADC authorized_user JSON file
// (e.g. ~/.config/gcloud/application_default_credentials.json) needed for
// the refresh_token grant.
type authorizedUserKey struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
}

const vertexOAuthScope = "https://www.googleapis.com/auth/cloud-platform"

// resolveADCAccessToken finds an Application Default Credentials source the
// way google-auth-library does when constructed with `{keyFilename}` or, if
// keyFilename is empty, its default search path (GOOGLE_APPLICATION_CREDENTIALS,
// then the well-known gcloud ADC file), and exchanges it for a bearer token.
func resolveADCAccessToken(ctx context.Context, httpClient *http.Client, keyFilename string) (string, error) {
	path := keyFilename
	if path == "" {
		path = os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	}
	if path == "" {
		path = defaultADCPath()
	}
	if path == "" {
		return "", errors.New("no Application Default Credentials found: set GOOGLE_APPLICATION_CREDENTIALS or run `gcloud auth application-default login`")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading ADC file %s: %w", path, err)
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("parsing ADC file %s: %w", path, err)
	}
	switch probe.Type {
	case "service_account":
		var sa serviceAccountKey
		if err := json.Unmarshal(raw, &sa); err != nil {
			return "", err
		}
		return exchangeServiceAccountJWT(ctx, httpClient, sa)
	case "authorized_user":
		var au authorizedUserKey
		if err := json.Unmarshal(raw, &au); err != nil {
			return "", err
		}
		return exchangeRefreshToken(ctx, httpClient, au)
	default:
		return "", fmt.Errorf("unsupported ADC credential type %q in %s (only service_account and authorized_user are implemented)", probe.Type, path)
	}
}

func defaultADCPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	var rel string
	if os.Getenv("APPDATA") != "" { // windows convention, harmless elsewhere
		rel = filepath.Join(os.Getenv("APPDATA"), "gcloud", "application_default_credentials.json")
	} else {
		rel = filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	}
	if _, err := os.Stat(rel); err == nil {
		return rel
	}
	return ""
}

// exchangeServiceAccountJWT implements RFC 7523's JWT bearer grant: a
// self-signed JWT (RS256 over the service account's private key) is
// exchanged at token_uri for an access token. This is the same flow
// google-auth-library's JWTAccess/GoogleAuth performs for a service-account
// keyFilename.
func exchangeServiceAccountJWT(ctx context.Context, httpClient *http.Client, sa serviceAccountKey) (string, error) {
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", errors.New("service account private_key is not valid PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parsing service account private key: %w", err)
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return "", errors.New("service account private key is not RSA")
	}

	now := time.Now()
	header := base64URL([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iss":   sa.ClientEmail,
		"scope": vertexOAuthScope,
		"aud":   sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64URL(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}
	assertion := signingInput + "." + base64URL(sig)

	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	return doTokenExchange(ctx, httpClient, tokenURI, form)
}

// exchangeRefreshToken implements the OAuth2 refresh_token grant
// google-auth-library uses for an authorized_user ADC credential (the file
// `gcloud auth application-default login` writes).
func exchangeRefreshToken(ctx context.Context, httpClient *http.Client, au authorizedUserKey) (string, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {au.ClientID},
		"client_secret": {au.ClientSecret},
		"refresh_token": {au.RefreshToken},
	}
	return doTokenExchange(ctx, httpClient, "https://oauth2.googleapis.com/token", form)
}

func doTokenExchange(ctx context.Context, httpClient *http.Client, tokenURI string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token exchange at %s failed: status=%d body=%s", tokenURI, resp.StatusCode, string(body))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("token exchange response had no access_token")
	}
	return out.AccessToken, nil
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// --- request construction & streaming ---

func vertexHeaders(model provider.Model, apiKey, bearerToken string) http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if apiKey != "" {
		h.Set("x-goog-api-key", apiKey)
	} else if bearerToken != "" {
		h.Set("Authorization", "Bearer "+bearerToken)
	}
	for k, v := range model.Headers {
		if v == "" {
			h.Del(k)
		} else {
			h.Set(k, v)
		}
	}
	return h
}

// Stream starts a Google Vertex AI completion.
func (c *GoogleVertexClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	events := make(chan msg.StreamEvent, 16)
	done := make(chan struct{})
	var final *msg.AssistantMessage
	var finalErr error

	go func() {
		defer close(events)
		defer close(done)
		final, finalErr = c.run(ctx, model, transcript, opts, auth, events)
	}()

	wait := func() (*msg.AssistantMessage, error) {
		<-done
		return final, finalErr
	}
	return events, wait
}

func (c *GoogleVertexClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiGoogleVertex),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	httpClient := c.httpClient()
	apiKey := resolveVertexAPIKey(auth)
	var bearerToken string
	var endpointURL string

	if apiKey != "" {
		// google-vertex.js's createClientWithApiKey path: no project/location
		// needed, the SDK's Vertex-with-API-key client still addresses the
		// same publishers/google/models resource under the default global
		// Vertex host.
		baseURL := model.BaseURL
		if baseURL == "" {
			baseURL = "https://aiplatform.googleapis.com/v1"
		}
		endpointURL = strings.TrimRight(baseURL, "/") + "/publishers/google/models/" + model.ID + ":streamGenerateContent?alt=sse"
	} else {
		project := c.resolveProject()
		location := c.resolveLocation()
		if project == "" {
			return errorOut(partial, events, false, errors.New("vertex AI requires a project ID: set GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT or GoogleVertexClient.Project"))
		}
		if location == "" {
			return errorOut(partial, events, false, errors.New("vertex AI requires a location: set GOOGLE_CLOUD_LOCATION or GoogleVertexClient.Location"))
		}
		token, err := resolveADCAccessToken(ctx, httpClient, "")
		if err != nil {
			return errorOut(partial, events, false, fmt.Errorf("resolving Vertex AI credentials: %w", err))
		}
		bearerToken = token
		baseURL := model.BaseURL
		if baseURL == "" {
			host := location + "-aiplatform.googleapis.com"
			if location == "global" {
				host = "aiplatform.googleapis.com"
			}
			baseURL = "https://" + host + "/v1"
		}
		endpointURL = strings.TrimRight(baseURL, "/") + "/projects/" + project + "/locations/" + location + "/publishers/google/models/" + model.ID + ":streamGenerateContent?alt=sse"
	}

	headers := vertexHeaders(model, apiKey, bearerToken)
	for k, v := range auth.Headers {
		headers.Set(k, v)
	}

	return runGoogleGenerateContentStream(ctx, httpClient, provider.ApiGoogleVertex, endpointURL, headers, model, transcript, opts, events)
}
