package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Amazon Bedrock ConverseStream client, ported from pi-ai's
// dist/api/bedrock-converse-stream.js. pi drives Bedrock through
// @aws-sdk/client-bedrock-runtime's ConverseStreamCommand; this client
// speaks the same wire protocol directly: a SigV4-signed
// POST {baseUrl or https://bedrock-runtime.{region}.amazonaws.com}/model/{modelId}/converse-stream
// whose response is `application/vnd.amazon.eventstream` -- AWS's binary
// event-stream framing (see eventstream.go in this file) carrying one JSON
// payload per Converse stream event (messageStart, contentBlockStart/Delta/
// Stop, messageStop, metadata, or a *Exception event for a mid-stream
// error).
//
// Deviations from bedrock-converse-stream.js, since there is no single line
// to quote per omission:
//   - Adaptive thinking (Opus 4.6+/Sonnet 4.6+, supportsAdaptiveThinking) is
//     NOT implemented; only the budget-based `thinking: {type: "enabled",
//     budget_tokens}` path (lines 1007-1026) is ported, matching the same
//     phase-2 deviation the Anthropic Messages client already documents.
//   - Prompt caching (cachePoint / supportsPromptCaching, lines 654-699) is
//     NOT implemented.
//   - Non-Anthropic reasoning (redactedContent from e.g. Bedrock-hosted
//     GPT-5.6, lines 491-509) IS parsed on the response side (round-tripped
//     into ThinkingContent.Redacted/ThinkingSignature as base64) since it is
//     a small, self-contained branch, but request-side replay of a redacted
//     block (line 797-803, decodeRedactedContent) is NOT implemented.
//   - Credential resolution implements env keys (AWS_ACCESS_KEY_ID/
//     SECRET_ACCESS_KEY/SESSION_TOKEN), AWS_BEARER_TOKEN_BEDROCK, and
//     AWS_PROFILE via a minimal ~/.aws/credentials parser. NOT implemented:
//     ECS task-role and web-identity-token credentials (amazon-bedrock.js's
//     `resolve` only detects these env vars to name the credential *source*
//     to pi's auth UI; the SDK's default chain would fetch them, which this
//     port does not reproduce), SSO, and HTTP(S) proxy handling.
//   - GovCloud's thinking.display omission (isGovCloudBedrockTarget) is NOT
//     implemented; `display: "summarized"` behavior for adaptive thinking is
//     moot since adaptive thinking itself is not implemented.
type BedrockConverseStreamClient struct {
	HTTPClient *http.Client
	// Region overrides AWS_REGION/AWS_DEFAULT_REGION resolution.
	Region string
	// Profile overrides AWS_PROFILE resolution.
	Profile string
}

func (c *BedrockConverseStreamClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// --- wire request shapes (Converse REST JSON body) ---

type bedrockMessage struct {
	Role    string               `json:"role"`
	Content []bedrockContentWire `json:"content"`
}

type bedrockContentWire struct {
	Text             string                 `json:"text,omitempty"`
	Image            *bedrockImageWire      `json:"image,omitempty"`
	ToolUse          *bedrockToolUseWire    `json:"toolUse,omitempty"`
	ToolResult       *bedrockToolResultWire `json:"toolResult,omitempty"`
	ReasoningContent *bedrockReasoningWire  `json:"reasoningContent,omitempty"`
}

type bedrockImageWire struct {
	Format string             `json:"format"`
	Source bedrockImageSource `json:"source"`
}

type bedrockImageSource struct {
	Bytes string `json:"bytes"`
}

type bedrockToolUseWire struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

type bedrockToolResultWire struct {
	ToolUseID string               `json:"toolUseId"`
	Content   []bedrockContentWire `json:"content"`
	Status    string               `json:"status,omitempty"`
}

type bedrockReasoningWire struct {
	ReasoningText   *bedrockReasoningTextWire `json:"reasoningText,omitempty"`
	RedactedContent string                    `json:"redactedContent,omitempty"`
}

type bedrockReasoningTextWire struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

type bedrockToolSpecWire struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	InputSchema bedrockJSONSchema `json:"inputSchema"`
	Strict      bool              `json:"strict,omitempty"`
}

type bedrockJSONSchema struct {
	JSON json.RawMessage `json:"json"`
}

type bedrockToolConfigWire struct {
	Tools      []bedrockToolWrapper `json:"tools"`
	ToolChoice map[string]any       `json:"toolChoice,omitempty"`
}

