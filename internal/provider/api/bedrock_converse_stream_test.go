package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// --- a small AWS event-stream encoder, used only by these tests to build
// testdata/bedrock/*.eventstream fixtures and the corrupted-CRC case. It is
// the mirror image of readEventStreamMessage in bedrock_converse_stream.go. ---

func encodeEventStreamMessage(t *testing.T, headers map[string]string, payload []byte) []byte {
	t.Helper()
	var headerBuf bytes.Buffer
	for name, value := range headers {
		headerBuf.WriteByte(byte(len(name)))
		headerBuf.WriteString(name)
		headerBuf.WriteByte(7) // string type
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(value)))
		headerBuf.Write(lenBuf[:])
		headerBuf.WriteString(value)
	}
	headerBytes := headerBuf.Bytes()
	totalLength := uint32(12 + len(headerBytes) + len(payload) + 4)

	var prelude [8]byte
	binary.BigEndian.PutUint32(prelude[0:4], totalLength)
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(headerBytes)))
	preludeCRC := crc32.ChecksumIEEE(prelude[:])

	var msgBuf bytes.Buffer
	msgBuf.Write(prelude[:])
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], preludeCRC)
	msgBuf.Write(crcBuf[:])
	msgBuf.Write(headerBytes)
	msgBuf.Write(payload)

	messageCRC := crc32.ChecksumIEEE(msgBuf.Bytes())
	var msgCRCBuf [4]byte
	binary.BigEndian.PutUint32(msgCRCBuf[:], messageCRC)
	msgBuf.Write(msgCRCBuf[:])
	return msgBuf.Bytes()
}

func eventStreamEvent(t *testing.T, eventType string, payload any) []byte {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return encodeEventStreamMessage(t, map[string]string{
		":message-type": "event",
		":event-type":   eventType,
		":content-type": "application/json",
	}, body)
}

// buildTextConverseStreamFixture builds a full ConverseStream event-stream
// response for a plain-text turn: messageStart, contentBlockDelta(text) x2,
// contentBlockStop, messageStop, metadata.
func buildTextConverseStreamFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(eventStreamEvent(t, "messageStart", map[string]any{"role": "assistant"}))
	buf.Write(eventStreamEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "Hello "}}))
	buf.Write(eventStreamEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "world."}}))
	buf.Write(eventStreamEvent(t, "contentBlockStop", map[string]any{"contentBlockIndex": 0}))
	buf.Write(eventStreamEvent(t, "messageStop", map[string]any{"stopReason": "end_turn"}))
	buf.Write(eventStreamEvent(t, "metadata", map[string]any{"usage": map[string]any{"inputTokens": 12, "outputTokens": 6, "totalTokens": 18}}))
	return buf.Bytes()
}

// buildToolCallConverseStreamFixture builds a tool-call turn: messageStart,
// contentBlockStart(toolUse), contentBlockDelta(toolUse partial JSON) x2,
// contentBlockStop, messageStop(tool_use), metadata.
func buildToolCallConverseStreamFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(eventStreamEvent(t, "messageStart", map[string]any{"role": "assistant"}))
	buf.Write(eventStreamEvent(t, "contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{"toolUse": map[string]any{"toolUseId": "tc1", "name": "search"}}}))
	buf.Write(eventStreamEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": `{"query":`}}}))
	buf.Write(eventStreamEvent(t, "contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": `"weather"}`}}}))
	buf.Write(eventStreamEvent(t, "contentBlockStop", map[string]any{"contentBlockIndex": 0}))
	buf.Write(eventStreamEvent(t, "messageStop", map[string]any{"stopReason": "tool_use"}))
	buf.Write(eventStreamEvent(t, "metadata", map[string]any{"usage": map[string]any{"inputTokens": 20, "outputTokens": 10, "totalTokens": 30}}))
	return buf.Bytes()
}

func bedrockModel(baseURL string) provider.Model {
	return provider.Model{
		ID:            "anthropic.claude-sonnet",
		Name:          "Claude Sonnet",
		Api:           provider.ApiBedrockConverseStream,
		Provider:      "amazon-bedrock",
		BaseURL:       baseURL,
		ContextWindow: 200000,
		MaxTokens:     4096,
		Cost:          provider.ModelCost{ModelCostRates: provider.ModelCostRates{Input: 3, Output: 15}},
	}
}

func bedrockReplayServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBedrockConverseStreamTextOnly(t *testing.T) {
	body := buildTextConverseStreamFixture(t)
	srv := bedrockReplayServer(t, body)
	model := bedrockModel(srv.URL)
	client := &BedrockConverseStreamClient{Region: "us-east-1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "bearer-token"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)
	assertTextMonotonic(t, all)

	if msg.TextOf(final.Content) != "Hello world." {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
	if final.StopReason != msg.StopStop {
		t.Fatalf("StopReason = %q, want stop", final.StopReason)
	}
	if final.RawStopReason != "end_turn" {
		t.Fatalf("RawStopReason = %q", final.RawStopReason)
	}
	if final.Usage.Input != 12 || final.Usage.Output != 6 || final.Usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v", final.Usage)
	}
	assertCost(t, model, final.Usage)
}

func TestBedrockConverseStreamToolCall(t *testing.T) {
	body := buildToolCallConverseStreamFixture(t)
	srv := bedrockReplayServer(t, body)
	model := bedrockModel(srv.URL)
	client := &BedrockConverseStreamClient{Region: "us-east-1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("weather?")}}}, provider.StreamOptions{}, Auth{APIKey: "bearer-token"})
	all := collectEvents(events)
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	assertPartialIdentity(t, all, final)

	deltaCount := 0
	for _, e := range all {
		if e.Type == msg.EventToolCallDelta {
			deltaCount++
		}
	}
	if deltaCount < 2 {
		t.Fatalf("tool call delta events = %d, want >=2 (partial JSON across chunks)", deltaCount)
	}
	calls := msg.ToolCallsOf(final.Content)
	if len(calls) != 1 || calls[0].Name != "search" || calls[0].ID != "tc1" || calls[0].Arguments["query"] != "weather" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if final.StopReason != msg.StopToolUse {
		t.Fatalf("StopReason = %q, want toolUse", final.StopReason)
	}
}

// TestBedrockConverseStreamCorruptedCRC exercises the CRC verification in
// readEventStreamMessage: a fixture whose message CRC has been flipped must
// surface as an error, not silently accepted/misparsed frame data.
func TestBedrockConverseStreamCorruptedCRC(t *testing.T) {
	body := buildTextConverseStreamFixture(t)
	// Flip a byte inside the first message's payload region without touching
	// its CRC trailer, so the prelude CRC still checks out but the message
	// CRC no longer matches.
	corrupted := append([]byte{}, body...)
	corrupted[20] ^= 0xFF
	srv := bedrockReplayServer(t, corrupted)
	model := bedrockModel(srv.URL)
	client := &BedrockConverseStreamClient{Region: "us-east-1"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{}, Auth{APIKey: "bearer-token"})
	all := collectEvents(events)
	_, err := wait()
	if err == nil {
		t.Fatal("expected an error for a corrupted message CRC")
	}
	last := all[len(all)-1]
	if last.Type != msg.EventError {
		t.Fatalf("last event = %q, want error", last.Type)
	}
	if last.Error == nil || last.Error.ErrorMessage == "" {
		t.Fatal("expected a non-empty error message describing the CRC mismatch")
	}
}

// TestReadEventStreamMessageDetectsPreludeCorruption flips a byte in the
// prelude itself (before any payload), which must be caught by the prelude
// CRC check rather than reading a garbage total_length.
func TestReadEventStreamMessageDetectsPreludeCorruption(t *testing.T) {
	msgBytes := eventStreamEvent(t, "messageStart", map[string]any{"role": "assistant"})
	corrupted := append([]byte{}, msgBytes...)
	corrupted[0] ^= 0x01 // corrupt total_length's high byte
	_, _, err := readEventStreamMessage(bufio.NewReader(bytes.NewReader(corrupted)))
	if err == nil {
		t.Fatal("expected an error for a corrupted prelude")
	}
}

func TestReadEventStreamMessageRoundTrip(t *testing.T) {
	payload := map[string]any{"role": "assistant"}
	encoded := eventStreamEvent(t, "messageStart", payload)
	headers, gotPayload, err := readEventStreamMessage(bufio.NewReader(bytes.NewReader(encoded)))
	if err != nil {
		t.Fatalf("readEventStreamMessage: %v", err)
	}
	if headers[":event-type"] != "messageStart" {
		t.Fatalf("headers = %+v", headers)
	}
	var decoded map[string]any
	if err := json.Unmarshal(gotPayload, &decoded); err != nil {
		t.Fatalf("payload did not round-trip: %v", err)
	}
	if decoded["role"] != "assistant" {
		t.Fatalf("payload = %+v", decoded)
	}
}

// --- request shape ---

// TestBuildBedrockRequestShape asserts the JSON body matches the Converse
// wire fields bedrock-converse-stream.js's convertMessages/buildSystemPrompt/
// convertToolConfig/buildAdditionalModelRequestFields build (the budget-based
// thinking path; see this file's doc comment for the adaptive-thinking
// deviation): "thinking: {type: 'enabled', budget_tokens}" plus
// "anthropic_beta: ['interleaved-thinking-2025-05-14']"
// (bedrock-converse-stream.js:1019-1029).
func TestBuildBedrockRequestShape(t *testing.T) {
	model := bedrockModel("https://example.test")
	model.Reasoning = true
	transcript := []msg.Message{
		msg.SystemMessage{Role: msg.RoleSystem, Content: msg.Blocks{msg.Text("You are helpful.")}},
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}
	opts := provider.StreamOptions{
		ThinkingLevel: provider.ThinkingMedium,
		Tools:         []provider.ToolDef{{Name: "search", Description: "search", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}},
	}
	req := buildBedrockRequest(model, transcript, opts)
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	sysBlocks, ok := decoded["system"].([]any)
	if !ok || len(sysBlocks) != 1 {
		t.Fatalf("system = %#v", decoded["system"])
	}
	toolConfig, ok := decoded["toolConfig"].(map[string]any)
	if !ok {
		t.Fatal("toolConfig missing")
	}
	tools, ok := toolConfig["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("toolConfig.tools = %#v", toolConfig["tools"])
	}
	fields, ok := decoded["additionalModelRequestFields"].(map[string]any)
	if !ok {
		t.Fatal("additionalModelRequestFields missing")
	}
	thinking, ok := fields["thinking"].(map[string]any)
	if !ok {
		t.Fatal("additionalModelRequestFields.thinking missing")
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled", thinking["type"])
	}
	if thinking["budget_tokens"] != float64(8192) {
		t.Fatalf("thinking.budget_tokens = %v, want 8192 (medium)", thinking["budget_tokens"])
	}
	betas, ok := fields["anthropic_beta"].([]any)
	if !ok || len(betas) != 1 || betas[0] != betaInterleavedThinking {
		t.Fatalf("anthropic_beta = %#v", fields["anthropic_beta"])
	}
}

func TestBedrockEndpointURL(t *testing.T) {
	model := provider.Model{ID: "anthropic.claude-sonnet"}
	got := bedrockEndpointURL(model, "eu-west-1")
	want := "https://bedrock-runtime.eu-west-1.amazonaws.com/model/anthropic.claude-sonnet/converse-stream"
	if got != want {
		t.Fatalf("bedrockEndpointURL = %q, want %q", got, want)
	}
}

func TestResolveBedrockRegionFromARN(t *testing.T) {
	model := provider.Model{ID: "arn:aws:bedrock:ap-southeast-2:123456789012:inference-profile/foo"}
	if got := resolveBedrockRegion(model, ""); got != "ap-southeast-2" {
		t.Fatalf("region = %q, want ap-southeast-2 (extracted from the ARN)", got)
	}
}

func TestResolveBedrockCredentialsBearerToken(t *testing.T) {
	creds, err := resolveBedrockCredentials(Auth{APIKey: "my-bearer-token"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !creds.UseBearer || creds.BearerToken != "my-bearer-token" {
		t.Fatalf("creds = %+v, want a bearer-token credential", creds)
	}
}

func TestResolveBedrockCredentialsEnvKeys(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "session")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "")
	t.Setenv("AWS_PROFILE", "")
	creds, err := resolveBedrockCredentials(Auth{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if creds.UseBearer || creds.AccessKeyID != "AKIDEXAMPLE" || creds.SecretAccessKey != "secret" || creds.SessionToken != "session" {
		t.Fatalf("creds = %+v", creds)
	}
}

// --- SigV4 known-answer test ---
//
// This independently recomputes the AWS Signature Version 4 canonical
// request / string-to-sign / signing-key chain per AWS's published
// algorithm (https://docs.aws.amazon.com/general/latest/gr/sigv4-calculate-signature.html)
// for a fixed request, and asserts aws-sdk-go-v2's v4.Signer -- the signer
// bedrock_converse_stream.go's run() calls -- produces the identical
// Authorization signature. This is the "known answer": an implementation of
// the spec written independently of the library under test, compared
// against the library's output for the same fixed inputs.
func TestSigV4SignerMatchesIndependentComputation(t *testing.T) {
	accessKey := "AKIDEXAMPLE"
	secretKey := "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	region := "us-east-1"
	service := "bedrock"
	signingTime := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	amzDate := signingTime.Format("20060102T150405Z")
	dateStamp := signingTime.Format("20060102")

	payload := []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)
	payloadHash := sha256Hex(payload)

	host := "bedrock-runtime.us-east-1.amazonaws.com"
	uri := "/model/anthropic.claude-sonnet/converse-stream"

	req, err := http.NewRequest(http.MethodPost, "https://"+host+uri, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Host = host

	signer := v4.NewSigner()
	creds := aws.Credentials{AccessKeyID: accessKey, SecretAccessKey: secretKey}
	if err := signer.SignHTTP(context.Background(), creds, req, payloadHash, service, region, signingTime); err != nil {
		t.Fatalf("SignHTTP: %v", err)
	}
	gotAuth := req.Header.Get("Authorization")
	if gotAuth == "" {
		t.Fatal("SignHTTP did not set an Authorization header")
	}

	// --- independent computation, straight from the AWS spec ---
	// http.NewRequest sets Content-Length from the io.Reader's *bytes.Reader
	// length, and the signer includes it in SignedHeaders, so it must be
	// part of the canonical request here too.
	signedHeaders := "content-length;content-type;host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := fmt.Sprintf("content-length:%d\ncontent-type:application/json\nhost:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", len(payload), host, payloadHash, amzDate)
	canonicalRequest := strings.Join([]string{
		"POST",
		uri,
		"", // no query string
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
	canonicalRequestHash := sha256Hex([]byte(canonicalRequest))

	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, region, service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		canonicalRequestHash,
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	wantAuth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature)

	if gotAuth != wantAuth {
		t.Fatalf("Authorization header =\n  %s\nwant\n  %s", gotAuth, wantAuth)
	}
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
