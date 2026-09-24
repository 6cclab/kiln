package api

// Recorder + replay infrastructure for the SSE conformance fixtures under
// testdata/{anthropic,openai}/*.sse.
//
// Fixtures are recorded once from a live internal/testkit/faux server (run
// `go test ./internal/provider/api/... -run TestRecordFixtures -record` to
// re-record) and replayed by default through an httptest server that plays
// the captured bytes back verbatim -- so the conformance suite is fast,
// deterministic, and does not depend on faux's script engine at test time.
// Fixtures faux's script engine cannot produce in one turn (multiple
// tool_use/tool_calls blocks in a single assistant message; a stream that
// stops mid-event) are hand-authored instead; see fixtures_test.go for which
// ones and why.

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	fauxkit "github.com/andrepato/harness/internal/testkit/faux"
)

// recordFixtures is true when the test binary was run with `-record`:
// `go test ./internal/provider/api/... -record` re-records every fixture
// from a live faux server instead of replaying testdata/*.sse.
var recordFixtures = flag.Bool("record", false, "record SSE fixtures from a live faux server instead of replaying testdata")

// teeBody wraps a response body, copying every byte read through it into
// buf, so a live client run against faux can be recorded without changing
// how the client itself works.
type teeBody struct {
	r io.Reader
	c io.Closer
}

func (t *teeBody) Read(p []byte) (int, error) { return t.r.Read(p) }
func (t *teeBody) Close() error               { return t.c.Close() }

type teeTransport struct {
	base http.RoundTripper
	buf  *bytes.Buffer
}

func (t *teeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &teeBody{r: io.TeeReader(resp.Body, t.buf), c: resp.Body}
	return resp, nil
}

// startFauxForFixture starts a faux server from scriptYAML and returns its
// base URL and a cleanup func.
func startFauxForFixture(t *testing.T, scriptYAML string) string {
	t.Helper()
	s, err := fauxkit.New(fauxkit.Options{ScriptYAML: scriptYAML})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return "http://" + addr
}

// recordAnthropicFixture runs a live AnthropicClient.Stream against a faux
// server built from scriptYAML, capturing the raw bytes of the streamed
// HTTP response body -- exactly what a real anthropic-messages endpoint
// would have sent.
func recordAnthropicFixture(t *testing.T, scriptYAML string, transcript []msg.Message, opts provider.StreamOptions) []byte {
	t.Helper()
	baseURL := startFauxForFixture(t, scriptYAML)
	buf := &bytes.Buffer{}
	client := &AnthropicClient{HTTPClient: &http.Client{Transport: &teeTransport{base: http.DefaultTransport, buf: buf}}}
	model := provider.Model{ID: "faux-1", Name: "faux-1", Api: provider.ApiAnthropicMessages, Provider: "faux", BaseURL: baseURL, ContextWindow: 32768, MaxTokens: 4096}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, transcript, opts, Auth{APIKey: "test-key"})
	for range events {
	}
	if _, err := wait(); err != nil {
		t.Fatalf("recording run failed: %v", err)
	}
	return buf.Bytes()
}

// recordOpenAIFixture is recordAnthropicFixture's openai-completions
// counterpart.
func recordOpenAIFixture(t *testing.T, scriptYAML string, transcript []msg.Message, opts provider.StreamOptions) []byte {
	t.Helper()
	baseURL := startFauxForFixture(t, scriptYAML)
	buf := &bytes.Buffer{}
	client := &OpenAICompletionsClient{HTTPClient: &http.Client{Transport: &teeTransport{base: http.DefaultTransport, buf: buf}}}
	model := provider.Model{ID: "faux-1", Name: "faux-1", Api: provider.ApiOpenAICompletions, Provider: "faux", BaseURL: baseURL + "/v1", ContextWindow: 32768, MaxTokens: 4096}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, transcript, opts, Auth{APIKey: "test-key"})
	for range events {
	}
	if _, err := wait(); err != nil {
		t.Fatalf("recording run failed: %v", err)
	}
	return buf.Bytes()
}

// loadFixture returns testdata/<protocol>/<name>.sse's bytes. With -record
// and a non-nil record func, it instead re-records the fixture (running
// record()) and overwrites the file first.
func loadFixture(t *testing.T, protocol, name string, record func() []byte) []byte {
	t.Helper()
	path := filepath.Join("testdata", protocol, name+".sse")
	if *recordFixtures && record != nil {
		body := record()
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", path, err)
		}
		return body
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v (run with -record to generate it)", path, err)
	}
	return body
}

// recordFauxErrorBody starts a faux server scripted to return a single
// error turn and returns the raw JSON error body it sends (its status is
// t.errSpec.Status, already known to the caller -- see faux/script.go's
// ErrorSpec and handleAnthropicMessages/handleOpenAIChatCompletions).
func recordFauxErrorBody(t *testing.T, protocol string, status int, errType, message string) ([]byte, error) {
	t.Helper()
	scriptYAML := "model: faux-1\nsteps:\n  - error: {status: " +
		itoa(status) + ", type: " + errType + ", message: \"" + message + "\"}\n"
	baseURL := startFauxForFixture(t, scriptYAML)

	var path, reqBody string
	switch protocol {
	case "anthropic":
		path = "/v1/messages"
		reqBody = `{"model":"faux-1","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`
	case "openai":
		path = "/v1/chat/completions"
		reqBody = `{"model":"faux-1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	default:
		t.Fatalf("recordFauxErrorBody: unknown protocol %q", protocol)
	}

	resp, err := http.Post(baseURL+path, "application/json", strings.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// replaySSE starts an httptest server that ignores the request and replays
// body verbatim as an SSE (or plain JSON error) response with the given
// status.
func replaySSE(t *testing.T, status int, contentType string, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}