type bedrockToolWrapper struct {
	ToolSpec bedrockToolSpecWire `json:"toolSpec"`
}

type bedrockInferenceConfig struct {
	MaxTokens   int      `json:"maxTokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type bedrockConverseRequest struct {
	Messages                     []bedrockMessage        `json:"messages"`
	System                       []bedrockContentWire    `json:"system,omitempty"`
	InferenceConfig              *bedrockInferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig                   *bedrockToolConfigWire  `json:"toolConfig,omitempty"`
	AdditionalModelRequestFields map[string]any          `json:"additionalModelRequestFields,omitempty"`
}

const emptyTextPlaceholder = "<empty>"

// --- request construction ---

func normalizeBedrockToolCallID(id string) string {
	sanitized := regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(id, "_")
	if len(sanitized) > 64 {
		sanitized = sanitized[:64]
	}
	return sanitized
}

func nonBlankBedrockText(text string) (bedrockContentWire, bool) {
	if strings.TrimSpace(text) == "" {
		return bedrockContentWire{}, false
	}
	return bedrockContentWire{Text: text}, true
}

func bedrockImageFormat(mimeType string) (string, error) {
	switch mimeType {
	case "image/jpeg", "image/jpg":
		return "jpeg", nil
	case "image/png":
		return "png", nil
	case "image/gif":
		return "gif", nil
	case "image/webp":
		return "webp", nil
	default:
		return "", fmt.Errorf("unknown image type: %s", mimeType)
	}
}

func convertBedrockToolResultContent(blocks msg.Blocks) []bedrockContentWire {
	var out []bedrockContentWire
	for _, b := range blocks {
		switch c := b.(type) {
		case msg.ImageContent:
			format, err := bedrockImageFormat(c.MimeType)
			if err == nil {
				out = append(out, bedrockContentWire{Image: &bedrockImageWire{Format: format, Source: bedrockImageSource{Bytes: c.Data}}})
			}
		case msg.TextContent:
			if tc, ok := nonBlankBedrockText(c.Text); ok {
				out = append(out, tc)
			}
		}
	}
	if len(out) == 0 {
		out = append(out, bedrockContentWire{Text: emptyTextPlaceholder})
	}
	return out
}

// isAnthropicClaudeBedrockModel mirrors isAnthropicClaudeModel.
func isAnthropicClaudeBedrockModel(model provider.Model) bool {
	id := strings.ToLower(model.ID)
	name := strings.ToLower(model.Name)
	return strings.Contains(id, "anthropic.claude") || strings.Contains(id, "anthropic/claude") ||
		strings.Contains(name, "anthropic.claude") || strings.Contains(name, "anthropic/claude") ||
		strings.Contains(name, "claude")
}

func supportsBedrockThinkingSignature(model provider.Model) bool {
	return isAnthropicClaudeBedrockModel(model)
}

// convertBedrockMessages ports bedrock-converse-stream.js's convertMessages
// (the budget-based/non-adaptive-thinking, non-prompt-caching subset; see
// this file's doc comment for what is intentionally left out).
func convertBedrockMessages(model provider.Model, transcript []msg.Message, requiresToolCallID bool) []bedrockMessage {
	var out []bedrockMessage
	normalizeID := func(id string) string {
		if !requiresToolCallID {
			return id
		}
		return normalizeBedrockToolCallID(id)
	}

	for i := 0; i < len(transcript); i++ {
		switch t := transcript[i].(type) {
		case msg.SystemMessage:
			continue

		case msg.UserMessage:
			var content []bedrockContentWire
			for _, b := range t.Content {
				switch c := b.(type) {
				case msg.TextContent:
					if tc, ok := nonBlankBedrockText(c.Text); ok {
						content = append(content, tc)
					}
				case msg.ImageContent:
					format, err := bedrockImageFormat(c.MimeType)
					if err == nil {
						content = append(content, bedrockContentWire{Image: &bedrockImageWire{Format: format, Source: bedrockImageSource{Bytes: c.Data}}})
					}
				}
			}
			if len(content) == 0 {
				content = append(content, bedrockContentWire{Text: emptyTextPlaceholder})
			}
			out = append(out, bedrockMessage{Role: "user", Content: content})

		case msg.AssistantMessage:
			var content []bedrockContentWire
			for _, b := range t.Content {
				switch c := b.(type) {
				case msg.TextContent:
					if tc, ok := nonBlankBedrockText(c.Text); ok {
						content = append(content, tc)
					}
				case msg.ToolCall:
					content = append(content, bedrockContentWire{ToolUse: &bedrockToolUseWire{ToolUseID: normalizeID(c.ID), Name: c.Name, Input: c.Arguments}})
				case msg.ThinkingContent:
					if c.Redacted {
						continue // request-side redacted replay not implemented; see doc comment
					}
					thinking := c.Thinking
					if strings.TrimSpace(thinking) == "" {
						continue
					}
					if supportsBedrockThinkingSignature(model) {
						if strings.TrimSpace(c.ThinkingSignature) == "" {
							content = append(content, bedrockContentWire{Text: thinking})
						} else {
							content = append(content, bedrockContentWire{ReasoningContent: &bedrockReasoningWire{ReasoningText: &bedrockReasoningTextWire{Text: thinking, Signature: c.ThinkingSignature}}})
						}
					} else {
						content = append(content, bedrockContentWire{ReasoningContent: &bedrockReasoningWire{ReasoningText: &bedrockReasoningTextWire{Text: thinking}}})
					}
				}
			}
			if len(content) == 0 {
				continue // Bedrock rejects empty assistant content arrays
			}
			out = append(out, bedrockMessage{Role: "assistant", Content: content})

		case msg.ToolResultMessage:
			status := "success"
			if t.IsError {
				status = "error"
			}
			results := []bedrockContentWire{{ToolResult: &bedrockToolResultWire{ToolUseID: t.ToolCallID, Content: convertBedrockToolResultContent(t.Content), Status: status}}}
			j := i + 1
			for j < len(transcript) {
				next, ok := transcript[j].(msg.ToolResultMessage)
				if !ok {
					break
				}
				nStatus := "success"
				if next.IsError {
					nStatus = "error"
				}
				results = append(results, bedrockContentWire{ToolResult: &bedrockToolResultWire{ToolUseID: next.ToolCallID, Content: convertBedrockToolResultContent(next.Content), Status: nStatus}})
				j++
			}
			i = j - 1
			out = append(out, bedrockMessage{Role: "user", Content: results})
		}
	}
	return out
}

func buildBedrockToolConfig(tools []provider.ToolDef) *bedrockToolConfigWire {
	if len(tools) == 0 {
		return nil
	}
	wrapped := make([]bedrockToolWrapper, 0, len(tools))
	for _, td := range tools {
		schema := td.Parameters
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		wrapped = append(wrapped, bedrockToolWrapper{ToolSpec: bedrockToolSpecWire{Name: td.Name, Description: td.Description, InputSchema: bedrockJSONSchema{JSON: schema}}})
	}
	return &bedrockToolConfigWire{Tools: wrapped}
}

// bedrockThinkingBudgets mirrors buildAdditionalModelRequestFields's
// defaultBudgets table (bedrock-converse-stream.js:1008-1015).
func bedrockThinkingBudget(level provider.ThinkingLevel) int {
	switch level {
	case provider.ThinkingMinimal:
		return 1024
	case provider.ThinkingLow:
		return 2048
	case provider.ThinkingMedium:
		return 8192
	default: // high, xhigh, max all clamp to the "high" budget (budget-based Claude has no xhigh/max distinction)
		return 16384
	}
}

func buildBedrockAdditionalModelRequestFields(model provider.Model, opts provider.StreamOptions) map[string]any {
	if !model.Reasoning || opts.ThinkingLevel == "" || opts.ThinkingLevel == provider.ThinkingOff {
		return nil
	}
	if !isAnthropicClaudeBedrockModel(model) {
		return nil
	}
	budget := bedrockThinkingBudget(opts.ThinkingLevel)
	if model.ThinkingLevelMap != nil {
		if mapped, ok := model.ThinkingLevelMap[opts.ThinkingLevel]; ok && mapped != nil {
			if n, err := strconv.Atoi(*mapped); err == nil {
				budget = n
			}
		}
	}
	fields := map[string]any{
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		},
		"anthropic_beta": []string{betaInterleavedThinking},
	}
	return fields
}

func buildBedrockRequest(model provider.Model, transcript []msg.Message, opts provider.StreamOptions) bedrockConverseRequest {
	requiresToolCallID := false // Bedrock's Converse API always accepts caller-chosen toolUseId; pi's normalizeToolCallId sanitizes unconditionally
	req := bedrockConverseRequest{
		Messages: convertBedrockMessages(model, transcript, requiresToolCallID),
	}

	var systemText string
	for _, m := range transcript {
		if sm, ok := m.(msg.SystemMessage); ok {
			systemText = msg.TextOf(sm.Content)
			break
		}
	}
	if opts.SystemPrompt != "" {
		systemText = opts.SystemPrompt
	}
	if systemText != "" {
		req.System = []bedrockContentWire{{Text: systemText}}
	}

	ic := &bedrockInferenceConfig{}
	maxTokens := model.MaxTokens
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	if maxTokens > 0 {
		ic.MaxTokens = maxTokens
	}
	if opts.Temperature != nil {
		ic.Temperature = opts.Temperature
	}
	if ic.MaxTokens > 0 || ic.Temperature != nil {
		req.InferenceConfig = ic
	}

	req.ToolConfig = buildBedrockToolConfig(opts.Tools)
	req.AdditionalModelRequestFields = buildBedrockAdditionalModelRequestFields(model, opts)

	return req
}

// mapBedrockStopReason mirrors mapStopReason (bedrock-converse-stream.js:931-946).
func mapBedrockStopReason(reason string) (msg.StopReason, string) {
	switch reason {
	case "end_turn", "stop_sequence":
		return msg.StopStop, ""
	case "max_tokens", "content_filtered", "model_context_window_exceeded":
		if reason == "content_filtered" {
			return msg.StopError, fmt.Sprintf("Provider stopped with: %s", reason)
		}
		return msg.StopLength, ""
	case "tool_use":
		return msg.StopToolUse, ""
	default:
		if reason == "" {
			return msg.StopError, ""
		}
		return msg.StopError, fmt.Sprintf("Provider stopped with: %s", reason)
	}
}

// --- endpoint & credentials ---

func bedrockEndpointURL(model provider.Model, region string) string {
	if model.BaseURL != "" {
		return strings.TrimRight(model.BaseURL, "/") + "/model/" + url.PathEscape(model.ID) + "/converse-stream"
	}
	return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/model/%s/converse-stream", region, url.PathEscape(model.ID))
}

// bedrockARNRegionRe extracts the region from a Bedrock inference-profile
// ARN model ID, per bedrock-converse-stream.js:76.
var bedrockARNRegionRe = regexp.MustCompile(`^arn:aws(?:-[a-z0-9-]+)?:bedrock:([a-z0-9-]+):`)

// resolveBedrockRegion mirrors the region resolution order in
// bedrock-converse-stream.js's stream() (ARN > explicit option > env vars >
// default "us-east-1").
func resolveBedrockRegion(model provider.Model, override string) string {
	if m := bedrockARNRegionRe.FindStringSubmatch(model.ID); m != nil {
		return m[1]
	}
	if override != "" {
		return override
	}
	if v := os.Getenv("AWS_REGION"); v != "" {
		return v
	}
	if v := os.Getenv("AWS_DEFAULT_REGION"); v != "" {
		return v
	}
	return "us-east-1"
}

// bedrockCredentials is the resolved SigV4 identity, or a bearer token when
// UseBearer is set (AWS_BEARER_TOKEN_BEDROCK auth, bypassing SigV4
// entirely -- amazon-bedrock.js's bearer-token login method).
type bedrockCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	BearerToken     string
	UseBearer       bool
}

// resolveBedrockCredentials mirrors amazon-bedrock.js's `resolve` order for
// the sources this port implements (see this file's doc comment for what is
// left out): a bearer token, then AWS_PROFILE via ~/.aws/credentials, then
// AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY[/AWS_SESSION_TOKEN] env vars.
func resolveBedrockCredentials(auth Auth, profileOverride string) (bedrockCredentials, error) {
	if auth.APIKey != "" {
		return bedrockCredentials{BearerToken: auth.APIKey, UseBearer: true}, nil
	}
	if v := os.Getenv("AWS_BEARER_TOKEN_BEDROCK"); v != "" {
		return bedrockCredentials{BearerToken: v, UseBearer: true}, nil
	}
	profile := profileOverride
	if profile == "" {
		profile = os.Getenv("AWS_PROFILE")
	}
	if profile != "" {
		creds, err := readAWSProfileCredentials(profile)
		if err != nil {
			return bedrockCredentials{}, err
		}
		return creds, nil
	}
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")
	if accessKey != "" && secretKey != "" {
		return bedrockCredentials{AccessKeyID: accessKey, SecretAccessKey: secretKey, SessionToken: os.Getenv("AWS_SESSION_TOKEN")}, nil
	}
	return bedrockCredentials{}, errors.New("no AWS credentials found: set AWS_BEARER_TOKEN_BEDROCK, AWS_PROFILE, or AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY")
}

// readAWSProfileCredentials is a minimal ~/.aws/credentials INI parser
// covering the aws_access_key_id/aws_secret_access_key/aws_session_token
// keys under a `[profile]` section. It does not resolve `role_arn`/
// `source_profile` assumed-role chains, `credential_process`, or SSO --
// those are the AWS SDK default chain's job and are not reproduced here.
func readAWSProfileCredentials(profile string) (bedrockCredentials, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return bedrockCredentials{}, err
	}
	path := filepath.Join(home, ".aws", "credentials")
	data, err := os.ReadFile(path)
	if err != nil {
		return bedrockCredentials{}, fmt.Errorf("reading %s for AWS_PROFILE=%s: %w", path, profile, err)
	}
	section := ""
	creds := bedrockCredentials{}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		if section != profile {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		found = true
		switch k {
		case "aws_access_key_id":
			creds.AccessKeyID = v
		case "aws_secret_access_key":
			creds.SecretAccessKey = v
		case "aws_session_token":
			creds.SessionToken = v
		}
	}
	if !found {
		return bedrockCredentials{}, fmt.Errorf("no [%s] section in %s", profile, path)
	}
	return creds, nil
}

// --- streaming ---

// Stream starts an Amazon Bedrock ConverseStream completion.
func (c *BedrockConverseStreamClient) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
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

func (c *BedrockConverseStreamClient) run(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions, auth Auth, events chan<- msg.StreamEvent) (*msg.AssistantMessage, error) {
	partial := &msg.AssistantMessage{
		API:        string(provider.ApiBedrockConverseStream),
		Provider:   model.Provider,
		Model:      model.ID,
		Role:       msg.RoleAssistant,
		StopReason: msg.StopPending,
		Content:    msg.Blocks{},
	}

	creds, err := resolveBedrockCredentials(auth, c.Profile)
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	region := resolveBedrockRegion(model, c.Region)

	wireReq := buildBedrockRequest(model, transcript, opts)
	body, err := json.Marshal(wireReq)
	if err != nil {
		return errorOut(partial, events, false, err)
	}

	endpoint := bedrockEndpointURL(model, region)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errorOut(partial, events, false, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/vnd.amazon.eventstream")
	for k, v := range model.Headers {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}
	for k, v := range auth.Headers {
		httpReq.Header.Set(k, v)
	}

	if creds.UseBearer {
		httpReq.Header.Set("Authorization", "Bearer "+creds.BearerToken)
	} else {
		signer := v4.NewSigner()
		payloadHash := sha256Hex(body)
		httpReq.Header.Set("X-Amz-Content-Sha256", payloadHash)
		awsCreds := aws.Credentials{AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey, SessionToken: creds.SessionToken}
		if err := signer.SignHTTP(ctx, awsCreds, httpReq, payloadHash, "bedrock", region, time.Now()); err != nil {
			return errorOut(partial, events, false, fmt.Errorf("signing bedrock request: %w", err))
		}
	}

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return errorOut(partial, events, ctx.Err() != nil, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(resp.Body)
		return errorOut(partial, events, isRetriableStatus(resp.StatusCode), &StatusError{Status: resp.StatusCode, Body: string(buf), Retriable: isRetriableStatus(resp.StatusCode)})
	}

	events <- msg.StreamEvent{Type: msg.EventStart, Partial: partial}

	type blockState struct {
		kind        string // "text" | "thinking" | "toolCall"
		partialJSON string
		redacted    bool
		redactedB64 strings.Builder
	}
	blockOf := map[int]*blockState{}
	indexOf := map[int]int{}

	r := bufio.NewReader(resp.Body)
	var streamErr error
	for {
		headers, payload, err := readEventStreamMessage(r)
		if err != nil {
			streamErr = err
			break
		}
		eventType := headers[":event-type"]
		messageType := headers[":message-type"]
		if messageType == "exception" {
			excType := headers[":exception-type"]
			var body struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(payload, &body)
			streamErr = fmt.Errorf("bedrock stream exception %s: %s", excType, body.Message)
			break
		}

		switch eventType {
		case "messageStart":
			// nothing to record; role is always assistant.

		case "contentBlockStart":
			var ev struct {
				ContentBlockIndex int `json:"contentBlockIndex"`
				Start             struct {
					ToolUse *struct {
						ToolUseID string `json:"toolUseId"`
						Name      string `json:"name"`
					} `json:"toolUse"`
				} `json:"start"`
			}
			if json.Unmarshal(payload, &ev) != nil {
				continue
			}
			if ev.Start.ToolUse != nil {
				partial.Content = append(partial.Content, msg.NewToolCall(ev.Start.ToolUse.ToolUseID, ev.Start.ToolUse.Name, nil))
				pos := len(partial.Content) - 1
				indexOf[ev.ContentBlockIndex] = pos
				blockOf[ev.ContentBlockIndex] = &blockState{kind: "toolCall"}
				events <- msg.StreamEvent{Type: msg.EventToolCallStart, ContentIndex: pos, Partial: partial}
			}

		case "contentBlockDelta":
			var ev struct {
				ContentBlockIndex int `json:"contentBlockIndex"`
				Delta             struct {
					Text    *string `json:"text"`
					ToolUse *struct {
						Input string `json:"input"`
					} `json:"toolUse"`
					ReasoningContent *struct {
						Text            string `json:"text"`
						Signature       string `json:"signature"`
						RedactedContent string `json:"redactedContent"`
					} `json:"reasoningContent"`
				} `json:"delta"`
			}
			if json.Unmarshal(payload, &ev) != nil {
				continue
			}
			bs, known := blockOf[ev.ContentBlockIndex]
			pos, hasPos := indexOf[ev.ContentBlockIndex]

			switch {
			case ev.Delta.Text != nil:
				if !known {
					partial.Content = append(partial.Content, msg.Text(""))
					pos = len(partial.Content) - 1
					indexOf[ev.ContentBlockIndex] = pos
					bs = &blockState{kind: "text"}
					blockOf[ev.ContentBlockIndex] = bs
					events <- msg.StreamEvent{Type: msg.EventTextStart, ContentIndex: pos, Partial: partial}
				}
				if bs.kind == "text" {
					tc := partial.Content[pos].(msg.TextContent)
					tc.Text += *ev.Delta.Text
					partial.Content[pos] = tc
					events <- msg.StreamEvent{Type: msg.EventTextDelta, ContentIndex: pos, Delta: *ev.Delta.Text, Partial: partial}
				}

			case ev.Delta.ToolUse != nil && known && hasPos && bs.kind == "toolCall":
				bs.partialJSON += ev.Delta.ToolUse.Input
				tc := partial.Content[pos].(msg.ToolCall)
				var args map[string]any
				if json.Unmarshal([]byte(bs.partialJSON), &args) == nil {
					tc.Arguments = args
				}
				partial.Content[pos] = tc
				events <- msg.StreamEvent{Type: msg.EventToolCallDelta, ContentIndex: pos, Delta: ev.Delta.ToolUse.Input, Partial: partial}

			case ev.Delta.ReasoningContent != nil:
				if !known {
					partial.Content = append(partial.Content, msg.Thinking(""))
					pos = len(partial.Content) - 1
					indexOf[ev.ContentBlockIndex] = pos
					bs = &blockState{kind: "thinking"}
					blockOf[ev.ContentBlockIndex] = bs
					events <- msg.StreamEvent{Type: msg.EventThinkingStart, ContentIndex: pos, Partial: partial}
				}
				if bs.kind != "thinking" {
					continue
				}
				tc := partial.Content[pos].(msg.ThinkingContent)
				rc := ev.Delta.ReasoningContent
				if rc.Text != "" {
					tc.Thinking += rc.Text
					events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: pos, Delta: rc.Text, Partial: partial}
				}
				if rc.Signature != "" && !bs.redacted {
					tc.ThinkingSignature += rc.Signature
				}
				if rc.RedactedContent != "" {
					if !bs.redacted {
						bs.redacted = true
						tc.Redacted = true
						tc.ThinkingSignature = ""
						tc.Thinking += redactedThinkingPlaceholder
						events <- msg.StreamEvent{Type: msg.EventThinkingDelta, ContentIndex: pos, Delta: redactedThinkingPlaceholder, Partial: partial}
					}
					bs.redactedB64.WriteString(rc.RedactedContent)
				}
				partial.Content[pos] = tc
			}

		case "contentBlockStop":
			var ev struct {
				ContentBlockIndex int `json:"contentBlockIndex"`
			}
			if json.Unmarshal(payload, &ev) != nil {
				continue
			}
			bs, ok := blockOf[ev.ContentBlockIndex]
			pos, hasPos := indexOf[ev.ContentBlockIndex]
			if !ok || !hasPos {
				continue
			}
			switch bs.kind {
			case "text":
				tc := partial.Content[pos].(msg.TextContent)
				events <- msg.StreamEvent{Type: msg.EventTextEnd, ContentIndex: pos, Content: tc.Text, Partial: partial}
			case "thinking":
				tc := partial.Content[pos].(msg.ThinkingContent)
				if bs.redacted && bs.redactedB64.Len() > 0 {
					tc.ThinkingSignature = bs.redactedB64.String()
					partial.Content[pos] = tc
				}
				events <- msg.StreamEvent{Type: msg.EventThinkingEnd, ContentIndex: pos, Content: tc.Thinking, Partial: partial}
			case "toolCall":
				tc := partial.Content[pos].(msg.ToolCall)
				events <- msg.StreamEvent{Type: msg.EventToolCallEnd, ContentIndex: pos, ToolCall: &tc, Partial: partial}
			}

		case "messageStop":
			var ev struct {
				StopReason string `json:"stopReason"`
			}
			if json.Unmarshal(payload, &ev) != nil {
				continue
			}
			partial.RawStopReason = ev.StopReason
			reason, errMsg := mapBedrockStopReason(ev.StopReason)
			partial.StopReason = reason
			if errMsg != "" {
				partial.ErrorMessage = errMsg
			}

		case "metadata":
			var ev struct {
				Usage *struct {
					InputTokens           int `json:"inputTokens"`
					OutputTokens          int `json:"outputTokens"`
					TotalTokens           int `json:"totalTokens"`
					CacheReadInputTokens  int `json:"cacheReadInputTokens"`
					CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(payload, &ev) != nil {
				continue
			}
			if ev.Usage != nil {
				partial.Usage.Input = ev.Usage.InputTokens
				partial.Usage.Output = ev.Usage.OutputTokens
				partial.Usage.CacheRead = ev.Usage.CacheReadInputTokens
				partial.Usage.CacheWrite = ev.Usage.CacheWriteInputTokens
				totalTokens := ev.Usage.TotalTokens
				if totalTokens == 0 {
					totalTokens = partial.Usage.Input + partial.Usage.Output
				}
				partial.Usage.TotalTokens = totalTokens
				computeAnthropicCost(model, &partial.Usage)
			}
		}
	}

	if streamErr != io.EOF {
		return errorOut(partial, events, ctx.Err() != nil, streamErr)
	}

	if ctx.Err() != nil {
		return errorOut(partial, events, true, fmt.Errorf("request was aborted"))
	}
	if partial.StopReason == msg.StopPending {
		return errorOut(partial, events, false, fmt.Errorf("bedrock stream ended without a stop reason"))
	}
	if partial.StopReason == msg.StopAborted || partial.StopReason == msg.StopError {
		errMsg := partial.ErrorMessage
		if errMsg == "" {
			errMsg = "an unknown error occurred"
		}
		events <- msg.StreamEvent{Type: msg.EventError, Reason: partial.StopReason, Error: partial}
		return nil, errors.New(errMsg)
	}

	events <- msg.StreamEvent{Type: msg.EventDone, Reason: partial.StopReason, Message: partial}
	return partial, nil
}

const redactedThinkingPlaceholder = "[Reasoning redacted]"

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- AWS binary event-stream framing (application/vnd.amazon.eventstream) ---
//
// Message layout (big-endian), per AWS's event-stream spec:
//
//	total_length    uint32
//	headers_length  uint32
//	prelude_crc     uint32  -- CRC32 (IEEE) of the 8 prelude bytes above
//	headers         headers_length bytes
//	payload         total_length - headers_length - 16 bytes
//	message_crc     uint32  -- CRC32 (IEEE) of every byte before it
//
// Each header is: name_len(uint8) name(utf8) value_type(uint8) value.
// Only the string value type (7: uint16 length-prefixed utf8) is needed for
// Bedrock's `:event-type`/`:message-type`/`:exception-type`/`:content-type`
// headers, but byte(6, length-prefixed) and bool(0/1, no body) are decoded
// too since a strict decoder must be able to skip any header it doesn't use.

func readEventStreamMessage(r io.Reader) (headers map[string]string, payload []byte, err error) {
	var prelude [12]byte
	if _, err := io.ReadFull(r, prelude[:]); err != nil {
		return nil, nil, err
	}
	totalLength := binary.BigEndian.Uint32(prelude[0:4])
	headersLength := binary.BigEndian.Uint32(prelude[4:8])
	preludeCRC := binary.BigEndian.Uint32(prelude[8:12])

	if got := crc32.ChecksumIEEE(prelude[0:8]); got != preludeCRC {
		return nil, nil, fmt.Errorf("event-stream prelude CRC mismatch: got %08x, want %08x", got, preludeCRC)
	}
	if totalLength < 16 || uint32(headersLength) > totalLength-16 {
		return nil, nil, fmt.Errorf("event-stream message has invalid lengths: total=%d headers=%d", totalLength, headersLength)
	}

	rest := make([]byte, totalLength-12)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, nil, err
	}
	headerBytes := rest[:headersLength]
	payloadLen := totalLength - 12 - headersLength - 4
	payload = rest[headersLength : headersLength+payloadLen]
	messageCRC := binary.BigEndian.Uint32(rest[headersLength+payloadLen:])

	fullMessage := append(append([]byte{}, prelude[:]...), rest[:len(rest)-4]...)
	if got := crc32.ChecksumIEEE(fullMessage); got != messageCRC {
		return nil, nil, fmt.Errorf("event-stream message CRC mismatch: got %08x, want %08x", got, messageCRC)
	}

	headers, err = decodeEventStreamHeaders(headerBytes)
	if err != nil {
		return nil, nil, err
	}
	return headers, payload, nil
}

func decodeEventStreamHeaders(b []byte) (map[string]string, error) {
	headers := map[string]string{}
	pos := 0
	for pos < len(b) {
		if pos+1 > len(b) {
			return nil, errors.New("event-stream headers truncated (name length)")
		}
		nameLen := int(b[pos])
		pos++
		if pos+nameLen > len(b) {
			return nil, errors.New("event-stream headers truncated (name)")
		}
		name := string(b[pos : pos+nameLen])
		pos += nameLen
		if pos+1 > len(b) {
			return nil, errors.New("event-stream headers truncated (value type)")
		}
		valueType := b[pos]
		pos++
		switch valueType {
		case 0, 1: // bool true/false: no value bytes
			if valueType == 0 {
				headers[name] = "true"
			} else {
				headers[name] = "false"
			}
		case 2: // byte
			if pos+1 > len(b) {
				return nil, errors.New("event-stream headers truncated (byte)")
			}
			headers[name] = strconv.Itoa(int(int8(b[pos])))
			pos++
		case 3: // short
			if pos+2 > len(b) {
				return nil, errors.New("event-stream headers truncated (short)")
			}
			headers[name] = strconv.Itoa(int(int16(binary.BigEndian.Uint16(b[pos : pos+2]))))
			pos += 2
		case 4: // int32
			if pos+4 > len(b) {
				return nil, errors.New("event-stream headers truncated (int32)")
			}
			headers[name] = strconv.Itoa(int(int32(binary.BigEndian.Uint32(b[pos : pos+4]))))
			pos += 4
		case 5: // int64
			if pos+8 > len(b) {
				return nil, errors.New("event-stream headers truncated (int64)")
			}
			headers[name] = strconv.FormatInt(int64(binary.BigEndian.Uint64(b[pos:pos+8])), 10)
			pos += 8
		case 6: // byte array: uint16 length prefix
			if pos+2 > len(b) {
				return nil, errors.New("event-stream headers truncated (byte-array length)")
			}
			n := int(binary.BigEndian.Uint16(b[pos : pos+2]))
			pos += 2
			if pos+n > len(b) {
				return nil, errors.New("event-stream headers truncated (byte-array)")
			}
			headers[name] = base64.StdEncoding.EncodeToString(b[pos : pos+n])
			pos += n
		case 7: // string: uint16 length prefix, utf8
			if pos+2 > len(b) {
				return nil, errors.New("event-stream headers truncated (string length)")
			}
			n := int(binary.BigEndian.Uint16(b[pos : pos+2]))
			pos += 2
			if pos+n > len(b) {
				return nil, errors.New("event-stream headers truncated (string)")
			}
			headers[name] = string(b[pos : pos+n])
			pos += n
		case 8: // timestamp: int64 millis
			if pos+8 > len(b) {
				return nil, errors.New("event-stream headers truncated (timestamp)")
			}
			headers[name] = strconv.FormatInt(int64(binary.BigEndian.Uint64(b[pos:pos+8])), 10)
			pos += 8
		case 9: // uuid: 16 bytes
			if pos+16 > len(b) {
				return nil, errors.New("event-stream headers truncated (uuid)")
			}
			headers[name] = base64.StdEncoding.EncodeToString(b[pos : pos+16])
			pos += 16
		default:
			return nil, fmt.Errorf("event-stream header %q has unknown value type %d", name, valueType)
		}
	}
	return headers, nil
}
